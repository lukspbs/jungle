package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/domain/wallet"
	"github.com/lukspbs/jungle/test/dbtest"
)

var brl = money.MustCurrency("BRL")

func brlOf(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q): %v", amount, err)
	}
	return m
}

func novaID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	return id
}

// abreCarteira cria uma carteira com saldo, com sua transação de abertura e o
// lançamento de crédito, tudo no mesmo commit — como o caso de uso fará.
func abreCarteira(t *testing.T, store *postgres.Store, saldo string) *wallet.Wallet {
	t.Helper()
	ctx := context.Background()
	agora := time.Now().UTC().Truncate(time.Microsecond)

	w, entry, err := wallet.Open(wallet.OpeningRequest{
		WalletID: novaID(t), PlayerID: novaID(t),
		TransactionID: novaID(t), EntryID: novaID(t),
		InitialBalance: brlOf(t, saldo), Now: agora,
	})
	if err != nil {
		t.Fatalf("wallet.Open: %v", err)
	}

	opening, err := wagering.NewOpening(wagering.OpeningRequest{
		ID: entry.TransactionID(), WalletID: w.ID(), PlayerID: w.PlayerID(),
		Amount: brlOf(t, saldo), Now: agora,
	})
	if err != nil {
		t.Fatalf("wagering.NewOpening: %v", err)
	}

	err = store.InTx(ctx, func(ctx context.Context, repos *postgres.Repositories) error {
		if err := repos.Wallets.Insert(ctx, w); err != nil {
			return err
		}
		if err := repos.Transactions.Insert(ctx, opening); err != nil {
			return err
		}
		return repos.Ledger.Insert(ctx, entry)
	})
	if err != nil {
		t.Fatalf("abertura da carteira: %v", err)
	}
	return w
}

