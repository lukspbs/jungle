// Package wallet implementa o agregado financeiro da carteira.
//
// A carteira é a raiz do agregado e o lançamento do ledger vive dentro dela,
// no mesmo pacote e não em um pacote irmão. A razão é a invariante: um
// lançamento afirma "o saldo foi de X para Y", e se ele pudesse ser construído
// fora do agregado essa afirmação poderia divergir do saldo real. Aqui o único
// caminho para obter um lançamento é movimentar a carteira, então os dois não
// têm como discordar.
//
// O pacote não conhece Fx, HTTP, SQS nem biblioteca de persistência. Instantes
// e identificadores chegam por parâmetro, o que mantém as transições
// determinísticas e testáveis sem relógio nem gerador globais.
package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jonatancruz/jungle/internal/domain/money"
)

// Wallet é a carteira de um jogador em uma moeda.
//
// O estado é totalmente encapsulado: não há campo exportado nem setter. O
// saldo só muda por Debit e Credit, que devolvem o lançamento correspondente,
// de modo que é impossível mover o saldo sem produzir a contrapartida no
// ledger.
type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	currency  money.Currency
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// OpeningRequest reúne os dados da abertura de uma carteira.
//
// TransactionID e EntryID identificam a transação OPENING e seu lançamento de
// crédito. Só são exigidos quando o saldo inicial é positivo: abertura com
// saldo zero não cria transação nem lançamento.
type OpeningRequest struct {
	WalletID       uuid.UUID
	PlayerID       uuid.UUID
	TransactionID  uuid.UUID
	EntryID        uuid.UUID
	InitialBalance money.Money
	Now            time.Time
}

// Snapshot é a forma persistida da carteira.
type Snapshot struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Open cria uma carteira.
//
// A versão nasce em 1 e permanece em 1 após a abertura, mesmo quando há saldo
// inicial: o crédito de abertura faz parte do estado inicial, não é uma
// movimentação posterior.
//
// O lançamento devolvido é nil quando o saldo inicial é zero — nesse caso não
// há transação de abertura, lançamento nem evento financeiro.
func Open(req OpeningRequest) (*Wallet, *LedgerEntry, error) {
	if req.WalletID == uuid.Nil {
		return nil, nil, fmt.Errorf("%w: carteira sem id", ErrInvalidID)
	}
	if req.PlayerID == uuid.Nil {
		return nil, nil, fmt.Errorf("%w: carteira sem jogador", ErrInvalidID)
	}
	if req.Now.IsZero() {
		return nil, nil, fmt.Errorf("%w: abertura sem instante", ErrInvalidTimestamp)
	}
	if req.InitialBalance.IsUninitialized() {
		return nil, nil, fmt.Errorf("%w: saldo inicial sem moeda", money.ErrUninitialized)
	}
	if req.InitialBalance.IsNegative() {
		return nil, nil, fmt.Errorf("%w: saldo inicial de %s", ErrNegativeBalance, req.InitialBalance)
	}

	w := &Wallet{
		id:        req.WalletID,
		playerID:  req.PlayerID,
		currency:  req.InitialBalance.Currency(),
		balance:   req.InitialBalance,
		version:   1,
		createdAt: req.Now,
		updatedAt: req.Now,
	}

	if !req.InitialBalance.IsPositive() {
		return w, nil, nil
	}

	if req.TransactionID == uuid.Nil {
		return nil, nil, fmt.Errorf("%w: abertura com saldo exige transação", ErrInvalidID)
	}
	if req.EntryID == uuid.Nil {
		return nil, nil, fmt.Errorf("%w: abertura com saldo exige lançamento", ErrInvalidID)
	}

	zero, err := money.Zero(w.currency)
	if err != nil {
		return nil, nil, err
	}

	entry := &LedgerEntry{
		id:            req.EntryID,
		walletID:      w.id,
		transactionID: req.TransactionID,
		direction:     Credit,
		amount:        req.InitialBalance,
		balanceBefore: zero,
		balanceAfter:  req.InitialBalance,
		createdAt:     req.Now,
	}
	return w, entry, nil
}

