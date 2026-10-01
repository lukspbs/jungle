package sqs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/platform/config"
)

// ConsumerName identifica este consumidor na inbox.
//
// A unicidade da inbox é por (consumerName, messageId): um nome fixo permite
// que várias instâncias do mesmo serviço compartilhem a deduplicação, enquanto
// um consumidor diferente processaria a mesma mensagem de forma independente.
const ConsumerName = "wager-transactions-consumer"

// Envelope é o formato das mensagens da fila de entrada.
type Envelope struct {
	MessageID  string          `json:"messageId"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurredAt"`
	Data       json.RawMessage `json:"data"`
}

// WagerRequested é o conteúdo de uma operação solicitada.
type WagerRequested struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	IdempotencyKey                 string      `json:"idempotencyKey"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       string      `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}

// TypeWagerTransactionRequested é o único tipo aceito na fila de entrada.
const TypeWagerTransactionRequested = "WagerTransactionRequested"

// errPermanent marca um erro que nenhuma nova tentativa resolve.
var errPermanent = errors.New("sqs: erro permanente")

// Consumer processa as operações recebidas pela fila.
//
// # Deduplicação em duas camadas
//
// A chave de idempotência do negócio vem de data.idempotencyKey e é a mesma que
// a entrada HTTP usa — o caso de uso é compartilhado, e com ele as garantias.
// A inbox acrescenta a deduplicação por transporte: a mesma mensagem reentregue
// é reconhecida por (consumerName, messageId) antes de chegar ao domínio.
//
// # MessageGroupId e MessageDeduplicationId
//
// São responsabilidade de quem produz. O grupo recomendado é o walletId: o SQS
// FIFO garante ordem dentro de um grupo, e agrupar por carteira preserva a
// ordem das operações de um jogador sem serializar jogadores distintos. O
// MessageDeduplicationId recomendado é o messageId do envelope, o que dá à
// deduplicação do broker a mesma identidade que a inbox usa.
//
// A deduplicação do SQS FIFO cobre apenas cinco minutos e só vale entre envios
// idênticos. Ela não substitui a inbox nem as constraints: as garantias
// financeiras não dependem dela.
//
// # Remoção da fila
//
// A mensagem só é apagada depois do commit do seu tratamento. Uma queda antes
// disso deixa a mensagem para reentrega; a inbox reconhece a repetição e o
// efeito financeiro não se duplica.
//
// # Falhas
//
// Erro transitório não apaga a mensagem: ela volta a ficar visível quando o
// visibility timeout vencer, e o SQS a reentrega. Erro permanente — envelope
// malformado, tipo desconhecido, mensagem com o mesmo identificador e conteúdo
// diferente — também não apaga, e a política de redrive da fila a encaminha
// para a DLQ depois de maxReceiveCount recebimentos. Usar o mecanismo que a
// fila já tem evita duplicar a lógica de descarte na aplicação.
type Consumer struct {
	client    *awssqs.Client
	store     *postgres.Store
	processar *app.ProcessWager
	clock     app.Clock

	queueURL          string
	maxMessages       int32
	waitTime          int32
	visibilityTimeout int32
}

// NewConsumer monta o consumidor.
func NewConsumer(
	client *awssqs.Client, store *postgres.Store, processar *app.ProcessWager,
	clock app.Clock, cfg config.SQS,
) *Consumer {
	return &Consumer{
		client: client, store: store, processar: processar, clock: clock,
		queueURL:          cfg.InboundQueueURL,
		maxMessages:       int32(cfg.MaxMessages),
		waitTime:          int32(cfg.WaitTime.Seconds()),
		visibilityTimeout: int32(cfg.VisibilityTimeout.Seconds()),
	}
}

// ConsumeResult resume um ciclo de recebimento.
type ConsumeResult struct {
	Received  int
	Handled   int
	Duplicate int
	Failed    int

	// Errors carrega o que impediu cada mensagem de ser tratada. Sem isso uma
	// falha vira apenas um contador, e nem o operador nem o teste conseguem
	// saber o motivo.
	Errors []error
}

