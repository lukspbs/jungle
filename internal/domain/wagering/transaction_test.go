package wagering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jonatancruz/jungle/internal/domain/money"
	"github.com/jonatancruz/jungle/internal/domain/wagering"
)

var (
	txID     = uuid.MustParse("0192f298-0000-7000-8000-000000000010")
	refID    = uuid.MustParse("0192f298-0000-7000-8000-000000000011")
	walletID = uuid.MustParse("0192f291-0000-7000-8000-000000000001")
	playerID = uuid.MustParse("0192f28f-0000-7000-8000-0000000000a1")
	instante = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	depois   = instante.Add(time.Minute)
)

func brlOf(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q) devolveu erro: %v", amount, err)
	}
	return m
}

func requisicaoValida(t *testing.T) wagering.ExternalRequest {
	t.Helper()
	return wagering.ExternalRequest{
		ID:                    txID,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PayloadHash:           []byte{0xaa, 0xbb},
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  wagering.Bet,
		Amount:                brlOf(t, "25.00"),
		Now:                   instante,
	}
}

func TestNovaOperacaoExternaNasceEmPending(t *testing.T) {
	tx, err := wagering.NewExternal(requisicaoValida(t))
	if err != nil {
		t.Fatalf("NewExternal devolveu erro: %v", err)
	}

	if tx.Status() != wagering.Pending {
		t.Errorf("estado = %v, esperado PENDING", tx.Status())
	}
	if tx.Source() != wagering.External {
		t.Errorf("origem = %v, esperado EXTERNAL", tx.Source())
	}
	if !tx.ResultBalance().IsUninitialized() {
		t.Error("transação pendente não pode ter saldo resultante")
	}
	if tx.FailureCode() != "" {
		t.Error("transação pendente não pode ter código de falha")
	}
	if tx.IdempotencyKey() != "provider-a:transaction-123" {
		t.Errorf("chave = %q, o servidor não pode substituí-la", tx.IdempotencyKey())
	}
}

func TestOpeningNaoPodeVirDeProvedor(t *testing.T) {
	req := requisicaoValida(t)
	req.Kind = wagering.Opening
	req.Amount = brlOf(t, "1000.00")

	if _, err := wagering.NewExternal(req); !errors.Is(err, wagering.ErrKindNotAllowed) {
		t.Errorf("NewExternal devolveu %v, esperado ErrKindNotAllowed", err)
	}

	// A mesma recusa precisa acontecer já no parsing da entrada externa.
	if _, err := wagering.ParseExternalKind("OPENING"); !errors.Is(err, wagering.ErrKindNotAllowed) {
		t.Errorf("ParseExternalKind devolveu %v, esperado ErrKindNotAllowed", err)
	}
	// E os tipos externos precisam continuar passando.
	for _, k := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		if _, err := wagering.ParseExternalKind(k); err != nil {
			t.Errorf("ParseExternalKind(%q) devolveu erro: %v", k, err)
		}
	}
}

func TestPoliticaDeValorPorTipo(t *testing.T) {
	tests := []struct {
		kind    wagering.Kind
		amount  string
		wantErr error
	}{
		{wagering.Bet, "25.00", nil},
		{wagering.Win, "25.00", nil},
		{wagering.Loss, "0.00", nil},
		{wagering.Refund, "25.00", nil},
		{wagering.Rollback, "25.00", nil},

		{wagering.Bet, "0.00", wagering.ErrAmountPolicy},
		{wagering.Win, "0.00", wagering.ErrAmountPolicy},
		{wagering.Refund, "0.00", wagering.ErrAmountPolicy},
		{wagering.Rollback, "0.00", wagering.ErrAmountPolicy},
		{wagering.Loss, "0.01", wagering.ErrAmountPolicy},
		{wagering.Loss, "25.00", wagering.ErrAmountPolicy},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind)+" com "+tt.amount, func(t *testing.T) {
			req := requisicaoValida(t)
			req.Kind = tt.kind
			req.Amount = brlOf(t, tt.amount)
			if tt.kind.IsReversal() {
				req.ReferenceExternalTransactionID = "transaction-original"
			}

			_, err := wagering.NewExternal(req)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("NewExternal devolveu erro inesperado: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewExternal devolveu %v, esperado %v", err, tt.wantErr)
			}
		})
	}
}

