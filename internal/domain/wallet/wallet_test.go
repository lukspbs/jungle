package wallet_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jonatancruz/jungle/internal/domain/money"
	"github.com/jonatancruz/jungle/internal/domain/wallet"
)

var (
	walletID = uuid.MustParse("0192f291-0000-7000-8000-000000000001")
	playerID = uuid.MustParse("0192f28f-0000-7000-8000-0000000000a1")
	txID     = uuid.MustParse("0192f298-0000-7000-8000-000000000010")
	entryID  = uuid.MustParse("0192f299-0000-7000-8000-000000000020")
	instante = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	brl      = money.MustCurrency("BRL")
)

func brlOf(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q) devolveu erro: %v", amount, err)
	}
	return m
}

// carteiraCom devolve uma carteira aberta com o saldo informado.
func carteiraCom(t *testing.T, saldo string) *wallet.Wallet {
	t.Helper()
	w, _, err := wallet.Open(wallet.OpeningRequest{
		WalletID:       walletID,
		PlayerID:       playerID,
		TransactionID:  txID,
		EntryID:        entryID,
		InitialBalance: brlOf(t, saldo),
		Now:            instante,
	})
	if err != nil {
		t.Fatalf("Open devolveu erro: %v", err)
	}
	return w
}

func TestAberturaComSaldoPositivo(t *testing.T) {
	w, entry, err := wallet.Open(wallet.OpeningRequest{
		WalletID:       walletID,
		PlayerID:       playerID,
		TransactionID:  txID,
		EntryID:        entryID,
		InitialBalance: brlOf(t, "1000.00"),
		Now:            instante,
	})
	if err != nil {
		t.Fatalf("Open devolveu erro: %v", err)
	}

	if w.Balance().String() != "1000.00" {
		t.Errorf("saldo = %q, esperado \"1000.00\"", w.Balance())
	}
	// A versão permanece em 1: o crédito de abertura integra o estado inicial
	// e não é uma movimentação posterior.
	if w.Version() != 1 {
		t.Errorf("versão = %d, esperado 1", w.Version())
	}
	if w.Currency() != brl {
		t.Errorf("moeda = %v, esperado BRL", w.Currency())
	}
	if !w.CreatedAt().Equal(instante) || !w.UpdatedAt().Equal(instante) {
		t.Errorf("instantes = %v/%v, esperado %v", w.CreatedAt(), w.UpdatedAt(), instante)
	}

	if entry == nil {
		t.Fatal("abertura com saldo positivo deveria produzir lançamento")
	}
	if entry.Direction() != wallet.Credit {
		t.Errorf("direção = %v, esperado CREDIT", entry.Direction())
	}
	if entry.BalanceBefore().String() != "0.00" {
		t.Errorf("saldo anterior = %q, esperado \"0.00\"", entry.BalanceBefore())
	}
	if entry.BalanceAfter().String() != "1000.00" {
		t.Errorf("saldo posterior = %q, esperado \"1000.00\"", entry.BalanceAfter())
	}
	if entry.TransactionID() != txID || entry.WalletID() != walletID {
		t.Error("lançamento não aponta para a transação e a carteira corretas")
	}
}

func TestAberturaComSaldoZeroNaoProduzLancamento(t *testing.T) {
	w, entry, err := wallet.Open(wallet.OpeningRequest{
		WalletID:       walletID,
		PlayerID:       playerID,
		InitialBalance: brlOf(t, "0.00"),
		Now:            instante,
	})
	if err != nil {
		t.Fatalf("Open devolveu erro: %v", err)
	}
	if entry != nil {
		t.Error("abertura com saldo zero não deveria produzir lançamento")
	}
	if !w.Balance().IsZero() || w.Version() != 1 {
		t.Errorf("carteira = saldo %q versão %d, esperado \"0.00\" e 1", w.Balance(), w.Version())
	}
}

