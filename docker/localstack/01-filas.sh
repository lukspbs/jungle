#!/bin/bash
# Provisiona as filas assim que o LocalStack fica pronto.
#
# A DLQ é criada primeiro porque a política de redrive da fila principal
# referencia o ARN dela.
set -euo pipefail

DLQ=wager-transactions-dlq.fifo
PRINCIPAL=wager-transactions.fifo
SAIDA=wager-events

awslocal sqs create-queue --queue-name "$DLQ" \
  --attributes FifoQueue=true >/dev/null

DLQ_ARN=$(awslocal sqs get-queue-attributes \
  --queue-url "http://localhost:4566/000000000000/$DLQ" \
  --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)

# maxReceiveCount 5: uma mensagem que falha cinco recebimentos seguidos vai
# para a DLQ. O visibility timeout de 30s precisa cobrir o processamento, ou a
# mensagem seria reentregue enquanto ainda está sendo tratada.
awslocal sqs create-queue --queue-name "$PRINCIPAL" --attributes "$(cat <<JSON
{
  "FifoQueue": "true",
  "VisibilityTimeout": "30",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"$DLQ_ARN\",\"maxReceiveCount\":\"5\"}"
}
JSON
)" >/dev/null

# A fila de saída é padrão, não FIFO: os eventos carregam eventId próprio, e a
# deduplicação a jusante se apoia nele, não na do broker.
awslocal sqs create-queue --queue-name "$SAIDA" >/dev/null

echo "filas provisionadas: $PRINCIPAL, $DLQ (redrive após 5), $SAIDA"

# Filas dedicadas à suíte de testes.
#
# Sem elas, os testes de mensageria disputariam as mensagens com o consumidor
# da aplicação que está rodando no Compose — e perderiam, de forma
# intermitente. Separar as filas deixa a suíte independente de haver ou não uma
# instância no ar.
awslocal sqs create-queue --queue-name test-wager-transactions-dlq.fifo \
  --attributes FifoQueue=true >/dev/null

TEST_DLQ_ARN=$(awslocal sqs get-queue-attributes \
  --queue-url "http://localhost:4566/000000000000/test-wager-transactions-dlq.fifo" \
  --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)

awslocal sqs create-queue --queue-name test-wager-transactions.fifo --attributes "$(cat <<JSON
{
  "FifoQueue": "true",
  "VisibilityTimeout": "5",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"$TEST_DLQ_ARN\",\"maxReceiveCount\":\"5\"}"
}
JSON
)" >/dev/null

awslocal sqs create-queue --queue-name test-wager-events >/dev/null

echo "filas de teste provisionadas"