func TestPoliticaDeReferencia(t *testing.T) {
	t.Run("reversão sem referência é recusada", func(t *testing.T) {
		for _, k := range []wagering.Kind{wagering.Refund, wagering.Rollback} {
			req := requisicaoValida(t)
			req.Kind = k
			if _, err := wagering.NewExternal(req); !errors.Is(err, wagering.ErrReferencePolicy) {
				t.Errorf("%s sem referência devolveu %v, esperado ErrReferencePolicy", k, err)
			}
		}
	})

	t.Run("operação comum com referência é recusada", func(t *testing.T) {
		for _, k := range []wagering.Kind{wagering.Bet, wagering.Win} {
			req := requisicaoValida(t)
			req.Kind = k
			req.ReferenceExternalTransactionID = "transaction-original"
			if _, err := wagering.NewExternal(req); !errors.Is(err, wagering.ErrReferencePolicy) {
				t.Errorf("%s com referência devolveu %v, esperado ErrReferencePolicy", k, err)
			}
		}
	})
}

func TestCamposObrigatorios(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*wagering.ExternalRequest)
		wantErr error
	}{
		{"sem id", func(r *wagering.ExternalRequest) { r.ID = uuid.Nil }, wagering.ErrInvalidID},
		{"sem carteira", func(r *wagering.ExternalRequest) { r.WalletID = uuid.Nil }, wagering.ErrInvalidID},
		{"sem jogador", func(r *wagering.ExternalRequest) { r.PlayerID = uuid.Nil }, wagering.ErrInvalidID},
		{"sem instante", func(r *wagering.ExternalRequest) { r.Now = time.Time{} }, wagering.ErrInvalidTimestamp},
		{"sem provedor", func(r *wagering.ExternalRequest) { r.ProviderID = "" }, wagering.ErrMissingField},
		{"sem id externo", func(r *wagering.ExternalRequest) { r.ExternalTransactionID = "" }, wagering.ErrMissingField},
		{"sem chave", func(r *wagering.ExternalRequest) { r.IdempotencyKey = "" }, wagering.ErrMissingField},
		{"sem rodada", func(r *wagering.ExternalRequest) { r.RoundID = "" }, wagering.ErrMissingField},
		{"sem jogo", func(r *wagering.ExternalRequest) { r.GameID = "" }, wagering.ErrMissingField},
		{"sem hash", func(r *wagering.ExternalRequest) { r.PayloadHash = nil }, wagering.ErrMissingField},
		{"valor sem moeda", func(r *wagering.ExternalRequest) { r.Amount = money.Money{} }, money.ErrUninitialized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := requisicaoValida(t)
			tt.mutate(&req)
			tx, err := wagering.NewExternal(req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewExternal devolveu %v, esperado %v", err, tt.wantErr)
			}
			if tx != nil {
				t.Error("NewExternal devolveu transação utilizável junto com o erro")
			}
		})
	}
}

func TestAberturaInternaNasceConcluida(t *testing.T) {
	tx, err := wagering.NewOpening(wagering.OpeningRequest{
		ID: txID, WalletID: walletID, PlayerID: playerID,
		Amount: brlOf(t, "1000.00"), Now: instante,
	})
	if err != nil {
		t.Fatalf("NewOpening devolveu erro: %v", err)
	}

	// Nasce PROCESSED porque acontece no mesmo commit da carteira: não existe
	// janela em que a abertura esteja pendente.
	if tx.Status() != wagering.Processed {
		t.Errorf("estado = %v, esperado PROCESSED", tx.Status())
	}
	if tx.Source() != wagering.Internal || tx.Kind() != wagering.Opening {
		t.Errorf("origem/tipo = %v/%v, esperado INTERNAL/OPENING", tx.Source(), tx.Kind())
	}
	if tx.ResultBalance().String() != "1000.00" {
		t.Errorf("saldo resultante = %q, esperado \"1000.00\"", tx.ResultBalance())
	}
	// Metadados externos não se aplicam à origem interna.
	if tx.ProviderID() != "" || tx.ExternalTransactionID() != "" ||
		tx.IdempotencyKey() != "" || len(tx.PayloadHash()) != 0 ||
		tx.RoundID() != "" || tx.GameID() != "" {
		t.Error("abertura interna carregou metadados externos")
	}
}

