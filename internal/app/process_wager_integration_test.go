package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/events"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/domain/wallet"
	"github.com/lukspbs/jungle/test/dbtest"
)

type cenario struct {
	store     *postgres.Store
	processar *app.ProcessWager
	carteira  *wallet.Wallet

	// prefixo isola os identificadores externos deste cenário. A suíte
	// compartilha o banco e nunca o limpa, então um id fixo como "ext-bet-1"
	// colidiria com a execução anterior e viraria conflito de idempotência.
	prefixo string
}

// novoCenario abre uma carteira com o saldo pedido e devolve o caso de uso
// pronto para receber operações sobre ela.
func novoCenario(t *testing.T, saldo string) cenario {
	t.Helper()
	store := dbtest.Store(t)
	ids := novosIDs(t)
	relogio := relogioFixo{instante}

	res, err := app.NewOpenWallet(store, relogio, ids).Execute(context.Background(),
		app.OpenWalletCommand{
			PlayerID: uuid.New(), InitialBalance: brlOf(t, saldo), CorrelationID: "corr-setup",
		})
	if err != nil {
		t.Fatalf("abertura da carteira: %v", err)
	}
	return cenario{
		store:     store,
		processar: app.NewProcessWager(store, relogio, ids),
		carteira:  res.Wallet,
		prefixo:   uuid.NewString(),
	}
}

func (c cenario) comando(kind wagering.Kind, valor, externalID string) app.ProcessWagerCommand {
	externalID = c.prefixo + "-" + externalID
	return app.ProcessWagerCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: externalID,
		IdempotencyKey:        "provider-a:" + externalID,
		PlayerID:              c.carteira.PlayerID(),
		WalletID:              c.carteira.ID(),
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 mustBRL(valor),
		CorrelationID:         "corr-" + externalID,
	}
}

func mustBRL(amount string) money.Money {
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		panic(err)
	}
	return m
}

