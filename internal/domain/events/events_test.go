package events_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/events"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/domain/wallet"
)

var (
	eventID  = uuid.MustParse("0192f2a0-0000-7000-8000-000000000001")
	txID     = uuid.MustParse("0192f298-0000-7000-8000-000000000010")
	walletID = uuid.MustParse("0192f291-0000-7000-8000-000000000001")
	playerID = uuid.MustParse("0192f28f-0000-7000-8000-0000000000a1")
	// Instante com fuso diferente de UTC, para provar a normalização.
	instante = time.Date(2026, 9, 8, 9, 0, 0, 0, time.FixedZone("BRT", -3*60*60))
)

func brlOf(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q): %v", amount, err)
	}
	return m
}

func saldoAlterado(t *testing.T) events.WalletBalanceChanged {
	t.Helper()
	return events.WalletBalanceChanged{
		WalletID: walletID, TransactionID: txID,
		Direction:     wallet.Debit,
		Money:         brlOf(t, "25.00"),
		BalanceBefore: brlOf(t, "1000.00"),
		BalanceAfter:  brlOf(t, "975.00"),
		WalletVersion: 2,
		ChangedAt:     "2026-09-08T12:00:00.000Z",
	}
}

func TestTipoEVersaoVemDoPayload(t *testing.T) {
	tests := []struct {
		payload  events.Payload
		tipo     events.Type
		agregado events.Aggregate
	}{
		{events.WagerTransactionProcessed{
			TransactionID: txID, WalletID: walletID, PlayerID: playerID,
			Kind: wagering.Bet, Money: brlOf(t, "25.00"), Balance: brlOf(t, "975.00"),
			ProcessedAt: "2026-09-08T12:00:00.000Z",
		}, events.TypeWagerTransactionProcessed, events.AggregateWagerTransaction},

		{events.WagerTransactionRejected{
			TransactionID: txID, WalletID: walletID, PlayerID: playerID,
			Kind: wagering.Bet, Money: brlOf(t, "25.00"),
			FailureCode: wagering.FailureInsufficientFunds,
			RejectedAt:  "2026-09-08T12:00:00.000Z",
		}, events.TypeWagerTransactionRejected, events.AggregateWagerTransaction},

		{events.WagerTransactionPendingReference{
			TransactionID: txID, ProviderID: "provider-a", ExternalTransactionID: "ext-1",
			ReferenceExternalTransactionID: "ext-0",
			WalletID:                       walletID, PlayerID: playerID,
			Kind: wagering.Refund, Money: brlOf(t, "25.00"),
			ExpiresAt: "2026-09-08T13:00:00.000Z", PendingAt: "2026-09-08T12:00:00.000Z",
		}, events.TypeWagerTransactionPendingReference, events.AggregateWagerTransaction},

		{saldoAlterado(t), events.TypeWalletBalanceChanged, events.AggregateWallet},
	}

	for _, tt := range tests {
		t.Run(tt.tipo.String(), func(t *testing.T) {
			e, err := events.New(eventID, tt.payload, "corr-1", "", instante)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if e.Type() != tt.tipo {
				t.Errorf("tipo = %v, esperado %v", e.Type(), tt.tipo)
			}
			if e.Version() != 1 {
				t.Errorf("versão = %d, esperado 1", e.Version())
			}
			if e.Aggregate() != tt.agregado {
				t.Errorf("agregado = %v, esperado %v", e.Aggregate(), tt.agregado)
			}
		})
	}
}

func TestEnvelopeSerializado(t *testing.T) {
	e, err := events.New(eventID, saldoAlterado(t), "corr-1", "causa-1", instante)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := `{"eventId":"0192f2a0-0000-7000-8000-000000000001",` +
		`"eventType":"WalletBalanceChanged",` +
		`"aggregateType":"Wallet",` +
		`"aggregateId":"0192f291-0000-7000-8000-000000000001",` +
		`"correlationId":"corr-1","causationId":"causa-1",` +
		`"occurredAt":"2026-09-08T12:00:00.000Z","version":1,` +
		`"data":{"walletId":"0192f291-0000-7000-8000-000000000001",` +
		`"transactionId":"0192f298-0000-7000-8000-000000000010",` +
		`"direction":"DEBIT","money":{"amount":"25.00","currency":"BRL"},` +
		`"balanceBefore":{"amount":"1000.00","currency":"BRL"},` +
		`"balanceAfter":{"amount":"975.00","currency":"BRL"},` +
		`"walletVersion":2,"changedAt":"2026-09-08T12:00:00.000Z"}}`

	if string(raw) != want {
		t.Errorf("envelope diferente do contrato:\n  obtido:   %s\n  esperado: %s", raw, want)
	}
}

// TestInstanteNormalizadoParaUTC garante que o fuso da instância não vaza para
// o payload: 09:00 em BRT precisa sair como 12:00Z.
func TestInstanteNormalizadoParaUTC(t *testing.T) {
	e, err := events.New(eventID, saldoAlterado(t), "corr-1", "", instante)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.OccurredAt().Location() != time.UTC {
		t.Errorf("fuso = %v, esperado UTC", e.OccurredAt().Location())
	}
	raw, _ := json.Marshal(e)
	if !jsonContem(string(raw), `"occurredAt":"2026-09-08T12:00:00.000Z"`) {
		t.Errorf("instante não normalizado: %s", raw)
	}
}

