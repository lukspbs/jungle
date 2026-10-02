package sqs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	adaptersqs "github.com/lukspbs/jungle/internal/adapter/sqs"
	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/platform/metrics"
	"github.com/lukspbs/jungle/test/dbtest"
	"github.com/lukspbs/jungle/test/sqstest"
)

type relogio struct{}

func (relogio) Now() time.Time { return time.Now().UTC() }

type fila struct {
	consumidor *adaptersqs.Consumer
	client     *awssqs.Client
	store      *postgres.Store
	queueURL   string
	carteira   uuid.UUID
	jogador    uuid.UUID
	prefixo    string
}

func novaFila(t *testing.T, saldo string) fila {
	t.Helper()
	store := dbtest.Store(t)
	client, cfg := sqstest.Client(t)
	if cfg.InboundQueueURL == "" {
		t.Skip("TEST_SQS_INBOUND_QUEUE_URL não definida")
	}
	sqstest.Drain(t, client, cfg.InboundQueueURL)
	// A fila é compartilhada pela suíte, e mensagens que o consumidor
	// deliberadamente não apaga — as inválidas — ficariam afogando os
	// recebimentos dos testes seguintes.
	t.Cleanup(func() { sqstest.Drain(t, client, cfg.InboundQueueURL) })

	ids := app.UUIDv7{}
	clock := relogio{}
	politica := app.ReferencePolicy{
		TTL: time.Minute, MaxAttempts: 3,
		InitialBackoff: 10 * time.Millisecond, MaxBackoff: time.Second,
	}
	processar := app.NewProcessWager(store, clock, ids, metrics.New(), politica)

	valor, err := money.Parse(saldo, "BRL")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	aberta, err := app.NewOpenWallet(store, clock, ids).Execute(context.Background(),
		app.OpenWalletCommand{PlayerID: uuid.New(), InitialBalance: valor, CorrelationID: "corr-fila"})
	if err != nil {
		t.Fatalf("abertura: %v", err)
	}

	// Long polling curto: o padrão de 20s faria cada recebimento vazio parar a
	// suíte por esse tempo.
	cfg.WaitTime = time.Second
	return fila{
		consumidor: adaptersqs.NewConsumer(client, store, processar, clock,
			slog.New(slog.NewJSONHandler(io.Discard, nil)), metrics.New(), cfg),
		client: client, store: store, queueURL: cfg.InboundQueueURL,
		carteira: aberta.Wallet.ID(), jogador: aberta.Wallet.PlayerID(),
		prefixo: uuid.NewString(),
	}
}

func (f fila) envia(t *testing.T, messageID string, corpo any) {
	t.Helper()
	raw, err := json.Marshal(corpo)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err = f.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:    aws.String(f.queueURL),
		MessageBody: aws.String(string(raw)),
		// O grupo é a carteira: o FIFO garante ordem dentro do grupo, e assim
		// as operações de um jogador chegam ordenadas sem serializar jogadores
		// distintos.
		MessageGroupId: aws.String(f.carteira.String()),
		// A deduplicação do broker usa a mesma identidade da inbox.
		MessageDeduplicationId: aws.String(messageID),
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

func (f fila) mensagem(messageID, externalID, kind, valor string) map[string]any {
	// O messageId também leva o prefixo do cenário. A inbox recusa o mesmo
	// identificador com conteúdo diferente, e um literal fixo colidiria com a
	// execução anterior — que usou outra carteira.
	return map[string]any{
		"messageId":  f.prefixo + "-" + messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339),
		"data": map[string]any{
			"providerId":            "provider-a",
			"externalTransactionId": f.prefixo + "-" + externalID,
			"idempotencyKey":        "provider-a:" + f.prefixo + "-" + externalID,
			"playerId":              f.jogador.String(),
			"walletId":              f.carteira.String(),
			"roundId":               "round-987",
			"gameId":                "fortune-chimp",
			"kind":                  kind,
			"money":                 map[string]string{"amount": valor, "currency": "BRL"},
		},
	}
}

