package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/domain/events"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/test/dbtest"
)

func eventoDeTeste(t *testing.T, walletID, txID uuid.UUID, agora time.Time) events.Event {
	t.Helper()
	e, err := events.New(novaID(t), events.WagerTransactionProcessed{
		TransactionID: txID, ProviderID: "provider-a", ExternalTransactionID: "ext-" + txID.String(),
		WalletID: walletID, PlayerID: novaID(t),
		RoundID: "round-987", GameID: "fortune-chimp",
		Kind: wagering.Bet, Money: brlOf(t, "25.00"), Balance: brlOf(t, "975.00"),
		ProcessedAt: agora.UTC().Format("2006-01-02T15:04:05.000Z"),
	}, "corr-"+txID.String(), "", agora)
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	return e
}

func gravaEvento(t *testing.T, store *postgres.Store, e events.Event) {
	t.Helper()
	err := store.InTx(context.Background(), func(ctx context.Context, r *postgres.Repositories) error {
		return r.Outbox.Insert(ctx, e)
	})
	if err != nil {
		t.Fatalf("Outbox.Insert: %v", err)
	}
}

func TestOutboxPreservaOEnvelopeByteABytes(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	agora := time.Now().UTC().Truncate(time.Millisecond)

	e := eventoDeTeste(t, novaID(t), novaID(t), agora)
	esperado, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	gravaEvento(t, store, e)

	rec, achou, err := store.Read().Outbox.FindByID(ctx, e.ID())
	if err != nil || !achou {
		t.Fatalf("FindByID: %v (achou=%t)", err, achou)
	}
	if !bytes.Equal(rec.Payload, esperado) {
		t.Errorf("payload mudou na ida e volta:\n  gravado: %s\n  lido:    %s", esperado, rec.Payload)
	}
	if rec.EventType != string(events.TypeWagerTransactionProcessed) {
		t.Errorf("tipo = %q", rec.EventType)
	}
	if rec.Attempts != 0 {
		t.Errorf("tentativas = %d, esperado 0 antes de qualquer reivindicação", rec.Attempts)
	}
}

// TestDoisPublishersNaoPegamOMesmoEvento é a garantia que o SKIP LOCKED
// oferece: publishers concorrentes dividem a fila em vez de disputarem linha.
func TestDoisPublishersNaoPegamOMesmoEvento(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	agora := time.Now().UTC()

	meus := map[uuid.UUID]bool{}
	for i := 0; i < 20; i++ {
		e := eventoDeTeste(t, novaID(t), novaID(t), agora)
		gravaEvento(t, store, e)
		meus[e.ID()] = true
	}

	var (
		mu       sync.Mutex
		coletado = map[uuid.UUID]int{}
		wg       sync.WaitGroup
		largada  = make(chan struct{})
	)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-largada
			for volta := 0; volta < 5; volta++ {
				recs, err := store.Read().Outbox.Claim(ctx,
					"instancia-"+uuid.NewString(), 5, time.Minute, time.Now().UTC())
				if err != nil {
					t.Errorf("Claim: %v", err)
					return
				}
				mu.Lock()
				for _, r := range recs {
					if meus[r.EventID] {
						coletado[r.EventID]++
					}
				}
				mu.Unlock()
			}
		}(i)
	}
	close(largada)
	wg.Wait()

	for id, vezes := range coletado {
		if vezes > 1 {
			t.Errorf("evento %s foi reivindicado %d vezes dentro do lease", id, vezes)
		}
	}
	if len(coletado) != len(meus) {
		t.Errorf("eventos reivindicados = %d, esperado %d", len(coletado), len(meus))
	}
}

