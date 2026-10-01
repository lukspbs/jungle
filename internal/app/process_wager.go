package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/domain/events"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/domain/wallet"
)

// ProcessWagerCommand é a operação recebida de um provedor, por HTTP ou SQS.
type ProcessWagerCommand struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wagering.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	CorrelationID                  string
}

// ProcessWagerResult é o desfecho devolvido ao provedor.
type ProcessWagerResult struct {
	TransactionID uuid.UUID
	Status        wagering.Status

	// Balance é o saldo observado no processamento. Num replay ele é o saldo
	// original, não o atual: a transação o congelou.
	Balance money.Money

	FailureCode wagering.FailureCode

	// IdempotentReplay indica que este resultado veio de um processamento
	// anterior, sem reaplicar a operação.
	IdempotentReplay bool
}

// ProcessWager processa uma operação de provedor.
//
// HTTP e SQS compartilham este caso de uso, e com ele as garantias de
// idempotência: a chave de entrada muda de lugar, o resto do caminho é o mesmo.
type ProcessWager struct {
	store     *postgres.Store
	clock     Clock
	ids       IDs
	reference ReferencePolicy
}

// NewProcessWager monta o caso de uso.
func NewProcessWager(store *postgres.Store, clock Clock, ids IDs, reference ReferencePolicy) *ProcessWager {
	return &ProcessWager{store: store, clock: clock, ids: ids, reference: reference}
}

// Execute processa a operação.
//
// O caminho tem três etapas: consulta de replay, processamento, e reconsulta em
// caso de corrida. A consulta inicial é otimização — ela evita trabalho quando
// a operação já foi processada — mas não é a garantia. A garantia é a constraint
// de unicidade: se duas requisições simultâneas passarem pela consulta sem achar
// nada, uma insere e a outra recebe violação, e aí reconsulta o resultado que a
// vencedora gravou.
func (uc *ProcessWager) Execute(ctx context.Context, cmd ProcessWagerCommand) (ProcessWagerResult, error) {
	if err := uc.validate(cmd); err != nil {
		return ProcessWagerResult{}, err
	}

	fingerprint, err := uc.fingerprintOf(cmd)
	if err != nil {
		return ProcessWagerResult{}, err
	}

	if res, encontrado, err := uc.replay(ctx, uc.store.Read(), cmd, fingerprint); err != nil || encontrado {
		return res, err
	}

	res, err := uc.process(ctx, cmd, fingerprint)
	if isDuplicate(err) {
		// Corrida perdida: outra requisição com a mesma identidade gravou
		// primeiro. O resultado correto é o que ela produziu.
		res, encontrado, errReplay := uc.replay(ctx, uc.store.Read(), cmd, fingerprint)
		if errReplay != nil {
			return ProcessWagerResult{}, errReplay
		}
		if !encontrado {
			return ProcessWagerResult{}, fmt.Errorf("app: violação de unicidade sem transação correspondente: %w", err)
		}
		return res, nil
	}
	return res, err
}

func (uc *ProcessWager) validate(cmd ProcessWagerCommand) error {
	if cmd.CorrelationID == "" {
		return fmt.Errorf("%w: correlationId ausente", ErrInvalidCommand)
	}
	if cmd.IdempotencyKey == "" {
		return fmt.Errorf("%w: chave de idempotência ausente", ErrInvalidCommand)
	}
	return validateReversal(cmd)
}