// consomeAte roda ciclos até tratar o esperado ou esgotar o prazo.
func (f fila) consomeAte(t *testing.T, esperados int, prazo time.Duration) adaptersqs.ConsumeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), prazo+20*time.Second)
	defer cancel()

	var (
		total  adaptersqs.ConsumeResult
		vazias int
	)
	limite := time.Now().Add(prazo)
	for time.Now().Before(limite) && total.Handled+total.Duplicate < esperados {
		// A desistência antecipada só vale depois de a primeira mensagem ter
		// chegado. Antes disso, recebimento vazio significa que a fila ainda
		// não entregou — e numa FIFO isso leva mais tempo sob carga, como
		// acontece quando a suíte roda com o detector de corrida.
		if total.Received > 0 && vazias >= 3 {
			break
		}
		res, err := f.consumidor.ReceiveOnce(ctx)
		if err != nil {
			t.Fatalf("ReceiveOnce: %v", err)
		}
		if res.Received == 0 {
			vazias++
		} else {
			vazias = 0
		}
		total.Received += res.Received
		total.Handled += res.Handled
		total.Duplicate += res.Duplicate
		total.Failed += res.Failed
		total.Errors = append(total.Errors, res.Errors...)
	}
	return total
}

func (f fila) saldo(t *testing.T) string {
	t.Helper()
	w, err := f.store.Read().Wallets.FindByID(context.Background(), f.carteira)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	return w.Balance().String()
}

func TestApostaPelaFilaMovimentaACarteira(t *testing.T) {
	f := novaFila(t, "1000.00")
	f.envia(t, "msg-"+uuid.NewString(), f.mensagem("msg-1", "sqs-bet", "BET", "25.00"))

	res := f.consomeAte(t, 1, 10*time.Second)
	if res.Handled != 1 {
		t.Fatalf("tratadas = %d, esperado 1. erros: %v", res.Handled, res.Errors)
	}
	if got := f.saldo(t); got != "975.00" {
		t.Errorf("saldo = %q, esperado \"975.00\"", got)
	}

	tx, err := f.store.Read().Transactions.FindByProviderAndExternalID(
		context.Background(), "provider-a", f.prefixo+"-sqs-bet")
	if err != nil {
		t.Fatalf("FindByProviderAndExternalID: %v", err)
	}
	if tx.Status() != wagering.Processed {
		t.Errorf("estado = %v", tx.Status())
	}
}

// TestMensagemReentregueNaoDuplicaMovimentacao é o teste central da inbox.
func TestMensagemReentregueNaoDuplicaMovimentacao(t *testing.T) {
	f := novaFila(t, "1000.00")
	messageID := "msg-" + uuid.NewString()
	corpo := f.mensagem(messageID, "sqs-dup", "BET", "40.00")

	// A mesma mensagem enviada três vezes. O MessageDeduplicationId do FIFO
	// pode barrar algumas, mas o teste não depende disso: a garantia é da
	// inbox e das constraints.
	for i := 0; i < 3; i++ {
		f.envia(t, messageID, corpo)
	}

	f.consomeAte(t, 3, 8*time.Second)

	if got := f.saldo(t); got != "960.00" {
		t.Errorf("saldo = %q, esperado \"960.00\": houve movimentação duplicada", got)
	}

	registro, err := f.store.Read().Inbox.Find(context.Background(),
		adaptersqs.ConsumerName, f.prefixo+"-"+messageID)
	if err != nil {
		t.Fatalf("Inbox.Find: %v", err)
	}
	if !registro.Completed() {
		t.Error("a mensagem não foi marcada como concluída")
	}
}