func novaOperacao(t *testing.T, w *wallet.Wallet, provider, externalID string, kind wagering.Kind, valor string) *wagering.WagerTransaction {
	t.Helper()
	tx, err := wagering.NewExternal(wagering.ExternalRequest{
		ID: novaID(t), ProviderID: provider,
		ExternalTransactionID: externalID,
		IdempotencyKey:        provider + ":" + externalID,
		PayloadHash:           []byte{0xaa, 0xbb},
		WalletID:              w.ID(), PlayerID: w.PlayerID(),
		RoundID: "round-987", GameID: "fortune-chimp",
		Kind: kind, Amount: brlOf(t, valor),
		Now: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("wagering.NewExternal: %v", err)
	}
	return tx
}

func TestCarteiraIdaEVolta(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "1000.00")

	lida, err := store.Read().Wallets.FindByID(ctx, w.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if !lida.Balance().Equal(w.Balance()) {
		t.Errorf("saldo = %q, esperado %q", lida.Balance(), w.Balance())
	}
	if lida.Version() != 1 || lida.PlayerID() != w.PlayerID() {
		t.Errorf("versão/jogador = %d/%v", lida.Version(), lida.PlayerID())
	}

	porJogador, err := store.Read().Wallets.FindByPlayerAndCurrency(ctx, w.PlayerID(), brl)
	if err != nil {
		t.Fatalf("FindByPlayerAndCurrency: %v", err)
	}
	if porJogador.ID() != w.ID() {
		t.Error("busca por (jogador, moeda) devolveu outra carteira")
	}

	if _, err := store.Read().Wallets.FindByID(ctx, novaID(t)); !errors.Is(err, postgres.ErrWalletNotFound) {
		t.Errorf("carteira inexistente devolveu %v, esperado ErrWalletNotFound", err)
	}
}

func TestSegundaCarteiraParaMesmoJogadorEMoeda(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "100.00")

	duplicada, _, err := wallet.Open(wallet.OpeningRequest{
		WalletID: novaID(t), PlayerID: w.PlayerID(),
		InitialBalance: brlOf(t, "0.00"), Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("wallet.Open: %v", err)
	}

	err = store.InTx(ctx, func(ctx context.Context, repos *postgres.Repositories) error {
		return repos.Wallets.Insert(ctx, duplicada)
	})
	if !errors.Is(err, postgres.ErrWalletAlreadyExists) {
		t.Errorf("devolveu %v, esperado ErrWalletAlreadyExists", err)
	}
}

func TestLockEAtualizacaoDeSaldo(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "100.00")
	aposta := novaOperacao(t, w, "provider-a", "ext-"+w.ID().String(), wagering.Bet, "25.00")

	err := store.InTx(ctx, func(ctx context.Context, repos *postgres.Repositories) error {
		travada, err := repos.Wallets.LockForUpdate(ctx, w.ID())
		if err != nil {
			return err
		}
		versaoAnterior := travada.Version()

		entry, err := travada.Debit(novaID(t), aposta.ID(), brlOf(t, "25.00"), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := repos.Transactions.Insert(ctx, aposta); err != nil {
			return err
		}
		if err := repos.Ledger.Insert(ctx, entry); err != nil {
			return err
		}
		return repos.Wallets.UpdateBalance(ctx, travada, versaoAnterior)
	})
	if err != nil {
		t.Fatalf("débito: %v", err)
	}

	lida, err := store.Read().Wallets.FindByID(ctx, w.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if lida.Balance().String() != "75.00" {
		t.Errorf("saldo = %q, esperado \"75.00\"", lida.Balance())
	}
	if lida.Version() != 2 {
		t.Errorf("versão = %d, esperado 2", lida.Version())
	}
}

// TestVersaoDesatualizadaEhDetectada prova que a cláusula de versão pega uma
// escrita que não passou pelo lock, em vez de sobrescrever silenciosamente.
func TestVersaoDesatualizadaEhDetectada(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "100.00")

	err := store.InTx(ctx, func(ctx context.Context, repos *postgres.Repositories) error {
		travada, err := repos.Wallets.LockForUpdate(ctx, w.ID())
		if err != nil {
			return err
		}
		if _, err := travada.Credit(novaID(t), novaID(t), brlOf(t, "10.00"), time.Now().UTC()); err != nil {
			return err
		}
		// Versão esperada propositalmente errada.
		return repos.Wallets.UpdateBalance(ctx, travada, 99)
	})
	if !errors.Is(err, postgres.ErrConcurrentUpdate) {
		t.Fatalf("devolveu %v, esperado ErrConcurrentUpdate", err)
	}

	lida, _ := store.Read().Wallets.FindByID(ctx, w.ID())
	if lida.Balance().String() != "100.00" {
		t.Errorf("saldo mudou para %q apesar da detecção", lida.Balance())
	}
}

func TestIdempotenciaPersistida(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "1000.00")
	externalID := "ext-" + w.ID().String()
	aposta := novaOperacao(t, w, "provider-a", externalID, wagering.Bet, "25.00")

	if err := store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		return r.Transactions.Insert(ctx, aposta)
	}); err != nil {
		t.Fatalf("primeira inserção: %v", err)
	}

	t.Run("mesma operação sob outra chave é recusada", func(t *testing.T) {
		outra := novaOperacao(t, w, "provider-a", externalID, wagering.Bet, "25.00")
		err := store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
			return r.Transactions.Insert(ctx, outra)
		})
		if !errors.Is(err, postgres.ErrDuplicateExternalTransaction) {
			t.Errorf("devolveu %v, esperado ErrDuplicateExternalTransaction", err)
		}
	})

	t.Run("chave reutilizada em outra operação é recusada", func(t *testing.T) {
		outra, err := wagering.NewExternal(wagering.ExternalRequest{
			ID: novaID(t), ProviderID: "provider-a",
			ExternalTransactionID: externalID + "-diferente",
			IdempotencyKey:        "provider-a:" + externalID,
			PayloadHash:           []byte{0xcc},
			WalletID:              w.ID(), PlayerID: w.PlayerID(),
			RoundID: "round-987", GameID: "fortune-chimp",
			Kind: wagering.Bet, Amount: brlOf(t, "25.00"), Now: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("NewExternal: %v", err)
		}
		err = store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
			return r.Transactions.Insert(ctx, outra)
		})
		if !errors.Is(err, postgres.ErrDuplicateIdempotencyKey) {
			t.Errorf("devolveu %v, esperado ErrDuplicateIdempotencyKey", err)
		}
	})

	t.Run("o mesmo provedor recupera sua operação", func(t *testing.T) {
		lida, err := store.Read().Transactions.FindByProviderAndExternalID(ctx, "provider-a", externalID)
		if err != nil {
			t.Fatalf("FindByProviderAndExternalID: %v", err)
		}
		if lida.ID() != aposta.ID() || !lida.HasSamePayload([]byte{0xaa, 0xbb}) {
			t.Error("operação recuperada não confere")
		}
	})

	t.Run("outro provedor não enxerga a operação", func(t *testing.T) {
		_, err := store.Read().Transactions.FindByProviderAndExternalID(ctx, "provider-b", externalID)
		if !errors.Is(err, postgres.ErrTransactionNotFound) {
			t.Errorf("devolveu %v, esperado ErrTransactionNotFound", err)
		}
	})
}