func jsonContem(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestEnvelopeRejeitaCamposAusentes(t *testing.T) {
	valido := saldoAlterado(t)

	tests := []struct {
		name          string
		id            uuid.UUID
		payload       events.Payload
		correlationID string
		now           time.Time
		wantErr       error
	}{
		{"sem id", uuid.Nil, valido, "corr-1", instante, events.ErrInvalidEvent},
		{"sem payload", eventID, nil, "corr-1", instante, events.ErrInvalidPayload},
		{"sem correlationId", eventID, valido, "", instante, events.ErrInvalidEvent},
		{"sem instante", eventID, valido, "corr-1", time.Time{}, events.ErrInvalidEvent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := events.New(tt.id, tt.payload, tt.correlationID, "", tt.now)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("New devolveu %v, esperado %v", err, tt.wantErr)
			}
		})
	}
}

// TestSaldoAlteradoValidaAritmetica impede publicar um evento que afirma um
// saldo que não decorre do movimento: para o consumidor, um payload incoerente
// é indistinguível de um correto.
func TestSaldoAlteradoValidaAritmetica(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*events.WalletBalanceChanged)
	}{
		{"saldo posterior não fecha", func(p *events.WalletBalanceChanged) {
			p.BalanceAfter = brlOf(t, "900.00")
		}},
		{"direção invertida sem ajustar saldos", func(p *events.WalletBalanceChanged) {
			p.Direction = wallet.Credit
		}},
		{"direção desconhecida", func(p *events.WalletBalanceChanged) {
			p.Direction = "TRANSFER"
		}},
		{"movimento zerado", func(p *events.WalletBalanceChanged) {
			p.Money = brlOf(t, "0.00")
			p.BalanceAfter = brlOf(t, "1000.00")
		}},
		{"sem transação", func(p *events.WalletBalanceChanged) {
			p.TransactionID = uuid.Nil
		}},
		{"versão zerada", func(p *events.WalletBalanceChanged) {
			p.WalletVersion = 0
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := saldoAlterado(t)
			tt.mutate(&p)
			if _, err := events.New(eventID, p, "corr-1", "", instante); !errors.Is(err, events.ErrInvalidPayload) {
				t.Errorf("New devolveu %v, esperado ErrInvalidPayload", err)
			}
		})
	}
}

func TestEsperaPorReferenciaSoParaReversoes(t *testing.T) {
	base := events.WagerTransactionPendingReference{
		TransactionID: txID, ProviderID: "provider-a", ExternalTransactionID: "ext-1",
		ReferenceExternalTransactionID: "ext-0",
		WalletID:                       walletID, PlayerID: playerID,
		Kind: wagering.Refund, Money: brlOf(t, "25.00"),
		ExpiresAt: "2026-09-08T13:00:00.000Z", PendingAt: "2026-09-08T12:00:00.000Z",
	}

	if _, err := events.New(eventID, base, "corr-1", "", instante); err != nil {
		t.Fatalf("REFUND válido foi recusado: %v", err)
	}

	semRef := base
	semRef.ReferenceExternalTransactionID = ""
	if _, err := events.New(eventID, semRef, "corr-1", "", instante); !errors.Is(err, events.ErrInvalidPayload) {
		t.Errorf("sem referência devolveu %v, esperado ErrInvalidPayload", err)
	}

	naoReversao := base
	naoReversao.Kind = wagering.Bet
	if _, err := events.New(eventID, naoReversao, "corr-1", "", instante); !errors.Is(err, events.ErrInvalidPayload) {
		t.Errorf("BET devolveu %v, esperado ErrInvalidPayload", err)
	}
}

func TestRejeicaoExigeCodigoConhecido(t *testing.T) {
	base := events.WagerTransactionRejected{
		TransactionID: txID, WalletID: walletID, PlayerID: playerID,
		Kind: wagering.Bet, Money: brlOf(t, "25.00"),
		FailureCode: wagering.FailureInsufficientFunds,
		Correctable: false,
		RejectedAt:  "2026-09-08T12:00:00.000Z",
	}
	if _, err := events.New(eventID, base, "corr-1", "", instante); err != nil {
		t.Fatalf("rejeição válida foi recusada: %v", err)
	}

	desconhecido := base
	desconhecido.FailureCode = "NAO_EXISTE"
	if _, err := events.New(eventID, desconhecido, "corr-1", "", instante); !errors.Is(err, events.ErrInvalidPayload) {
		t.Errorf("código desconhecido devolveu %v, esperado ErrInvalidPayload", err)
	}
}

// TestAberturaInternaOmiteMetadadosExternos cobre a regra do desafio: os
// eventos da abertura de carteira não carregam provedor, id externo, rodada
// nem jogo.
func TestAberturaInternaOmiteMetadadosExternos(t *testing.T) {
	abertura := events.WagerTransactionProcessed{
		TransactionID: txID, WalletID: walletID, PlayerID: playerID,
		Kind: wagering.Opening, Money: brlOf(t, "1000.00"), Balance: brlOf(t, "1000.00"),
		ProcessedAt: "2026-09-08T12:00:00.000Z",
	}
	e, err := events.New(eventID, abertura, "corr-1", "", instante)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, _ := json.Marshal(e)

	for _, ausente := range []string{"providerId", "externalTransactionId", "roundId", "gameId", "causationId"} {
		if jsonContem(string(raw), `"`+ausente+`"`) {
			t.Errorf("evento de abertura carregou %q: %s", ausente, raw)
		}
	}
	if !jsonContem(string(raw), `"kind":"OPENING"`) {
		t.Errorf("evento não identificou a abertura: %s", raw)
	}
}