// TestMesmoMessageIdComOutroConteudoEhPermanente cobre o reuso indevido do
// identificador de mensagem.
func TestMesmoMessageIdComOutroConteudoEhPermanente(t *testing.T) {
	f := novaFila(t, "1000.00")
	messageID := "msg-" + uuid.NewString()

	f.envia(t, messageID, f.mensagem(messageID, "sqs-hash-a", "BET", "10.00"))
	f.consomeAte(t, 1, 8*time.Second)
	if got := f.saldo(t); got != "990.00" {
		t.Fatalf("saldo = %q", got)
	}

	// Mesmo messageId, operação diferente. O FIFO deduplica por 5 minutos, então
	// o envio usa outro id de deduplicação para garantir a entrega.
	raw, _ := json.Marshal(f.mensagem(messageID, "sqs-hash-b", "BET", "20.00"))
	ctx := context.Background()
	if _, err := f.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(f.queueURL),
		MessageBody:            aws.String(string(raw)),
		MessageGroupId:         aws.String(f.carteira.String()),
		MessageDeduplicationId: aws.String(uuid.NewString()),
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	res := f.consomeAte(t, 1, 5*time.Second)
	if res.Failed == 0 {
		t.Error("a mensagem com conteúdo divergente foi aceita")
	}
	if got := f.saldo(t); got != "990.00" {
		t.Errorf("saldo = %q: a mensagem divergente movimentou a carteira", got)
	}
}

func TestMensagemInvalidaNaoEhApagada(t *testing.T) {
	f := novaFila(t, "100.00")
	ctx := context.Background()

	casos := []struct {
		nome  string
		corpo string
	}{
		{"json malformado", `{"messageId":`},
		{"sem messageId", `{"type":"WagerTransactionRequested","data":{}}`},
		{"tipo desconhecido", `{"messageId":"m1","type":"OutroTipo","data":{}}`},
		{"OPENING pela fila", `{"messageId":"m2","type":"WagerTransactionRequested","data":{"providerId":"p","externalTransactionId":"e","idempotencyKey":"k","playerId":"` + f.jogador.String() + `","walletId":"` + f.carteira.String() + `","roundId":"r","gameId":"g","kind":"OPENING","money":{"amount":"10.00","currency":"BRL"}}}`},
	}

	for _, caso := range casos {
		if _, err := f.client.SendMessage(ctx, &awssqs.SendMessageInput{
			QueueUrl:               aws.String(f.queueURL),
			MessageBody:            aws.String(caso.corpo),
			MessageGroupId:         aws.String(f.carteira.String()),
			MessageDeduplicationId: aws.String(uuid.NewString()),
		}); err != nil {
			t.Fatalf("SendMessage(%s): %v", caso.nome, err)
		}
	}

	res, err := f.consumidor.ReceiveOnce(ctx)
	if err != nil {
		t.Fatalf("ReceiveOnce: %v", err)
	}
	if res.Failed != res.Received {
		t.Errorf("recebidas=%d falhas=%d: alguma inválida foi aceita", res.Received, res.Failed)
	}
	if res.Handled != 0 {
		t.Errorf("tratadas = %d, esperado nenhuma", res.Handled)
	}
	if got := f.saldo(t); got != "100.00" {
		t.Errorf("saldo = %q: mensagem inválida movimentou a carteira", got)
	}
	// Não apagadas: continuam na fila. Quem as tira de lá é o redrive, e é o
	// que TestMensagemEsgotadaChegaNaDLQ verifica.
}