// replay consulta o resultado de um processamento anterior.
//
// A busca é por (provedor, id externo) e por (provedor, chave). A primeira
// reconhece a mesma operação financeira ainda que enviada sob outra chave; a
// segunda reconhece a chave reutilizada em outra operação. Hash igual é replay;
// hash diferente é conflito.
func (uc *ProcessWager) replay(
	ctx context.Context, repos *postgres.Repositories,
	cmd ProcessWagerCommand, fingerprint Fingerprint,
) (ProcessWagerResult, bool, error) {
	porOperacao, err := repos.Transactions.FindByProviderAndExternalID(
		ctx, cmd.ProviderID, cmd.ExternalTransactionID)
	switch {
	case err == nil:
		return decide(porOperacao, fingerprint)
	case !errors.Is(err, postgres.ErrTransactionNotFound):
		return ProcessWagerResult{}, false, err
	}

	porChave, err := repos.Transactions.FindByProviderAndIdempotencyKey(
		ctx, cmd.ProviderID, cmd.IdempotencyKey)
	switch {
	case err == nil:
		// A chave existe, mas aponta para outra operação financeira: é
		// reutilização, e o hash necessariamente difere.
		return decide(porChave, fingerprint)
	case errors.Is(err, postgres.ErrTransactionNotFound):
		return ProcessWagerResult{}, false, nil
	default:
		return ProcessWagerResult{}, false, err
	}
}

// decide compara o hash persistido com o da chegada atual.
func decide(tx *wagering.WagerTransaction, fingerprint Fingerprint) (ProcessWagerResult, bool, error) {
	if !tx.HasSamePayload(fingerprint.Bytes()) {
		return ProcessWagerResult{}, false, fmt.Errorf(
			"%w: %s/%s", ErrIdempotencyConflict, tx.ProviderID(), tx.ExternalTransactionID())
	}
	return ProcessWagerResult{
		TransactionID:    tx.ID(),
		Status:           tx.Status(),
		Balance:          tx.ResultBalance(),
		FailureCode:      tx.FailureCode(),
		IdempotentReplay: true,
	}, true, nil
}

// ExecuteWithin processa dentro de uma transação já aberta.
//
// Existe para o consumidor SQS, que precisa gravar o registro de inbox no mesmo
// commit das alterações de domínio. Sem isso, inbox e efeito financeiro
// poderiam divergir numa queda entre as duas transações.
//
// Diferente de Execute, não trata violação de unicidade: no PostgreSQL ela
// invalida a transação inteira, e só um rollback seguido de nova tentativa
// resolve. Cabe a quem chama repetir a transação, e aí o caminho de replay
// encontra o que a concorrente gravou.
func (uc *ProcessWager) ExecuteWithin(
	ctx context.Context, r *postgres.Repositories, cmd ProcessWagerCommand,
) (ProcessWagerResult, error) {
	if err := uc.validate(cmd); err != nil {
		return ProcessWagerResult{}, err
	}
	fingerprint, err := uc.fingerprintOf(cmd)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	if res, encontrado, err := uc.replay(ctx, r, cmd, fingerprint); err != nil || encontrado {
		return res, err
	}
	return uc.processWithin(ctx, r, cmd, fingerprint, uc.clock.Now())
}

// fingerprintOf calcula o hash dos campos de negócio do comando.
func (uc *ProcessWager) fingerprintOf(cmd ProcessWagerCommand) (Fingerprint, error) {
	return ComputeFingerprint(FingerprintFields{
		ProviderID:                     cmd.ProviderID,
		ExternalTransactionID:          cmd.ExternalTransactionID,
		PlayerID:                       cmd.PlayerID,
		WalletID:                       cmd.WalletID,
		RoundID:                        cmd.RoundID,
		GameID:                         cmd.GameID,
		Kind:                           cmd.Kind,
		Money:                          cmd.Money,
		ReferenceExternalTransactionID: cmd.ReferenceExternalTransactionID,
	})
}

// process abre a transação e executa o miolo.
func (uc *ProcessWager) process(
	ctx context.Context, cmd ProcessWagerCommand, fingerprint Fingerprint,
) (ProcessWagerResult, error) {
	agora := uc.clock.Now()
	var resultado ProcessWagerResult

	err := uc.store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		var err error
		resultado, err = uc.processWithin(ctx, r, cmd, fingerprint, agora)
		return err
	})
	if err != nil {
		return ProcessWagerResult{}, err
	}
	return resultado, nil
}