func TestAberturaRejeitaEntradasInvalidas(t *testing.T) {
	valido := wallet.OpeningRequest{
		WalletID:       walletID,
		PlayerID:       playerID,
		TransactionID:  txID,
		EntryID:        entryID,
		InitialBalance: brlOf(t, "1000.00"),
		Now:            instante,
	}
	negativo, err := money.FromMinorUnits(-1, brl)
	if err != nil {
		t.Fatalf("FromMinorUnits devolveu erro: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*wallet.OpeningRequest)
		wantErr error
	}{
		{"sem id de carteira", func(r *wallet.OpeningRequest) { r.WalletID = uuid.Nil }, wallet.ErrInvalidID},
		{"sem jogador", func(r *wallet.OpeningRequest) { r.PlayerID = uuid.Nil }, wallet.ErrInvalidID},
		{"sem transação com saldo positivo", func(r *wallet.OpeningRequest) { r.TransactionID = uuid.Nil }, wallet.ErrInvalidID},
		{"sem lançamento com saldo positivo", func(r *wallet.OpeningRequest) { r.EntryID = uuid.Nil }, wallet.ErrInvalidID},
		{"sem instante", func(r *wallet.OpeningRequest) { r.Now = time.Time{} }, wallet.ErrInvalidTimestamp},
		{"saldo sem moeda", func(r *wallet.OpeningRequest) { r.InitialBalance = money.Money{} }, money.ErrUninitialized},
		{"saldo inicial negativo", func(r *wallet.OpeningRequest) { r.InitialBalance = negativo }, wallet.ErrNegativeBalance},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valido
			tt.mutate(&req)
			w, entry, err := wallet.Open(req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Open devolveu %v, esperado %v", err, tt.wantErr)
			}
			if w != nil || entry != nil {
				t.Error("Open devolveu agregado utilizável junto com o erro")
			}
		})
	}
}

func TestDebito(t *testing.T) {
	w := carteiraCom(t, "100.00")

	entry, err := w.Debit(entryID, txID, brlOf(t, "25.00"), instante)
	if err != nil {
		t.Fatalf("Debit devolveu erro: %v", err)
	}

	if w.Balance().String() != "75.00" {
		t.Errorf("saldo = %q, esperado \"75.00\"", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("versão = %d, esperado 2", w.Version())
	}
	if entry.Direction() != wallet.Debit {
		t.Errorf("direção = %v, esperado DEBIT", entry.Direction())
	}
	if entry.BalanceBefore().String() != "100.00" || entry.BalanceAfter().String() != "75.00" {
		t.Errorf("lançamento = %q -> %q, esperado \"100.00\" -> \"75.00\"",
			entry.BalanceBefore(), entry.BalanceAfter())
	}
}

func TestDebitoAteZerarESaldoExato(t *testing.T) {
	w := carteiraCom(t, "100.00")
	if _, err := w.Debit(entryID, txID, brlOf(t, "100.00"), instante); err != nil {
		t.Fatalf("Debit devolveu erro: %v", err)
	}
	if !w.Balance().IsZero() {
		t.Errorf("saldo = %q, esperado zero exato", w.Balance())
	}
}

func TestCredito(t *testing.T) {
	w := carteiraCom(t, "100.00")

	entry, err := w.Credit(entryID, txID, brlOf(t, "50.00"), instante)
	if err != nil {
		t.Fatalf("Credit devolveu erro: %v", err)
	}
	if w.Balance().String() != "150.00" {
		t.Errorf("saldo = %q, esperado \"150.00\"", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("versão = %d, esperado 2", w.Version())
	}
	if entry.Direction() != wallet.Credit {
		t.Errorf("direção = %v, esperado CREDIT", entry.Direction())
	}
}

// TestMovimentacaoRecusadaNaoAlteraAgregado é o teste central do encapsulamento.
// Uma rejeição precisa deixar a carteira exatamente como estava: saldo, versão
// e instante de atualização intactos.
func TestMovimentacaoRecusadaNaoAlteraAgregado(t *testing.T) {
	emDolar, err := money.Parse("10.00", "USD")
	if err != nil {
		t.Fatalf("Parse devolveu erro: %v", err)
	}
	negativo, err := money.FromMinorUnits(-100, brl)
	if err != nil {
		t.Fatalf("FromMinorUnits devolveu erro: %v", err)
	}
	tests := []struct {
		name    string
		run     func(*wallet.Wallet) (*wallet.LedgerEntry, error)
		wantErr error
	}{
		{"débito maior que o saldo", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Debit(entryID, txID, brlOf(t, "100.01"), instante)
		}, wallet.ErrInsufficientFunds},

		{"débito em moeda diferente", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Debit(entryID, txID, emDolar, instante)
		}, wallet.ErrCurrencyMismatch},

		{"crédito em moeda diferente", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Credit(entryID, txID, emDolar, instante)
		}, wallet.ErrCurrencyMismatch},

		{"movimentação de valor zero", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Debit(entryID, txID, brlOf(t, "0.00"), instante)
		}, wallet.ErrNonPositiveAmount},

		{"movimentação de valor negativo", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Credit(entryID, txID, negativo, instante)
		}, wallet.ErrNonPositiveAmount},

		{"movimentação sem moeda", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Debit(entryID, txID, money.Money{}, instante)
		}, money.ErrUninitialized},

		{"movimentação sem id de lançamento", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Debit(uuid.Nil, txID, brlOf(t, "10.00"), instante)
		}, wallet.ErrInvalidID},

		{"movimentação sem transação", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Debit(entryID, uuid.Nil, brlOf(t, "10.00"), instante)
		}, wallet.ErrInvalidID},

		{"movimentação sem instante", func(w *wallet.Wallet) (*wallet.LedgerEntry, error) {
			return w.Debit(entryID, txID, brlOf(t, "10.00"), time.Time{})
		}, wallet.ErrInvalidTimestamp},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := carteiraCom(t, "100.00")
			saldoAntes, versaoAntes, atualizadoAntes := w.Balance(), w.Version(), w.UpdatedAt()

			entry, err := tt.run(w)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("devolveu %v, esperado %v", err, tt.wantErr)
			}
			if entry != nil {
				t.Error("devolveu lançamento junto com o erro")
			}
			if !w.Balance().Equal(saldoAntes) {
				t.Errorf("saldo mudou para %q apesar da rejeição (era %q)", w.Balance(), saldoAntes)
			}
			if w.Version() != versaoAntes {
				t.Errorf("versão avançou para %d apesar da rejeição (era %d)", w.Version(), versaoAntes)
			}
			if !w.UpdatedAt().Equal(atualizadoAntes) {
				t.Error("instante de atualização mudou apesar da rejeição")
			}
		})
	}
}