func (c cenario) saldo(t *testing.T) money.Money {
	t.Helper()
	w, err := c.store.Read().Wallets.FindByID(context.Background(), c.carteira.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	return w.Balance()
}

func (c cenario) versao(t *testing.T) int64 {
	t.Helper()
	w, err := c.store.Read().Wallets.FindByID(context.Background(), c.carteira.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	return w.Version()
}

func (c cenario) eventosDa(t *testing.T, txID uuid.UUID) []string {
	t.Helper()
	ctx := context.Background()
	daTx, err := c.store.Read().Outbox.ListByAggregate(ctx, events.AggregateWagerTransaction, txID)
	if err != nil {
		t.Fatalf("ListByAggregate: %v", err)
	}
	daCarteira, err := c.store.Read().Outbox.ListByAggregate(ctx, events.AggregateWallet, c.carteira.ID())
	if err != nil {
		t.Fatalf("ListByAggregate: %v", err)
	}

	var tipos []string
	for _, r := range daTx {
		tipos = append(tipos, r.EventType)
	}
	for _, r := range daCarteira {
		// Só os desta transação: a carteira acumula eventos de toda a vida.
		var payload struct {
			Data struct {
				TransactionID uuid.UUID `json:"transactionId"`
			} `json:"data"`
		}
		if err := jsonDecode(r.Payload, &payload); err != nil {
			t.Fatalf("payload inválido: %v", err)
		}
		if payload.Data.TransactionID == txID {
			tipos = append(tipos, r.EventType)
		}
	}
	return tipos
}

func TestApostaDebitaEEmiteOsDoisEventos(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	res, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "ext-bet-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if res.Status != wagering.Processed {
		t.Errorf("estado = %v, esperado PROCESSED", res.Status)
	}
	if res.Balance.String() != "975.00" {
		t.Errorf("saldo devolvido = %q, esperado \"975.00\"", res.Balance)
	}
	if res.IdempotentReplay {
		t.Error("primeira chegada marcada como replay")
	}
	if got := c.saldo(t); got.String() != "975.00" {
		t.Errorf("saldo persistido = %q", got)
	}
	if got := c.versao(t); got != 2 {
		t.Errorf("versão = %d, esperado 2", got)
	}

	tipos := c.eventosDa(t, res.TransactionID)
	if !contemTodos(tipos, "WagerTransactionProcessed", "WalletBalanceChanged") {
		t.Errorf("eventos = %v, esperado os dois", tipos)
	}
}

func TestGanhoCredita(t *testing.T) {
	c := novoCenario(t, "100.00")
	res, err := c.processar.Execute(context.Background(), c.comando(wagering.Win, "50.00", "ext-win-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Balance.String() != "150.00" {
		t.Errorf("saldo = %q, esperado \"150.00\"", res.Balance)
	}
}

// TestDerrotaConcluiSemMovimentar cobre a regra do desafio: LOSS exige valor
// zero, não cria lançamento, não altera a versão e produz apenas
// WagerTransactionProcessed.
func TestDerrotaConcluiSemMovimentar(t *testing.T) {
	c := novoCenario(t, "100.00")
	ctx := context.Background()
	versaoAntes := c.versao(t)

	res, err := c.processar.Execute(ctx, c.comando(wagering.Loss, "0.00", "ext-loss-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if res.Status != wagering.Processed {
		t.Errorf("estado = %v, esperado PROCESSED", res.Status)
	}
	if res.Balance.String() != "100.00" {
		t.Errorf("saldo = %q, esperado \"100.00\" inalterado", res.Balance)
	}
	if got := c.versao(t); got != versaoAntes {
		t.Errorf("versão avançou de %d para %d sem movimentação", versaoAntes, got)
	}

	tipos := c.eventosDa(t, res.TransactionID)
	if contem(tipos, "WalletBalanceChanged") {
		t.Errorf("LOSS produziu WalletBalanceChanged: %v", tipos)
	}
	if !contem(tipos, "WagerTransactionProcessed") {
		t.Errorf("LOSS não produziu WagerTransactionProcessed: %v", tipos)
	}

	// Nenhum lançamento novo além do crédito de abertura.
	resumo, err := c.store.Read().Ledger.Summarize(ctx, c.carteira.ID(), money.MustCurrency("BRL"))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if resumo.Entries != 1 {
		t.Errorf("lançamentos = %d, esperado 1 (só a abertura)", resumo.Entries)
	}
}

func TestApostaSemSaldoEhRecusadaEPersistida(t *testing.T) {
	c := novoCenario(t, "10.00")
	ctx := context.Background()

	res, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "ext-sem-saldo"))
	if err != nil {
		t.Fatalf("Execute devolveu erro, esperado recusa persistida: %v", err)
	}

	if res.Status != wagering.Rejected {
		t.Errorf("estado = %v, esperado REJECTED", res.Status)
	}
	if res.FailureCode != wagering.FailureInsufficientFunds {
		t.Errorf("código = %v, esperado INSUFFICIENT_FUNDS", res.FailureCode)
	}
	if got := c.saldo(t); got.String() != "10.00" {
		t.Errorf("saldo mudou para %q apesar da recusa", got)
	}

	tipos := c.eventosDa(t, res.TransactionID)
	if !contem(tipos, "WagerTransactionRejected") || contem(tipos, "WalletBalanceChanged") {
		t.Errorf("eventos = %v, esperado apenas a rejeição", tipos)
	}

	// A recusa é terminal e foi persistida: um reenvio devolve o mesmo
	// resultado em vez de reprocessar.
	repetido, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "ext-sem-saldo"))
	if err != nil {
		t.Fatalf("reenvio: %v", err)
	}
	if !repetido.IdempotentReplay || repetido.Status != wagering.Rejected {
		t.Errorf("reenvio = replay %t / %v", repetido.IdempotentReplay, repetido.Status)
	}
}

// TestReplayDevolveOSaldoDoProcessamentoOriginal é o requisito sutil do §9: o
// saldo devolvido num replay é o observado na época, não o atual.
func TestReplayDevolveOSaldoDoProcessamentoOriginal(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	primeira, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "ext-replay"))
	if err != nil {
		t.Fatalf("primeira: %v", err)
	}
	if primeira.Balance.String() != "975.00" {
		t.Fatalf("saldo original = %q", primeira.Balance)
	}

	// A carteira se movimenta depois.
	if _, err := c.processar.Execute(ctx, c.comando(wagering.Win, "500.00", "ext-depois")); err != nil {
		t.Fatalf("movimentação posterior: %v", err)
	}
	if got := c.saldo(t); got.String() != "1475.00" {
		t.Fatalf("saldo atual = %q, esperado \"1475.00\"", got)
	}

	replay, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "ext-replay"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.IdempotentReplay {
		t.Error("não foi marcado como replay")
	}
	if replay.Balance.String() != "975.00" {
		t.Errorf("replay devolveu %q, esperado o saldo original \"975.00\"", replay.Balance)
	}
	if replay.TransactionID != primeira.TransactionID {
		t.Error("o replay criou outra transação")
	}
}