func TestTransacaoTerminalNaoAceitaAtualizacao(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "1000.00")
	aposta := novaOperacao(t, w, "provider-a", "term-"+w.ID().String(), wagering.Bet, "25.00")

	if err := aposta.MarkProcessed(brlOf(t, "975.00"), time.Now().UTC()); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if err := store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		return r.Transactions.Insert(ctx, aposta)
	}); err != nil {
		t.Fatalf("inserção: %v", err)
	}

	// O domínio recusaria a transição, então forçamos o UPDATE direto para
	// provar que a trigger é uma camada independente da aplicação.
	err := store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		reidratada, err := wagering.Rehydrate(wagering.Snapshot{
			ID: aposta.ID(), Kind: wagering.Bet, Status: wagering.Rejected,
			WalletID: w.ID(), PlayerID: w.PlayerID(), Amount: brlOf(t, "25.00"),
			ProviderID: aposta.ProviderID(), ExternalTransactionID: aposta.ExternalTransactionID(),
			IdempotencyKey: aposta.IdempotencyKey(), PayloadHash: aposta.PayloadHash(),
			RoundID: "round-987", GameID: "fortune-chimp",
			FailureCode: wagering.FailureInsufficientFunds,
			CreatedAt:   aposta.CreatedAt(), UpdatedAt: time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		return r.Transactions.Update(ctx, reidratada)
	})
	if !errors.Is(err, postgres.ErrTerminalTransaction) {
		t.Errorf("devolveu %v, esperado ErrTerminalTransaction", err)
	}
}

func TestLedgerRecusaMovimentacaoDuplicada(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "1000.00")
	aposta := novaOperacao(t, w, "provider-a", "dup-"+w.ID().String(), wagering.Bet, "25.00")

	err := store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		travada, err := r.Wallets.LockForUpdate(ctx, w.ID())
		if err != nil {
			return err
		}
		primeiro, err := travada.Debit(novaID(t), aposta.ID(), brlOf(t, "25.00"), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := r.Transactions.Insert(ctx, aposta); err != nil {
			return err
		}
		if err := r.Ledger.Insert(ctx, primeiro); err != nil {
			return err
		}
		// Segundo lançamento para a mesma transação: a constraint recusa.
		segundo, err := travada.Debit(novaID(t), aposta.ID(), brlOf(t, "25.00"), time.Now().UTC())
		if err != nil {
			return err
		}
		return r.Ledger.Insert(ctx, segundo)
	})
	if !errors.Is(err, postgres.ErrDuplicateLedgerEntry) {
		t.Fatalf("devolveu %v, esperado ErrDuplicateLedgerEntry", err)
	}

	// E o rollback precisa ter desfeito tudo: nem o débito válido ficou.
	lida, _ := store.Read().Wallets.FindByID(ctx, w.ID())
	if lida.Balance().String() != "1000.00" {
		t.Errorf("saldo = %q, esperado \"1000.00\": o rollback não desfez a transação", lida.Balance())
	}
}