// Rehydrate reconstrói a carteira a partir do estado persistido.
//
// Reidratação não reaplica movimentações nem emite eventos: ela apenas
// restaura o estado e recusa um estado que o agregado não poderia ter
// produzido.
func Rehydrate(s Snapshot) (*Wallet, error) {
	if s.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: carteira sem id", ErrInvalidID)
	}
	if s.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: carteira sem jogador", ErrInvalidID)
	}
	if s.Balance.IsUninitialized() {
		return nil, fmt.Errorf("%w: saldo sem moeda", money.ErrUninitialized)
	}
	if s.Balance.IsNegative() {
		return nil, fmt.Errorf("%w: saldo persistido de %s", ErrNegativeBalance, s.Balance)
	}
	if s.Version < 1 {
		return nil, fmt.Errorf("%w: versão %d", ErrInvalidVersion, s.Version)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("%w: carteira sem instantes de criação e atualização", ErrInvalidTimestamp)
	}
	if s.UpdatedAt.Before(s.CreatedAt) {
		return nil, fmt.Errorf("%w: atualização anterior à criação", ErrInconsistentSnapshot)
	}

	return &Wallet{
		id:        s.ID,
		playerID:  s.PlayerID,
		currency:  s.Balance.Currency(),
		balance:   s.Balance,
		version:   s.Version,
		createdAt: s.CreatedAt,
		updatedAt: s.UpdatedAt,
	}, nil
}

// Debit reduz o saldo e devolve o lançamento correspondente.
//
// Um débito que deixaria o saldo negativo é recusado com ErrInsufficientFunds
// e a carteira permanece intacta.
func (w *Wallet) Debit(entryID, transactionID uuid.UUID, amount money.Money, now time.Time) (*LedgerEntry, error) {
	return w.apply(Debit, entryID, transactionID, amount, now)
}

// Credit aumenta o saldo e devolve o lançamento correspondente.
func (w *Wallet) Credit(entryID, transactionID uuid.UUID, amount money.Money, now time.Time) (*LedgerEntry, error) {
	return w.apply(Credit, entryID, transactionID, amount, now)
}

// apply concentra a movimentação. A carteira só é mutada depois que todas as
// validações passaram e o lançamento foi construído: uma operação recusada
// jamais deixa o agregado meio alterado.
func (w *Wallet) apply(direction Direction, entryID, transactionID uuid.UUID, amount money.Money, now time.Time) (*LedgerEntry, error) {
	if entryID == uuid.Nil {
		return nil, fmt.Errorf("%w: movimentação sem id de lançamento", ErrInvalidID)
	}
	if transactionID == uuid.Nil {
		return nil, fmt.Errorf("%w: movimentação sem transação", ErrInvalidID)
	}
	if now.IsZero() {
		return nil, fmt.Errorf("%w: movimentação sem instante", ErrInvalidTimestamp)
	}
	if amount.IsUninitialized() {
		return nil, fmt.Errorf("%w: movimentação sem moeda", money.ErrUninitialized)
	}
	if amount.Currency() != w.currency {
		return nil, fmt.Errorf("%w: %s em carteira %s", ErrCurrencyMismatch, amount.Currency(), w.currency)
	}
	if !amount.IsPositive() {
		return nil, fmt.Errorf("%w: %s", ErrNonPositiveAmount, amount)
	}

	before := w.balance

	var after money.Money
	var err error
	switch direction {
	case Credit:
		after, err = before.Add(amount)
	case Debit:
		after, err = before.Sub(amount)
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidDirection, direction)
	}
	if err != nil {
		return nil, err
	}

	if after.IsNegative() {
		return nil, fmt.Errorf("%w: débito de %s sobre saldo de %s", ErrInsufficientFunds, amount, before)
	}

	entry := &LedgerEntry{
		id:            entryID,
		walletID:      w.id,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: before,
		balanceAfter:  after,
		createdAt:     now,
	}

	w.balance = after
	w.version++
	w.updatedAt = now

	return entry, nil
}

// ID devolve o identificador da carteira.
func (w *Wallet) ID() uuid.UUID { return w.id }

// PlayerID devolve o jogador dono da carteira.
func (w *Wallet) PlayerID() uuid.UUID { return w.playerID }

// Currency devolve a moeda da carteira.
func (w *Wallet) Currency() money.Currency { return w.currency }

// Balance devolve o saldo atual.
func (w *Wallet) Balance() money.Money { return w.balance }

// Version devolve a versão atual, usada para detectar lost update.
func (w *Wallet) Version() int64 { return w.version }

// CreatedAt devolve o instante de criação.
func (w *Wallet) CreatedAt() time.Time { return w.createdAt }

// UpdatedAt devolve o instante da última alteração de saldo.
func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }
