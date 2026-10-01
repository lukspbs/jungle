package events

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/domain/wallet"
)

// Os metadados de provedor levam omitempty porque a abertura interna de
// carteira produz os mesmos eventos sem eles: provedor, id externo, rodada e
// jogo não se aplicam àquela origem.

// WagerTransactionProcessed é emitido na conclusão bem-sucedida de uma
// operação, incluindo LOSS — que conclui sem movimentar saldo.
type WagerTransactionProcessed struct {
	TransactionID         uuid.UUID     `json:"transactionId"`
	ProviderID            string        `json:"providerId,omitempty"`
	ExternalTransactionID string        `json:"externalTransactionId,omitempty"`
	WalletID              uuid.UUID     `json:"walletId"`
	PlayerID              uuid.UUID     `json:"playerId"`
	RoundID               string        `json:"roundId,omitempty"`
	GameID                string        `json:"gameId,omitempty"`
	Kind                  wagering.Kind `json:"kind"`
	Money                 money.Money   `json:"money"`
	Balance               money.Money   `json:"balance"`
	ProcessedAt           string        `json:"processedAt"`
}

func (WagerTransactionProcessed) Type() Type           { return TypeWagerTransactionProcessed }
func (WagerTransactionProcessed) Version() int         { return 1 }
func (WagerTransactionProcessed) Aggregate() Aggregate { return AggregateWagerTransaction }

func (p WagerTransactionProcessed) AggregateID() uuid.UUID { return p.TransactionID }

func (p WagerTransactionProcessed) validate() error {
	if p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return fmt.Errorf("%w: %s sem carteira ou jogador", ErrInvalidPayload, p.Type())
	}
	if p.Money.IsUninitialized() || p.Balance.IsUninitialized() {
		return fmt.Errorf("%w: %s sem valor ou saldo", ErrInvalidPayload, p.Type())
	}
	if p.ProcessedAt == "" {
		return fmt.Errorf("%w: %s sem instante de conclusão", ErrInvalidPayload, p.Type())
	}
	return nil
}

// WagerTransactionRejected é emitido na recusa definitiva por regra de negócio.
type WagerTransactionRejected struct {
	TransactionID         uuid.UUID            `json:"transactionId"`
	ProviderID            string               `json:"providerId,omitempty"`
	ExternalTransactionID string               `json:"externalTransactionId,omitempty"`
	WalletID              uuid.UUID            `json:"walletId"`
	PlayerID              uuid.UUID            `json:"playerId"`
	Kind                  wagering.Kind        `json:"kind"`
	Money                 money.Money          `json:"money"`
	FailureCode           wagering.FailureCode `json:"failureCode"`

	// Correctable acompanha o código para que o consumidor decida sem manter
	// uma cópia da tabela de códigos.
	Correctable bool   `json:"correctable"`
	RejectedAt  string `json:"rejectedAt"`
}

func (WagerTransactionRejected) Type() Type           { return TypeWagerTransactionRejected }
func (WagerTransactionRejected) Version() int         { return 1 }
func (WagerTransactionRejected) Aggregate() Aggregate { return AggregateWagerTransaction }

func (p WagerTransactionRejected) AggregateID() uuid.UUID { return p.TransactionID }

func (p WagerTransactionRejected) validate() error {
	if p.WalletID == uuid.Nil {
		return fmt.Errorf("%w: %s sem carteira", ErrInvalidPayload, p.Type())
	}
	if !p.FailureCode.IsKnown() {
		return fmt.Errorf("%w: %s com código desconhecido %q", ErrInvalidPayload, p.Type(), p.FailureCode)
	}
	if p.RejectedAt == "" {
		return fmt.Errorf("%w: %s sem instante de recusa", ErrInvalidPayload, p.Type())
	}
	return nil
}