// TestDisputaDeDuasApostasEntreConexoes é o cenário obrigatório do desafio,
// agora com concorrência real: duas goroutines, conexões distintas, mesma
// carteira de 100.00, duas apostas de 80.00.
func TestDisputaDeDuasApostasEntreConexoes(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "100.00")

	type resultado struct{ err error }
	resultados := make([]resultado, 2)
	var wg sync.WaitGroup
	largada := make(chan struct{})

	for i := range resultados {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			aposta := novaOperacao(t, w, "provider-a",
				"race-"+w.ID().String()+"-"+uuid.NewString(), wagering.Bet, "80.00")

			<-largada
			resultados[i].err = store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
				travada, err := r.Wallets.LockForUpdate(ctx, w.ID())
				if err != nil {
					return err
				}
				versao := travada.Version()
				entry, err := travada.Debit(novaID(t), aposta.ID(), brlOf(t, "80.00"), time.Now().UTC())
				if err != nil {
					return err
				}
				if err := r.Transactions.Insert(ctx, aposta); err != nil {
					return err
				}
				if err := r.Ledger.Insert(ctx, entry); err != nil {
					return err
				}
				return r.Wallets.UpdateBalance(ctx, travada, versao)
			})
		}(i)
	}

	close(largada)
	wg.Wait()

	var aceitas, rejeitadas int
	for _, r := range resultados {
		switch {
		case r.err == nil:
			aceitas++
		case errors.Is(r.err, wallet.ErrInsufficientFunds):
			rejeitadas++
		default:
			t.Fatalf("erro inesperado: %v", r.err)
		}
	}

	if aceitas != 1 || rejeitadas != 1 {
		t.Fatalf("aceitas=%d rejeitadas=%d, esperado 1 e 1", aceitas, rejeitadas)
	}

	lida, err := store.Read().Wallets.FindByID(ctx, w.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if lida.Balance().String() != "20.00" {
		t.Errorf("saldo final = %q, esperado \"20.00\"", lida.Balance())
	}
	if lida.Version() != 2 {
		t.Errorf("versão = %d, esperado 2: só uma movimentação foi confirmada", lida.Version())
	}

	// Exatamente um débito no ledger, além do crédito de abertura.
	pagina, err := store.Read().Ledger.ListByWallet(ctx, w.ID(), 0, 50)
	if err != nil {
		t.Fatalf("ListByWallet: %v", err)
	}
	var debitos int
	for _, e := range pagina.Entries {
		if e.Direction() == wallet.Debit {
			debitos++
		}
	}
	if debitos != 1 {
		t.Errorf("débitos no ledger = %d, esperado 1", debitos)
	}
}

