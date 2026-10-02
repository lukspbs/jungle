package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/events"
)

// OutboxRepository persiste os eventos de integração pendentes de publicação.
//
// O registro é gravado na mesma transação da mudança financeira. Nenhum evento
// chega ao mundo antes do commit que o originou: o worker publica a partir
// daqui, depois.
type OutboxRepository struct {
	db DBTX
}

const outboxColumns = `event_id, aggregate_type, aggregate_id, event_type, event_version,
	payload, correlation_id, causation_id, occurred_at,
	attempts, next_attempt_at, published_at, locked_by, locked_until`

// OutboxRecord é um registro reivindicado para publicação.
type OutboxRecord struct {
	EventID     uuid.UUID
	EventType   string
	AggregateID uuid.UUID

	// Payload é o envelope completo tal como foi gravado no commit. O worker
	// publica exatamente estes bytes: nada é recalculado na publicação, então
	// uma republicação é byte a byte idêntica à primeira tentativa.
	Payload []byte

	CorrelationID string
	Attempts      int
	OccurredAt    time.Time
}

// Insert grava o evento como pendente.
//
// next_attempt_at nasce igual ao instante do commit, de modo que o worker pode
// pegá-lo na primeira varredura seguinte.
func (r *OutboxRepository) Insert(ctx context.Context, e events.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("postgres: falha ao serializar evento: %w", err)
	}

	const query = `
		INSERT INTO outbox_events (` + outboxColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 0, $10, NULL, NULL, NULL)`

	_, err = r.db.Exec(ctx, query,
		e.ID(), e.Aggregate().String(), e.AggregateID(), e.Type().String(), e.Version(),
		payload, e.CorrelationID(), nullableText(e.CausationID()), e.OccurredAt(),
		e.OccurredAt())
	if err != nil {
		return fmt.Errorf("postgres: falha ao inserir evento na outbox: %w", classify(err))
	}
	return nil
}

// Claim reivindica registros pendentes para uma instância.
//
// SKIP LOCKED faz publishers concorrentes pegarem registros distintos em vez de
// enfileirarem no mesmo. O lease em locked_until cobre o outro caso: se a
// instância que reivindicou morrer antes de publicar, o registro volta a ficar
// disponível quando o prazo vence, e outra instância assume.
//
// attempts é incrementado na reivindicação, não no sucesso: uma tentativa que
// nunca retorna precisa contar, senão um registro problemático seria tentado
// para sempre.
func (r *OutboxRepository) Claim(
	ctx context.Context, instanceID string, limit int, lease time.Duration, now time.Time,
) ([]OutboxRecord, error) {
	const query = `
		UPDATE outbox_events
		   SET locked_by = $1,
		       locked_until = $2,
		       attempts = attempts + 1
		 WHERE event_id IN (
		       SELECT event_id
		         FROM outbox_events
		        WHERE published_at IS NULL
		          AND next_attempt_at <= $3
		          AND (locked_until IS NULL OR locked_until <= $3)
		        ORDER BY next_attempt_at, occurred_at
		        LIMIT $4
		        FOR UPDATE SKIP LOCKED
		 )
		 RETURNING event_id, event_type, aggregate_id, payload, correlation_id,
		           attempts, occurred_at`

	rows, err := r.db.Query(ctx, query, instanceID, now.Add(lease), now, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao reivindicar eventos: %w", classify(err))
	}
	defer rows.Close()

	var registros []OutboxRecord
	for rows.Next() {
		var rec OutboxRecord
		if err := rows.Scan(&rec.EventID, &rec.EventType, &rec.AggregateID,
			&rec.Payload, &rec.CorrelationID, &rec.Attempts, &rec.OccurredAt); err != nil {
			return nil, fmt.Errorf("postgres: falha ao ler evento reivindicado: %w", err)
		}
		registros = append(registros, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: falha ao percorrer eventos: %w", err)
	}
	return registros, nil
}

// MarkPublished confirma a publicação e libera o lease.
//
// Entre a publicação e esta confirmação existe uma janela: se o processo cair
// ali, o evento será republicado quando o lease vencer. É aceitável porque o
// eventId é preservado, então o consumidor a jusante deduplica.
//
// Nenhuma linha afetada não é erro: significa que outra instância já confirmou
// este evento depois de um lease vencido, e o estado desejado é justamente o
// que já está lá.
func (r *OutboxRepository) MarkPublished(ctx context.Context, eventID uuid.UUID, now time.Time) error {
	const query = `
		UPDATE outbox_events
		   SET published_at = $1, locked_by = NULL, locked_until = NULL
		 WHERE event_id = $2 AND published_at IS NULL`

	if _, err := r.db.Exec(ctx, query, now, eventID); err != nil {
		return fmt.Errorf("postgres: falha ao confirmar publicação: %w", classify(err))
	}
	return nil
}

// Reschedule devolve o registro à fila com novo prazo, liberando o lease para
// que qualquer instância possa assumi-lo.
func (r *OutboxRepository) Reschedule(
	ctx context.Context, eventID uuid.UUID, nextAttemptAt time.Time,
) error {
	const query = `
		UPDATE outbox_events
		   SET next_attempt_at = $1, locked_by = NULL, locked_until = NULL
		 WHERE event_id = $2 AND published_at IS NULL`

	if _, err := r.db.Exec(ctx, query, nextAttemptAt, eventID); err != nil {
		return fmt.Errorf("postgres: falha ao reagendar evento: %w", classify(err))
	}
	return nil
}

// PendingStats resume a fila para métricas e health checks.
type PendingStats struct {
	Pending int

	// OldestPendingAge é o atraso da outbox: há quanto tempo espera o evento
	// mais antigo ainda não publicado. É a métrica que denuncia um worker
	// parado.
	OldestPendingAge time.Duration
}

// Stats devolve o tamanho e o atraso da fila de publicação.
func (r *OutboxRepository) Stats(ctx context.Context, now time.Time) (PendingStats, error) {
	const query = `
		SELECT count(*), COALESCE(MIN(occurred_at), $1)
		  FROM outbox_events
		 WHERE published_at IS NULL`

	var (
		stats      PendingStats
		maisAntigo time.Time
	)
	if err := r.db.QueryRow(ctx, query, now).Scan(&stats.Pending, &maisAntigo); err != nil {
		return PendingStats{}, fmt.Errorf("postgres: falha ao medir a outbox: %w", classify(err))
	}
	if stats.Pending > 0 {
		stats.OldestPendingAge = now.Sub(maisAntigo)
	}
	return stats, nil
}

// FindByID lê um registro da outbox. Serve às consultas de diagnóstico e aos
// testes de recuperação.
func (r *OutboxRepository) FindByID(ctx context.Context, eventID uuid.UUID) (OutboxRecord, bool, error) {
	const query = `
		SELECT event_id, event_type, aggregate_id, payload, correlation_id, attempts, occurred_at
		  FROM outbox_events WHERE event_id = $1`

	var rec OutboxRecord
	err := r.db.QueryRow(ctx, query, eventID).Scan(&rec.EventID, &rec.EventType,
		&rec.AggregateID, &rec.Payload, &rec.CorrelationID, &rec.Attempts, &rec.OccurredAt)
	if noRows(err) {
		return OutboxRecord{}, false, nil
	}
	if err != nil {
		return OutboxRecord{}, false, fmt.Errorf("postgres: falha ao ler evento: %w", classify(err))
	}
	return rec, true, nil
}

// ListByAggregate devolve os eventos de um agregado em ordem de ocorrência.
// Serve ao diagnóstico — "o que foi publicado por causa desta operação" — e às
// verificações de atomicidade nos testes.
func (r *OutboxRepository) ListByAggregate(
	ctx context.Context, aggregate events.Aggregate, aggregateID uuid.UUID,
) ([]OutboxRecord, error) {
	const query = `
		SELECT event_id, event_type, aggregate_id, payload, correlation_id, attempts, occurred_at
		  FROM outbox_events
		 WHERE aggregate_type = $1 AND aggregate_id = $2
		 ORDER BY occurred_at, event_id`

	rows, err := r.db.Query(ctx, query, aggregate.String(), aggregateID)
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao listar eventos do agregado: %w", classify(err))
	}
	defer rows.Close()

	var registros []OutboxRecord
	for rows.Next() {
		var rec OutboxRecord
		if err := rows.Scan(&rec.EventID, &rec.EventType, &rec.AggregateID,
			&rec.Payload, &rec.CorrelationID, &rec.Attempts, &rec.OccurredAt); err != nil {
			return nil, fmt.Errorf("postgres: falha ao ler evento: %w", err)
		}
		registros = append(registros, rec)
	}
	return registros, rows.Err()
}
