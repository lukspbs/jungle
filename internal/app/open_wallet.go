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

// OpenWalletCommand é o pedido de abertura de carteira.
type OpenWalletCommand struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
	CorrelationID  string
}

// OpenWalletResult é a carteira recém-aberta.
type OpenWalletResult struct {
	Wallet *wallet.Wallet
}

// OpenWallet abre a carteira de um jogador.
//
// A abertura com saldo positivo confirma num único commit: a carteira, a
// transação OPENING já em PROCESSED, o lançamento de crédito e os dois eventos
// de integração. Não existe instante em que a carteira exista com saldo e o
// ledger ainda não tenha a contrapartida.
//
// A abertura com saldo zero não cria transação, lançamento nem evento
// financeiro: não houve movimentação a registrar.
type OpenWallet struct {
	store *postgres.Store
	clock Clock
	ids   IDs
}

// NewOpenWallet monta o caso de uso.
func NewOpenWallet(store *postgres.Store, clock Clock, ids IDs) *OpenWallet {
	return &OpenWallet{store: store, clock: clock, ids: ids}
}

// Execute abre a carteira.
func (uc *OpenWallet) Execute(ctx context.Context, cmd OpenWalletCommand) (OpenWalletResult, error) {
	if cmd.PlayerID == uuid.Nil {
		return OpenWalletResult{}, fmt.Errorf("%w: playerId ausente", ErrInvalidCommand)
	}
	if cmd.CorrelationID == "" {
		return OpenWalletResult{}, fmt.Errorf("%w: correlationId ausente", ErrInvalidCommand)
	}
	if cmd.InitialBalance.IsUninitialized() {
		return OpenWalletResult{}, fmt.Errorf("%w: saldo inicial sem moeda", ErrInvalidCommand)
	}
	if cmd.InitialBalance.IsNegative() {
		return OpenWalletResult{}, fmt.Errorf("%w: saldo inicial negativo", ErrInvalidCommand)
	}

	agora := uc.clock.Now()
	comSaldo := cmd.InitialBalance.IsPositive()

	ids, err := uc.reserveIDs(comSaldo)
	if err != nil {
		return OpenWalletResult{}, err
	}

	w, entry, err := wallet.Open(wallet.OpeningRequest{
		WalletID: ids.wallet, PlayerID: cmd.PlayerID,
		TransactionID: ids.transaction, EntryID: ids.entry,
		InitialBalance: cmd.InitialBalance, Now: agora,
	})
	if err != nil {
		return OpenWalletResult{}, fmt.Errorf("%w: %s", ErrInvalidCommand, err)
	}

	// Saldo zero: só a carteira.
	if entry == nil {
		if err := uc.persist(ctx, w, nil, nil, nil); err != nil {
			return OpenWalletResult{}, err
		}
		return OpenWalletResult{Wallet: w}, nil
	}

	opening, err := wagering.NewOpening(wagering.OpeningRequest{
		ID: ids.transaction, WalletID: w.ID(), PlayerID: w.PlayerID(),
		Amount: cmd.InitialBalance, Now: agora,
	})
	if err != nil {
		return OpenWalletResult{}, err
	}

	emitidos, err := uc.openingEvents(ids, w, opening, entry, cmd.CorrelationID, agora)
	if err != nil {
		return OpenWalletResult{}, err
	}

	if err := uc.persist(ctx, w, opening, entry, emitidos); err != nil {
		return OpenWalletResult{}, err
	}
	return OpenWalletResult{Wallet: w}, nil
}

// openingIDs reúne os identificadores reservados antes da transação. Gerá-los
// fora do commit mantém a transação curta: ela não espera por nada além do
// banco.
type openingIDs struct {
	wallet      uuid.UUID
	transaction uuid.UUID
	entry       uuid.UUID
	processed   uuid.UUID
	balance     uuid.UUID
}

func (uc *OpenWallet) reserveIDs(comSaldo bool) (openingIDs, error) {
	var ids openingIDs
	var err error

	if ids.wallet, err = uc.ids.New(); err != nil {
		return openingIDs{}, fmt.Errorf("app: falha ao gerar identificador: %w", err)
	}
	if !comSaldo {
		return ids, nil
	}
	for _, destino := range []*uuid.UUID{&ids.transaction, &ids.entry, &ids.processed, &ids.balance} {
		if *destino, err = uc.ids.New(); err != nil {
			return openingIDs{}, fmt.Errorf("app: falha ao gerar identificador: %w", err)
		}
	}
	return ids, nil
}

// openingEvents monta os dois eventos exigidos para a abertura com saldo.
//
// Os metadados de provedor ficam de fora: esta origem é interna, e o contrato
// dos eventos omite os campos inaplicáveis em vez de preenchê-los com vazio.
func (uc *OpenWallet) openingEvents(
	ids openingIDs, w *wallet.Wallet, opening *wagering.WagerTransaction,
	entry *wallet.LedgerEntry, correlationID string, agora time.Time,
) ([]events.Event, error) {
	instante := agora.UTC().Format("2006-01-02T15:04:05.000Z")

	processed, err := events.New(ids.processed, events.WagerTransactionProcessed{
		TransactionID: opening.ID(),
		WalletID:      w.ID(),
		PlayerID:      w.PlayerID(),
		Kind:          opening.Kind(),
		Money:         opening.Amount(),
		Balance:       w.Balance(),
		ProcessedAt:   instante,
	}, correlationID, "", agora)
	if err != nil {
		return nil, err
	}

	balanceChanged, err := events.New(ids.balance, events.WalletBalanceChanged{
		WalletID:      w.ID(),
		TransactionID: opening.ID(),
		Direction:     entry.Direction(),
		Money:         entry.Amount(),
		BalanceBefore: entry.BalanceBefore(),
		BalanceAfter:  entry.BalanceAfter(),
		WalletVersion: w.Version(),
		ChangedAt:     instante,
	}, correlationID, opening.ID().String(), agora)
	if err != nil {
		return nil, err
	}

	return []events.Event{processed, balanceChanged}, nil
}

// persist confirma tudo num commit só.
func (uc *OpenWallet) persist(
	ctx context.Context, w *wallet.Wallet, opening *wagering.WagerTransaction,
	entry *wallet.LedgerEntry, emitidos []events.Event,
) error {
	err := uc.store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		if err := r.Wallets.Insert(ctx, w); err != nil {
			return err
		}
		if opening == nil {
			return nil
		}
		if err := r.Transactions.Insert(ctx, opening); err != nil {
			return err
		}
		if err := r.Ledger.Insert(ctx, entry); err != nil {
			return err
		}
		for _, e := range emitidos {
			if err := r.Outbox.Insert(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})

	switch {
	case err == nil:
		return nil
	case errors.Is(err, postgres.ErrWalletAlreadyExists):
		return fmt.Errorf("%w: jogador %s", ErrWalletAlreadyExists, w.PlayerID())
	default:
		return err
	}
}