// processWithin executa a operação sob o lock da carteira, usando os
// repositórios de uma transação já aberta.
func (uc *ProcessWager) processWithin(
	ctx context.Context, r *postgres.Repositories,
	cmd ProcessWagerCommand, fingerprint Fingerprint, agora time.Time,
) (ProcessWagerResult, error) {

	txID, err := uc.ids.New()
	if err != nil {
		return ProcessWagerResult{}, fmt.Errorf("app: falha ao gerar identificador: %w", err)
	}

	tx, err := wagering.NewExternal(wagering.ExternalRequest{
		ID:                             txID,
		ProviderID:                     cmd.ProviderID,
		ExternalTransactionID:          cmd.ExternalTransactionID,
		IdempotencyKey:                 cmd.IdempotencyKey,
		PayloadHash:                    fingerprint.Bytes(),
		WalletID:                       cmd.WalletID,
		PlayerID:                       cmd.PlayerID,
		RoundID:                        cmd.RoundID,
		GameID:                         cmd.GameID,
		Kind:                           cmd.Kind,
		Amount:                         cmd.Money,
		ReferenceExternalTransactionID: cmd.ReferenceExternalTransactionID,
		Now:                            agora,
	})
	if err != nil {
		return ProcessWagerResult{}, fmt.Errorf("%w: %s", ErrInvalidCommand, err)
	}

	// A carteira é travada antes de qualquer decisão. Daqui até o commit,
	// nenhum outro escritor desta carteira avança — e carteiras distintas
	// seguem em paralelo, porque o lock é de linha.
	w, err := r.Wallets.LockForUpdate(ctx, cmd.WalletID)
	if errors.Is(err, postgres.ErrWalletNotFound) {
		// A chave estrangeira impede registrar uma recusa para uma carteira
		// que não existe, então este caso sai como erro e não como
		// transação REJECTED persistida.
		return ProcessWagerResult{}, fmt.Errorf("%w: %s", ErrWalletNotFound, cmd.WalletID)
	}
	if err != nil {
		return ProcessWagerResult{}, err
	}

	if code, recusa := uc.checkWallet(w, cmd); recusa {
		return uc.rejectResult(ctx, r, tx, code, agora, cmd.CorrelationID)
	}

	// Reversões dependem de uma referência que pode não ter chegado ainda.
	direcao := movementDirection(cmd.Kind)
	if cmd.Kind.IsReversal() {
		ref, dir, decisao, code, err := uc.resolveReference(ctx, r, cmd, w)
		if err != nil {
			return ProcessWagerResult{}, err
		}
		switch decisao {
		case resolutionWait:
			return uc.waitResult(ctx, r, tx, cmd, agora)
		case resolutionReject:
			return uc.rejectResult(ctx, r, tx, code, agora, cmd.CorrelationID)
		}
		if err := tx.ResolveReference(ref.ID(), agora); err != nil {
			return ProcessWagerResult{}, err
		}
		direcao = dir
	}

	versaoAnterior := w.Version()
	entry, movErr := uc.move(w, tx, cmd, direcao, agora)
	if movErr != nil {
		code, recusa := classifyMovement(movErr, cmd.Kind)
		if !recusa {
			return ProcessWagerResult{}, movErr
		}
		return uc.rejectResult(ctx, r, tx, code, agora, cmd.CorrelationID)
	}

	if err := tx.MarkProcessed(w.Balance(), agora); err != nil {
		return ProcessWagerResult{}, err
	}
	if err := r.Transactions.Insert(ctx, tx); err != nil {
		return ProcessWagerResult{}, err
	}

	emitidos := []events.Payload{processedPayload(tx, w, cmd)}

	if entry != nil {
		if err := r.Ledger.Insert(ctx, entry); err != nil {
			return ProcessWagerResult{}, err
		}
		if err := r.Wallets.UpdateBalance(ctx, w, versaoAnterior); err != nil {
			return ProcessWagerResult{}, err
		}
		emitidos = append(emitidos, balanceChangedPayload(tx, w, entry, agora))
	}

	if err := uc.emit(ctx, r, emitidos, cmd.CorrelationID, tx.ID().String(), agora); err != nil {
		return ProcessWagerResult{}, err
	}

	return ProcessWagerResult{
		TransactionID: tx.ID(),
		Status:        tx.Status(),
		Balance:       tx.ResultBalance(),
	}, nil

}

