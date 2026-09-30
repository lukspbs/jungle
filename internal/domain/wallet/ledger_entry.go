package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jonatancruz/jungle/internal/domain/money"
)

// Direction é o sentido de um lançamento.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

// ParseDirection converte a forma persistida em Direction.
func ParseDirection(s string) (Direction, error) {
	switch Direction(s) {
	case Debit:
		return Debit, nil
	case Credit:
		return Credit, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidDirection, s)
	}
}

// String devolve a forma persistida.
func (d Direction) String() string { return string(d) }

// LedgerEntry é um lançamento do ledger da carteira.
//
// O lançamento é imutável: não há setter, e o único caminho de criação no fluxo
// normal é através do agregado, em Wallet.Debit, Wallet.Credit e Open. Isso é
// deliberado — se o lançamento pudesse ser construído à parte, ele poderia
// discordar do saldo que diz descrever.
//
// RehydrateLedgerEntry existe para a leitura do banco e revalida a aritmética,
// de modo que um dado corrompido na persistência não passe despercebido.
type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// LedgerEntrySnapshot é a forma persistida de um lançamento.
type LedgerEntrySnapshot struct {
	ID            uuid.UUID
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	CreatedAt     time.Time
}

// RehydrateLedgerEntry reconstrói um lançamento vindo do banco.
//
// Reidratação não é reaplicação: nada é movimentado e nenhum evento é emitido.
// A validação existe apenas para recusar um estado que não poderia ter sido
// produzido pelo agregado.
func RehydrateLedgerEntry(s LedgerEntrySnapshot) (*LedgerEntry, error) {
	if s.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: lançamento sem id", ErrInvalidID)
	}
	if s.WalletID == uuid.Nil {
		return nil, fmt.Errorf("%w: lançamento sem carteira", ErrInvalidID)
	}
	if s.TransactionID == uuid.Nil {
		return nil, fmt.Errorf("%w: lançamento sem transação", ErrInvalidID)
	}
	if s.Direction != Debit && s.Direction != Credit {
		return nil, fmt.Errorf("%w: %q", ErrInvalidDirection, s.Direction)
	}
	if s.CreatedAt.IsZero() {
		return nil, fmt.Errorf("%w: lançamento sem instante de criação", ErrInvalidTimestamp)
	}
	if err := validateLedgerAmounts(s.Direction, s.Amount, s.BalanceBefore, s.BalanceAfter); err != nil {
		return nil, err
	}

	return &LedgerEntry{
		id:            s.ID,
		walletID:      s.WalletID,
		transactionID: s.TransactionID,
		direction:     s.Direction,
		amount:        s.Amount,
		balanceBefore: s.BalanceBefore,
		balanceAfter:  s.BalanceAfter,
		createdAt:     s.CreatedAt,
	}, nil
}

// validateLedgerAmounts impõe a invariante balanceAfter = balanceBefore ± money
// e a não negatividade dos saldos. O banco impõe a mesma regra por CHECK; ter
// as duas camadas é intencional, porque uma cobre a escrita nova e a outra
// cobre o dado já gravado.
func validateLedgerAmounts(direction Direction, amount, before, after money.Money) error {
	if !amount.IsPositive() {
		return fmt.Errorf("%w: lançamento de %s", ErrNonPositiveAmount, amount)
	}
	if before.IsNegative() || after.IsNegative() {
		return fmt.Errorf("%w: lançamento com saldo %s -> %s", ErrNegativeBalance, before, after)
	}

	var expected money.Money
	var err error
	switch direction {
	case Credit:
		expected, err = before.Add(amount)
	case Debit:
		expected, err = before.Sub(amount)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidDirection, direction)
	}
	if err != nil {
		return err
	}

	if !expected.Equal(after) {
		return fmt.Errorf("%w: %s %s sobre %s deveria resultar em %s, não %s",
			ErrLedgerArithmetic, direction, amount, before, expected, after)
	}
	return nil
}

// ID devolve o identificador do lançamento.
func (e *LedgerEntry) ID() uuid.UUID { return e.id }

// WalletID devolve a carteira movimentada.
func (e *LedgerEntry) WalletID() uuid.UUID { return e.walletID }

// TransactionID devolve a transação que originou o lançamento.
func (e *LedgerEntry) TransactionID() uuid.UUID { return e.transactionID }

// Direction devolve o sentido do lançamento.
func (e *LedgerEntry) Direction() Direction { return e.direction }

// Amount devolve o valor movimentado, sempre positivo.
func (e *LedgerEntry) Amount() money.Money { return e.amount }

// BalanceBefore devolve o saldo anterior ao lançamento.
func (e *LedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }

// BalanceAfter devolve o saldo posterior ao lançamento.
func (e *LedgerEntry) BalanceAfter() money.Money { return e.balanceAfter }

// CreatedAt devolve o instante do lançamento.
func (e *LedgerEntry) CreatedAt() time.Time { return e.createdAt }