// transacaoEm devolve uma transação no estado pedido, montada por reidratação
// para não depender das transições que estamos testando.
func transacaoEm(t *testing.T, status wagering.Status, kind wagering.Kind) *wagering.WagerTransaction {
	t.Helper()
	// LOSS é o único tipo sem movimentação: exige exatamente zero.
	valor := "25.00"
	if !kind.RequiresPositiveAmount() {
		valor = "0.00"
	}
	s := wagering.Snapshot{
		ID: txID, Kind: kind, Status: status,
		WalletID: walletID, PlayerID: playerID, Amount: brlOf(t, valor),
		ProviderID: "provider-a", ExternalTransactionID: "transaction-123",
		IdempotencyKey: "provider-a:transaction-123", PayloadHash: []byte{0xaa},
		RoundID: "round-987", GameID: "fortune-chimp",
		CreatedAt: instante, UpdatedAt: instante,
	}
	if kind.IsReversal() {
		s.ReferenceExternalTransactionID = "transaction-original"
	}
	switch status {
	case wagering.Processed:
		s.ResultBalance = brlOf(t, "975.00")
	case wagering.Rejected, wagering.Failed:
		s.FailureCode = wagering.FailureInsufficientFunds
	}

	tx, err := wagering.Rehydrate(s)
	if err != nil {
		t.Fatalf("Rehydrate(%v) devolveu erro: %v", status, err)
	}
	return tx
}

// TestMaquinaDeEstados percorre a matriz completa de transições. É a prova de
// que um estado terminal é realmente final.
func TestMaquinaDeEstados(t *testing.T) {
	todos := []wagering.Status{
		wagering.Pending, wagering.PendingReference,
		wagering.Processed, wagering.Rejected, wagering.Failed,
	}
	permitidas := map[wagering.Status]map[wagering.Status]bool{
		wagering.Pending: {
			wagering.PendingReference: true,
			wagering.Processed:        true,
			wagering.Rejected:         true,
			wagering.Failed:           true,
		},
		wagering.PendingReference: {
			wagering.Processed: true,
			wagering.Rejected:  true,
			wagering.Failed:    true,
		},
		wagering.Processed: {},
		wagering.Rejected:  {},
		wagering.Failed:    {},
	}

	for _, de := range todos {
		for _, para := range todos {
			esperado := permitidas[de][para]
			if got := de.CanTransitionTo(para); got != esperado {
				t.Errorf("CanTransitionTo(%v -> %v) = %t, esperado %t", de, para, got, esperado)
			}
		}
		if got := de.IsTerminal(); got != (len(permitidas[de]) == 0) {
			t.Errorf("IsTerminal(%v) = %t", de, got)
		}
	}
}

func TestEstadoTerminalRecusaQualquerTransicao(t *testing.T) {
	terminais := []wagering.Status{wagering.Processed, wagering.Rejected, wagering.Failed}

	for _, status := range terminais {
		t.Run(string(status), func(t *testing.T) {
			operacoes := map[string]func(*wagering.WagerTransaction) error{
				"MarkProcessed": func(tx *wagering.WagerTransaction) error {
					return tx.MarkProcessed(brlOf(t, "10.00"), depois)
				},
				"Reject": func(tx *wagering.WagerTransaction) error {
					return tx.Reject(wagering.FailureInsufficientFunds, depois)
				},
				"Fail": func(tx *wagering.WagerTransaction) error {
					return tx.Fail(wagering.FailureInfrastructure, depois)
				},
				"MarkPendingReference": func(tx *wagering.WagerTransaction) error {
					return tx.MarkPendingReference(depois)
				},
				"ResolveReference": func(tx *wagering.WagerTransaction) error {
					return tx.ResolveReference(refID, depois)
				},
			}

			for nome, op := range operacoes {
				tx := transacaoEm(t, status, wagering.Refund)
				antes, atualizadoAntes := tx.Status(), tx.UpdatedAt()

				if err := op(tx); !errors.Is(err, wagering.ErrTerminalStatus) {
					t.Errorf("%s em %s devolveu %v, esperado ErrTerminalStatus", nome, status, err)
				}
				if tx.Status() != antes || !tx.UpdatedAt().Equal(atualizadoAntes) {
					t.Errorf("%s alterou uma transação terminal", nome)
				}
			}
		})
	}
}