// checkWallet aplica as regras que dependem da carteira já travada.
func (uc *ProcessWager) checkWallet(
	w *wallet.Wallet, cmd ProcessWagerCommand,
) (wagering.FailureCode, bool) {
	if w.PlayerID() != cmd.PlayerID {
		return wagering.FailureWalletPlayerMismatch, true
	}
	if w.Currency() != cmd.Money.Currency() {
		return wagering.FailureCurrencyMismatch, true
	}
	return "", false
}

// move aplica a movimentação do tipo. LOSS não movimenta: devolve lançamento
// nulo e a carteira segue intacta, sem avançar a versão.
func (uc *ProcessWager) move(
	w *wallet.Wallet, tx *wagering.WagerTransaction, cmd ProcessWagerCommand,
	direcao wallet.Direction, agora time.Time,
) (*wallet.LedgerEntry, error) {
	if !cmd.Kind.MovesBalance() {
		return nil, nil
	}

	entryID, err := uc.ids.New()
	if err != nil {
		return nil, fmt.Errorf("app: falha ao gerar identificador: %w", err)
	}

	switch direcao {
	case wallet.Debit:
		return w.Debit(entryID, tx.ID(), cmd.Money, agora)
	case wallet.Credit:
		return w.Credit(entryID, tx.ID(), cmd.Money, agora)
	default:
		return nil, fmt.Errorf("%w: %s sem movimentação definida", ErrInvalidCommand, cmd.Kind)
	}
}

// movementDirection devolve o sentido fixo dos tipos que não dependem de
// referência. Reversões não aparecem aqui: o sentido delas vem do que estão
// desfazendo.
func movementDirection(kind wagering.Kind) wallet.Direction {
	switch kind {
	case wagering.Bet:
		return wallet.Debit
	case wagering.Win:
		return wallet.Credit
	default:
		return ""
	}
}

// classifyMovement distingue recusa de negócio de erro de infraestrutura.
func classifyMovement(err error, kind wagering.Kind) (wagering.FailureCode, bool) {
	switch {
	case errors.Is(err, wallet.ErrInsufficientFunds):
		if kind.IsReversal() {
			// Uma reversão sem saldo tem causa e ação corretiva diferentes de
			// uma aposta sem saldo, então leva código próprio.
			return wagering.FailureReversalInsufficientFunds, true
		}
		return wagering.FailureInsufficientFunds, true
	case errors.Is(err, wallet.ErrCurrencyMismatch):
		return wagering.FailureCurrencyMismatch, true
	case errors.Is(err, wallet.ErrNonPositiveAmount):
		return wagering.FailureInvalidAmount, true
	default:
		return "", false
	}
}

// reject registra a recusa e a confirma.
//
// A recusa é um desfecho, não uma falha: ela commita a transação em REJECTED e
// o evento correspondente. O que não acontece é lançamento no ledger nem
// mudança de saldo. Sem isso, um reenvio reprocessaria a operação recusada em
// vez de devolver o resultado já conhecido.
func (uc *ProcessWager) reject(
	ctx context.Context, r *postgres.Repositories, tx *wagering.WagerTransaction,
	code wagering.FailureCode, agora time.Time, correlationID string, out *ProcessWagerResult,
) error {
	return uc.rejectWith(ctx, r, tx, code, agora, correlationID, out, r.Transactions.Insert)
}

// rejectResult recusa a operação e devolve o resultado.
func (uc *ProcessWager) rejectResult(
	ctx context.Context, r *postgres.Repositories, tx *wagering.WagerTransaction,
	code wagering.FailureCode, agora time.Time, correlationID string,
) (ProcessWagerResult, error) {
	var out ProcessWagerResult
	err := uc.reject(ctx, r, tx, code, agora, correlationID, &out)
	return out, err
}

// waitResult registra a pendência e devolve o resultado.
func (uc *ProcessWager) waitResult(
	ctx context.Context, r *postgres.Repositories, tx *wagering.WagerTransaction,
	cmd ProcessWagerCommand, agora time.Time,
) (ProcessWagerResult, error) {
	var out ProcessWagerResult
	err := uc.waitForReference(ctx, r, tx, cmd, agora, &out)
	return out, err
}

