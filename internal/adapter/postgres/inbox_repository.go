package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrInboxHashMismatch indica a mesma mensagem reentregue com outro conteúdo.
//
// Numa entrega at-least-once a reentrega é normal, mas o conteúdo precisa ser
// o mesmo. Divergência significa reuso indevido do identificador de mensagem, e
// aceitar seria processar uma operação diferente sob a identidade de outra.
var ErrInboxHashMismatch = errors.New("postgres: mensagem reentregue com conteúdo diferente")

// InboxRepository registra o consumo de mensagens.
type InboxRepository struct {
	db DBTX
}

// InboxRecord é o registro de uma mensagem recebida.
type InboxRecord struct {
	ConsumerName string
	MessageID    string
	PayloadHash  []byte
	ReceivedAt   time.Time
	CompletedAt  *time.Time
}

// Completed informa se o tratamento desta mensagem já foi confirmado.
func (r InboxRecord) Completed() bool { return r.CompletedAt != nil }

// Claim registra o recebimento, ou devolve o registro existente.
//
// Insere e trata a colisão, em vez de consultar antes: a reentrega concorrente
// da mesma mensagem é exatamente o caso que uma consulta prévia deixaria
// passar. Quem perde a corrida recebe de volta o que a vencedora gravou.
//
// A chamada precisa acontecer dentro da mesma transação das alterações de
// domínio. É isso que torna atômicos o efeito financeiro e o registro de que a
// mensagem foi consumida.
func (r *InboxRepository) Claim(
	ctx context.Context, consumerName, messageID string, payloadHash []byte, now time.Time,
) (InboxRecord, bool, error) {
	const query = `
		INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (consumer_name, message_id) DO NOTHING
		RETURNING consumer_name, message_id, payload_hash, received_at, completed_at`

	var rec InboxRecord
	err := r.db.QueryRow(ctx, query, consumerName, messageID, payloadHash, now).Scan(
		&rec.ConsumerName, &rec.MessageID, &rec.PayloadHash, &rec.ReceivedAt, &rec.CompletedAt)

	switch {
	case err == nil:
		// Inserida agora: primeira vez que esta mensagem é vista.
		return rec, true, nil
	case !noRows(err):
		return InboxRecord{}, false, fmt.Errorf("postgres: falha ao registrar a mensagem: %w", classify(err))
	}

	// DO NOTHING não devolveu linha: a mensagem já existia.
	existente, err := r.Find(ctx, consumerName, messageID)
	if err != nil {
		return InboxRecord{}, false, err
	}
	if !bytes.Equal(existente.PayloadHash, payloadHash) {
		return InboxRecord{}, false, fmt.Errorf("%w: %s/%s",
			ErrInboxHashMismatch, consumerName, messageID)
	}
	return existente, false, nil
}

// Complete marca o tratamento como concluído.
func (r *InboxRepository) Complete(
	ctx context.Context, consumerName, messageID string, now time.Time,
) error {
	const query = `
		UPDATE inbox_messages SET completed_at = $1
		 WHERE consumer_name = $2 AND message_id = $3 AND completed_at IS NULL`

	if _, err := r.db.Exec(ctx, query, now, consumerName, messageID); err != nil {
		return fmt.Errorf("postgres: falha ao concluir a mensagem: %w", classify(err))
	}
	return nil
}

// Find lê o registro de uma mensagem.
func (r *InboxRepository) Find(
	ctx context.Context, consumerName, messageID string,
) (InboxRecord, error) {
	const query = `
		SELECT consumer_name, message_id, payload_hash, received_at, completed_at
		  FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`

	var rec InboxRecord
	err := r.db.QueryRow(ctx, query, consumerName, messageID).Scan(
		&rec.ConsumerName, &rec.MessageID, &rec.PayloadHash, &rec.ReceivedAt, &rec.CompletedAt)
	if noRows(err) {
		return InboxRecord{}, fmt.Errorf("postgres: mensagem %s/%s não registrada", consumerName, messageID)
	}
	if err != nil {
		return InboxRecord{}, fmt.Errorf("postgres: falha ao ler a mensagem: %w", classify(err))
	}
	return rec, nil
}