// Run consome até o contexto ser cancelado.
//
// O cancelamento interrompe a busca por trabalho novo, mas um ciclo em
// andamento termina: ele está numa transação, e abandoná-lo no meio só deixaria
// a mensagem para reentrega sem ter avançado nada.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := c.ReceiveOnce(ctx); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			// Falha ao buscar não derruba o consumidor: as mensagens continuam
			// na fila e o próximo ciclo tenta de novo.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
}

// ReceiveOnce busca e trata um lote.
func (c *Consumer) ReceiveOnce(ctx context.Context) (ConsumeResult, error) {
	out, err := c.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.queueURL),
		MaxNumberOfMessages: c.maxMessages,
		// Long polling: espera no servidor em vez de varrer fila vazia.
		WaitTimeSeconds:   c.waitTime,
		VisibilityTimeout: c.visibilityTimeout,
	})
	if err != nil {
		return ConsumeResult{}, fmt.Errorf("sqs: falha ao receber mensagens: %w", err)
	}

	resultado := ConsumeResult{Received: len(out.Messages)}
	for _, msg := range out.Messages {
		duplicada, err := c.handle(ctx, msg)
		switch {
		case err != nil:
			// Permanente ou transitório, a mensagem fica: a diferença é que a
			// permanente vai esgotar maxReceiveCount e chegar à DLQ.
			resultado.Failed++
			resultado.Errors = append(resultado.Errors, err)
		case duplicada:
			resultado.Duplicate++
			c.delete(ctx, msg)
		default:
			resultado.Handled++
			c.delete(ctx, msg)
		}
	}
	return resultado, nil
}

// handle trata uma mensagem, devolvendo se ela já havia sido processada.
func (c *Consumer) handle(ctx context.Context, msg types.Message) (bool, error) {
	corpo := []byte(aws.ToString(msg.Body))
	hash := sha256.Sum256(corpo)

	envelope, cmd, err := parse(corpo)
	if err != nil {
		return false, err
	}

	// Uma violação de unicidade invalida a transação inteira no PostgreSQL, e
	// só o rollback resolve. Nesse caso a segunda tentativa encontra, pelo
	// caminho de replay, o que a concorrente gravou.
	for tentativa := 0; tentativa < 2; tentativa++ {
		duplicada, err := c.handleOnce(ctx, envelope, cmd, hash[:])
		if err == nil {
			return duplicada, nil
		}
		if !isDuplicateWrite(err) {
			return false, err
		}
	}
	return false, fmt.Errorf("sqs: mensagem %s não convergiu após nova tentativa", envelope.MessageID)
}

// handleOnce executa o tratamento numa transação.
//
// O registro da inbox e as alterações de domínio compartilham o commit. É isso
// que torna impossível um estado em que a operação foi aplicada mas a mensagem
// aparece como não consumida, ou o inverso.
func (c *Consumer) handleOnce(
	ctx context.Context, envelope Envelope, cmd app.ProcessWagerCommand, hash []byte,
) (bool, error) {
	agora := c.clock.Now()
	var jaProcessada bool

	err := c.store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		registro, nova, err := r.Inbox.Claim(ctx, ConsumerName, envelope.MessageID, hash, agora)
		if errors.Is(err, postgres.ErrInboxHashMismatch) {
			// Mesmo identificador, outro conteúdo: aceitar seria processar uma
			// operação sob a identidade de outra.
			return fmt.Errorf("%w: %s", errPermanent, err)
		}
		if err != nil {
			return err
		}
		if !nova && registro.Completed() {
			jaProcessada = true
			return nil
		}

		if _, err := c.processar.ExecuteWithin(ctx, r, cmd); err != nil {
			// Comando inválido é permanente: reentregar não muda o conteúdo.
			if errors.Is(err, app.ErrInvalidCommand) || errors.Is(err, app.ErrIdempotencyConflict) {
				return fmt.Errorf("%w: %s", errPermanent, err)
			}
			return err
		}

		// Uma operação que ficou aguardando referência também é concluída aqui:
		// a pendência está persistida e o worker assume a continuidade, então
		// segurar a mensagem não acrescentaria nada.
		return r.Inbox.Complete(ctx, ConsumerName, envelope.MessageID, agora)
	})
	if err != nil {
		return false, err
	}
	return jaProcessada, nil
}

// delete remove a mensagem da fila, depois do commit.
func (c *Consumer) delete(ctx context.Context, msg types.Message) {
	// Um contexto próprio: o da execução pode já estar cancelado por SIGTERM, e
	// deixar de apagar uma mensagem já tratada causaria reentrega desnecessária.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	_, _ = c.client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	})
}

// parse converte o corpo da mensagem em comando.
func parse(corpo []byte) (Envelope, app.ProcessWagerCommand, error) {
	var envelope Envelope
	if err := json.Unmarshal(corpo, &envelope); err != nil {
		return Envelope{}, app.ProcessWagerCommand{},
			fmt.Errorf("%w: envelope malformado: %s", errPermanent, err)
	}
	if envelope.MessageID == "" {
		return Envelope{}, app.ProcessWagerCommand{},
			fmt.Errorf("%w: envelope sem messageId", errPermanent)
	}
	if envelope.Type != TypeWagerTransactionRequested {
		return Envelope{}, app.ProcessWagerCommand{},
			fmt.Errorf("%w: tipo %q não é aceito nesta fila", errPermanent, envelope.Type)
	}

	var pedido WagerRequested
	if err := json.Unmarshal(envelope.Data, &pedido); err != nil {
		return Envelope{}, app.ProcessWagerCommand{},
			fmt.Errorf("%w: data malformado: %s", errPermanent, err)
	}

	playerID, err := uuid.Parse(pedido.PlayerID)
	if err != nil {
		return Envelope{}, app.ProcessWagerCommand{},
			fmt.Errorf("%w: playerId inválido", errPermanent)
	}
	walletID, err := uuid.Parse(pedido.WalletID)
	if err != nil {
		return Envelope{}, app.ProcessWagerCommand{},
			fmt.Errorf("%w: walletId inválido", errPermanent)
	}
	kind, err := wagering.ParseExternalKind(pedido.Kind)
	if err != nil {
		return Envelope{}, app.ProcessWagerCommand{}, fmt.Errorf("%w: %s", errPermanent, err)
	}
	if pedido.IdempotencyKey == "" {
		return Envelope{}, app.ProcessWagerCommand{},
			fmt.Errorf("%w: data.idempotencyKey é obrigatório", errPermanent)
	}

	return envelope, app.ProcessWagerCommand{
		ProviderID:                     pedido.ProviderID,
		ExternalTransactionID:          pedido.ExternalTransactionID,
		IdempotencyKey:                 pedido.IdempotencyKey,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        pedido.RoundID,
		GameID:                         pedido.GameID,
		Kind:                           kind,
		Money:                          pedido.Money,
		ReferenceExternalTransactionID: pedido.ReferenceExternalTransactionID,
		// O correlationId amarra os eventos desta operação à mensagem que a
		// originou, o que permite rastrear do broker até o ledger.
		CorrelationID: "sqs:" + envelope.MessageID,
	}, nil
}

// isDuplicateWrite reconhece a violação de unicidade que exige repetir a
// transação.
func isDuplicateWrite(err error) bool {
	return errors.Is(err, postgres.ErrDuplicateExternalTransaction) ||
		errors.Is(err, postgres.ErrDuplicateIdempotencyKey)
}

// IsPermanent informa se o erro dispensa nova tentativa.
func IsPermanent(err error) bool { return errors.Is(err, errPermanent) }