// rejectWith recusa a operação persistindo-a pelo caminho indicado.
//
// A entrada normal insere uma transação nova; o worker de referências atualiza
// uma que já existe. O resto da recusa — transição, evento, resultado — é o
// mesmo nos dois casos, e manter um caminho só evita que eles divirjam.
func (uc *ProcessWager) rejectWith(
	ctx context.Context, r *postgres.Repositories, tx *wagering.WagerTransaction,
	code wagering.FailureCode, agora time.Time, correlationID string, out *ProcessWagerResult,
	persist func(context.Context, *wagering.WagerTransaction) error,
) error {
	if err := tx.Reject(code, agora); err != nil {
		return err
	}
	if err := persist(ctx, tx); err != nil {
		return err
	}
	payload := events.WagerTransactionRejected{
		TransactionID:         tx.ID(),
		ProviderID:            tx.ProviderID(),
		ExternalTransactionID: tx.ExternalTransactionID(),
		WalletID:              tx.WalletID(),
		PlayerID:              tx.PlayerID(),
		Kind:                  tx.Kind(),
		Money:                 tx.Amount(),
		FailureCode:           code,
		Correctable:           code.Correctable(),
		RejectedAt:            rfc3339(agora),
	}
	if err := uc.emit(ctx, r, []events.Payload{payload}, correlationID, "", agora); err != nil {
		return err
	}

	*out = ProcessWagerResult{
		TransactionID: tx.ID(),
		Status:        tx.Status(),
		FailureCode:   code,
	}
	return nil
}

// emit grava os eventos na outbox, dentro da mesma transação.
func (uc *ProcessWager) emit(
	ctx context.Context, r *postgres.Repositories, payloads []events.Payload,
	correlationID, causationID string, agora time.Time,
) error {
	for _, p := range payloads {
		id, err := uc.ids.New()
		if err != nil {
			return fmt.Errorf("app: falha ao gerar identificador de evento: %w", err)
		}
		e, err := events.New(id, p, correlationID, causationID, agora)
		if err != nil {
			return err
		}
		if err := r.Outbox.Insert(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

func processedPayload(
	tx *wagering.WagerTransaction, w *wallet.Wallet, cmd ProcessWagerCommand,
) events.WagerTransactionProcessed {
	return events.WagerTransactionProcessed{
		TransactionID:         tx.ID(),
		ProviderID:            tx.ProviderID(),
		ExternalTransactionID: tx.ExternalTransactionID(),
		WalletID:              tx.WalletID(),
		PlayerID:              tx.PlayerID(),
		RoundID:               tx.RoundID(),
		GameID:                tx.GameID(),
		Kind:                  tx.Kind(),
		Money:                 tx.Amount(),
		Balance:               w.Balance(),
		ProcessedAt:           rfc3339(tx.UpdatedAt()),
	}
}

func balanceChangedPayload(
	tx *wagering.WagerTransaction, w *wallet.Wallet, entry *wallet.LedgerEntry, agora time.Time,
) events.WalletBalanceChanged {
	return events.WalletBalanceChanged{
		WalletID:      w.ID(),
		TransactionID: tx.ID(),
		Direction:     entry.Direction(),
		Money:         entry.Amount(),
		BalanceBefore: entry.BalanceBefore(),
		BalanceAfter:  entry.BalanceAfter(),
		WalletVersion: w.Version(),
		ChangedAt:     rfc3339(agora),
	}
}

// isDuplicate reconhece as violações de unicidade que significam "outra
// requisição com a mesma identidade chegou primeiro".
func isDuplicate(err error) bool {
	return errors.Is(err, postgres.ErrDuplicateExternalTransaction) ||
		errors.Is(err, postgres.ErrDuplicateIdempotencyKey)
}

// rfc3339 formata um instante no padrão do contrato externo: UTC, com
// milissegundos e sufixo Z.
func rfc3339(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