// TestDisputaDeDuasApostasSobreOMesmoSaldo reproduz no domínio o cenário
// obrigatório do desafio: 100.00 recebe duas apostas de 80.00.
func TestDisputaDeDuasApostasSobreOMesmoSaldo(t *testing.T) {
	w := carteiraCom(t, "100.00")
	segundaTx := uuid.MustParse("0192f298-0000-7000-8000-000000000011")
	segundoEntry := uuid.MustParse("0192f299-0000-7000-8000-000000000021")

	primeiro, err := w.Debit(entryID, txID, brlOf(t, "80.00"), instante)
	if err != nil {
		t.Fatalf("primeira aposta devolveu erro: %v", err)
	}
	if primeiro.BalanceAfter().String() != "20.00" {
		t.Errorf("saldo após a primeira = %q, esperado \"20.00\"", primeiro.BalanceAfter())
	}

	segundo, err := w.Debit(segundoEntry, segundaTx, brlOf(t, "80.00"), instante)
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("segunda aposta devolveu %v, esperado ErrInsufficientFunds", err)
	}
	if segundo != nil {
		t.Error("aposta rejeitada produziu lançamento")
	}
	if w.Balance().String() != "20.00" {
		t.Errorf("saldo final = %q, esperado \"20.00\"", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("versão = %d, esperado 2: só uma movimentação foi aceita", w.Version())
	}
}

func TestCreditoComOverflowNaoAlteraAgregado(t *testing.T) {
	quaseMaximo, err := money.FromMinorUnits(math.MaxInt64-10, brl)
	if err != nil {
		t.Fatalf("FromMinorUnits devolveu erro: %v", err)
	}
	w, err := wallet.Rehydrate(wallet.Snapshot{
		ID: walletID, PlayerID: playerID, Balance: quaseMaximo,
		Version: 7, CreatedAt: instante, UpdatedAt: instante,
	})
	if err != nil {
		t.Fatalf("Rehydrate devolveu erro: %v", err)
	}

	if _, err := w.Credit(entryID, txID, brlOf(t, "1.00"), instante); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("Credit devolveu %v, esperado ErrOverflow", err)
	}
	if !w.Balance().Equal(quaseMaximo) || w.Version() != 7 {
		t.Error("overflow deixou o agregado alterado")
	}
}