func TestProcessamentoCongelaOSaldo(t *testing.T) {
	tx := transacaoEm(t, wagering.Pending, wagering.Bet)

	if err := tx.MarkProcessed(brlOf(t, "975.00"), depois); err != nil {
		t.Fatalf("MarkProcessed devolveu erro: %v", err)
	}
	if tx.Status() != wagering.Processed {
		t.Errorf("estado = %v, esperado PROCESSED", tx.Status())
	}
	if tx.ResultBalance().String() != "975.00" {
		t.Errorf("saldo congelado = %q, esperado \"975.00\"", tx.ResultBalance())
	}
	if !tx.UpdatedAt().Equal(depois) {
		t.Error("instante de atualização não avançou")
	}

	// Uma vez concluída, nem uma nova conclusão passa: o replay lê o saldo
	// guardado em vez de recalcular.
	if err := tx.MarkProcessed(brlOf(t, "500.00"), depois); !errors.Is(err, wagering.ErrTerminalStatus) {
		t.Errorf("segunda conclusão devolveu %v, esperado ErrTerminalStatus", err)
	}
	if tx.ResultBalance().String() != "975.00" {
		t.Errorf("saldo congelado foi sobrescrito para %q", tx.ResultBalance())
	}
}

func TestProcessamentoRecusaSaldoIncoerente(t *testing.T) {
	emDolar, err := money.Parse("975.00", "USD")
	if err != nil {
		t.Fatalf("Parse devolveu erro: %v", err)
	}
	negativo, err := money.FromMinorUnits(-1, money.MustCurrency("BRL"))
	if err != nil {
		t.Fatalf("FromMinorUnits devolveu erro: %v", err)
	}

	tests := []struct {
		name    string
		saldo   money.Money
		wantErr error
	}{
		{"sem moeda", money.Money{}, money.ErrUninitialized},
		{"em moeda diferente", emDolar, money.ErrCurrencyMismatch},
		{"negativo", negativo, wagering.ErrInconsistentSnapshot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := transacaoEm(t, wagering.Pending, wagering.Bet)
			if err := tx.MarkProcessed(tt.saldo, depois); !errors.Is(err, tt.wantErr) {
				t.Fatalf("MarkProcessed devolveu %v, esperado %v", err, tt.wantErr)
			}
			if tx.Status() != wagering.Pending {
				t.Error("saldo incoerente mudou o estado da transação")
			}
		})
	}
}

func TestEsperaPorReferenciaSoValeParaReversoes(t *testing.T) {
	t.Run("reversões podem esperar", func(t *testing.T) {
		for _, k := range []wagering.Kind{wagering.Refund, wagering.Rollback} {
			tx := transacaoEm(t, wagering.Pending, k)
			if err := tx.MarkPendingReference(depois); err != nil {
				t.Errorf("%s: MarkPendingReference devolveu erro: %v", k, err)
			}
			if tx.Status() != wagering.PendingReference {
				t.Errorf("%s: estado = %v, esperado PENDING_REFERENCE", k, tx.Status())
			}
		}
	})

	t.Run("demais tipos não esperam", func(t *testing.T) {
		for _, k := range []wagering.Kind{wagering.Bet, wagering.Win, wagering.Loss} {
			tx := transacaoEm(t, wagering.Pending, k)
			if err := tx.MarkPendingReference(depois); !errors.Is(err, wagering.ErrInvalidTransition) {
				t.Errorf("%s: devolveu %v, esperado ErrInvalidTransition", k, err)
			}
			if tx.Status() != wagering.Pending {
				t.Errorf("%s: estado mudou apesar da recusa", k)
			}
		}
	})
}

func TestResolucaoDeReferencia(t *testing.T) {
	tx := transacaoEm(t, wagering.PendingReference, wagering.Refund)

	if tx.ReferenceTransactionID() != uuid.Nil {
		t.Error("referência já nasceu resolvida")
	}
	if err := tx.ResolveReference(refID, depois); err != nil {
		t.Fatalf("ResolveReference devolveu erro: %v", err)
	}
	if tx.ReferenceTransactionID() != refID {
		t.Errorf("referência = %v, esperado %v", tx.ReferenceTransactionID(), refID)
	}
	// Resolver não conclui: o estado continua aguardando o processamento.
	if tx.Status() != wagering.PendingReference {
		t.Errorf("estado = %v, resolver referência não deveria concluir", tx.Status())
	}

	t.Run("tipo sem referência recusa", func(t *testing.T) {
		bet := transacaoEm(t, wagering.Pending, wagering.Bet)
		if err := bet.ResolveReference(refID, depois); !errors.Is(err, wagering.ErrReferencePolicy) {
			t.Errorf("devolveu %v, esperado ErrReferencePolicy", err)
		}
	})
}