// WagerTransactionPendingReference é emitido quando a operação passa a aguardar
// uma referência ainda indisponível.
type WagerTransactionPendingReference struct {
	TransactionID                  uuid.UUID     `json:"transactionId"`
	ProviderID                     string        `json:"providerId"`
	ExternalTransactionID          string        `json:"externalTransactionId"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId"`
	WalletID                       uuid.UUID     `json:"walletId"`
	PlayerID                       uuid.UUID     `json:"playerId"`
	Kind                           wagering.Kind `json:"kind"`
	Money                          money.Money   `json:"money"`

	// ExpiresAt informa até quando a referência será procurada. Depois disso a
	// operação é recusada com código de referência não encontrada.
	ExpiresAt string `json:"expiresAt"`
	PendingAt string `json:"pendingAt"`
}

func (WagerTransactionPendingReference) Type() Type   { return TypeWagerTransactionPendingReference }
func (WagerTransactionPendingReference) Version() int { return 1 }

func (WagerTransactionPendingReference) Aggregate() Aggregate { return AggregateWagerTransaction }

func (p WagerTransactionPendingReference) AggregateID() uuid.UUID { return p.TransactionID }

func (p WagerTransactionPendingReference) validate() error {
	if p.ReferenceExternalTransactionID == "" {
		return fmt.Errorf("%w: %s sem referência", ErrInvalidPayload, p.Type())
	}
	if !p.Kind.IsReversal() {
		return fmt.Errorf("%w: %s para %s, que não depende de referência", ErrInvalidPayload, p.Type(), p.Kind)
	}
	if p.ExpiresAt == "" || p.PendingAt == "" {
		return fmt.Errorf("%w: %s sem prazos", ErrInvalidPayload, p.Type())
	}
	return nil
}

// WalletBalanceChanged é emitido em toda alteração efetiva de saldo.
//
// LOSS não produz este evento: ele conclui sem movimentação.
type WalletBalanceChanged struct {
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     wallet.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
	ChangedAt     string           `json:"changedAt"`
}

func (WalletBalanceChanged) Type() Type           { return TypeWalletBalanceChanged }
func (WalletBalanceChanged) Version() int         { return 1 }
func (WalletBalanceChanged) Aggregate() Aggregate { return AggregateWallet }

func (p WalletBalanceChanged) AggregateID() uuid.UUID { return p.WalletID }

// validate confere a aritmética do próprio payload. Um evento que afirma um
// saldo que não decorre do movimento seria indistinguível de um correto para o
// consumidor, então a incoerência é barrada na origem.
func (p WalletBalanceChanged) validate() error {
	if p.TransactionID == uuid.Nil {
		return fmt.Errorf("%w: %s sem transação", ErrInvalidPayload, p.Type())
	}
	if p.WalletVersion < 1 {
		return fmt.Errorf("%w: %s com versão %d", ErrInvalidPayload, p.Type(), p.WalletVersion)
	}
	if p.Money.IsUninitialized() || p.BalanceBefore.IsUninitialized() || p.BalanceAfter.IsUninitialized() {
		return fmt.Errorf("%w: %s com valor sem moeda", ErrInvalidPayload, p.Type())
	}
	if !p.Money.IsPositive() {
		return fmt.Errorf("%w: %s com movimento de %s", ErrInvalidPayload, p.Type(), p.Money)
	}

	var esperado money.Money
	var err error
	switch p.Direction {
	case wallet.Credit:
		esperado, err = p.BalanceBefore.Add(p.Money)
	case wallet.Debit:
		esperado, err = p.BalanceBefore.Sub(p.Money)
	default:
		return fmt.Errorf("%w: %s com direção %q", ErrInvalidPayload, p.Type(), p.Direction)
	}
	if err != nil {
		return err
	}
	if !esperado.Equal(p.BalanceAfter) {
		return fmt.Errorf("%w: %s afirma %s -> %s para %s de %s, que daria %s",
			ErrInvalidPayload, p.Type(), p.BalanceBefore, p.BalanceAfter,
			p.Direction, p.Money, esperado)
	}
	if p.ChangedAt == "" {
		return fmt.Errorf("%w: %s sem instante", ErrInvalidPayload, p.Type())
	}
	return nil
}
