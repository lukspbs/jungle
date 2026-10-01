package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/events"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/test/dbtest"
)

var instante = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func abridor(t *testing.T) (*app.OpenWallet, *postgres.Store) {
	t.Helper()
	store := dbtest.Store(t)
	return app.NewOpenWallet(store, relogioFixo{instante}, novosIDs(t)), store
}

func TestAberturaComSaldoConfirmaTudoNumCommit(t *testing.T) {
	uc, store := abridor(t)
	ctx := context.Background()
	playerID := uuid.New()

	res, err := uc.Execute(ctx, app.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: brlOf(t, "1000.00"),
		CorrelationID:  "corr-abertura",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	w := res.Wallet
	if w.Balance().String() != "1000.00" || w.Version() != 1 {
		t.Errorf("carteira = %q/v%d, esperado \"1000.00\"/v1", w.Balance(), w.Version())
	}

	t.Run("a carteira está no banco", func(t *testing.T) {
		lida, err := store.Read().Wallets.FindByID(ctx, w.ID())
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		if !lida.Balance().Equal(w.Balance()) || lida.Version() != 1 {
			t.Errorf("persistida = %q/v%d", lida.Balance(), lida.Version())
		}
	})

	t.Run("a abertura nasceu PROCESSED", func(t *testing.T) {
		pagina, err := store.Read().Ledger.ListByWallet(ctx, w.ID(), 0, 10)
		if err != nil {
			t.Fatalf("ListByWallet: %v", err)
		}
		if len(pagina.Entries) != 1 {
			t.Fatalf("lançamentos = %d, esperado 1", len(pagina.Entries))
		}
		entry := pagina.Entries[0]
		if entry.BalanceBefore().String() != "0.00" || entry.BalanceAfter().String() != "1000.00" {
			t.Errorf("lançamento = %q -> %q", entry.BalanceBefore(), entry.BalanceAfter())
		}

		tx, err := store.Read().Transactions.FindByID(ctx, entry.TransactionID())
		if err != nil {
			t.Fatalf("FindByID da transação: %v", err)
		}
		if tx.Kind() != wagering.Opening || tx.Status() != wagering.Processed {
			t.Errorf("transação = %v/%v, esperado OPENING/PROCESSED", tx.Kind(), tx.Status())
		}
		if tx.Source() != wagering.Internal {
			t.Errorf("origem = %v, esperado INTERNAL", tx.Source())
		}
		if tx.ResultBalance().String() != "1000.00" {
			t.Errorf("saldo congelado = %q", tx.ResultBalance())
		}
	})

	t.Run("os dois eventos estão na outbox", func(t *testing.T) {
		pagina, _ := store.Read().Ledger.ListByWallet(ctx, w.ID(), 0, 10)
		txID := pagina.Entries[0].TransactionID()

		daTransacao, err := store.Read().Outbox.ListByAggregate(ctx, events.AggregateWagerTransaction, txID)
		if err != nil {
			t.Fatalf("ListByAggregate: %v", err)
		}
		daCarteira, err := store.Read().Outbox.ListByAggregate(ctx, events.AggregateWallet, w.ID())
		if err != nil {
			t.Fatalf("ListByAggregate: %v", err)
		}

		if len(daTransacao) != 1 || daTransacao[0].EventType != string(events.TypeWagerTransactionProcessed) {
			t.Errorf("eventos da transação = %v", resumo(daTransacao))
		}
		if len(daCarteira) != 1 || daCarteira[0].EventType != string(events.TypeWalletBalanceChanged) {
			t.Errorf("eventos da carteira = %v", resumo(daCarteira))
		}
		for _, rec := range append(daTransacao, daCarteira...) {
			if rec.CorrelationID != "corr-abertura" {
				t.Errorf("%s sem o correlationId da requisição", rec.EventType)
			}
		}
	})

	t.Run("os eventos internos omitem metadados de provedor", func(t *testing.T) {
		pagina, _ := store.Read().Ledger.ListByWallet(ctx, w.ID(), 0, 10)
		recs, _ := store.Read().Outbox.ListByAggregate(ctx,
			events.AggregateWagerTransaction, pagina.Entries[0].TransactionID())

		var env map[string]json.RawMessage
		if err := json.Unmarshal(recs[0].Payload, &env); err != nil {
			t.Fatalf("payload inválido: %v", err)
		}
		var data map[string]any
		if err := json.Unmarshal(env["data"], &data); err != nil {
			t.Fatalf("data inválido: %v", err)
		}
		for _, inaplicavel := range []string{"providerId", "externalTransactionId", "roundId", "gameId"} {
			if _, presente := data[inaplicavel]; presente {
				t.Errorf("evento de abertura carregou %q", inaplicavel)
			}
		}
		if data["kind"] != "OPENING" {
			t.Errorf("kind = %v, esperado OPENING", data["kind"])
		}
	})

	t.Run("o saldo reconstruído bate com o armazenado", func(t *testing.T) {
		resumo, err := store.Read().Ledger.Summarize(ctx, w.ID(), money.MustCurrency("BRL"))
		if err != nil {
			t.Fatalf("Summarize: %v", err)
		}
		if !resumo.Balance.Equal(w.Balance()) {
			t.Errorf("ledger diz %q, carteira diz %q", resumo.Balance, w.Balance())
		}
	})
}

func TestAberturaComSaldoZeroNaoCriaNada(t *testing.T) {
	uc, store := abridor(t)
	ctx := context.Background()

	res, err := uc.Execute(ctx, app.OpenWalletCommand{
		PlayerID:       uuid.New(),
		InitialBalance: brlOf(t, "0.00"),
		CorrelationID:  "corr-zero",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	w := res.Wallet

	if !w.Balance().IsZero() || w.Version() != 1 {
		t.Errorf("carteira = %q/v%d", w.Balance(), w.Version())
	}

	pagina, err := store.Read().Ledger.ListByWallet(ctx, w.ID(), 0, 10)
	if err != nil {
		t.Fatalf("ListByWallet: %v", err)
	}
	if len(pagina.Entries) != 0 {
		t.Errorf("lançamentos = %d, esperado nenhum", len(pagina.Entries))
	}

	daCarteira, _ := store.Read().Outbox.ListByAggregate(ctx, events.AggregateWallet, w.ID())
	if len(daCarteira) != 0 {
		t.Errorf("eventos = %d, esperado nenhum: não houve movimentação", len(daCarteira))
	}
}

func TestSegundaAberturaEhConflito(t *testing.T) {
	uc, _ := abridor(t)
	ctx := context.Background()
	playerID := uuid.New()

	cmd := app.OpenWalletCommand{
		PlayerID: playerID, InitialBalance: brlOf(t, "100.00"), CorrelationID: "corr-1",
	}
	if _, err := uc.Execute(ctx, cmd); err != nil {
		t.Fatalf("primeira abertura: %v", err)
	}

	cmd.CorrelationID = "corr-2"
	_, err := uc.Execute(ctx, cmd)
	if !errors.Is(err, app.ErrWalletAlreadyExists) {
		t.Fatalf("segunda abertura devolveu %v, esperado ErrWalletAlreadyExists", err)
	}
}

func TestAberturaRejeitaComandoInvalido(t *testing.T) {
	uc, _ := abridor(t)
	ctx := context.Background()

	negativo, err := money.FromMinorUnits(-1, money.MustCurrency("BRL"))
	if err != nil {
		t.Fatalf("FromMinorUnits: %v", err)
	}

	tests := []struct {
		name string
		cmd  app.OpenWalletCommand
	}{
		{"sem jogador", app.OpenWalletCommand{
			InitialBalance: brlOf(t, "10.00"), CorrelationID: "c"}},
		{"sem correlationId", app.OpenWalletCommand{
			PlayerID: uuid.New(), InitialBalance: brlOf(t, "10.00")}},
		{"saldo sem moeda", app.OpenWalletCommand{
			PlayerID: uuid.New(), CorrelationID: "c"}},
		{"saldo negativo", app.OpenWalletCommand{
			PlayerID: uuid.New(), InitialBalance: negativo, CorrelationID: "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := uc.Execute(ctx, tt.cmd)
			if !errors.Is(err, app.ErrInvalidCommand) {
				t.Fatalf("devolveu %v, esperado ErrInvalidCommand", err)
			}
			if res.Wallet != nil {
				t.Error("devolveu carteira junto com o erro")
			}
		})
	}
}

func resumo(recs []postgres.OutboxRecord) []string {
	var tipos []string
	for _, r := range recs {
		tipos = append(tipos, r.EventType)
	}
	return tipos
}
