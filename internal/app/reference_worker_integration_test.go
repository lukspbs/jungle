package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/platform/metrics"

	"github.com/lukspbs/jungle/internal/app"
)

// statusDe lê o estado atual de uma transação.
//
// As asserções desta suíte olham a transação, e não os contadores da varredura:
// o worker é global por natureza — ele varre todas as pendências do banco,
// inclusive as deixadas por outros testes — então contador agregado não diz
// nada sobre o caso em questão.
func (c cenario) statusDe(t *testing.T, id uuid.UUID) *wagering.WagerTransaction {
	t.Helper()
	tx, err := c.store.Read().Transactions.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	return tx
}

// worker devolve o worker e o relógio que o governa. O teste avança esse
// relógio para fazer os prazos vencerem, em vez de esperar de verdade.
func (c cenario) worker(t *testing.T, policy app.ReferencePolicy) (*app.ReferenceWorker, *relogioAjustavel) {
	t.Helper()
	relogio := novoRelogio(instante.Add(time.Second))
	return app.NewReferenceWorker(c.processar, c.store, relogio, loggerDeTeste(), metrics.New(), policy, 50, time.Millisecond), relogio
}

// TestWorkerConcluiAPendenciaQuandoAReferenciaChega é o cenário 7 da
// verificação obrigatória na sua forma bem-sucedida.
func TestWorkerConcluiAPendenciaQuandoAReferenciaChega(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	// O estorno chega antes da aposta.
	pendente, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "25.00", "estorno", "aposta"))
	if err != nil {
		t.Fatalf("estorno: %v", err)
	}
	if pendente.Status != wagering.PendingReference {
		t.Fatalf("estado = %v, esperado PENDING_REFERENCE", pendente.Status)
	}

	// Uma varredura antes da aposta chegar apenas adia.
	w, relogio := c.worker(t, politicaDeTeste)
	if _, err := w.Sweep(ctx); err != nil {
		t.Fatalf("varredura: %v", err)
	}
	if got := c.statusDe(t, pendente.TransactionID).Status(); got != wagering.PendingReference {
		t.Errorf("estado = %v, esperado continuar aguardando", got)
	}

	// A aposta chega.
	if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "aposta")); err != nil {
		t.Fatalf("aposta: %v", err)
	}
	if got := c.saldo(t); got.String() != "975.00" {
		t.Fatalf("saldo após aposta = %q", got)
	}

	// Agora a varredura conclui.
	relogio.Avanca(time.Second)
	if _, err := w.Sweep(ctx); err != nil {
		t.Fatalf("segunda varredura: %v", err)
	}
	if got := c.saldo(t); got.String() != "1000.00" {
		t.Errorf("saldo = %q, esperado \"1000.00\": o estorno não foi aplicado", got)
	}

	// O estado final é visível por consulta, com a referência resolvida.
	tx, err := c.store.Read().Transactions.FindByID(ctx, pendente.TransactionID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if tx.Status() != wagering.Processed {
		t.Errorf("estado = %v, esperado PROCESSED", tx.Status())
	}
	if tx.ReferenceTransactionID().String() == "00000000-0000-0000-0000-000000000000" {
		t.Error("a referência resolvida não foi gravada")
	}
	if tx.ResultBalance().String() != "1000.00" {
		t.Errorf("saldo congelado = %q", tx.ResultBalance())
	}

	tipos := c.eventosDa(t, pendente.TransactionID)
	if !contemTodos(tipos, "WagerTransactionPendingReference", "WagerTransactionProcessed", "WalletBalanceChanged") {
		t.Errorf("eventos = %v, esperado a pendência e a conclusão", tipos)
	}
}