// TestMensagemEsgotadaChegaNaDLQ fecha o §10: tentativas esgotadas devem chegar
// à DLQ.
//
// Quem move a mensagem é o broker, pela política de redrive da fila, e não a
// aplicação — então o teste confere as duas metades: que a política provisionada
// existe, e que ela de fato move a mensagem.
//
// Os recebimentos vêm do próprio teste, com VisibilityTimeout 0 para devolver a
// mensagem na hora. Deixar o consumidor falhar as cinco vezes custaria dois
// minutos e meio de suíte — o visibility timeout é de 30s — para afirmar
// exatamente a mesma coisa. O grupo FIFO é próprio para não interferir na ordem
// dos outros cenários, e novaFila garante que a fila esteja vazia na entrada.
func TestMensagemEsgotadaChegaNaDLQ(t *testing.T) {
	f := novaFila(t, "100.00")
	dlq := sqstest.FilaDLQ(t)
	ctx := context.Background()

	sqstest.Drain(t, f.client, dlq)
	t.Cleanup(func() { sqstest.Drain(t, f.client, dlq) })

	// Primeira metade: a política está provisionada.
	attrs, err := f.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(f.queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameRedrivePolicy},
	})
	if err != nil {
		t.Fatalf("GetQueueAttributes: %v", err)
	}
	politica := attrs.Attributes[string(types.QueueAttributeNameRedrivePolicy)]
	if politica == "" {
		t.Fatal("a fila de entrada não tem RedrivePolicy: mensagem esgotada ficaria presa para sempre")
	}
	if !strings.Contains(politica, "deadLetterTargetArn") || !strings.Contains(politica, "maxReceiveCount") {
		t.Errorf("RedrivePolicy incompleta: %s", politica)
	}

	// Segunda metade: a política move a mensagem.
	marca := f.prefixo + "-esgotada"
	if _, err := f.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(f.queueURL),
		MessageBody:            aws.String(`{"messageId":"` + marca + `","type":"TipoDesconhecido","data":{}}`),
		MessageGroupId:         aws.String("dlq-" + f.prefixo),
		MessageDeduplicationId: aws.String(uuid.NewString()),
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Enquanto não migrar, a mensagem segue sendo entregue e devolvida. O laço
	// conta entregas de verdade em vez de só chamar ReceiveMessage n vezes:
	// num FIFO o grupo fica indisponível por um instante depois de cada
	// entrega, e um laço cego voltaria vazio sem incrementar contador nenhum.
	// A DLQ é consultada a cada rodada porque o limite exato em que o broker
	// transfere não é contrato nosso — o que importa é que ele transfira.
	prazo := time.Now().Add(45 * time.Second)
	entregas := 0
	chegou := false
	for !chegou && time.Now().Before(prazo) {
		origem, err := f.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(f.queueURL),
			MaxNumberOfMessages: 1,
			VisibilityTimeout:   0,
			WaitTimeSeconds:     1,
		})
		if err != nil {
			t.Fatalf("ReceiveMessage na entrada: %v", err)
		}
		for _, m := range origem.Messages {
			if !strings.Contains(aws.ToString(m.Body), marca) {
				continue
			}
			entregas++
			// Devolve a mensagem na hora. O VisibilityTimeout do próprio
			// ReceiveMessage não basta: o emulador aplica o da fila de
			// qualquer forma, e esperar os 5s dela a cada entrega faria este
			// teste sozinho custar meio minuto.
			if _, err := f.client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
				QueueUrl:          aws.String(f.queueURL),
				ReceiptHandle:     m.ReceiptHandle,
				VisibilityTimeout: 0,
			}); err != nil {
				t.Fatalf("ChangeMessageVisibility: %v", err)
			}
		}

		morta, err := f.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(dlq),
			MaxNumberOfMessages: 10,
			VisibilityTimeout:   0,
			WaitTimeSeconds:     0,
		})
		if err != nil {
			t.Fatalf("ReceiveMessage na DLQ: %v", err)
		}
		for _, m := range morta.Messages {
			if strings.Contains(aws.ToString(m.Body), marca) {
				chegou = true
			}
		}
	}
	if !chegou {
		t.Errorf("a mensagem não chegou à DLQ depois de %d entregas (maxReceiveCount provisionado: 5)", entregas)
	}

	if got := f.saldo(t); got != "100.00" {
		t.Errorf("saldo = %q: a mensagem esgotada movimentou a carteira", got)
	}
}