func TestChaveReutilizadaComConteudoDiferenteEhConflito(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "ext-conflito")); err != nil {
		t.Fatalf("primeira: %v", err)
	}

	// Mesma chave, valor diferente.
	outro := c.comando(wagering.Bet, "99.00", "ext-conflito")
	_, err := c.processar.Execute(ctx, outro)
	if !errors.Is(err, app.ErrIdempotencyConflict) {
		t.Errorf("devolveu %v, esperado ErrIdempotencyConflict", err)
	}
}

// TestMesmaOperacaoSobOutraChaveNaoReaplica cobre a regra de que
// (provider, externalTransactionId) identifica a operação financeira.
func TestMesmaOperacaoSobOutraChaveNaoReaplica(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	primeira, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "ext-outra-chave"))
	if err != nil {
		t.Fatalf("primeira: %v", err)
	}

	sobOutraChave := c.comando(wagering.Bet, "25.00", "ext-outra-chave")
	sobOutraChave.IdempotencyKey = "chave-totalmente-diferente"

	segunda, err := c.processar.Execute(ctx, sobOutraChave)
	if err != nil {
		t.Fatalf("segunda: %v", err)
	}
	if segunda.TransactionID != primeira.TransactionID {
		t.Error("a operação foi reaplicada sob outra chave")
	}
	if c.saldo(t).String() != "975.00" {
		t.Errorf("saldo = %q: houve movimentação duplicada", c.saldo(t))
	}
}

func TestCarteiraInexistenteEDeOutroJogador(t *testing.T) {
	c := novoCenario(t, "100.00")
	ctx := context.Background()

	t.Run("carteira inexistente", func(t *testing.T) {
		cmd := c.comando(wagering.Bet, "10.00", "ext-sem-carteira")
		cmd.WalletID = uuid.New()
		if _, err := c.processar.Execute(ctx, cmd); !errors.Is(err, app.ErrWalletNotFound) {
			t.Errorf("devolveu %v, esperado ErrWalletNotFound", err)
		}
	})

	t.Run("carteira de outro jogador", func(t *testing.T) {
		cmd := c.comando(wagering.Bet, "10.00", "ext-outro-jogador")
		cmd.PlayerID = uuid.New()
		res, err := c.processar.Execute(ctx, cmd)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.Status != wagering.Rejected || res.FailureCode != wagering.FailureWalletPlayerMismatch {
			t.Errorf("resultado = %v/%v", res.Status, res.FailureCode)
		}
	})

	t.Run("moeda divergente", func(t *testing.T) {
		emDolar, err := money.Parse("10.00", "USD")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		cmd := c.comando(wagering.Bet, "10.00", "ext-moeda")
		cmd.Money = emDolar
		res, err := c.processar.Execute(ctx, cmd)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.Status != wagering.Rejected || res.FailureCode != wagering.FailureCurrencyMismatch {
			t.Errorf("resultado = %v/%v", res.Status, res.FailureCode)
		}
	})
}

// TestCinquentaEnviosParalelosProduzemUmDebito é o cenário 1 da verificação
// obrigatória do desafio.
func TestCinquentaEnviosParalelosProduzemUmDebito(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()
	cmd := c.comando(wagering.Bet, "25.00", "ext-50x")

	const envios = 50
	resultados := make([]app.ProcessWagerResult, envios)
	erros := make([]error, envios)

	var wg sync.WaitGroup
	largada := make(chan struct{})
	for i := 0; i < envios; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-largada
			resultados[i], erros[i] = c.processar.Execute(ctx, cmd)
		}(i)
	}
	close(largada)
	wg.Wait()

	var primeiras, replays int
	for i, err := range erros {
		if err != nil {
			t.Fatalf("envio %d devolveu erro: %v", i, err)
		}
		if resultados[i].IdempotentReplay {
			replays++
		} else {
			primeiras++
		}
		if resultados[i].TransactionID != resultados[0].TransactionID {
			t.Fatalf("envio %d produziu outra transação", i)
		}
	}

	if primeiras != 1 {
		t.Errorf("processamentos reais = %d, esperado 1 (replays: %d)", primeiras, replays)
	}
	if got := c.saldo(t); got.String() != "975.00" {
		t.Errorf("saldo = %q, esperado \"975.00\": houve movimentação duplicada", got)
	}
	if got := c.versao(t); got != 2 {
		t.Errorf("versão = %d, esperado 2", got)
	}

	debitos := contaDebitos(t, c)
	if debitos != 1 {
		t.Errorf("débitos no ledger = %d, esperado 1", debitos)
	}
}