// TestWorkerRecusaQuandoOPrazoEsgota é a outra metade do cenário 7: a
// referência nunca chega.
func TestWorkerRecusaQuandoOPrazoEsgota(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	pendente, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "25.00", "estorno", "nunca-chega"))
	if err != nil {
		t.Fatalf("estorno: %v", err)
	}

	// Política que esgota na terceira tentativa.
	w, relogio := c.worker(t, app.ReferencePolicy{
		TTL: time.Hour, MaxAttempts: 3,
		InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond,
	})

	for i := 0; i < 5; i++ {
		relogio.Avanca(time.Second)
		if _, err := w.Sweep(ctx); err != nil {
			t.Fatalf("varredura %d: %v", i, err)
		}
		if c.statusDe(t, pendente.TransactionID).Status().IsTerminal() {
			break
		}
	}

	tx, err := c.store.Read().Transactions.FindByID(ctx, pendente.TransactionID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if tx.Status() != wagering.Rejected {
		t.Errorf("estado = %v, esperado REJECTED", tx.Status())
	}
	if tx.FailureCode() != wagering.FailureReferenceNotFound {
		t.Errorf("código = %v, esperado REFERENCE_NOT_FOUND", tx.FailureCode())
	}
	if got := c.saldo(t); got.String() != "1000.00" {
		t.Errorf("saldo = %q: a recusa movimentou a carteira", got)
	}

	tipos := c.eventosDa(t, pendente.TransactionID)
	if !contem(tipos, "WagerTransactionRejected") {
		t.Errorf("eventos = %v, esperado a rejeição anunciada", tipos)
	}
}

// TestWorkerNaoReaplicaPendenciaJaConcluida cobre a reivindicação de uma
// pendência que outra instância resolveu no intervalo.
func TestWorkerNaoReaplicaPendenciaJaConcluida(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	if _, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "25.00", "estorno", "aposta")); err != nil {
		t.Fatalf("estorno: %v", err)
	}
	if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "aposta")); err != nil {
		t.Fatalf("aposta: %v", err)
	}

	w, relogio := c.worker(t, politicaDeTeste)
	if _, err := w.Sweep(ctx); err != nil {
		t.Fatalf("primeira varredura: %v", err)
	}
	saldoDepois := c.saldo(t)
	if saldoDepois.String() != "1000.00" {
		t.Fatalf("saldo após conclusão = %q", saldoDepois)
	}

	// Varreduras seguintes não encontram mais nada para fazer naquela
	// pendência, e o saldo não se move.
	for i := 0; i < 3; i++ {
		relogio.Avanca(time.Second)
		if _, err := w.Sweep(ctx); err != nil {
			t.Fatalf("varredura %d: %v", i, err)
		}
	}
	if got := c.saldo(t); !got.Equal(saldoDepois) {
		t.Errorf("saldo mudou para %q em varreduras seguintes", got)
	}
}

// TestWorkersConcorrentesNaoDuplicamAReversao usa duas instâncias do worker
// sobre a mesma pendência.
func TestWorkersConcorrentesNaoDuplicamAReversao(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	if _, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "40.00", "estorno", "aposta")); err != nil {
		t.Fatalf("estorno: %v", err)
	}
	if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "40.00", "aposta")); err != nil {
		t.Fatalf("aposta: %v", err)
	}

	a, _ := c.worker(t, politicaDeTeste)
	b, _ := c.worker(t, politicaDeTeste)

	resultados := make(chan app.SweepResult, 2)
	erros := make(chan error, 2)
	for _, w := range []*app.ReferenceWorker{a, b} {
		go func(w *app.ReferenceWorker) {
			res, err := w.Sweep(ctx)
			resultados <- res
			erros <- err
		}(w)
	}

	for i := 0; i < 2; i++ {
		if err := <-erros; err != nil {
			t.Fatalf("varredura concorrente: %v", err)
		}
		<-resultados
	}
	if got := c.saldo(t); got.String() != "1000.00" {
		t.Errorf("saldo = %q, esperado \"1000.00\": a reversão foi aplicada duas vezes", got)
	}
}

func TestVarreduraVaziaNaoFazNada(t *testing.T) {
	c := novoCenario(t, "100.00")
	w, _ := c.worker(t, politicaDeTeste)
	if _, err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("varredura: %v", err)
	}
	if got := c.saldo(t); got.String() != "100.00" {
		t.Errorf("saldo = %q: a varredura mexeu numa carteira sem pendências", got)
	}
}
