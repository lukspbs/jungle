package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	adaptersqs "github.com/lukspbs/jungle/internal/adapter/sqs"
	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/test/dbtest"
	"github.com/lukspbs/jungle/test/sqstest"
)

// publicadorControlado liga e desliga a falha sob comando.
//
// Contar falhas por chamada não serve: a outbox é compartilhada, e as primeiras
// chamadas de uma varredura podem ser de eventos de outros testes. O controle
// explícito torna o teste determinístico independentemente do que mais está na
// fila.
type publicadorControlado struct {
	mu       sync.Mutex
	falhando bool
	entregas map[string]int
}

func novoPublicadorControlado(falhando bool) *publicadorControlado {
	return &publicadorControlado{falhando: falhando, entregas: map[string]int{}}
}

func (p *publicadorControlado) Publish(_ context.Context, rec postgres.OutboxRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.falhando {
		return errors.New("falha proposital de publicação")
	}
	p.entregas[rec.EventID.String()]++
	return nil
}

func (p *publicadorControlado) pararDeFalhar() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.falhando = false
}

func (p *publicadorControlado) entregasDe(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entregas[id]
}

// novoPublicador devolve o publicador e o relógio que o governa.
//
// O relógio precisa avançar: o reagendamento após falha grava um prazo futuro, e
// um relógio parado nunca o faria vencer — a nova tentativa jamais aconteceria.
func novoPublicador(
	t *testing.T, store *postgres.Store, destino app.EventPublisher,
) (*app.OutboxPublisher, *relogioAjustavel) {
	t.Helper()
	relogio := novoRelogio(instante.Add(time.Minute))
	return app.NewOutboxPublisher(store, destino, relogio, "instancia-"+t.Name(),
		50, 30*time.Second, time.Millisecond, time.Millisecond, 10*time.Millisecond), relogio
}

// TestEventoChegaNaFilaComOsMesmosBytes é a prova de ponta a ponta: o evento
// gravado no commit sai na fila sem ser remontado.
func TestEventoChegaNaFilaComOsMesmosBytes(t *testing.T) {
	store := dbtest.Store(t)
	dbtest.DrainOutbox(t, dbtest.Pool(t))
	client, cfg := sqstest.Client(t)
	sqstest.Drain(t, client, cfg.OutboundQueueURL)

	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	res, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "publicar"))
	if err != nil {
		t.Fatalf("aposta: %v", err)
	}

	// O payload gravado, antes de publicar.
	gravados, err := store.Read().Outbox.ListByAggregate(ctx,
		"WagerTransaction", res.TransactionID)
	if err != nil {
		t.Fatalf("ListByAggregate: %v", err)
	}
	if len(gravados) != 1 {
		t.Fatalf("eventos gravados = %d, esperado 1", len(gravados))
	}
	esperado := gravados[0].Payload

	publicador, _ := novoPublicador(t, c.store, adaptersqs.NewPublisher(client, cfg))
	if _, err := publicador.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	mensagens := sqstest.Receive(t, client, cfg.OutboundQueueURL, 1, 10*time.Second)
	if len(mensagens) == 0 {
		t.Fatal("nenhuma mensagem chegou na fila")
	}

	var achou bool
	for _, m := range mensagens {
		if bytes.Equal([]byte(*m.Body), esperado) {
			achou = true
			if attr, ok := m.MessageAttributes["eventId"]; !ok || *attr.StringValue != gravados[0].EventID.String() {
				t.Error("o atributo eventId não acompanha a mensagem")
			}
			if attr, ok := m.MessageAttributes["eventType"]; !ok || *attr.StringValue == "" {
				t.Error("o atributo eventType não acompanha a mensagem")
			}
		}
	}
	if !achou {
		t.Errorf("o corpo publicado difere do gravado.\n  gravado: %s", esperado)
	}

	// Publicado sai da fila da outbox e não volta.
	rec, _, err := store.Read().Outbox.FindByID(ctx, gravados[0].EventID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if _, err := publicador.Sweep(ctx); err != nil {
		t.Fatalf("segunda varredura: %v", err)
	}
	novamente, _, _ := store.Read().Outbox.FindByID(ctx, rec.EventID)
	if novamente.Attempts != rec.Attempts {
		t.Error("o evento já publicado foi reivindicado de novo")
	}
}

