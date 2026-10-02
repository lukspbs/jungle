package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/platform/logging"
	"github.com/lukspbs/jungle/internal/platform/metrics"
)

// EventPublisher entrega um evento ao destino externo.
//
// É uma porta de verdade, e não uma abstração decorativa: o worker não deve
// conhecer SQS, e o destino é genuinamente substituível — um tópico, outra
// fila, um barramento. O que ele precisa saber é só se a entrega aconteceu.
type EventPublisher interface {
	Publish(ctx context.Context, record postgres.OutboxRecord) error
}

// OutboxPublisher publica os eventos pendentes.
//
// O worker é separado da transação que gravou o evento, e é isso que garante a
// ordem exigida: o evento só existe depois do commit, e só então alguém o
// publica. Não há caminho em que a publicação preceda a confirmação.
type OutboxPublisher struct {
	store      *postgres.Store
	publisher  EventPublisher
	clock      Clock
	logger     *slog.Logger
	metrics    *metrics.Metrics
	instanceID string

	batchSize      int
	lease          time.Duration
	interval       time.Duration
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// NewOutboxPublisher monta o worker.
func NewOutboxPublisher(
	store *postgres.Store, publisher EventPublisher, clock Clock, logger *slog.Logger,
	m *metrics.Metrics, instanceID string,
	batchSize int, lease, interval, initialBackoff, maxBackoff time.Duration,
) *OutboxPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.New()
	}
	return &OutboxPublisher{
		store: store, publisher: publisher, clock: clock,
		logger:     logger.With(slog.String("component", "outbox-publisher")),
		metrics:    m,
		instanceID: instanceID,
		batchSize:  batchSize, lease: lease, interval: interval,
		initialBackoff: initialBackoff, maxBackoff: maxBackoff,
	}
}

// PublishResult resume uma varredura.
type PublishResult struct {
	Claimed   int
	Published int
	Failed    int
}

// Run publica periodicamente até o contexto ser cancelado.
func (p *OutboxPublisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := p.Sweep(ctx); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				p.logger.ErrorContext(ctx, "varredura da outbox falhou",
					slog.String("error", err.Error()))
				continue
			}
		}
	}
}

// Sweep reivindica e publica um lote.
func (p *OutboxPublisher) Sweep(ctx context.Context) (PublishResult, error) {
	agora := p.clock.Now()

	// O atraso é medido antes de publicar: é o estado que o operador precisa
	// ver, e medir depois esconderia justamente o acúmulo.
	if stats, err := p.store.Read().Outbox.Stats(ctx, agora); err == nil {
		p.metrics.ObserveOutboxBacklog(stats.Pending, stats.OldestPendingAge.Seconds())
	}

	registros, err := p.store.Read().Outbox.Claim(
		ctx, p.instanceID, p.batchSize, p.lease, agora)
	if err != nil {
		return PublishResult{}, err
	}

	resultado := PublishResult{Claimed: len(registros)}
	for _, rec := range registros {
		if err := p.publisher.Publish(ctx, rec); err != nil {
			// A publicação falhou: devolve à fila com recuo. O evento continua
			// lá, e nenhuma tentativa é descartada.
			proxima := agora.Add(p.backoffFor(rec.Attempts))
			_ = p.store.Read().Outbox.Reschedule(ctx, rec.EventID, proxima)
			resultado.Failed++
			p.metrics.ObserveOutboxPublish(false)
			p.logger.WarnContext(ctx, "publicação falhou, evento reagendado",
				slog.String(logging.FieldEventID, rec.EventID.String()),
				slog.String("eventType", rec.EventType),
				slog.String(logging.FieldCorrelationID, rec.CorrelationID),
				slog.Int("attempts", rec.Attempts),
				slog.String("error", err.Error()))
			continue
		}

		// A janela entre publicar e confirmar é real: se o processo cair aqui,
		// o evento será republicado quando o lease vencer. É aceitável porque o
		// eventId é preservado e o consumidor deduplica — e é preferível ao
		// inverso, que perderia o evento.
		if err := p.store.Read().Outbox.MarkPublished(ctx, rec.EventID, p.clock.Now()); err != nil {
			resultado.Failed++
			p.logger.ErrorContext(ctx, "evento publicado mas não confirmado",
				slog.String(logging.FieldEventID, rec.EventID.String()),
				slog.String("error", err.Error()))
			continue
		}
		resultado.Published++
		p.metrics.ObserveOutboxPublish(true)
		p.logger.DebugContext(ctx, "evento publicado",
			slog.String(logging.FieldEventID, rec.EventID.String()),
			slog.String("eventType", rec.EventType),
			slog.String(logging.FieldCorrelationID, rec.CorrelationID))
	}
	return resultado, nil
}

// backoffFor dobra o recuo a cada tentativa até o teto.
//
// A duplicação é por deslocamento de bits, em aritmética inteira. math.Pow
// resolveria, mas traria ponto flutuante para dentro do módulo sem necessidade,
// e a trava automática do projeto proíbe float em qualquer lugar — não só onde
// há dinheiro. Um teto de deslocamento evita estouro em contagens absurdas.
func (p *OutboxPublisher) backoffFor(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	const maxShift = 32
	deslocamento := attempts - 1
	if deslocamento > maxShift {
		return p.maxBackoff
	}
	recuo := p.initialBackoff << deslocamento
	if recuo > p.maxBackoff || recuo <= 0 {
		return p.maxBackoff
	}
	return recuo
}
