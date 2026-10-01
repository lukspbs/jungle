// Package events define os eventos de integração publicados pelo serviço.
//
// Um evento é um envelope com payload tipado. O tipo e a versão não são
// informados por quem constrói: eles vêm do próprio payload, o que torna
// impossível publicar um envelope rotulado com um tipo que não corresponde ao
// conteúdo.
//
// O payload é um instantâneo imutável do momento do commit. Ele não é
// recalculado na publicação: o worker da outbox publica exatamente os bytes
// que foram confirmados junto com a mudança financeira.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Erros do pacote.
var (
	// ErrInvalidEvent indica envelope incompleto.
	ErrInvalidEvent = errors.New("events: evento inválido")

	// ErrInvalidPayload indica payload ausente ou incoerente.
	ErrInvalidPayload = errors.New("events: payload inválido")
)

// Type é o nome do evento no contrato externo.
type Type string

// Os quatro eventos exigidos pelo desafio.
const (
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
)

// String devolve o nome do tipo.
func (t Type) String() string { return string(t) }

// Aggregate é o tipo de agregado que originou o evento.
type Aggregate string

const (
	AggregateWagerTransaction Aggregate = "WagerTransaction"
	AggregateWallet           Aggregate = "Wallet"
)

// String devolve o nome do agregado.
func (a Aggregate) String() string { return string(a) }

// Payload é o conteúdo tipado de um evento.
//
// Cada implementação declara o próprio tipo, versão e agregado. É daí que o
// envelope os obtém, em vez de recebê-los por parâmetro.
type Payload interface {
	Type() Type
	Version() int
	Aggregate() Aggregate
	AggregateID() uuid.UUID
	validate() error
}

// Event é o envelope publicado.
type Event struct {
	id            uuid.UUID
	correlationID string
	causationID   string
	occurredAt    time.Time
	payload       Payload
}

// New monta um evento a partir do payload.
//
// O instante é normalizado para UTC: o contrato externo usa RFC 3339 em UTC, e
// normalizar aqui evita que o fuso de uma instância vaze para o payload.
func New(id uuid.UUID, payload Payload, correlationID, causationID string, now time.Time) (Event, error) {
	if id == uuid.Nil {
		return Event{}, fmt.Errorf("%w: evento sem id", ErrInvalidEvent)
	}
	if payload == nil {
		return Event{}, fmt.Errorf("%w: evento sem payload", ErrInvalidPayload)
	}
	if correlationID == "" {
		return Event{}, fmt.Errorf("%w: evento sem correlationId", ErrInvalidEvent)
	}
	if now.IsZero() {
		return Event{}, fmt.Errorf("%w: evento sem instante", ErrInvalidEvent)
	}
	if payload.AggregateID() == uuid.Nil {
		return Event{}, fmt.Errorf("%w: payload sem agregado", ErrInvalidPayload)
	}
	if err := payload.validate(); err != nil {
		return Event{}, err
	}

	return Event{
		id:            id,
		correlationID: correlationID,
		causationID:   causationID,
		occurredAt:    now.UTC(),
		payload:       payload,
	}, nil
}

// ID devolve o identificador estável do evento. Republicações preservam este
// valor, de modo que o consumidor a jusante consegue deduplicar.
func (e Event) ID() uuid.UUID { return e.id }

// Type devolve o tipo, derivado do payload.
func (e Event) Type() Type { return e.payload.Type() }

// Version devolve a versão do contrato, derivada do payload.
func (e Event) Version() int { return e.payload.Version() }

// Aggregate devolve o tipo de agregado de origem.
func (e Event) Aggregate() Aggregate { return e.payload.Aggregate() }

// AggregateID devolve o agregado de origem.
func (e Event) AggregateID() uuid.UUID { return e.payload.AggregateID() }

// CorrelationID devolve o identificador que amarra a operação ponta a ponta.
func (e Event) CorrelationID() string { return e.correlationID }

// CausationID devolve o identificador do que causou este evento, se houver.
func (e Event) CausationID() string { return e.causationID }

// OccurredAt devolve o instante em UTC.
func (e Event) OccurredAt() time.Time { return e.occurredAt }

// Payload devolve o conteúdo tipado.
func (e Event) Payload() Payload { return e.payload }

// envelope é a forma serializada. Os campos estão na ordem do contrato, o que
// torna o JSON determinístico.
type envelope struct {
	EventID       uuid.UUID `json:"eventId"`
	EventType     Type      `json:"eventType"`
	AggregateType Aggregate `json:"aggregateType"`
	AggregateID   uuid.UUID `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    string    `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          Payload   `json:"data"`
}

// MarshalJSON produz o instantâneo que vai para a outbox.
func (e Event) MarshalJSON() ([]byte, error) {
	if e.payload == nil {
		return nil, ErrInvalidPayload
	}
	return json.Marshal(envelope{
		EventID:       e.id,
		EventType:     e.payload.Type(),
		AggregateType: e.payload.Aggregate(),
		AggregateID:   e.payload.AggregateID(),
		CorrelationID: e.correlationID,
		CausationID:   e.causationID,
		// RFC 3339 com milissegundos e sufixo Z, como no contrato do desafio.
		OccurredAt: e.occurredAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		Version:    e.payload.Version(),
		Data:       e.payload,
	})
}