func TestRejeicaoEFalhaExigemCodigoConhecido(t *testing.T) {
	t.Run("código conhecido é aceito", func(t *testing.T) {
		tx := transacaoEm(t, wagering.Pending, wagering.Bet)
		if err := tx.Reject(wagering.FailureInsufficientFunds, depois); err != nil {
			t.Fatalf("Reject devolveu erro: %v", err)
		}
		if tx.Status() != wagering.Rejected || tx.FailureCode() != wagering.FailureInsufficientFunds {
			t.Errorf("estado/código = %v/%v", tx.Status(), tx.FailureCode())
		}
	})

	t.Run("código desconhecido é recusado", func(t *testing.T) {
		tx := transacaoEm(t, wagering.Pending, wagering.Bet)
		if err := tx.Reject("ALGO_QUE_NAO_EXISTE", depois); !errors.Is(err, wagering.ErrInvalidFailureCode) {
			t.Fatalf("Reject devolveu %v, esperado ErrInvalidFailureCode", err)
		}
		if tx.Status() != wagering.Pending || tx.FailureCode() != "" {
			t.Error("código desconhecido alterou a transação")
		}
	})
}

// TestCodigosDeFalhaDistinguemSaldoDeApostaEDeReversao cobre a exigência
// explícita do desafio: reversão sem saldo tem código diferente de aposta sem
// saldo.
func TestCodigosDeFalhaDistinguemSaldoDeApostaEDeReversao(t *testing.T) {
	if wagering.FailureInsufficientFunds == wagering.FailureReversalInsufficientFunds {
		t.Fatal("aposta e reversão sem saldo precisam de códigos distintos")
	}

	corrigiveis := map[wagering.FailureCode]bool{
		wagering.FailureInsufficientFunds:         false,
		wagering.FailureReversalInsufficientFunds: false,
		wagering.FailureReferenceNotFound:         false,
		wagering.FailureReferenceAlreadyReversed:  false,
		wagering.FailureReferenceMismatch:         true,
		wagering.FailureWalletNotFound:            true,
		wagering.FailureInvalidAmount:             true,
	}
	for code, esperado := range corrigiveis {
		if got := code.Correctable(); got != esperado {
			t.Errorf("%s.Correctable() = %t, esperado %t", code, got, esperado)
		}
	}

	if _, err := wagering.ParseFailureCode("NAO_EXISTE"); !errors.Is(err, wagering.ErrInvalidFailureCode) {
		t.Errorf("ParseFailureCode devolveu %v, esperado ErrInvalidFailureCode", err)
	}
}

func TestHashDePayloadDistingueReplayDeConflito(t *testing.T) {
	req := requisicaoValida(t)
	req.PayloadHash = []byte{0x01, 0x02, 0x03}
	tx, err := wagering.NewExternal(req)
	if err != nil {
		t.Fatalf("NewExternal devolveu erro: %v", err)
	}

	if !tx.HasSamePayload([]byte{0x01, 0x02, 0x03}) {
		t.Error("mesmo payload foi reportado como diferente: seria conflito falso")
	}
	if tx.HasSamePayload([]byte{0x01, 0x02, 0x04}) {
		t.Error("payload diferente foi reportado como igual: seria replay indevido")
	}

	// O hash exposto é cópia: mutá-lo não pode corromper a transação.
	exposto := tx.PayloadHash()
	exposto[0] = 0xff
	if !tx.HasSamePayload([]byte{0x01, 0x02, 0x03}) {
		t.Error("mutação da cópia exposta alterou o estado interno")
	}

	// O hash de entrada também é copiado na construção.
	req.PayloadHash[0] = 0xff
	if !tx.HasSamePayload([]byte{0x01, 0x02, 0x03}) {
		t.Error("mutação do slice de entrada alterou o estado interno")
	}
}