// TestLeaseVencidoLiberaTrabalhoAbandonado cobre a queda de uma instância entre
// a reivindicação e a publicação.
func TestLeaseVencidoLiberaTrabalhoAbandonado(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	agora := time.Now().UTC()

	e := eventoDeTeste(t, novaID(t), novaID(t), agora)
	gravaEvento(t, store, e)

	// Instância A reivindica e "morre" sem publicar.
	recs, err := store.Read().Outbox.Claim(ctx, "instancia-a", 10, 2*time.Second, agora)
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}
	if !contemEvento(recs, e.ID()) {
		t.Fatal("instância A não reivindicou o evento")
	}

	// Enquanto o lease vale, ninguém mais pega.
	recs, err = store.Read().Outbox.Claim(ctx, "instancia-b", 10, time.Minute, agora.Add(time.Second))
	if err != nil {
		t.Fatalf("Claim B dentro do lease: %v", err)
	}
	if contemEvento(recs, e.ID()) {
		t.Error("instância B reivindicou um evento com lease válido")
	}

	// Depois que vence, outra instância assume.
	recs, err = store.Read().Outbox.Claim(ctx, "instancia-b", 10, time.Minute, agora.Add(5*time.Second))
	if err != nil {
		t.Fatalf("Claim B após o lease: %v", err)
	}
	if !contemEvento(recs, e.ID()) {
		t.Error("instância B não assumiu o evento abandonado")
	}

	// E o eventId é o mesmo: republicação preserva a identidade.
	rec, _, err := store.Read().Outbox.FindByID(ctx, e.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if rec.EventID != e.ID() {
		t.Error("o eventId mudou entre as tentativas")
	}
	if rec.Attempts != 2 {
		t.Errorf("tentativas = %d, esperado 2", rec.Attempts)
	}
}

func TestPublicacaoConfirmadaSaiDaFila(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	agora := time.Now().UTC()

	e := eventoDeTeste(t, novaID(t), novaID(t), agora)
	gravaEvento(t, store, e)

	if _, err := store.Read().Outbox.Claim(ctx, "instancia-a", 10, time.Minute, agora); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := store.Read().Outbox.MarkPublished(ctx, e.ID(), agora); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}

	// Nem com o lease vencido ele volta: publicado é definitivo.
	recs, err := store.Read().Outbox.Claim(ctx, "instancia-b", 10, time.Minute, agora.Add(time.Hour))
	if err != nil {
		t.Fatalf("Claim após publicação: %v", err)
	}
	if contemEvento(recs, e.ID()) {
		t.Error("evento já publicado voltou para a fila")
	}

	// Confirmar duas vezes não é erro: o resultado desejado já está lá.
	if err := store.Read().Outbox.MarkPublished(ctx, e.ID(), agora); err != nil {
		t.Errorf("segunda confirmação devolveu erro: %v", err)
	}
}

func TestReagendamentoRespeitaOPrazo(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	agora := time.Now().UTC()

	e := eventoDeTeste(t, novaID(t), novaID(t), agora)
	gravaEvento(t, store, e)

	if _, err := store.Read().Outbox.Claim(ctx, "instancia-a", 10, time.Minute, agora); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Publicação falhou: volta para a fila daqui a 30s.
	if err := store.Read().Outbox.Reschedule(ctx, e.ID(), agora.Add(30*time.Second)); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}

	recs, err := store.Read().Outbox.Claim(ctx, "instancia-b", 10, time.Minute, agora.Add(10*time.Second))
	if err != nil {
		t.Fatalf("Claim antes do prazo: %v", err)
	}
	if contemEvento(recs, e.ID()) {
		t.Error("evento foi reivindicado antes do prazo de nova tentativa")
	}

	recs, err = store.Read().Outbox.Claim(ctx, "instancia-b", 10, time.Minute, agora.Add(31*time.Second))
	if err != nil {
		t.Fatalf("Claim após o prazo: %v", err)
	}
	if !contemEvento(recs, e.ID()) {
		t.Error("evento não voltou para a fila depois do prazo")
	}
}

func TestEventoNaoSobreviveAoRollback(t *testing.T) {
	store := dbtest.Store(t)
	ctx := context.Background()
	agora := time.Now().UTC()
	e := eventoDeTeste(t, novaID(t), novaID(t), agora)

	falha := context.Canceled
	err := store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		if err := r.Outbox.Insert(ctx, e); err != nil {
			return err
		}
		return falha
	})
	if err != falha {
		t.Fatalf("InTx devolveu %v, esperado a falha proposital", err)
	}

	// Nenhum evento é publicado antes do commit que o originou — e se o commit
	// não acontece, o evento não existe.
	_, achou, err := store.Read().Outbox.FindByID(ctx, e.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if achou {
		t.Error("o evento sobreviveu ao rollback da transação que o originou")
	}
}

func contemEvento(recs []postgres.OutboxRecord, id uuid.UUID) bool {
	for _, r := range recs {
		if r.EventID == id {
			return true
		}
	}
	return false
}
