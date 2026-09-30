// Package wagering implementa a transação de aposta e sua máquina de estados.
//
// A transação é o registro durável de uma operação: ela nasce no aceite,
// caminha por no máximo uma transição intermediária e termina em um dos três
// estados definitivos. Depois disso é imutável — um reenvio consulta o
// resultado guardado em vez de reaplicar a operação.
//
// O pacote não conhece Fx, HTTP, SQS nem persistência. Instantes e
// identificadores chegam por parâmetro.
package wagering

import (
	"bytes"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jonatancruz/jungle/internal/domain/money"
)

// WagerTransaction é a operação registrada, interna ou de provedor.
//
// O estado é encapsulado e só muda pelos métodos de transição, que validam a
// máquina de estados. A origem não é campo: deriva do tipo, então não há como
// os dois divergirem.
type WagerTransaction struct {
	id       uuid.UUID
	kind     Kind
	status   Status
	walletID uuid.UUID
	playerID uuid.UUID
	amount   money.Money

	// Metadados de operação externa. Vazios na abertura interna.
	providerID            string
	externalTransactionID string
	idempotencyKey        string
	payloadHash           []byte
	roundID               string
	gameID                string

	// Referência de reversão. referenceTransactionID é uuid.Nil enquanto a
	// referência não tiver sido resolvida.
	referenceExternalTransactionID string
	referenceTransactionID         uuid.UUID

	// Resultado devolvido ao provedor. resultBalance fica não inicializado
	// enquanto a operação não é concluída.
	resultBalance money.Money
	failureCode   FailureCode

	createdAt time.Time
	updatedAt time.Time
}

// ExternalRequest reúne os dados de uma operação recebida de provedor.
type ExternalRequest struct {
	ID                             uuid.UUID
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    []byte
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Amount                         money.Money
	ReferenceExternalTransactionID string
	Now                            time.Time
}

// OpeningRequest reúne os dados da transação interna de abertura de carteira.
type OpeningRequest struct {
	ID       uuid.UUID
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Amount   money.Money
	Now      time.Time
}

// NewExternal registra uma operação de provedor no estado PENDING.
//
// Todas as regras estruturais são impostas aqui: tipo permitido para a origem,
// política de valor por tipo e obrigatoriedade da referência. Uma transação que
// chega a existir já é estruturalmente válida; o que sobra para o caso de uso
// são as regras que dependem do estado da carteira.
func NewExternal(req ExternalRequest) (*WagerTransaction, error) {
	if req.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: transação sem id", ErrInvalidID)
	}
	if req.WalletID == uuid.Nil {
		return nil, fmt.Errorf("%w: transação sem carteira", ErrInvalidID)
	}
	if req.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: transação sem jogador", ErrInvalidID)
	}
	if req.Now.IsZero() {
		return nil, fmt.Errorf("%w: transação sem instante", ErrInvalidTimestamp)
	}

	if req.Kind.Source() != External {
		return nil, fmt.Errorf("%w: %q não pode vir de provedor", ErrKindNotAllowed, req.Kind)
	}
	if _, err := ParseKind(req.Kind.String()); err != nil {
		return nil, err
	}

	for field, value := range map[string]string{
		"providerId":            req.ProviderID,
		"externalTransactionId": req.ExternalTransactionID,
		"idempotencyKey":        req.IdempotencyKey,
		"roundId":               req.RoundID,
		"gameId":                req.GameID,
	} {
		if value == "" {
			return nil, fmt.Errorf("%w: %s", ErrMissingField, field)
		}
	}
	if len(req.PayloadHash) == 0 {
		return nil, fmt.Errorf("%w: payloadHash", ErrMissingField)
	}

	if err := validateAmountPolicy(req.Kind, req.Amount); err != nil {
		return nil, err
	}
	if err := validateReferencePolicy(req.Kind, req.ReferenceExternalTransactionID); err != nil {
		return nil, err
	}

	return &WagerTransaction{
		id:                             req.ID,
		kind:                           req.Kind,
		status:                         Pending,
		walletID:                       req.WalletID,
		playerID:                       req.PlayerID,
		amount:                         req.Amount,
		providerID:                     req.ProviderID,
		externalTransactionID:          req.ExternalTransactionID,
		idempotencyKey:                 req.IdempotencyKey,
		payloadHash:                    bytes.Clone(req.PayloadHash),
		roundID:                        req.RoundID,
		gameID:                         req.GameID,
		referenceExternalTransactionID: req.ReferenceExternalTransactionID,
		createdAt:                      req.Now,
		updatedAt:                      req.Now,
	}, nil
}

