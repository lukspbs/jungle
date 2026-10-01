package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/platform/config"
)

// Publisher envia os eventos de integração para a fila de saída.
type Publisher struct {
	client   *sqs.Client
	queueURL string
}

// NewPublisher monta o publicador.
func NewPublisher(client *sqs.Client, cfg config.SQS) *Publisher {
	return &Publisher{client: client, queueURL: cfg.OutboundQueueURL}
}

// Publish envia um registro da outbox.
//
// O corpo é o payload tal como gravado no commit — nada é remontado aqui. Uma
// republicação depois de uma falha entrega exatamente os mesmos bytes, com o
// mesmo eventId, o que permite ao consumidor deduplicar.
//
// Os atributos repetem eventType e eventId fora do corpo para que um consumidor
// consiga filtrar e deduplicar sem desserializar o payload inteiro.
func (p *Publisher) Publish(ctx context.Context, rec postgres.OutboxRecord) error {
	if p.queueURL == "" {
		return fmt.Errorf("sqs: fila de saída não configurada")
	}

	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(p.queueURL),
		MessageBody: aws.String(string(rec.Payload)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType": {DataType: aws.String("String"), StringValue: aws.String(rec.EventType)},
			"eventId":   {DataType: aws.String("String"), StringValue: aws.String(rec.EventID.String())},
			"correlationId": {
				DataType:    aws.String("String"),
				StringValue: aws.String(rec.CorrelationID),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("sqs: falha ao publicar evento %s: %w", rec.EventID, err)
	}
	return nil
}