// TestFalhaDePublicacaoNaoPerdeOEvento confere que a entrega devolve o registro
// à fila em vez de descartá-lo.
func TestFalhaDePublicacaoNaoPerdeOEvento(t *testing.T) {
	store := dbtest.Store(t)
	dbtest.DrainOutbox(t, dbtest.Pool(t))

	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	res, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "falha"))
	if err != nil {
		t.Fatalf("aposta: %v", err)
	}
	gravados, _ := store.Read().Outbox.ListByAggregate(ctx, "WagerTransaction", res.TransactionID)
	if len(gravados) != 1 {
		t.Fatalf("eventos = %d", len(gravados))
	}
	eventID := gravados[0].EventID

	destino := novoPublicadorControlado(true)
	publicador, relogio := novoPublicador(t, c.store, destino)

	// Duas varreduras com o destino fora do ar.
	for i := 0; i < 2; i++ {
		if _, err := publicador.Sweep(ctx); err != nil {
			t.Fatalf("varredura %d: %v", i, err)
		}
		relogio.Avanca(time.Minute)
	}

	// O evento continua pendente e acumulou tentativas: nada foi descartado.
	rec, achou, err := store.Read().Outbox.FindByID(ctx, eventID)
	if err != nil || !achou {
		t.Fatalf("FindByID: %v (achou=%t)", err, achou)
	}
	if rec.Attempts < 2 {
		t.Errorf("tentativas = %d, esperado ao menos 2", rec.Attempts)
	}
	if destino.entregasDe(eventID.String()) != 0 {
		t.Error("o evento foi contado como entregue apesar das falhas")
	}

	// O destino volta: a varredura seguinte entrega.
	destino.pararDeFalhar()
	if _, err := publicador.Sweep(ctx); err != nil {
		t.Fatalf("varredura de recuperação: %v", err)
	}
	if destino.entregasDe(eventID.String()) != 1 {
		t.Errorf("entregas = %d, esperado 1", destino.entregasDe(eventID.String()))
	}

	// E depois do sucesso ele não volta para a fila.
	antes, _, _ := store.Read().Outbox.FindByID(ctx, eventID)
	relogio.Avanca(time.Minute)
	if _, err := publicador.Sweep(ctx); err != nil {
		t.Fatalf("varredura final: %v", err)
	}
	depois, _, _ := store.Read().Outbox.FindByID(ctx, eventID)
	if depois.Attempts != antes.Attempts {
		t.Errorf("o evento publicado voltou para a fila: %d -> %d", antes.Attempts, depois.Attempts)
	}
	if destino.entregasDe(eventID.String()) != 1 {
		t.Errorf("o evento foi entregue %d vezes", destino.entregasDe(eventID.String()))
	}
}

// TestDoisPublicadoresNaoDuplicamAEntrega cobre o cenário 6 da verificação
// obrigatória: dois publishers disputando a mesma outbox.
func TestDoisPublicadoresNaoDuplicamAEntrega(t *testing.T) {
	store := dbtest.Store(t)
	dbtest.DrainOutbox(t, dbtest.Pool(t))

	c := novoCenario(t, "10000.00")
	ctx := context.Background()

	meus := map[string]bool{}
	for i := 0; i < 6; i++ {
		res, err := c.processar.Execute(ctx,
			c.comando(wagering.Bet, "10.00", "dois-pub-"+string(rune('a'+i))))
		if err != nil {
			t.Fatalf("aposta %d: %v", i, err)
		}
		recs, _ := store.Read().Outbox.ListByAggregate(ctx, "WagerTransaction", res.TransactionID)
		for _, r := range recs {
			meus[r.EventID.String()] = true
		}
	}

	var (
		mu       sync.Mutex
		entregas = map[string]int{}
	)
	contador := contadorDeEntregas{mu: &mu, por: entregas}

	a, _ := novoPublicador(t, c.store, contador)
	b, _ := novoPublicador(t, c.store, contador)

	var wg sync.WaitGroup
	largada := make(chan struct{})
	for _, p := range []*app.OutboxPublisher{a, b} {
		wg.Add(1)
		go func(p *app.OutboxPublisher) {
			defer wg.Done()
			<-largada
			for i := 0; i < 3; i++ {
				if _, err := p.Sweep(ctx); err != nil {
					t.Errorf("Sweep: %v", err)
					return
				}
			}
		}(p)
	}
	close(largada)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	for id, vezes := range entregas {
		if meus[id] && vezes > 1 {
			t.Errorf("evento %s foi publicado %d vezes", id, vezes)
		}
	}
	var publicados int
	for id := range meus {
		if entregas[id] > 0 {
			publicados++
		}
	}
	if publicados != len(meus) {
		t.Errorf("eventos publicados = %d, esperado %d", publicados, len(meus))
	}
}

type contadorDeEntregas struct {
	mu  *sync.Mutex
	por map[string]int
}

func (c contadorDeEntregas) Publish(_ context.Context, rec postgres.OutboxRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.por[rec.EventID.String()]++
	return nil
}

// TestPayloadPublicadoEhOEnvelopeDoContrato confere a forma do que sai.
func TestPayloadPublicadoEhOEnvelopeDoContrato(t *testing.T) {
	store := dbtest.Store(t)
	dbtest.DrainOutbox(t, dbtest.Pool(t))

	c := novoCenario(t, "1000.00")
	ctx := context.Background()
	res, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "envelope"))
	if err != nil {
		t.Fatalf("aposta: %v", err)
	}

	recs, _ := store.Read().Outbox.ListByAggregate(ctx, "WagerTransaction", res.TransactionID)
	var env map[string]json.RawMessage
	if err := json.Unmarshal(recs[0].Payload, &env); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}

	for _, obrigatorio := range []string{
		"eventId", "eventType", "aggregateType", "aggregateId",
		"correlationId", "occurredAt", "version", "data",
	} {
		if _, tem := env[obrigatorio]; !tem {
			t.Errorf("envelope sem %q: %s", obrigatorio, recs[0].Payload)
		}
	}
	// A versão é comparada como texto cru: o número do contrato é inteiro e não
	// precisa passar por conversão nenhuma.
	if string(env["version"]) != "1" {
		t.Errorf("version = %s", env["version"])
	}
}