// NewOpening registra a transação interna de abertura já concluída.
//
// A abertura nasce em PROCESSED porque acontece no mesmo commit da carteira e
// do seu lançamento de crédito: não existe janela em que ela esteja pendente.
// O saldo resultante é o próprio saldo inicial.
func NewOpening(req OpeningRequest) (*WagerTransaction, error) {
	if req.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: abertura sem id", ErrInvalidID)
	}
	if req.WalletID == uuid.Nil {
		return nil, fmt.Errorf("%w: abertura sem carteira", ErrInvalidID)
	}
	if req.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: abertura sem jogador", ErrInvalidID)
	}
	if req.Now.IsZero() {
		return nil, fmt.Errorf("%w: abertura sem instante", ErrInvalidTimestamp)
	}
	if req.Amount.IsUninitialized() {
		return nil, fmt.Errorf("%w: abertura sem moeda", money.ErrUninitialized)
	}
	// Abertura com saldo zero não cria transação alguma, então chegar aqui com
	// valor não positivo é erro de uso.
	if !req.Amount.IsPositive() {
		return nil, fmt.Errorf("%w: abertura exige valor positivo, recebido %s", ErrAmountPolicy, req.Amount)
	}

	return &WagerTransaction{
		id:            req.ID,
		kind:          Opening,
		status:        Processed,
		walletID:      req.WalletID,
		playerID:      req.PlayerID,
		amount:        req.Amount,
		resultBalance: req.Amount,
		createdAt:     req.Now,
		updatedAt:     req.Now,
	}, nil
}

// validateAmountPolicy impõe a política de valor por tipo: LOSS exige
// exatamente zero e os demais exigem valor positivo.
func validateAmountPolicy(kind Kind, amount money.Money) error {
	if amount.IsUninitialized() {
		return fmt.Errorf("%w: valor sem moeda", money.ErrUninitialized)
	}
	if kind.RequiresPositiveAmount() {
		if !amount.IsPositive() {
			return fmt.Errorf("%w: %s exige valor positivo, recebido %s", ErrAmountPolicy, kind, amount)
		}
		return nil
	}
	if !amount.IsZero() {
		return fmt.Errorf("%w: %s exige valor zero, recebido %s", ErrAmountPolicy, kind, amount)
	}
	return nil
}

// validateReferencePolicy impõe que reversões tragam referência e que os demais
// tipos não a tragam.
func validateReferencePolicy(kind Kind, reference string) error {
	if kind.IsReversal() && reference == "" {
		return fmt.Errorf("%w: %s exige referenceExternalTransactionId", ErrReferencePolicy, kind)
	}
	if !kind.IsReversal() && reference != "" {
		return fmt.Errorf("%w: %s não admite referência", ErrReferencePolicy, kind)
	}
	return nil
}

// MarkProcessed conclui a operação com sucesso e congela o saldo observado.
//
// O saldo é congelado e não relido depois: um replay precisa devolver o saldo
// do processamento original, mesmo que a carteira já tenha se movimentado.
func (t *WagerTransaction) MarkProcessed(resultBalance money.Money, now time.Time) error {
	if resultBalance.IsUninitialized() {
		return fmt.Errorf("%w: saldo resultante sem moeda", money.ErrUninitialized)
	}
	if resultBalance.IsNegative() {
		return fmt.Errorf("%w: saldo resultante de %s", ErrInconsistentSnapshot, resultBalance)
	}
	if resultBalance.Currency() != t.amount.Currency() {
		return fmt.Errorf("%w: saldo em %s para operação em %s",
			money.ErrCurrencyMismatch, resultBalance.Currency(), t.amount.Currency())
	}
	if err := t.transition(Processed, now); err != nil {
		return err
	}
	t.resultBalance = resultBalance
	return nil
}

// MarkPendingReference registra a espera pela referência.
//
// Só reversões esperam: os demais tipos não dependem de referência e portanto
// não têm o que aguardar.
func (t *WagerTransaction) MarkPendingReference(now time.Time) error {
	if !t.kind.IsReversal() {
		return fmt.Errorf("%w: %s não depende de referência", ErrInvalidTransition, t.kind)
	}
	return t.transition(PendingReference, now)
}

// ResolveReference registra a transação interna que a referência externa
// resolveu. Não é transição de estado: apenas completa o registro, e continua
// possível enquanto a transação não for terminal.
func (t *WagerTransaction) ResolveReference(referenceID uuid.UUID, now time.Time) error {
	if !t.kind.IsReversal() {
		return fmt.Errorf("%w: %s não admite referência", ErrReferencePolicy, t.kind)
	}
	if referenceID == uuid.Nil {
		return fmt.Errorf("%w: referência resolvida sem id", ErrInvalidID)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: resolução sem instante", ErrInvalidTimestamp)
	}
	if t.status.IsTerminal() {
		return fmt.Errorf("%w: %s está em %s", ErrTerminalStatus, t.id, t.status)
	}
	t.referenceTransactionID = referenceID
	t.updatedAt = now
	return nil
}

