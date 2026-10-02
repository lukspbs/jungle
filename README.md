# Jungle — processamento distribuído de apostas

Serviço em Go que movimenta carteiras de jogadores a partir de operações de
provedores de jogo, recebidas por HTTP e por SQS. As garantias financeiras —
precisão monetária, idempotência, não negatividade do saldo, auditabilidade do
ledger — valem com várias instâncias em execução e com falhas entre as etapas.

As decisões de projeto estão em [ARCHITECTURE.md](ARCHITECTURE.md).

## Sumário

- [Pré-requisitos](#pré-requisitos)
- [Subindo o ambiente](#subindo-o-ambiente)
- [Variáveis de ambiente](#variáveis-de-ambiente)
- [Migrations](#migrations)
- [Identidades e autenticação](#identidades-e-autenticação)
- [Exemplos de chamada](#exemplos-de-chamada)
- [Mensageria](#mensageria)
- [Observabilidade](#observabilidade)
- [Testes](#testes)
- [Múltiplas instâncias e simulação de falhas](#múltiplas-instâncias-e-simulação-de-falhas)
- [Estrutura do projeto](#estrutura-do-projeto)

## Pré-requisitos

| Ferramenta | Versão | Para quê |
| --- | --- | --- |
| Docker + Compose | 24+ | subir o ambiente completo |
| Go | 1.27.1 | rodar os testes e os binários localmente |

A versão do Go está declarada em [`go.mod`](go.mod) e no [`Dockerfile`](Dockerfile).
Nada mais precisa estar instalado: PostgreSQL, SQS e o IdP sobem em containers.

## Subindo o ambiente

```sh
cp .env.example .env
docker compose up --build
```

Isso sobe cinco serviços, nesta ordem de dependência:

| Serviço | Porta | O que faz |
| --- | --- | --- |
| `postgres` | 5432 | banco de dados |
| `localstack` | 4566 | SQS; as filas são provisionadas na subida |
| `keycloak` | 8081 | IdP; o realm é importado com as identidades de teste |
| `migrate` | — | aplica o schema e sai |
| `api` | 8080 | o serviço |

Cada um só inicia depois que suas dependências ficam saudáveis, e a aplicação
só sobe depois que as migrations terminam. Em uma máquina com as imagens já
baixadas, o conjunto fica de pé em cerca de 25 segundos.

Para conferir:

```sh
curl -s http://localhost:8080/health/ready
```

Para derrubar tudo, incluindo o volume do banco:

```sh
docker compose down -v
```

### Três instâncias

```sh
docker compose --profile multi up --build
```

Sobe `api` (8080), `api-2` (8090) e `api-3` (8091). São processos
independentes, cada um com o próprio pool de conexões e a própria memória; a
coordenação acontece apenas pelo banco.

## Variáveis de ambiente

O arquivo [`.env.example`](.env.example) traz todas, comentadas, com valores
locais. Nenhum segredo real: as credenciais são de emulador e estão versionadas
de propósito, para que a reprodução não dependa de nada enviado por fora.

As que mais importam:

| Variável | Padrão | O que governa |
| --- | --- | --- |
| `DATABASE_URL` | — | obrigatória |
| `DATABASE_STATEMENT_TIMEOUT` | `10s` | teto de duração de um comando no servidor |
| `AUTH_ISSUER_URL` | — | obrigatória; o emissor que os tokens declaram |
| `AUTH_JWKS_URL` | vazio | onde buscar as chaves, quando o emissor não é alcançável pela aplicação |
| `AUTH_AUDIENCE` | `jungle-api` | audiência exigida nos tokens |
| `SQS_INBOUND_QUEUE_URL` | — | obrigatória; fila de operações recebidas |
| `SQS_OUTBOUND_QUEUE_URL` | — | obrigatória; destino dos eventos de integração |
| `SQS_VISIBILITY_TIMEOUT` | `30s` | precisa cobrir o processamento de uma mensagem |
| `OUTBOX_LEASE` | `30s` | reserva de um evento durante a publicação |
| `REFERENCE_TTL` | `15m` | prazo total de espera por uma referência |
| `REFERENCE_MAX_ATTEMPTS` | `10` | teto de tentativas; o que vencer primeiro encerra |
| `HTTP_SHUTDOWN_TIMEOUT` | `20s` | prazo para drenar requisições e encerrar workers |

A configuração é validada na inicialização e **todos** os problemas são
reportados de uma vez:

```
config: configuração inválida:
  - DATABASE_MIN_CONNS (50) não pode exceder DATABASE_MAX_CONNS (5)
  - HTTP_PORT fora do intervalo válido: 99999
```

Uma dependência indisponível impede a aplicação de subir, em vez de deixá-la
subir para falhar na primeira aposta.

## Migrations

As migrations são embutidas no binário com `go:embed`, então o mesmo artefato
aplica o schema em qualquer ambiente. O Compose roda o serviço `migrate` antes
de a aplicação subir.

Manualmente:

```sh
docker compose run --rm migrate up
```

```sh
docker compose run --rm migrate version
```

```sh
docker compose run --rm migrate down
```

```sh
docker compose run --rm migrate steps -1
```

Fora do Compose, com `DATABASE_URL` apontando para o banco:

```sh
go run ./cmd/migrate up
```

O `golang-migrate` toma um advisory lock durante a aplicação, então várias
instâncias subindo ao mesmo tempo não disputam o schema. Esse lock é de
bootstrap e não tem relação com a coordenação por carteira, que nunca usa lock
global.

### O que cada migration faz

| Arquivo | Conteúdo |
| --- | --- |
| `000001_wallets` | carteiras; saldo não negativo, unicidade de (jogador, moeda) |
| `000002_wager_transactions` | operações; dois índices de idempotência, separação interna/externa |
| `000003_wallet_ledger_entries` | ledger; aritmética verificada pelo banco, unicidade por transação |
| `000004_inbox_outbox` | inbox e outbox |
| `000005_protections` | triggers de append-only e imutabilidade, privilégios do papel da aplicação |
| `000006_outbox_payload_exact` | payload da outbox em `JSON` e não `JSONB`, para preservar os bytes |

## Identidades e autenticação

O realm em [`docker/keycloak/realm-jungle.json`](docker/keycloak/realm-jungle.json)
é importado automaticamente e traz quatro clientes, todos por
`client_credentials`:

| Cliente | Secret | Papel | `provider_id` |
| --- | --- | --- | --- |
| `jungle-internal` | `internal-secret-local` | `wallet-admin` | — |
| `provider-a` | `provider-a-secret-local` | `wager-provider` | `provider-a` |
| `provider-b` | `provider-b-secret-local` | `wager-provider` | `provider-b` |
| `provider-efemero` | `provider-efemero-secret-local` | `wager-provider` | `provider-efemero` |

O `provider-b` existe para demonstrar o isolamento: ele não enxerga as operações
do `provider-a`. O `provider-efemero` tem validade de token de **1 segundo** e
serve a um único teste, o de recusa de credencial expirada — forjar um JWT
vencido não serviria, porque a assinatura falharia antes de a validade ser
olhada.

Obtendo um token:

```sh
curl -s -X POST http://localhost:8081/realms/jungle/protocol/openid-connect/token -d grant_type=client_credentials -d client_id=provider-a -d client_secret=provider-a-secret-local
```

Para os exemplos adiante:

```sh
export TOKEN_INTERNO=$(curl -s -X POST http://localhost:8081/realms/jungle/protocol/openid-connect/token -d grant_type=client_credentials -d client_id=jungle-internal -d client_secret=internal-secret-local | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
```

```sh
export TOKEN_PROVEDOR=$(curl -s -X POST http://localhost:8081/realms/jungle/protocol/openid-connect/token -d grant_type=client_credentials -d client_id=provider-a -d client_secret=provider-a-secret-local | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
```

**Operações de carteira são do serviço interno.** Abertura, consulta, extrato e
reconciliação exigem o papel `wallet-admin`; um provedor autenticado recebe 403.
O caminho inverso também vale: o serviço interno não envia operação de provedor.

**O `providerId` vem do token, nunca do corpo.** Um provedor que envie o
identificador de outro recebe 403 e nenhum efeito financeiro acontece.

## Exemplos de chamada

### Abrir carteira

```sh
curl -s -X POST http://localhost:8080/wallets -H "Authorization: Bearer $TOKEN_INTERNO" -H 'Content-Type: application/json' -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}'
```

```json
{
  "id": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "balance": { "amount": "1000.00", "currency": "BRL" },
  "version": 1
}
```

### Enviar uma aposta

```sh
curl -s -X POST http://localhost:8080/wagering/transactions -H "Authorization: Bearer $TOKEN_PROVEDOR" -H 'Content-Type: application/json' -H 'Idempotency-Key: provider-a:transaction-123' -d '{"providerId":"provider-a","externalTransactionId":"transaction-123","playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}'
```

```json
{
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "status": "PROCESSED",
  "balance": { "amount": "975.00", "currency": "BRL" },
  "idempotentReplay": false
}
```

Repetir a mesma requisição devolve `"idempotentReplay": true` e o saldo
**observado no processamento original**, não o atual.

### Estornar

```sh
curl -s -X POST http://localhost:8080/wagering/transactions -H "Authorization: Bearer $TOKEN_PROVEDOR" -H 'Content-Type: application/json' -H 'Idempotency-Key: provider-a:transaction-124' -d '{"providerId":"provider-a","externalTransactionId":"transaction-124","referenceExternalTransactionId":"transaction-123","playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37","roundId":"round-987","gameId":"fortune-chimp","kind":"REFUND","money":{"amount":"25.00","currency":"BRL"}}'
```

Se a referência ainda não tiver chegado, a resposta é **202** com
`"status": "PENDING_REFERENCE"`, e um worker retoma a operação depois.

### Consultas

```sh
curl -s http://localhost:8080/wallets/$WALLET_ID -H "Authorization: Bearer $TOKEN_INTERNO"
```

```sh
curl -s "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=50" -H "Authorization: Bearer $TOKEN_INTERNO"
```

```sh
curl -s http://localhost:8080/providers/provider-a/wagering/transactions/transaction-123 -H "Authorization: Bearer $TOKEN_PROVEDOR"
```

A paginação do extrato usa cursor opaco: a resposta traz `nextCursor`, que deve
ser devolvido como está na chamada seguinte.

### Reconciliação

```sh
curl -s -X POST http://localhost:8080/wallets/$WALLET_ID/reconciliation -H "Authorization: Bearer $TOKEN_INTERNO"
```

```json
{
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "storedBalance": { "amount": "975.00", "currency": "BRL" },
  "calculatedBalance": { "amount": "975.00", "currency": "BRL" },
  "difference": { "amount": "0.00", "currency": "BRL" },
  "consistent": true,
  "checkedEntries": 2
}
```

A reconciliação reconstrói o saldo a partir do ledger e compara. Ela não altera
nada: relata.

### Códigos de resposta

| Código | Quando |
| --- | --- |
| `200` | operação concluída, inclusive em replay |
| `201` | carteira aberta |
| `202` | operação aguardando referência |
| `400` | entrada inválida, campo ausente, `Idempotency-Key` ausente |
| `401` | credencial ausente, expirada, com assinatura ou audiência inválida |
| `403` | identidade sem permissão, ou `providerId` divergente do token |
| `404` | carteira ou operação inexistente — ou pertencente a outro provedor |
| `409` | abertura repetida, ou chave de idempotência com outro conteúdo |
| `422` | recusa por regra de negócio; o corpo traz `failureCode` e `correctable` |
| `503` | dependência temporariamente indisponível |

Consulta de operação alheia devolve **404 e não 403**: responder "existe, mas
não é seu" confirmaria a existência de uma operação de outro provedor.

## Mensageria

As filas são provisionadas na subida do LocalStack por
[`docker/localstack/01-filas.sh`](docker/localstack/01-filas.sh):

| Fila | Tipo | Observações |
| --- | --- | --- |
| `wager-transactions.fifo` | FIFO | entrada; redrive para a DLQ após 5 recebimentos |
| `wager-transactions-dlq.fifo` | FIFO | destino das mensagens que esgotaram as tentativas |
| `wager-events` | padrão | saída dos eventos de integração |

Enviando uma operação pela fila:

```sh
docker compose exec localstack awslocal sqs send-message --queue-url http://localhost:4566/000000000000/wager-transactions.fifo --message-group-id "$WALLET_ID" --message-deduplication-id "msg-001" --message-body '{"messageId":"msg-001","type":"WagerTransactionRequested","occurredAt":"2026-10-02T12:00:00.000Z","data":{"providerId":"provider-a","externalTransactionId":"transaction-200","idempotencyKey":"provider-a:transaction-200","playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}'
```

Lendo os eventos publicados:

```sh
docker compose exec localstack awslocal sqs receive-message --queue-url http://localhost:4566/000000000000/wager-events --max-number-of-messages 10
```

**`MessageGroupId` recomendado é o `walletId`**: o FIFO garante ordem dentro do
grupo, e agrupar por carteira preserva a ordem das operações de um jogador sem
serializar jogadores distintos. **`MessageDeduplicationId` recomendado é o
`messageId`** do envelope, o que dá ao broker a mesma identidade que a inbox usa.

A deduplicação do FIFO cobre apenas cinco minutos. Ela não substitui a inbox nem
as constraints: as garantias financeiras não dependem dela.

## Observabilidade

### Logs

JSON em `stdout`, um objeto por linha:

```json
{"ts":"2026-10-02T13:41:27.222Z","level":"INFO","msg":"requisição atendida","env":"development","instance":"api-1","correlationId":"3d73cea6-65e9-4407-911b-4738b57c48fb","method":"POST","route":"POST /wagering/transactions","status":200,"durationMs":4}
```

```sh
docker compose logs -f api
```

Token, segredo, cabeçalho de autorização e corpo de requisição **nunca** são
registrados.

### Métricas

Formato Prometheus em `GET /metrics`:

```sh
curl -s http://localhost:8080/metrics | grep '^jungle_'
```

| Série | O que mostra |
| --- | --- |
| `jungle_wager_results_total` | operações por tipo, estado e código de falha |
| `jungle_wager_replays_total` | reenvios reconhecidos |
| `jungle_wager_idempotency_conflicts_total` | chaves reutilizadas com outro conteúdo |
| `jungle_wallet_concurrency_conflicts_total` | atualizações recusadas por versão |
| `jungle_outbox_lag_seconds` | idade do evento mais antigo não publicado |
| `jungle_consumer_messages_total` | mensagens por desfecho, com rótulo `permanent` |
| `jungle_reconciliation_mismatches_total` | divergências entre saldo e ledger |

O endpoint é público, como os health checks — um coletor dentro do cluster não
carrega credencial. Em produção ele fica atrás de política de rede ou numa porta
administrativa separada.

### Health checks

```sh
curl -s http://localhost:8080/health/live
```

```sh
curl -s http://localhost:8080/health/ready
```

`live` responde enquanto o processo estiver de pé; `ready` consulta as
dependências. Um processo vivo com banco fora do ar deve parar de receber
tráfego, não ser reiniciado.

## Testes

### Sem infraestrutura

Os testes de integração pulam sozinhos quando as variáveis não estão definidas,
então estes comandos funcionam numa máquina sem nada rodando:

```sh
go test ./...
```

```sh
go test -race ./...
```

```sh
go vet ./...
```

```sh
gofmt -l .
```

Isso exercita o domínio inteiro — `Money`, `Wallet`, `WagerTransaction`,
eventos, configuração — e a trava automática contra ponto flutuante.

### Com infraestrutura

Suba **só as dependências**, sem a aplicação, e carregue as variáveis dos
testes:

```sh
docker compose up -d postgres localstack keycloak migrate
```

O realm é importado só quando o container do Keycloak é criado, não a cada
subida. Se o seu já existia antes, recrie-o uma vez — senão o teste de
credencial expirada falha por não achar o `provider-efemero`:

```sh
docker compose up -d --force-recreate keycloak
```

```sh
set -a && source .env.test && set +a
```

```sh
go test -race ./...
```

O [`.env.test`](.env.test) aponta para os endereços expostos pelo Compose. As
filas usadas são dedicadas (`test-*`), então a suíte não disputa mensagens com o
consumidor da aplicação.

**A aplicação fica fora** porque a fila dedicada não basta: o publicador da
outbox de cada instância varre a tabela `outbox_events` do mesmo banco a cada
segundo, e é a tabela — não a fila de destino — que os testes de publicação
medem. Com instâncias no ar, uma delas reivindica o evento do teste antes do
publicador do teste, e os cenários de lease, reagendamento e entrega única
falham de forma intermitente. Não é defeito da aplicação: é a aplicação fazendo
o seu trabalho sobre a mesma fila que o teste está inspecionando.

Entre os pacotes de teste, a disputa pela outbox é resolvida por um advisory
lock no PostgreSQL: quem vai medir a fila limpa os pendentes primeiro, e essa
limpeza é necessariamente global. A trava é do banco porque o problema também é
— `go test ./...` roda cada pacote num processo próprio, e vários ao mesmo
tempo, então um mutex em Go não alcançaria o pacote vizinho. Por isso o comando
acima não precisa de `-p 1`.

Os cenários que precisam das instâncias no ar são os de múltiplas instâncias,
logo abaixo, e eles conversam por HTTP em vez de inspecionar a outbox.

Isso cobre, contra PostgreSQL, SQS e Keycloak reais: migrations, constraints,
imutabilidade do ledger, atomicidade, inbox, reentrega, outbox concorrente,
retry, redrive até a DLQ, autenticação — incluindo credencial ausente, inválida
e expirada — e isolamento entre provedores.

## Múltiplas instâncias e simulação de falhas

```sh
docker compose --profile multi up -d
```

```sh
set -a && source .env.test && set +a
```

```sh
export TEST_API_URLS="http://127.0.0.1:8080,http://127.0.0.1:8090,http://127.0.0.1:8091"
```

```sh
go test -race -count=1 ./test/scenarios/
```

Esses testes falam com as três instâncias pela rede e verificam:

- cinquenta envios da mesma operação, espalhados pelas três, produzindo um único
  débito;
- duas apostas de 80,00 sobre saldo de 100,00 chegando a instâncias diferentes:
  uma processada, uma recusada, saldo final de 20,00 e um débito no ledger;
- nove carteiras distintas avançando em paralelo;
- uma sequência distribuída fechando o saldo contra créditos menos débitos.

### Derrubando instâncias

Os cenários de recuperação param e religam containers, então ficam atrás de uma
variável — quem roda `go test ./...` numa máquina de desenvolvimento não espera
que uma instância seja derrubada no meio:

```sh
export TEST_ALLOW_CONTAINER_CONTROL=1
```

```sh
go test -race -count=1 ./test/scenarios/ -run 'Pendencia|Idempotencia' -v
```

Eles exercitam:

- uma pendência de referência registrada pela instância A, que é então parada; a
  aposta chega pela B e o worker da B ou da C conclui o estorno;
- o religamento da A, com um reenvio devolvendo o saldo do processamento
  original e não o atual;
- o reinício das três instâncias, com a idempotência preservada — o teste
  confere o próprio reinício comparando o instante de início dos containers.

## Estrutura do projeto

```
cmd/api          binário do serviço; composição Fx e sondagem de saúde
cmd/migrate      aplicação e reversão do schema
internal/domain  Money, Wallet, WagerTransaction, eventos — sem Fx, HTTP, SQS ou SQL
internal/app     casos de uso e workers
internal/adapter borda: HTTP, PostgreSQL, SQS
internal/platform configuração, autenticação, log, métricas, ciclo de vida
migrations       schema versionado, embutido no binário
docker           provisionamento de filas, realm do Keycloak, papel do banco
test             suporte de teste e cenários distribuídos
```

O domínio não conhece Fx, HTTP, SQS nem biblioteca de persistência. As decisões
sobre dinheiro, transações, locks, idempotência, reversões, inbox/outbox,
autenticação e encerramento estão em [ARCHITECTURE.md](ARCHITECTURE.md).