func TestReidratacaoNaoReaplicaNada(t *testing.T) {
	saldo := brlOf(t, "975.00")
	criado := instante
	atualizado := instante.Add(2 * time.Hour)

	w, err := wallet.Rehydrate(wallet.Snapshot{
		ID: walletID, PlayerID: playerID, Balance: saldo,
		Version: 42, CreatedAt: criado, UpdatedAt: atualizado,
	})
	if err != nil {
		t.Fatalf("Rehydrate devolveu erro: %v", err)
	}

	// Estado restaurado tal como estava: nenhuma movimentação reaplicada,
	// nenhuma versão recalculada.
	if !w.Balance().Equal(saldo) {
		t.Errorf("saldo = %q, esperado %q", w.Balance(), saldo)
	}
	if w.Version() != 42 {
		t.Errorf("versão = %d, esperado 42", w.Version())
	}
	if !w.CreatedAt().Equal(criado) || !w.UpdatedAt().Equal(atualizado) {
		t.Error("instantes não foram preservados na reidratação")
	}
	if w.ID() != walletID || w.PlayerID() != playerID {
		t.Error("identidade não foi preservada na reidratação")
	}
}

func TestReidratacaoRejeitaEstadoIncoerente(t *testing.T) {
	valido := wallet.Snapshot{
		ID: walletID, PlayerID: playerID, Balance: brlOf(t, "100.00"),
		Version: 3, CreatedAt: instante, UpdatedAt: instante,
	}
	negativo, err := money.FromMinorUnits(-1, brl)
	if err != nil {
		t.Fatalf("FromMinorUnits devolveu erro: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*wallet.Snapshot)
		wantErr error
	}{
		{"sem id", func(s *wallet.Snapshot) { s.ID = uuid.Nil }, wallet.ErrInvalidID},
		{"sem jogador", func(s *wallet.Snapshot) { s.PlayerID = uuid.Nil }, wallet.ErrInvalidID},
		{"saldo sem moeda", func(s *wallet.Snapshot) { s.Balance = money.Money{} }, money.ErrUninitialized},
		{"saldo negativo persistido", func(s *wallet.Snapshot) { s.Balance = negativo }, wallet.ErrNegativeBalance},
		{"versão zero", func(s *wallet.Snapshot) { s.Version = 0 }, wallet.ErrInvalidVersion},
		{"versão negativa", func(s *wallet.Snapshot) { s.Version = -1 }, wallet.ErrInvalidVersion},
		{"sem criação", func(s *wallet.Snapshot) { s.CreatedAt = time.Time{} }, wallet.ErrInvalidTimestamp},
		{"sem atualização", func(s *wallet.Snapshot) { s.UpdatedAt = time.Time{} }, wallet.ErrInvalidTimestamp},
		{"atualização anterior à criação", func(s *wallet.Snapshot) {
			s.UpdatedAt = instante.Add(-time.Hour)
		}, wallet.ErrInconsistentSnapshot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valido
			tt.mutate(&s)
			w, err := wallet.Rehydrate(s)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Rehydrate devolveu %v, esperado %v", err, tt.wantErr)
			}
			if w != nil {
				t.Error("Rehydrate devolveu carteira utilizável junto com o erro")
			}
		})
	}
}