func TestLedgerPaginaEReconcilia(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "1000.00")

	movimentos := []string{"10.00", "20.00", "30.00", "40.00", "50.00"}
	for i, valor := range movimentos {
		aposta := novaOperacao(t, w, "provider-a",
			"page-"+w.ID().String()+"-"+uuid.NewString(), wagering.Bet, valor)
		if err := store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
			travada, err := r.Wallets.LockForUpdate(ctx, w.ID())
			if err != nil {
				return err
			}
			versao := travada.Version()
			entry, err := travada.Debit(novaID(t), aposta.ID(), brlOf(t, valor), time.Now().UTC())
			if err != nil {
				return err
			}
			if err := r.Transactions.Insert(ctx, aposta); err != nil {
				return err
			}
			if err := r.Ledger.Insert(ctx, entry); err != nil {
				return err
			}
			return r.Wallets.UpdateBalance(ctx, travada, versao)
		}); err != nil {
			t.Fatalf("movimento %d: %v", i, err)
		}
	}

	t.Run("paginação percorre tudo sem repetir nem pular", func(t *testing.T) {
		vistos := map[uuid.UUID]bool{}
		cursor := int64(0)
		paginas := 0
		for {
			pagina, err := store.Read().Ledger.ListByWallet(ctx, w.ID(), cursor, 2)
			if err != nil {
				t.Fatalf("ListByWallet: %v", err)
			}
			for _, e := range pagina.Entries {
				if vistos[e.ID()] {
					t.Fatalf("lançamento %v apareceu em duas páginas", e.ID())
				}
				vistos[e.ID()] = true
			}
			paginas++
			if pagina.NextCursor == 0 {
				break
			}
			cursor = pagina.NextCursor
			if paginas > 10 {
				t.Fatal("paginação não terminou")
			}
		}
		// 1 crédito de abertura + 5 débitos.
		if len(vistos) != 6 {
			t.Errorf("lançamentos vistos = %d, esperado 6", len(vistos))
		}
	})

	t.Run("reconciliação reconstrói o saldo a partir do ledger", func(t *testing.T) {
		resumo, err := store.Read().Ledger.Summarize(ctx, w.ID(), brl)
		if err != nil {
			t.Fatalf("Summarize: %v", err)
		}
		lida, _ := store.Read().Wallets.FindByID(ctx, w.ID())

		if !resumo.Balance.Equal(lida.Balance()) {
			t.Errorf("saldo reconstruído %q difere do armazenado %q", resumo.Balance, lida.Balance())
		}
		// 1000.00 - (10+20+30+40+50)
		if resumo.Balance.String() != "850.00" {
			t.Errorf("saldo reconstruído = %q, esperado \"850.00\"", resumo.Balance)
		}
		if resumo.Entries != 6 {
			t.Errorf("lançamentos contados = %d, esperado 6", resumo.Entries)
		}
	})
}

func TestRollbackDesfazTudo(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	w := abreCarteira(t, store, "500.00")
	aposta := novaOperacao(t, w, "provider-a", "rb-"+w.ID().String(), wagering.Bet, "25.00")
	falhaProposital := errors.New("falha proposital depois das escritas")

	err := store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		travada, err := r.Wallets.LockForUpdate(ctx, w.ID())
		if err != nil {
			return err
		}
		versao := travada.Version()
		entry, err := travada.Debit(novaID(t), aposta.ID(), brlOf(t, "25.00"), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := r.Transactions.Insert(ctx, aposta); err != nil {
			return err
		}
		if err := r.Ledger.Insert(ctx, entry); err != nil {
			return err
		}
		if err := r.Wallets.UpdateBalance(ctx, travada, versao); err != nil {
			return err
		}
		return falhaProposital
	})
	if !errors.Is(err, falhaProposital) {
		t.Fatalf("devolveu %v, esperado a falha proposital", err)
	}

	lida, _ := store.Read().Wallets.FindByID(ctx, w.ID())
	if lida.Balance().String() != "500.00" || lida.Version() != 1 {
		t.Errorf("carteira = %q/v%d, esperado \"500.00\"/v1", lida.Balance(), lida.Version())
	}
	if _, err := store.Read().Transactions.FindByID(ctx, aposta.ID()); !errors.Is(err, postgres.ErrTransactionNotFound) {
		t.Error("a transação sobreviveu ao rollback")
	}
	resumo, _ := store.Read().Ledger.Summarize(ctx, w.ID(), brl)
	if resumo.Entries != 1 {
		t.Errorf("lançamentos = %d, esperado 1 (só a abertura)", resumo.Entries)
	}
}
