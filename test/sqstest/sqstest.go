// Package sqstest fornece um SQS real para os testes de integração.
//
// Como o dbtest, é o ponto único de obtenção da dependência: trocar o emulador
// não exige tocar em nenhum caso de teste.
package sqstest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	adaptersqs "github.com/lukspbs/jungle/internal/adapter/sqs"
	"github.com/lukspbs/jungle/internal/platform/config"
)

// Variáveis que apontam para o emulador.
const (
	EnvEndpoint = "TEST_SQS_ENDPOINT"
	EnvOutbound = "TEST_SQS_OUTBOUND_QUEUE_URL"
	EnvInbound  = "TEST_SQS_INBOUND_QUEUE_URL"
)

// Config devolve a configuração do emulador, pulando o teste sem ela.
func Config(t *testing.T) config.SQS {
	t.Helper()
	endpoint := os.Getenv(EnvEndpoint)
	if endpoint == "" {
		t.Skipf("%s não definida: teste de mensageria pulado", EnvEndpoint)
	}
	return config.SQS{
		Endpoint:          endpoint,
		Region:            "us-east-1",
		InboundQueueURL:   os.Getenv(EnvInbound),
		OutboundQueueURL:  os.Getenv(EnvOutbound),
		MaxMessages:       10,
		WaitTime:          time.Second,
		VisibilityTimeout: 30 * time.Second,
	}
}

// Client devolve um cliente conectado ao emulador.
func Client(t *testing.T) (*awssqs.Client, config.SQS) {
	t.Helper()
	cfg := Config(t)

	// Credenciais de emulador. O SDK exige alguma coisa na cadeia; o LocalStack
	// aceita qualquer valor e não as valida.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := adaptersqs.NewClient(ctx, cfg)
	if err != nil {
		t.Fatalf("falha ao montar o cliente SQS: %v", err)
	}
	return client, cfg
}

// Drain esvazia uma fila.
//
// Os testes de mensageria afirmam sobre o que chegou, e mensagens de execuções
// anteriores atrapalhariam essa contagem.
func Drain(t *testing.T, client *awssqs.Client, queueURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := client.PurgeQueue(ctx, &awssqs.PurgeQueueInput{
		QueueUrl: aws.String(queueURL),
	}); err != nil {
		// PurgeQueue tem limite de uma chamada por minuto. Quando recusado,
		// o esvaziamento manual resolve.
		drainByReceive(t, client, queueURL)
	}
}

func drainByReceive(t *testing.T, client *awssqs.Client, queueURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i := 0; i < 50; i++ {
		out, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     0,
		})
		if err != nil || len(out.Messages) == 0 {
			return
		}
		for _, m := range out.Messages {
			_, _ = client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
				QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle,
			})
		}
	}
}

// Receive busca mensagens da fila, esperando até o prazo.
func Receive(
	t *testing.T, client *awssqs.Client, queueURL string, esperadas int, prazo time.Duration,
) []types.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), prazo+15*time.Second)
	defer cancel()

	limite := time.Now().Add(prazo)
	var coletadas []types.Message

	for time.Now().Before(limite) && len(coletadas) < esperadas {
		out, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(queueURL),
			MaxNumberOfMessages:   10,
			WaitTimeSeconds:       1,
			MessageAttributeNames: []string{"All"},
		})
		if err != nil {
			t.Fatalf("ReceiveMessage: %v", err)
		}
		coletadas = append(coletadas, out.Messages...)
		for _, m := range out.Messages {
			_, _ = client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
				QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle,
			})
		}
	}
	return coletadas
}