func TestReidratacaoDeLancamentoValidaAritmetica(t *testing.T) {
	base := wallet.LedgerEntrySnapshot{
		ID: entryID, WalletID: walletID, TransactionID: txID,
		Direction: wallet.Debit, Amount: brlOf(t, "25.00"),
		BalanceBefore: brlOf(t, "100.00"), BalanceAfter: brlOf(t, "75.00"),
		CreatedAt: instante,
	}

	if _, err := wallet.RehydrateLedgerEntry(base); err != nil {
		t.Fatalf("lançamento coerente foi recusado: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*wallet.LedgerEntrySnapshot)
		wantErr error
	}{
		{"saldo posterior não decorre do débito", func(s *wallet.LedgerEntrySnapshot) {
			s.BalanceAfter = brlOf(t, "80.00")
		}, wallet.ErrLedgerArithmetic},
		{"direção invertida sem ajustar saldos", func(s *wallet.LedgerEntrySnapshot) {
			s.Direction = wallet.Credit
		}, wallet.ErrLedgerArithmetic},
		{"valor zero", func(s *wallet.LedgerEntrySnapshot) {
			s.Amount = brlOf(t, "0.00")
			s.BalanceAfter = brlOf(t, "100.00")
		}, wallet.ErrNonPositiveAmount},
		{"direção desconhecida", func(s *wallet.LedgerEntrySnapshot) {
			s.Direction = "TRANSFER"
		}, wallet.ErrInvalidDirection},
		{"sem id", func(s *wallet.LedgerEntrySnapshot) { s.ID = uuid.Nil }, wallet.ErrInvalidID},
		{"sem transação", func(s *wallet.LedgerEntrySnapshot) { s.TransactionID = uuid.Nil }, wallet.ErrInvalidID},
		{"sem instante", func(s *wallet.LedgerEntrySnapshot) { s.CreatedAt = time.Time{} }, wallet.ErrInvalidTimestamp},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			tt.mutate(&s)
			e, err := wallet.RehydrateLedgerEntry(s)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("RehydrateLedgerEntry devolveu %v, esperado %v", err, tt.wantErr)
			}
			if e != nil {
				t.Error("devolveu lançamento utilizável junto com o erro")
			}
		})
	}
}

func TestParseDirection(t *testing.T) {
	for _, valida := range []string{"DEBIT", "CREDIT"} {
		if d, err := wallet.ParseDirection(valida); err != nil || d.String() != valida {
			t.Errorf("ParseDirection(%q) = (%v, %v), esperado sucesso", valida, d, err)
		}
	}
	for _, invalida := range []string{"", "debit", "Debit", "TRANSFER"} {
		if _, err := wallet.ParseDirection(invalida); !errors.Is(err, wallet.ErrInvalidDirection) {
			t.Errorf("ParseDirection(%q) devolveu %v, esperado ErrInvalidDirection", invalida, err)
		}
	}
}

// TestLedgerAcompanhaOSaldoEmSequencia confere que a cadeia de lançamentos
// reconstrói exatamente o saldo final — é a base da reconciliação.
func TestLedgerAcompanhaOSaldoEmSequencia(t *testing.T) {
	w := carteiraCom(t, "1000.00")
	movimentos := []struct {
		debito bool
		valor  string
	}{
		{true, "25.00"}, {false, "10.00"}, {true, "100.50"},
		{false, "0.01"}, {true, "0.99"}, {false, "500.00"},
	}

	var anterior = w.Balance()
	for i, m := range movimentos {
		eID := uuid.New()
		tID := uuid.New()

		var entry *wallet.LedgerEntry
		var err error
		if m.debito {
			entry, err = w.Debit(eID, tID, brlOf(t, m.valor), instante)
		} else {
			entry, err = w.Credit(eID, tID, brlOf(t, m.valor), instante)
		}
		if err != nil {
			t.Fatalf("movimento %d devolveu erro: %v", i, err)
		}

		if !entry.BalanceBefore().Equal(anterior) {
			t.Fatalf("movimento %d: saldo anterior %q não encadeia com %q",
				i, entry.BalanceBefore(), anterior)
		}
		if !entry.BalanceAfter().Equal(w.Balance()) {
			t.Fatalf("movimento %d: saldo posterior %q difere da carteira %q",
				i, entry.BalanceAfter(), w.Balance())
		}
		anterior = w.Balance()
	}

	// 1000.00 - 25.00 + 10.00 - 100.50 + 0.01 - 0.99 + 500.00
	if w.Balance().String() != "1383.52" {
		t.Errorf("saldo final = %q, esperado \"1383.52\"", w.Balance())
	}
	if w.Version() != int64(1+len(movimentos)) {
		t.Errorf("versão = %d, esperado %d", w.Version(), 1+len(movimentos))
	}
}