// Reject recusa a operação por regra de negócio, de forma definitiva.
func (t *WagerTransaction) Reject(code FailureCode, now time.Time) error {
	return t.terminate(Rejected, code, now)
}

// Fail registra falha permanente de infraestrutura para auditoria.
func (t *WagerTransaction) Fail(code FailureCode, now time.Time) error {
	return t.terminate(Failed, code, now)
}

// terminate concentra os dois desfechos que carregam código de falha.
func (t *WagerTransaction) terminate(status Status, code FailureCode, now time.Time) error {
	if !code.IsKnown() {
		return fmt.Errorf("%w: %q", ErrInvalidFailureCode, code)
	}
	if err := t.transition(status, now); err != nil {
		return err
	}
	t.failureCode = code
	return nil
}

// transition aplica a máquina de estados. O estado só muda depois que a
// transição é validada, então uma transição recusada deixa a transação intacta.
func (t *WagerTransaction) transition(next Status, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("%w: transição sem instante", ErrInvalidTimestamp)
	}
	if t.status.IsTerminal() {
		return fmt.Errorf("%w: %s está em %s e não vai para %s", ErrTerminalStatus, t.id, t.status, next)
	}
	if !t.status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.status, next)
	}
	t.status = next
	t.updatedAt = now
	return nil
}

// ID devolve o identificador interno.
func (t *WagerTransaction) ID() uuid.UUID { return t.id }

// Kind devolve o tipo da operação.
func (t *WagerTransaction) Kind() Kind { return t.kind }

// Source devolve a origem, derivada do tipo.
func (t *WagerTransaction) Source() Source { return t.kind.Source() }

// Status devolve o estado atual.
func (t *WagerTransaction) Status() Status { return t.status }

// WalletID devolve a carteira alvo.
func (t *WagerTransaction) WalletID() uuid.UUID { return t.walletID }

// PlayerID devolve o jogador.
func (t *WagerTransaction) PlayerID() uuid.UUID { return t.playerID }

// Amount devolve o valor da operação.
func (t *WagerTransaction) Amount() money.Money { return t.amount }

// ProviderID devolve o provedor, vazio na abertura interna.
func (t *WagerTransaction) ProviderID() string { return t.providerID }

// ExternalTransactionID devolve o identificador do provedor.
func (t *WagerTransaction) ExternalTransactionID() string { return t.externalTransactionID }

// IdempotencyKey devolve a chave recebida, nunca uma recalculada.
func (t *WagerTransaction) IdempotencyKey() string { return t.idempotencyKey }

// PayloadHash devolve uma cópia do hash do payload de negócio.
func (t *WagerTransaction) PayloadHash() []byte { return bytes.Clone(t.payloadHash) }

// HasSamePayload compara o hash persistido com o de uma nova chegada. É o que
// distingue replay legítimo de reutilização de chave com conteúdo diferente.
func (t *WagerTransaction) HasSamePayload(hash []byte) bool {
	return bytes.Equal(t.payloadHash, hash)
}

// RoundID devolve a rodada.
func (t *WagerTransaction) RoundID() string { return t.roundID }

// GameID devolve o jogo.
func (t *WagerTransaction) GameID() string { return t.gameID }

// ReferenceExternalTransactionID devolve a referência informada pelo provedor.
func (t *WagerTransaction) ReferenceExternalTransactionID() string {
	return t.referenceExternalTransactionID
}

// ReferenceTransactionID devolve a referência já resolvida, ou uuid.Nil.
func (t *WagerTransaction) ReferenceTransactionID() uuid.UUID { return t.referenceTransactionID }

// ResultBalance devolve o saldo congelado no processamento. Não inicializado
// enquanto a operação não tiver sido concluída com sucesso.
func (t *WagerTransaction) ResultBalance() money.Money { return t.resultBalance }

// FailureCode devolve o motivo da recusa ou falha, vazio nos demais estados.
func (t *WagerTransaction) FailureCode() FailureCode { return t.failureCode }

// CreatedAt devolve o instante do aceite.
func (t *WagerTransaction) CreatedAt() time.Time { return t.createdAt }

// UpdatedAt devolve o instante da última transição.
func (t *WagerTransaction) UpdatedAt() time.Time { return t.updatedAt }
