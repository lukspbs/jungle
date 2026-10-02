# Decisões de arquitetura

Este documento registra o porquê das escolhas, as interpretações adotadas onde o
enunciado deixava margem, e o que ficou de fora. As instruções de execução estão
no [README](README.md).

## Sumário

- [Camadas](#camadas)
- [Dinheiro](#dinheiro)
- [Persistência e transações](#persistência-e-transações)
- [Concorrência](#concorrência)
- [Idempotência](#idempotência)
- [Máquina de estados](#máquina-de-estados)
- [Reversões](#reversões)
- [Referências pendentes](#referências-pendentes)
- [Inbox e outbox](#inbox-e-outbox)
- [Autenticação e autorização](#autenticação-e-autorização)
- [Composição com Fx e encerramento](#composição-com-fx-e-encerramento)
- [Observabilidade](#observabilidade)
- [Testes](#testes)
- [Interpretações adotadas](#interpretações-adotadas)
- [Limitações e trabalho não concluído](#limitações-e-trabalho-não-concluído)

## Camadas

```
adapter/http · adapter/sqs      borda: traduz contrato externo em comando
         ↓
app                             casos de uso e workers
         ↓
domain                          regras; não conhece Fx, HTTP, SQS nem SQL
         ↓
adapter/postgres                SQL explícito, transações, locks
         ↓
PostgreSQL                      constraints, triggers, privilégios
```

O **domínio** não importa nada de infraestrutura. Instantes e identificadores
chegam por parâmetro, o que mantém as transições determinísticas e testáveis sem
relógio nem gerador globais.

A camada **app depende do pacote `postgres` diretamente**, sem interfaces no
meio. É deliberado: o desafio exige que transações, locks e constraints
permaneçam explícitos e verificáveis, e uma camada de abstração sobre os
repositórios esconderia justamente o que precisa estar visível — onde está o
`FOR UPDATE` e o que está dentro de qual transação. O que precisa ficar
independente de persistência é o domínio, e ele está.

A exceção é o destino dos eventos: `app.EventPublisher` é uma porta de verdade,
porque o worker não deve conhecer SQS e a fila de saída é genuinamente
substituível.

## Dinheiro

**`int64` em unidades mínimas**, escala fixa de duas casas, moeda carregada
junto no value object. Nenhum `float32` ou `float64` participa de parsing,
cálculo, serialização ou persistência.

Intervalo representável: `-92.233.720.368.547.758,08` a
`92.233.720.368.547.758,07`. Parsing, soma, subtração e negação detectam estouro
e devolvem erro em vez de truncar.

**Persistência**: `BIGINT` para o valor, `CHAR(3)` para a moeda, em colunas
separadas. `NUMERIC` foi descartado porque exigiria escolher entre ler em
`float64` — proibido — ou em string com reparsing a cada leitura. `BIGINT` casa
diretamente com a representação interna.

**Parsing estrito**: recusa string vazia, `NaN`, `Infinity`, notação científica,
sinal explícito, separador de milhar, escala excedente e valores negativos nas
entradas externas. Cada recusa tem erro próprio, classificável por `errors.Is`,
porque a API precisa de `failureCode` distinto para "mandou negativo" e "mandou
lixo". Entrada inválida nunca é arredondada em silêncio.

**Normalização**: `"25"`, `"25.0"` e `"25.00"` são formas equivalentes e
produzem o mesmo valor. A forma canônica, com exatamente duas casas, é o que
entra no hash de idempotência — então variação de formato na entrada não vira
conflito de payload.

**Trava automática**: um teste varre a árvore sintática do módulo inteiro atrás
de `float32`, `float64`, literais de ponto flutuante e `ParseFloat`. Cálculo
monetário em ponto flutuante é critério eliminatório, e isso merece verificação
executável em vez de revisão manual. A trava já se pagou: pegou um `math.Pow`
usado no cálculo de backoff.

Há uma lista de exceções, hoje com um único arquivo: o pacote de métricas, cuja
API do cliente Prometheus é `float64` e não admite outro tipo. A exceção não é
na palavra — um segundo teste confere que nenhum arquivo isento importa os
pacotes de domínio que carregam valor monetário. A regra que importa sempre foi
*dinheiro nunca toca float*, e não *o módulo não menciona float*.

## Persistência e transações

**`pgx` com SQL explícito.** Sem ORM e sem geração de código: as garantias do
desafio dependem de detalhes que precisam ser auditáveis — onde está o
`FOR UPDATE`, qual constraint produz o conflito, o que está dentro de qual
transação.

**A transação é delimitada por `postgres.Store.InTx`.** Ele entrega um conjunto
de repositórios que compartilham o mesmo executor; nenhum deles guarda o pool,
então não há caminho para escapar da transação por engano.

Uma operação completa confirma num único commit: o estado da transação, o saldo
da carteira, o lançamento do ledger e os registros de evento da outbox. Na
entrada por SQS, o registro de inbox entra no mesmo commit.

**Nível de isolamento das escritas: `READ COMMITTED`**, o padrão do PostgreSQL.
A coordenação entre escritores vem do `SELECT ... FOR UPDATE` explícito, não do
nível de isolamento: um lock de linha dá exclusão mútua determinística por
carteira, sem as falhas de serialização que `REPEATABLE READ` produziria sob as
cinquenta duplicatas simultâneas do teste obrigatório.

**A reconciliação é a exceção, e usa `REPEATABLE READ` em modo somente leitura.**
Ela não disputa nada com ninguém: ela compara dois lugares do banco entre si, o
saldo da carteira e a soma do ledger, por comandos diferentes. Em
`READ COMMITTED` cada comando pega um snapshot novo, e uma aposta confirmada no
intervalo apareceria como divergência — alarme falso justamente na métrica que
existe para denunciar divergência de verdade. `REPEATABLE READ` fixa o snapshot
no primeiro comando, que é a garantia de que a conferência precisa; sendo só
leitura, não há falha de serialização a tratar. A separação está em
`Store.InTx` para escrita e `Store.InSnapshot` para esta leitura.

### Invariantes no banco

As garantias que não podem depender da aplicação estão no schema:

| Garantia | Mecanismo |
| --- | --- |
| Saldo nunca negativo | `CHECK (balance_minor >= 0)` |
| `balanceAfter = balanceBefore ± money` | `CHECK` condicional por direção |
| Uma movimentação por transação | índice único `(wallet_id, transaction_id)` |
| Ledger append-only | triggers em `UPDATE`, `DELETE` **e `TRUNCATE`** |
| Transação terminal imutável | trigger que recusa transição a partir de `PROCESSED`, `REJECTED` ou `FAILED` |
| Versão como testemunha | trigger: saldo mudou ⇒ versão **exatamente** +1; saldo igual ⇒ versão igual |
| `OPENING` inatingível por provedor | `CHECK` amarrando `source` e `kind` |
| Crédito inicial único | índice único parcial em `(wallet_id) WHERE kind = 'OPENING'` |

`TRUNCATE` precisou de gatilho próprio `FOR EACH STATEMENT`: trigger de linha não
o intercepta, e sem ele o append-only seria contornável com um comando.

**Privilégios como segunda camada.** As triggers valem até para o dono do
schema; os grants são a camada de baixo. O papel da aplicação recebe apenas
`SELECT` e `INSERT` no ledger — não chega a ter o privilégio de tentar alterar
um lançamento. Nenhuma tabela concede `DELETE`.

## Concorrência

**Lock pessimista por carteira**: `SELECT ... FOR UPDATE` na linha da carteira,
mantido até o commit.

A alternativa — controle otimista com retry — funcionaria, mas produz tempestade
de retry justamente no cenário que o desafio manda exercitar: cinquenta envios
simultâneos da mesma operação. O lock pessimista resolve a disputa de forma
determinística.

O lock é **de linha**, então carteiras distintas travam linhas distintas e
avançam em paralelo. Não existe lock global em lugar nenhum do caminho
financeiro. O único advisory lock do sistema é o do `golang-migrate`, de
bootstrap, para que instâncias subindo juntas não disputem o schema.

A cláusula `WHERE version = $n` no `UPDATE` do saldo existe como **detecção**,
não como mecanismo: com o lock, ela nunca deveria falhar. Se falhar, alguém
escreveu por um caminho que não passou pelo lock, e abortar ruidosamente é
melhor que sobrescrever uma atualização confirmada. A métrica
`jungle_wallet_concurrency_conflicts_total` existe para que isso apareça.

`DATABASE_STATEMENT_TIMEOUT` limita a duração de um comando no servidor. Sem
ele, um cliente travado seguraria o lock de uma carteira indefinidamente e
bloquearia todas as operações daquele jogador.

## Idempotência

**Persistente, imposta por constraint, nunca em memória.**

Dois índices únicos, porque são duas garantias diferentes:

| Índice | Impede |
| --- | --- |
| `(provider_id, external_transaction_id)` | reaplicar a mesma operação financeira sob outra chave |
| `(provider_id, idempotency_key)` | reutilizar a chave em outra operação |

**A inserção não consulta antes.** Consultar e depois inserir abre uma janela
entre a decisão e a escrita, e é exatamente nela que duas requisições
simultâneas passariam. O caminho é: tenta inserir, e a constraint decide. A
consulta de replay que acontece antes é otimização — ela evita trabalho quando a
operação já foi processada — mas não é a garantia.

Perder a corrida é tratado: a violação de unicidade dispara uma reconsulta, que
devolve o resultado gravado pela vencedora.

### Hash do payload

SHA-256 sobre JSON canônico dos campos de negócio. A canonicidade vem de
serializar um `map`: a biblioteca padrão do Go ordena as chaves
lexicograficamente, então não depende da ordem de declaração em nenhuma struct.

Entram: `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`,
`gameId`, `kind`, `amount`, `currency`, `referenceExternalTransactionId`, e uma
versão de esquema.

Ficam de fora a chave de idempotência e todo metadado de transporte —
`messageId`, cabeçalhos, `occurredAt` do envelope. É isso que faz a mesma
operação chegar por HTTP e por SQS com o mesmo hash. A exclusão da chave é
estrutural: a struct de entrada do cálculo sequer tem campo para ela.

Campos ausentes entram como string vazia em vez de omitidos, para que presença e
ausência nunca fiquem ambíguas. O valor entra na forma canônica, com duas casas.

### O saldo congelado

A transação guarda `result_balance_minor`: o saldo observado no processamento.
Um replay devolve **esse** valor, não o atual. Reler a carteira no replay
responderia errado assim que ela se movimentasse — e um `CHECK` garante que o
campo existe exatamente quando o estado é `PROCESSED`.

## Máquina de estados

```
PENDING ──┬──> PENDING_REFERENCE ──┬──> PROCESSED   terminal
          │                        ├──> REJECTED    terminal
          ├────────────────────────┴──> FAILED      terminal
```

Os três estados terminais não têm transição de saída. Isso vale em dois lugares:
no domínio, onde o método de transição recusa; e no banco, onde uma trigger
recusa qualquer `UPDATE` sobre uma linha terminal. O teste força o `UPDATE`
direto, contornando o domínio, para provar que a trigger segura sozinha.

**Processamento síncrono.** O §6.3 do enunciado permite: *"Operações sem
dependências podem ser concluídas de forma síncrona, sem commit intermediário de
aceite."* Uma aposta entra e sai num commit só. Isso elimina legitimamente a
segunda metade do cenário 8 do enunciado, que se aplica apenas *"se houver
aceite assíncrono"*. O único estado intermediário é `PENDING_REFERENCE`, e ele
tem retomada durável por worker.

**Falha transitória × permanente.** Transitória é o que uma nova tentativa pode
resolver: indisponibilidade do banco, da fila. Ela não vira estado terminal —
a operação não é confirmada e a entrada é reentregue. Permanente é o que
nenhuma tentativa resolve: envelope malformado, tipo inexistente, chave
reutilizada com outro conteúdo. Essa chega à DLQ pela política de redrive.
`FAILED` é reservado à falha permanente de infraestrutura que precisa ficar
registrada para auditoria.

### Códigos de falha

Conjunto fechado, cada um declarando se a entrada é **corrigível** — isto é, se
o provedor pode ajustar o corpo e reenviar sob chave nova com chance de sucesso.

| Código | Corrigível |
| --- | --- |
| `INSUFFICIENT_FUNDS` | não |
| `REVERSAL_INSUFFICIENT_FUNDS` | não |
| `REFERENCE_NOT_FOUND` | não |
| `REFERENCE_NOT_PROCESSED` | não |
| `REFERENCE_ALREADY_REVERSED` | não |
| `INFRASTRUCTURE_FAILURE` | não |
| `REFERENCE_MISMATCH` | sim |
| `REFERENCE_AMOUNT_MISMATCH` | sim |
| `WALLET_NOT_FOUND` | sim |
| `WALLET_PLAYER_MISMATCH` | sim |
| `CURRENCY_MISMATCH` | sim |
| `INVALID_AMOUNT` | sim |
| `KIND_NOT_ALLOWED` | sim |

`INSUFFICIENT_FUNDS` e `REVERSAL_INSUFFICIENT_FUNDS` são distintos por exigência
do enunciado, e porque a causa e a ação corretiva do operador são diferentes: a
primeira é um jogador sem saldo, a segunda é uma reversão que estouraria o saldo
atual.

## Reversões

O sentido da reversão é sempre o oposto do movimento que ela desfaz:

| Referência | Movimento original | Reversão |
| --- | --- | --- |
| `BET` | débito | credita |
| `WIN` | crédito | debita |
| `REFUND` | crédito | debita |

`REFUND` só incide sobre `BET` — estornar um ganho não descreve nada. `ROLLBACK`
desfaz qualquer operação que tenha movimentado saldo.

A operação e sua referência precisam concordar em provedor, jogador, carteira,
moeda e rodada; divergência vira `REFERENCE_MISMATCH`. O valor precisa ser
idêntico: reversões parciais estão fora do escopo do desafio, e valor diferente
vira `REFERENCE_AMOUNT_MISMATCH`.

### `REFUND` e `ROLLBACK` sobre a mesma aposta

**Política adotada: uma referência admite no máximo uma reversão bem-sucedida,
de qualquer tipo.**

O índice único `(reference_transaction_id, kind) WHERE status = 'PROCESSED'`
impede duas reversões do *mesmo* tipo. Mas `REFUND` e `ROLLBACK` são tipos
diferentes: passariam pelo índice e devolveriam o mesmo débito duas vezes.

A verificação complementar acontece na aplicação, **sob o lock da carteira**,
então não há janela entre consultar e decidir. A segunda reversão é recusada com
`REFERENCE_ALREADY_REVERSED`.

O índice é parcial em `PROCESSED` de propósito: tentativas recusadas não
consomem a cota, e o provedor pode corrigir e reenviar.

### Reversão sem saldo

Uma reversão que precisaria debitar mais que o saldo disponível é recusada com
`REVERSAL_INSUFFICIENT_FUNDS` e fica registrada, auditável. O saldo não se move.

## Referências pendentes

Uma reversão pode chegar antes da transação que desfaz — esperado numa entrega
*at-least-once* sem ordem garantida.

Consultar a referência tem três desfechos:

| Situação | Ação |
| --- | --- |
| Não existe | `PENDING_REFERENCE`, com evento e retentativa agendada |
| Existe mas ainda não terminou | espera também: pode concluir em instantes |
| Terminou sem sucesso | recusa com `REFERENCE_NOT_PROCESSED` — não há o que desfazer, e o estado é terminal |

O recuo entre tentativas dobra a cada falha, por deslocamento de bits em
aritmética inteira, até um teto. O teto existe para que uma pendência longa não
acabe com intervalos de horas e atrase a resolução quando a referência enfim
chegar.

O contador de tentativas avança num único lugar: a reivindicação da pendência,
em `ClaimPendingReferences`. Contar ali, e não no desfecho, faz uma tentativa
que nunca retorna também contar — senão uma pendência problemática seria
retomada para sempre. O agendamento da próxima tentativa não toca o contador:
incrementar nos dois lugares faria cada rodada do worker contar duas, e o
`REFERENCE_MAX_ATTEMPTS` configurado valeria metade, com o recuo avançando dois
degraus por rodada em vez de um.

**Encerramento**: o que vencer primeiro entre `REFERENCE_TTL` e
`REFERENCE_MAX_ATTEMPTS` encerra a espera, com recusa `REFERENCE_NOT_FOUND` e
evento. Não é desaparecimento silencioso: o provedor precisa saber que a
reversão não vai mais acontecer, e a decisão precisa ficar auditável.

**Retomada durável**: a pendência vive no banco e a reivindicação usa
`FOR UPDATE SKIP LOCKED`. Qualquer instância assume qualquer pendência, e
reiniciar todos os processos não perde nada. Cada retomada relê a transação sob
o lock da carteira — entre reivindicar e processar, outra instância pode tê-la
concluído, e sem a releitura a reversão seria aplicada duas vezes.

O worker reusa o mesmo caminho do processamento síncrono para resolver
referência, movimentar, recusar e emitir. Não existe uma segunda implementação
da regra de reversão que possa divergir da primeira.

## Inbox e outbox

### Outbox

O evento é gravado numa tabela, na mesma transação da mudança financeira, e
publicado depois por um worker. Não há caminho em que a publicação preceda o
commit — um teste confirma que o evento não sobrevive ao rollback da transação
que o originou.

**O payload é um instantâneo imutável.** O worker publica exatamente os bytes
gravados; nada é remontado na publicação, então uma republicação entrega o mesmo
conteúdo com o mesmo `eventId`.

A coluna é `JSON` e não `JSONB`. `JSONB` é um formato binário normalizado: ele
reordena as chaves e descarta a formatação. O conteúdo semântico sobrevive, os
bytes não — e isso quebraria a promessa de republicar idêntico para um consumidor
que verifique assinatura ou hash sobre o payload. A migration `000006` registra
a troca.

**Reivindicação**: `FOR UPDATE SKIP LOCKED` faz publishers concorrentes
dividirem a fila em vez de enfileirarem no mesmo registro. Um *lease* em
`locked_until` devolve à fila o trabalho de uma instância que morreu entre
reivindicar e publicar.

**A janela entre publicar e confirmar é real e assumida.** Se o processo cair
ali, o evento é republicado quando o lease vencer. É preferível ao inverso, que
perderia o evento; o `eventId` é preservado e o consumidor deduplica.

**Sem limite de tentativas.** Um evento financeiro que não consegue ser
publicado é problema operacional a resolver, não dado a descartar.
`jungle_outbox_lag_seconds` é a métrica que denuncia a situação.

### Envelope dos eventos

```json
{
  "eventId": "...", "eventType": "WalletBalanceChanged",
  "aggregateType": "Wallet", "aggregateId": "...",
  "correlationId": "...", "causationId": "...",
  "occurredAt": "2026-09-08T12:00:00.000Z", "version": 1,
  "data": { }
}
```

O tipo, a versão e o agregado vêm do próprio payload, não de parâmetros: é
impossível publicar um envelope rotulado com um tipo que não corresponde ao
conteúdo. Instantes em RFC 3339 UTC com milissegundos; valores monetários em
strings decimais.

`WalletBalanceChanged` valida a própria aritmética antes de ser construído: um
payload que afirme um saldo que não decorre do movimento seria indistinguível de
um correto para o consumidor.

Os eventos da abertura interna omitem `providerId`, `externalTransactionId`,
`roundId` e `gameId` — ausentes, não vazios.

### Inbox

Identidade por `(consumerName, messageId)`, com hash do corpo da mensagem.
Reentrega é normal numa entrega *at-least-once*, mas o conteúdo precisa ser o
mesmo: divergência significa reuso indevido do identificador, e aceitar seria
processar uma operação diferente sob a identidade de outra.

O registro da inbox e as alterações de domínio **compartilham o commit**. Isso
exigiu expor `ExecuteWithin` no caso de uso, que executa dentro de uma transação
já aberta: sem isso, uma queda entre duas transações separadas deixaria a
operação aplicada e a mensagem marcada como não consumida, ou o inverso.

Uma operação que ficou aguardando referência também conclui a mensagem de
entrada: a pendência está persistida e o worker assume a continuidade.

### Consumo

A mensagem só é apagada depois do commit do seu tratamento.

| Situação | Ação |
| --- | --- |
| Erro transitório | não apaga; reentrega após o visibility timeout |
| Erro permanente | não apaga; o redrive leva à DLQ após `maxReceiveCount` |
| Recusa de negócio confirmada | **apaga** — é terminal, reentregar não mudaria o desfecho |

Usar o redrive que a fila já tem evita duplicar a lógica de descarte na
aplicação.

`MessageGroupId` recomendado é o `walletId`: o FIFO garante ordem dentro do
grupo, e agrupar por carteira preserva a ordem das operações de um jogador sem
serializar jogadores distintos. `MessageDeduplicationId` recomendado é o
`messageId` do envelope.

A deduplicação do FIFO cobre cinco minutos e só vale entre envios idênticos. Ela
não substitui a inbox nem as constraints: as garantias financeiras não dependem
dela.

## Autenticação e autorização

**IdP externo OIDC**, Keycloak, fluxo `client_credentials`. O serviço não emite
tokens nem guarda senhas: verifica assinatura, emissor, audiência e validade a
cada requisição. As chaves vêm do JWKS e são renovadas pelo cliente, então a
rotação no IdP não exige reinício.

**A identidade determina o `providerId`.** Ele vem do claim `provider_id` do
token, nunca do corpo. Um provedor que envie o identificador de outro recebe 403
e nenhum efeito financeiro acontece.

**Modelo de permissões**: dois papéis de realm.

| Papel | Autoriza |
| --- | --- |
| `wallet-admin` | abertura, consulta, extrato e reconciliação de carteira |
| `wager-provider` | envio e consulta das operações do próprio provedor |

A separação vale nos dois sentidos: um provedor não opera carteira, e o serviço
interno não envia operação de provedor. O isolamento alcança também o replay,
porque a busca é escopada pelo provedor autenticado.

**Consulta de operação alheia devolve 404, não 403.** Responder "existe, mas não
é seu" confirmaria a existência de uma operação de outro provedor.

**Não há interruptor que desligue a verificação.** Apenas `/health/live`,
`/health/ready` e `/metrics` são públicos: um verificador de readiness ou um
coletor de métricas não carrega credencial, e exigir uma transformaria
indisponibilidade do IdP em indisponibilidade aparente do serviço.

O emissor é descoberto na inicialização — um IdP inacessível impede a aplicação
de subir, em vez de deixá-la aceitar requisições que não consegue autenticar.

**`AUTH_JWKS_URL`** existe porque, em container, o endereço pelo qual os clientes
alcançam o IdP e o endereço pelo qual a aplicação o alcança são diferentes. O
Keycloak emite tokens com `iss` igual ao primeiro e repete esse endereço no
`jwks_uri`; dentro da rede do Compose, esse endereço é a própria aplicação.
Informar o JWKS explicitamente resolve, e o claim `iss` continua sendo verificado
contra o emissor declarado.

**Acesso à mensageria** é controlado por credenciais e políticas do broker. O
código não lê nem guarda credencial de fila: o SDK usa a cadeia padrão —
variáveis de ambiente, perfil, metadados da instância. As validações de domínio
no consumidor são as mesmas da entrada HTTP, porque o caso de uso é
compartilhado.

## Composição com Fx e encerramento

Cada pacote exporta o seu `fx.Module`; o `main` apenas os reúne. A ordem da
composição governa o encerramento, porque o Fx desfaz na ordem inversa:

```
sobe:   config → logging → métricas → postgres → app → sqs → http → workers
encerra:                                                  workers → http → postgres
```

O banco, declarado primeiro, é fechado por último — depois de servidor e workers
terem parado de usá-lo.

A configuração é provida **em partes**, e não como um objeto único, para que
cada construtor declare na assinatura apenas o que usa. Um construtor que
recebesse a configuração inteira poderia ler qualquer coisa, e a dependência real
ficaria invisível.

### Inicialização

A conexão com o banco é aberta na construção, e a porta HTTP dentro do
`OnStart`. Uma dependência indisponível ou uma porta ocupada precisam impedir a
aplicação de subir, em vez de deixá-la subir sem conseguir atender.

### Encerramento

Em `SIGTERM`:

1. o consumidor SQS deixa de buscar trabalho novo; o ciclo em andamento termina
   dentro do prazo, e uma mensagem não confirmada volta a ficar visível para
   reentrega segura;
2. os workers de referência e de outbox são cancelados e **confirmam** o
   término — `Stop` espera, e devolve erro se o prazo esgotar, porque o operador
   precisa saber que o encerramento não foi limpo e o processo precisa conseguir
   morrer;
3. o servidor HTTP para de aceitar conexões e drena as requisições em andamento;
4. o pool do banco fecha.

`HTTP_SHUTDOWN_TIMEOUT` governa o prazo de cada etapa; `fx.StopTimeout` o total.

## Observabilidade

**Logs** em JSON, um objeto por linha, com `correlationId`, `messageId`,
`transactionId`, `walletId` e `providerId` conforme disponíveis. Token, segredo,
cabeçalho de autorização e corpo de requisição nunca são registrados: o corpo
carrega valores monetários e identificadores de jogador, e espalhá-los pelo
coletor os tira do controle de acesso que o banco tem.

A rota registrada é o padrão casado pelo roteador, não o caminho literal, para
que a latência seja agrupável por endpoint.

**Métricas** em formato Prometheus, cobrindo resultados por estado, duplicatas,
conflitos de idempotência e de concorrência, publicações e falhas da outbox,
atraso da fila de publicação, mensagens consumidas por desfecho, desfechos das
pendências, latência de processamento e divergências de reconciliação.

Os rótulos são escolhidos para caber num painel: nada de `walletId` ou
`transactionId`, que gerariam uma série por carteira.

**Reconciliação**: reconstrói o saldo a partir do ledger, dentro de uma
transação para que carteira e ledger venham de uma visão consistente. Ela não
altera nada. A divergência aparece em três lugares, como o enunciado pede: na
resposta, no log e na métrica.

## Testes

| Nível | O que cobre |
| --- | --- |
| Unitários | domínio inteiro: parsing e aritmética de dinheiro, invariantes da carteira, máquina de estados, políticas por tipo, eventos, configuração |
| Asserções de schema | cada caso executa um comando proibido e falha se o banco aceitar |
| Integração | PostgreSQL, SQS e Keycloak reais; migrations, constraints, atomicidade, inbox, outbox, autenticação, isolamento |
| Cenários | três instâncias em containers distintos, comunicando-se pela rede |

A suíte é **repetível**: ela compartilha o banco e nunca limpa tabelas — o
ledger recusa `DELETE` e `TRUNCATE` —, então cada teste usa identificadores
próprios. Os testes de fila drenam a outbox antes de medir.

Os testes de integração pulam sozinhos sem as variáveis de ambiente, então
`go test ./...` funciona numa máquina sem infraestrutura.

## Interpretações adotadas

Pontos em que o enunciado deixava margem e foi preciso decidir:

1. **Mesma operação sob chave diferente** devolve o resultado persistido, com
   `idempotentReplay: true`, em vez de conflito. O enunciado diz que a operação
   *não pode ser reaplicada*; devolver o resultado original satisfaz isso. O
   conflito fica reservado ao caso explícito: chave reutilizada com conteúdo
   diferente.

2. **Carteira inexistente sai como erro 404**, não como transação `REJECTED`
   persistida. A chave estrangeira impede registrar uma recusa para uma carteira
   que não existe.

3. **Recusa de negócio responde 422.** O enunciado pede que ela seja
   distinguível de entrada inválida e de conflito, sem fixar o código.

4. **`REFUND` + `ROLLBACK` sobre a mesma aposta**: no máximo uma reversão
   bem-sucedida por referência, de qualquer tipo. Detalhado acima.

5. **Consulta de operação de outro provedor responde 404**, não 403.

6. **`/metrics` é público**, como os health checks. Em produção fica atrás de
   política de rede ou em porta administrativa separada.

7. **Processamento síncrono**, permitido pelo §6.3. Não há aceite assíncrono, o
   que torna a segunda metade do cenário 8 inaplicável por construção.

## Limitações e trabalho não concluído

Honestamente, o que não está pronto ou foi deliberadamente deixado de fora:

### Limitações conhecidas

- **Escala monetária fixa em duas casas.** Moedas com escala diferente — JPY com
  zero, BHD com três — não são suportadas. O enunciado permite escala fixa, mas
  a limitação é real: suportá-las exigiria a escala por moeda no tipo e na
  persistência.

- **Validação de moeda é de formato**, três letras maiúsculas ASCII, sem a
  tabela ISO 4217. Um código sintaticamente válido mas inexistente passaria. O
  risco é contido porque a moeda de toda movimentação é comparada com a da
  carteira, que nasce de uma abertura controlada.

- **Os cenários rodam em BRL.** O tipo carrega a moeda e há testes de
  incompatibilidade entre moedas, conforme o enunciado permite, mas não há
  cenário multimoeda de ponta a ponta.

- **O worker de referências faz polling.** Um `LISTEN/NOTIFY` resolveria a
  latência de resolução sem o intervalo de varredura, ao custo de mais um
  mecanismo a manter.

- **Sem limite de taxa.** Um provedor pode saturar o serviço.

### Diferenciais opcionais não implementados

O enunciado os lista como opcionais, e nenhum foi feito:

- **Tracing com OpenTelemetry.** Os logs já carregam `correlationId` ponta a
  ponta, o que cobre boa parte do diagnóstico, mas não há propagação de contexto
  de trace entre serviços.
- **Dashboards.** As métricas estão expostas em formato Prometheus, prontas para
  coleta, mas não acompanham painéis.
- **Testes de carga.** Não há medição de throughput, p50/p95/p99 nem de atraso da
  outbox sob carga.
- **Ledger de partidas dobradas.** O ledger é de uma perna por movimentação.

### Decisões que mereceriam revisão num sistema de produção

- **Os segredos do Keycloak estão versionados.** São de ambiente local e isso é
  deliberado — um avaliador precisa conseguir subir e autenticar sem receber
  nada por fora. Em produção viriam de um cofre.

- **`migrate down` é destrutivo** e reverte todas as migrations. Num ambiente
  real, a reversão seria por passos e com backup.

- **A camada `app` depende do pacote `postgres`.** Justificado acima, mas um
  sistema com mais de um backend de persistência precisaria da inversão.

- **O publisher da outbox tenta para sempre.** É a escolha certa para não perder
  evento financeiro, mas exige que alguém observe
  `jungle_outbox_lag_seconds` — sem alerta, um destino permanentemente
  indisponível acumula em silêncio.