// TestDuasApostasDe80Sobre100 é o cenário 2 da verificação obrigatória.
func TestDuasApostasDe80Sobre100(t *testing.T) {
	c := novoCenario(t, "100.00")
	ctx := context.Background()

	resultados := make([]app.ProcessWagerResult, 2)
	erros := make([]error, 2)
	var wg sync.WaitGroup
	largada := make(chan struct{})

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := c.comando(wagering.Bet, "80.00", fmt.Sprintf("ext-disputa-%d", i))
			<-largada
			resultados[i], erros[i] = c.processar.Execute(ctx, cmd)
		}(i)
	}
	close(largada)
	wg.Wait()

	var processadas, recusadas int
	for i, err := range erros {
		if err != nil {
			t.Fatalf("aposta %d devolveu erro: %v", i, err)
		}
		switch resultados[i].Status {
		case wagering.Processed:
			processadas++
		case wagering.Rejected:
			recusadas++
			if resultados[i].FailureCode != wagering.FailureInsufficientFunds {
				t.Errorf("recusa com código %v", resultados[i].FailureCode)
			}
		}
	}

	if processadas != 1 || recusadas != 1 {
		t.Fatalf("processadas=%d recusadas=%d, esperado 1 e 1", processadas, recusadas)
	}
	if got := c.saldo(t); got.String() != "20.00" {
		t.Errorf("saldo final = %q, esperado \"20.00\"", got)
	}
	if debitos := contaDebitos(t, c); debitos != 1 {
		t.Errorf("débitos no ledger = %d, esperado 1", debitos)
	}

	// Reenviar as duas não altera o resultado.
	for i := 0; i < 2; i++ {
		cmd := c.comando(wagering.Bet, "80.00", fmt.Sprintf("ext-disputa-%d", i))
		res, err := c.processar.Execute(ctx, cmd)
		if err != nil {
			t.Fatalf("reenvio %d: %v", i, err)
		}
		if !res.IdempotentReplay {
			t.Errorf("reenvio %d não foi replay", i)
		}
	}
	if got := c.saldo(t); got.String() != "20.00" {
		t.Errorf("saldo após reenvios = %q", got)
	}
}

// TestCarteirasDistintasAvancamEmParalelo confere que o lock é por carteira:
// operações em carteiras diferentes não se bloqueiam.
func TestCarteirasDistintasAvancamEmParalelo(t *testing.T) {
	ctx := context.Background()
	const carteiras = 8

	cenarios := make([]cenario, carteiras)
	for i := range cenarios {
		cenarios[i] = novoCenario(t, "100.00")
	}

	var wg sync.WaitGroup
	erros := make([]error, carteiras)
	largada := make(chan struct{})
	for i := range cenarios {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-largada
			_, erros[i] = cenarios[i].processar.Execute(ctx,
				cenarios[i].comando(wagering.Bet, "30.00", fmt.Sprintf("ext-paralelo-%d", i)))
		}(i)
	}
	close(largada)
	wg.Wait()

	for i, err := range erros {
		if err != nil {
			t.Fatalf("carteira %d: %v", i, err)
		}
		if got := cenarios[i].saldo(t); got.String() != "70.00" {
			t.Errorf("carteira %d ficou com %q, esperado \"70.00\"", i, got)
		}
	}
}

func contaDebitos(t *testing.T, c cenario) int {
	t.Helper()
	pagina, err := c.store.Read().Ledger.ListByWallet(context.Background(), c.carteira.ID(), 0, 200)
	if err != nil {
		t.Fatalf("ListByWallet: %v", err)
	}
	var n int
	for _, e := range pagina.Entries {
		if e.Direction() == wallet.Debit {
			n++
		}
	}
	return n
}

func contem(valores []string, alvo string) bool {
	for _, v := range valores {
		if v == alvo {
			return true
		}
	}
	return false
}

func contemTodos(valores []string, alvos ...string) bool {
	for _, a := range alvos {
		if !contem(valores, a) {
			return false
		}
	}
	return true
}