// TestRecusaDeNegocioEhTerminalEPermiteRemocao cobre a regra do §10: uma recusa
// confirmada não deve voltar para a fila.
func TestRecusaDeNegocioEhTerminalEPermiteRemocao(t *testing.T) {
	f := novaFila(t, "10.00")
	f.envia(t, "msg-"+uuid.NewString(), f.mensagem("msg-r", "sqs-recusa", "BET", "500.00"))

	res := f.consomeAte(t, 1, 10*time.Second)
	if res.Handled != 1 {
		t.Fatalf("tratadas = %d, esperado 1. erros: %v", res.Handled, res.Errors)
	}

	tx, err := f.store.Read().Transactions.FindByProviderAndExternalID(
		context.Background(), "provider-a", f.prefixo+"-sqs-recusa")
	if err != nil {
		t.Fatalf("FindByProviderAndExternalID: %v", err)
	}
	if tx.Status() != wagering.Rejected || tx.FailureCode() != wagering.FailureInsufficientFunds {
		t.Errorf("transação = %v/%v", tx.Status(), tx.FailureCode())
	}
	if got := f.saldo(t); got != "10.00" {
		t.Errorf("saldo = %q", got)
	}
}

// TestMesmaOperacaoPorHttpEPorFila cruza as duas entradas, conforme o §13.
func TestMesmaOperacaoPorHttpEPorFila(t *testing.T) {
	f := novaFila(t, "1000.00")
	ctx := context.Background()

	ids := app.UUIDv7{}
	politica := app.ReferencePolicy{
		TTL: time.Minute, MaxAttempts: 3,
		InitialBackoff: 10 * time.Millisecond, MaxBackoff: time.Second,
	}
	porHTTP := app.NewProcessWager(f.store, relogio{}, ids, metrics.New(), politica)

	externalID := f.prefixo + "-cruzada"
	valor, _ := money.Parse("30.00", "BRL")

	// Primeiro pela entrada síncrona.
	res, err := porHTTP.Execute(ctx, app.ProcessWagerCommand{
		ProviderID: "provider-a", ExternalTransactionID: externalID,
		IdempotencyKey: "provider-a:" + externalID,
		PlayerID:       f.jogador, WalletID: f.carteira,
		RoundID: "round-987", GameID: "fortune-chimp",
		Kind: wagering.Bet, Money: valor, CorrelationID: "corr-http",
	})
	if err != nil {
		t.Fatalf("via HTTP: %v", err)
	}
	if got := f.saldo(t); got != "970.00" {
		t.Fatalf("saldo após HTTP = %q", got)
	}

	// Depois a mesma operação pela fila.
	f.envia(t, "msg-"+uuid.NewString(), f.mensagem("msg-cruz", "cruzada", "BET", "30.00"))
	f.consomeAte(t, 1, 10*time.Second)

	if got := f.saldo(t); got != "970.00" {
		t.Errorf("saldo = %q: a mesma operação foi aplicada duas vezes", got)
	}
	tx, err := f.store.Read().Transactions.FindByProviderAndExternalID(ctx, "provider-a", externalID)
	if err != nil {
		t.Fatalf("FindByProviderAndExternalID: %v", err)
	}
	if tx.ID() != res.TransactionID {
		t.Error("a fila criou outra transação para a mesma operação")
	}
}

func TestVariasOperacoesEmSequencia(t *testing.T) {
	f := novaFila(t, "1000.00")

	for i := 0; i < 5; i++ {
		f.envia(t, "msg-"+uuid.NewString(),
			f.mensagem(fmt.Sprintf("msg-seq-%d", i), fmt.Sprintf("seq-%d", i), "BET", "10.00"))
	}

	res := f.consomeAte(t, 5, 15*time.Second)
	if res.Handled != 5 {
		t.Fatalf("tratadas = %d, esperado 5. erros: %v", res.Handled, res.Errors)
	}
	if got := f.saldo(t); got != "950.00" {
		t.Errorf("saldo = %q, esperado \"950.00\"", got)
	}
}
