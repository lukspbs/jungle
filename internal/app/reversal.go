package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/domain/events"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/domain/wallet"
)

// ReferencePolicy governa a espera por uma referência ainda indisponível.
type ReferencePolicy struct {
	TTL            time.Duration
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// backoffFor dobra o recuo a cada tentativa até o teto.
//
// A duplicação é por deslocamento de bits, em aritmética inteira. math.Pow
// resolveria, mas traria ponto flutuante para dentro do módulo sem necessidade,
// e a trava automática do projeto proíbe float em qualquer lugar — não só onde
// há dinheiro. Um teto de deslocamento evita estouro em contagens absurdas.
func (p ReferencePolicy) backoffFor(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	const maxShift = 32
	deslocamento := attempts - 1
	if deslocamento > maxShift {
		return p.MaxBackoff
	}
	recuo := p.InitialBackoff << deslocamento
	if recuo > p.MaxBackoff || recuo <= 0 {
		return p.MaxBackoff
	}
	return recuo
}

// resolution descreve o que fazer com uma reversão depois de consultar a
// referência.
type resolution int

const (
	// resolutionApply indica referência resolvida e reversão aplicável.
	resolutionApply resolution = iota
	// resolutionWait indica referência ainda indisponível ou em andamento.
	resolutionWait
	// resolutionReject indica recusa definitiva.
	resolutionReject
)

// resolveReference consulta a referência e decide o desfecho.
//
// Os três resultados possíveis correspondem às três situações do desafio: a
// referência chegou e está boa, ainda não chegou (ou não terminou), ou chegou
// mas não serve.
func (uc *ProcessWager) resolveReference(
	ctx context.Context, r *postgres.Repositories,
	cmd ProcessWagerCommand, w *wallet.Wallet,
) (ref *wagering.WagerTransaction, dir wallet.Direction, res resolution, code wagering.FailureCode, err error) {
	ref, err = r.Transactions.FindByProviderAndExternalID(
		ctx, cmd.ProviderID, cmd.ReferenceExternalTransactionID)

	if errors.Is(err, postgres.ErrTransactionNotFound) {
		// A reversão chegou antes da transação que ela desfaz. É esperado numa
		// entrega at-least-once sem ordem garantida.
		return nil, "", resolutionWait, "", nil
	}
	if err != nil {
		return nil, "", resolutionReject, "", err
	}

	switch ref.Status() {
	case wagering.Pending, wagering.PendingReference:
		// A referência existe mas ainda não tem desfecho. Esperar é melhor que
		// recusar: ela pode concluir com sucesso daqui a instantes.
		return ref, "", resolutionWait, "", nil
	case wagering.Rejected, wagering.Failed:
		// Terminou sem sucesso: não há movimentação a desfazer, e esperar não
		// mudaria nada porque o estado é terminal.
		return ref, "", resolutionReject, wagering.FailureReferenceNotProcessed, nil
	}

	if code, recusa := checkReferenceAgreement(ref, cmd, w); recusa {
		return ref, "", resolutionReject, code, nil
	}

	dir, ok := reversalDirection(ref.Kind(), cmd.Kind)
	if !ok {
		return ref, "", resolutionReject, wagering.FailureReferenceMismatch, nil
	}

	// Uma referência admite no máximo uma reversão bem-sucedida, de qualquer
	// tipo. O índice único do banco cobre duas do mesmo tipo; esta checagem
	// cobre a combinação REFUND + ROLLBACK, que passaria pelo índice e
	// devolveria o mesmo débito duas vezes. A consulta acontece sob o lock da
	// carteira, então não há janela entre decidir e gravar.
	existente, err := r.Transactions.FindProcessedReversal(ctx, ref.ID())
	switch {
	case err == nil && existente != nil:
		return ref, "", resolutionReject, wagering.FailureReferenceAlreadyReversed, nil
	case err != nil && !errors.Is(err, postgres.ErrTransactionNotFound):
		return ref, "", resolutionReject, "", err
	}

	return ref, dir, resolutionApply, "", nil
}

// checkReferenceAgreement exige que a reversão e a referência descrevam a mesma
// aposta: mesmo jogador, mesma carteira, mesma rodada, mesma moeda e mesmo
// valor.
func checkReferenceAgreement(
	ref *wagering.WagerTransaction, cmd ProcessWagerCommand, w *wallet.Wallet,
) (wagering.FailureCode, bool) {
	if ref.PlayerID() != cmd.PlayerID ||
		ref.WalletID() != cmd.WalletID ||
		ref.WalletID() != w.ID() ||
		ref.RoundID() != cmd.RoundID ||
		ref.Amount().Currency() != cmd.Money.Currency() {
		return wagering.FailureReferenceMismatch, true
	}
	// Reversões parciais estão fora do escopo: o valor precisa ser idêntico.
	if !ref.Amount().Equal(cmd.Money) {
		return wagering.FailureReferenceAmountMismatch, true
	}
	return "", false
}

// reversalDirection devolve o sentido da reversão, que é sempre o oposto do
// movimento que ela desfaz.
//
// REFUND devolve uma aposta, então só incide sobre BET. ROLLBACK desfaz
// qualquer operação que tenha movimentado saldo.
func reversalDirection(refKind, reversalKind wagering.Kind) (wallet.Direction, bool) {
	if reversalKind == wagering.Refund && refKind != wagering.Bet {
		return "", false
	}
	switch refKind {
	case wagering.Bet:
		// A aposta debitou; desfazê-la credita.
		return wallet.Credit, true
	case wagering.Win, wagering.Refund:
		// Ambas creditaram; desfazê-las debita.
		return wallet.Debit, true
	default:
		// OPENING, LOSS e a própria ROLLBACK não são reversíveis.
		return "", false
	}
}

// waitForReference registra a pendência e confirma.
//
// O registro é durável de propósito: outra instância assume a retentativa, e a
// pendência sobrevive ao reinício de todos os processos. A mensagem de entrada
// pode ser concluída aqui, porque daqui em diante o worker é quem continua.
func (uc *ProcessWager) waitForReference(
	ctx context.Context, r *postgres.Repositories, tx *wagering.WagerTransaction,
	cmd ProcessWagerCommand, agora time.Time, out *ProcessWagerResult,
) error {
	if err := tx.MarkPendingReference(agora); err != nil {
		return err
	}
	if err := r.Transactions.Insert(ctx, tx); err != nil {
		return err
	}

	expiraEm := agora.Add(uc.reference.TTL)
	proxima := agora.Add(uc.reference.backoffFor(1))
	if err := r.Transactions.ScheduleReferenceRetry(ctx, tx.ID(), proxima, expiraEm); err != nil {
		return err
	}

	payload := events.WagerTransactionPendingReference{
		TransactionID:                  tx.ID(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		WalletID:                       tx.WalletID(),
		PlayerID:                       tx.PlayerID(),
		Kind:                           tx.Kind(),
		Money:                          tx.Amount(),
		ExpiresAt:                      rfc3339(expiraEm),
		PendingAt:                      rfc3339(agora),
	}
	if err := uc.emit(ctx, r, []events.Payload{payload}, cmd.CorrelationID, "", agora); err != nil {
		return err
	}

	*out = ProcessWagerResult{TransactionID: tx.ID(), Status: tx.Status()}
	return nil
}

// validateReversal confere o que dá para conferir antes de tocar no banco.
func validateReversal(cmd ProcessWagerCommand) error {
	if !cmd.Kind.IsReversal() {
		if cmd.ReferenceExternalTransactionID != "" {
			return fmt.Errorf("%w: %s não admite referência", ErrInvalidCommand, cmd.Kind)
		}
		return nil
	}
	if cmd.ReferenceExternalTransactionID == "" {
		return fmt.Errorf("%w: %s exige referenceExternalTransactionId", ErrInvalidCommand, cmd.Kind)
	}
	if cmd.ReferenceExternalTransactionID == cmd.ExternalTransactionID {
		return fmt.Errorf("%w: a reversão não pode referenciar a si mesma", ErrInvalidCommand)
	}
	return nil
}