func TestReidratacaoRecusaEstadoIncoerente(t *testing.T) {
	valido := func() wagering.Snapshot {
		return wagering.Snapshot{
			ID: txID, Kind: wagering.Bet, Status: wagering.Pending,
			WalletID: walletID, PlayerID: playerID, Amount: brlOf(t, "25.00"),
			ProviderID: "provider-a", ExternalTransactionID: "transaction-123",
			IdempotencyKey: "provider-a:transaction-123", PayloadHash: []byte{0xaa},
			RoundID: "round-987", GameID: "fortune-chimp",
			CreatedAt: instante, UpdatedAt: instante,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*wagering.Snapshot)
		wantErr error
	}{
		{"tipo desconhecido", func(s *wagering.Snapshot) { s.Kind = "TRANSFER" }, wagering.ErrInvalidKind},
		{"estado desconhecido", func(s *wagering.Snapshot) { s.Status = "WAITING" }, wagering.ErrInvalidStatus},
		{"externa sem provedor", func(s *wagering.Snapshot) { s.ProviderID = "" }, wagering.ErrMissingField},
		{"externa sem hash", func(s *wagering.Snapshot) { s.PayloadHash = nil }, wagering.ErrMissingField},
		{"atualização antes da criação", func(s *wagering.Snapshot) {
			s.UpdatedAt = instante.Add(-time.Hour)
		}, wagering.ErrInconsistentSnapshot},
		{"pendente com saldo resultante", func(s *wagering.Snapshot) {
			s.ResultBalance = brlOf(t, "975.00")
		}, wagering.ErrInconsistentSnapshot},
		{"processada sem saldo resultante", func(s *wagering.Snapshot) {
			s.Status = wagering.Processed
		}, wagering.ErrInconsistentSnapshot},
		{"pendente com código de falha", func(s *wagering.Snapshot) {
			s.FailureCode = wagering.FailureInsufficientFunds
		}, wagering.ErrInconsistentSnapshot},
		{"rejeitada sem código de falha", func(s *wagering.Snapshot) {
			s.Status = wagering.Rejected
		}, wagering.ErrInconsistentSnapshot},
		{"LOSS com valor", func(s *wagering.Snapshot) {
			s.Kind = wagering.Loss
		}, wagering.ErrAmountPolicy},
		{"reversão sem referência", func(s *wagering.Snapshot) {
			s.Kind = wagering.Refund
		}, wagering.ErrReferencePolicy},
		{"abertura interna com metadados externos", func(s *wagering.Snapshot) {
			s.Kind = wagering.Opening
			s.Status = wagering.Processed
			s.ResultBalance = brlOf(t, "25.00")
		}, wagering.ErrInconsistentSnapshot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valido()
			tt.mutate(&s)
			tx, err := wagering.Rehydrate(s)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Rehydrate devolveu %v, esperado %v", err, tt.wantErr)
			}
			if tx != nil {
				t.Error("Rehydrate devolveu transação utilizável junto com o erro")
			}
		})
	}
}

func TestReidratacaoPreservaEstadoSemReaplicar(t *testing.T) {
	atualizado := instante.Add(3 * time.Hour)
	tx, err := wagering.Rehydrate(wagering.Snapshot{
		ID: txID, Kind: wagering.Refund, Status: wagering.Processed,
		WalletID: walletID, PlayerID: playerID, Amount: brlOf(t, "25.00"),
		ProviderID: "provider-a", ExternalTransactionID: "transaction-999",
		IdempotencyKey: "provider-a:transaction-999", PayloadHash: []byte{0xcd},
		RoundID: "round-987", GameID: "fortune-chimp",
		ReferenceExternalTransactionID: "transaction-123", ReferenceTransactionID: refID,
		ResultBalance: brlOf(t, "1000.00"),
		CreatedAt:     instante, UpdatedAt: atualizado,
	})
	if err != nil {
		t.Fatalf("Rehydrate devolveu erro: %v", err)
	}

	if tx.Status() != wagering.Processed {
		t.Errorf("estado = %v, esperado PROCESSED", tx.Status())
	}
	if tx.ResultBalance().String() != "1000.00" {
		t.Errorf("saldo = %q, esperado \"1000.00\"", tx.ResultBalance())
	}
	if tx.ReferenceTransactionID() != refID {
		t.Error("referência resolvida não foi preservada")
	}
	if !tx.UpdatedAt().Equal(atualizado) {
		t.Error("instante de atualização não foi preservado")
	}
}
