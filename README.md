# Wagering Service

Serviço em Go que movimenta carteiras de jogadores a partir de operações de provedores de jogos (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`), recebidas por **HTTP** e por **SQS**, com o mesmo resultado financeiro nas duas entradas, com várias instâncias e com falhas no meio do processamento.

- Enunciado: [`docs/DESAFIO.md`](docs/DESAFIO.md)
- Decisões, limitações e interpretações: [`ARCHITECTURE.md`](ARCHITECTURE.md)
- Especificações de cada etapa: [`specs/`](specs)

## Avaliação rápida

Precisa só de Docker, Go 1.27, `curl` e um shell POSIX (Linux, macOS, WSL ou Git Bash). Leva uns 10 minutos, quase tudo no primeiro `docker compose up`.

**1. Clonar e subir tudo, com três réplicas:**

```sh
git clone https://github.com/rafaelcorreadev1/junglegaming.git
cd junglegaming
docker compose --profile multi up -d --build --wait
curl -s http://localhost:8080/health/ready   # {"status":"up",...}
```

**2. Fluxo autenticado de ponta a ponta.** Cole o bloco inteiro no mesmo terminal:

```sh
API=http://localhost:8080
token() {
  curl -s -X POST http://localhost:8081/realms/wagering/protocol/openid-connect/token \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$1-secret" |
    sed -E 's/.*"access_token":"([^"]+)".*/\1/'
}
INTERNAL=$(token wallet-service); PROVIDER_A=$(token provider-a); PROVIDER_B=$(token provider-b)
PLAYER="player-$(date +%s)"

echo "--- sem token: 401"
curl -s -o /dev/null -w '%{http_code}\n' -X POST $API/wallets

echo "--- abrir carteira com 100.00"
WALLET=$(curl -s -X POST $API/wallets -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -d '{"playerId":"'"$PLAYER"'","initialBalance":{"amount":"100.00","currency":"BRL"}}' |
  sed -E 's/.*"id":"([^"]+)".*/\1/')
echo "walletId=$WALLET"

bet() { # $1 = id externo, $2 = valor
  curl -s -X POST $API/wagering/transactions -H "Authorization: Bearer $PROVIDER_A" \
    -H 'Content-Type: application/json' -H "Idempotency-Key: provider-a:$1" \
    -d '{"providerId":"provider-a","externalTransactionId":"'"$1"'","playerId":"'"$PLAYER"'",
         "walletId":"'"$WALLET"'","roundId":"round-1","gameId":"slot-1","kind":"BET",
         "money":{"amount":"'"$2"'","currency":"BRL"}}'; echo
}

echo "--- duas apostas de 80.00 ao mesmo tempo: uma PROCESSED, outra REJECTED (INSUFFICIENT_FUNDS)"
bet "$PLAYER-a" 80.00 & bet "$PLAYER-b" 80.00 & wait

echo "--- repetir a primeira: idempotentReplay=true, saldo original"
bet "$PLAYER-a" 80.00

echo "--- provider-b não enxerga a transação do provider-a: 403"
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $PROVIDER_B" \
  $API/providers/provider-a/wagering/transactions/$PLAYER-a

echo "--- reconciliação: saldo 20.00, consistent=true"
curl -s -X POST -H "Authorization: Bearer $INTERNAL" $API/wallets/$WALLET/reconciliation; echo
```

**3. Testes:**

```sh
go vet ./...
go test ./...                    # unitários, sem infraestrutura (~30 s)
go test -tags=integration ./...  # tudo contra o Compose: PostgreSQL, Keycloak, LocalStack e 3 réplicas reais (~3 min)
```

Com `-tags=integration`, nenhum teste é pulado: se um container estiver fora do ar, o teste falha. Para `-race`, veja [`go test -race`](#go-test--race); no Windows ele roda num container.

**4. Parar e apagar tudo:** `docker compose --profile multi down -v`.

### Onde cada exigência do desafio é verificada

| Exigência (§13 do enunciado) | Teste |
| --- | --- |
| Money, carteira, estados, regras dos cinco tipos, valores zero, abertura | `internal/domain` |
| Migrations, constraints, imutabilidade do ledger | `migrations/migrations_test.go` |
| Atomicidade, locks, inbox, outbox | `internal/infra/postgres`, `internal/application` |
| Reentrega, retry, DLQ, SIGTERM do consumidor | `internal/infra/sqs` |
| Composição Fx, start e stop sem vazar goroutines | `internal/app` |
| Tokens ausentes, inválidos e expirados; isolamento entre provedores; operações internas | `test/integration/auth_test.go` |
| 1. Mesma aposta 50 vezes em paralelo | `TestSameBetSentFiftyTimesInParallelDebitsOnce` (e `...AcrossInstances`) |
| 2. Duas apostas de 80.00 sobre 100.00 | `TestTwoBetsCompetingForTheSameBalance` (e `...AcrossInstances`) |
| 3. Carteiras distintas em paralelo | `TestDistinctWalletsAdvanceInParallel` (e `...OnEveryInstance`) |
| 4. Três instâncias independentes | `test/multiinstance` (falha se alguma réplica não participou) |
| 5. Consumidor morto entre commit e remoção da mensagem | `TestConsumerKilledAfterCommitBeforeDeletingTheMessage` |
| 6. Publishers disputando a outbox | `TestCompetingPublishersLoseNothingAndKeepEachAggregateInOrder`, `TestPublisherKilledAfterSendingRepublishesTheSameEvent` |
| 7. Reversão antes da referência | `TestReversalArrivingBeforeItsReference` |
| 8. Reinício preserva idempotência, pendências e consistência | `TestRestartKeepsIdempotencyPendingWorkAndConsistency` |
| Mesma operação por HTTP e SQS | `TestSameOperationOverHTTPAndSQSHasOneEffect` (e `...AcrossInstances`) |

Todos os cenários terminam conferindo o saldo armazenado contra créditos menos débitos do ledger.

## Pré-requisitos

| Ferramenta | Versão | Uso |
| --- | --- | --- |
| Docker com Compose v2 | Compose v2 | PostgreSQL, Keycloak, LocalStack, migrations e a aplicação |
| Go | 1.27 (declarado em `go.mod` e no `Dockerfile`) | Testes e execução fora do Docker |
| `curl` | qualquer | Exemplos de chamada |

Portas usadas no host: `8080` (aplicação), `8081` (Keycloak), `4566` (LocalStack), `5432` (PostgreSQL) e, com três réplicas, `8082` e `8083`. Todas podem ser trocadas no `.env` (veja `.env.example`).

## Subir tudo

```sh
docker compose up --build
```

O Compose sobe, nesta ordem:

1. **PostgreSQL 17**.
2. **migrate** (`golang-migrate`): aplica as migrations e termina.
3. **Keycloak 26.4**: importa o realm `wagering` com clients e papéis ([`deploy/keycloak/wagering-realm.json`](deploy/keycloak/wagering-realm.json)).
4. **LocalStack 4.9** (só SQS): cria as filas ([`deploy/localstack/init-queues.sh`](deploy/localstack/init-queues.sh)).
5. **app**: só depois que os quatro acima estão prontos.

A aplicação está pronta quando `curl http://localhost:8080/health/ready` responde `200`. O primeiro `up` leva de 1 a 2 minutos, por causa do Keycloak.

Para três instâncias independentes, cada uma com suas conexões e memória:

```sh
docker compose --profile multi up -d --build --wait
```

Parar: `docker compose --profile multi down`. Apagar também o banco: `docker compose --profile multi down -v`.

## Variáveis de ambiente

O Compose já define tudo; o `.env` só é necessário para mudar portas ou prazos. Para rodar fora do Docker (`go run ./cmd/app`), copie `.env.example` para `.env` e exporte as variáveis.

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Endereço do servidor HTTP |
| `DATABASE_URL` | obrigatória | URL do PostgreSQL |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `20s` | Prazo total de start e de stop |
| `AUTH_ISSUER` | obrigatória | `iss` esperado nos tokens |
| `AUTH_JWKS_URL` | obrigatória | De onde buscar as chaves públicas do Keycloak |
| `AUTH_AUDIENCE` | `wagering-service` | `aud` esperado nos tokens |
| `AWS_REGION` | `us-east-1` | Região do SQS |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | — | Credenciais (no LocalStack, qualquer valor) |
| `SQS_ENDPOINT` | vazio (AWS real) | Endpoint do LocalStack |
| `WAGER_QUEUE_URL` | obrigatória | Fila de entrada `wager-transactions.fifo` |
| `WAGER_DLQ_URL` | obrigatória | DLQ `wager-transactions-dlq.fifo` |
| `WAGER_QUEUE_PROVIDERS` | obrigatória | Provedores aceitos pela fila, separados por vírgula |
| `WAGER_VISIBILITY_TIMEOUT` | `30s` | Por quanto tempo uma mensagem recebida fica invisível |
| `OUTBOX_QUEUE_URL` | obrigatória | Fila de saída `wager-events.fifo` |
| `OUTBOX_POLL_INTERVAL` | `500ms` | Intervalo do publicador da outbox |
| `OUTBOX_LEASE` | `30s` | Reserva de um evento por uma instância |
| `REFERENCE_POLL_INTERVAL` | `1s` | Intervalo do worker de referências pendentes |
| `REFERENCE_RETRY_BASE_DELAY` | `1s` | Primeira espera do backoff |
| `REFERENCE_RETRY_MAX_DELAY` | `1m` | Teto do backoff |
| `REFERENCE_MAX_ATTEMPTS` | `10` | Tentativas até rejeitar com `REFERENCE_NOT_FOUND` |
| `REFERENCE_LEASE` | `30s` | Reserva de uma pendência por uma instância |
| `FAULT_INJECTION` | vazio | Só para testes: `exit-after-commit` ou `exit-after-publish` |

Configuração inválida ou ausente impede a subida, com todos os problemas listados de uma vez.

## Filas

O script [`deploy/localstack/init-queues.sh`](deploy/localstack/init-queues.sh) roda dentro do LocalStack quando ele fica pronto, e o healthcheck do Compose só fica verde depois que as filas existem.

| Fila | Papel |
| --- | --- |
| `wager-transactions.fifo` | Entrada de operações. Redrive para a DLQ após 5 recebimentos |
| `wager-transactions-dlq.fifo` | Mensagens inválidas ou que esgotaram as tentativas (retenção de 14 dias) |
| `wager-events.fifo` | Eventos publicados pela outbox |

O LocalStack não guarda filas entre reinícios: ao recriar o container, o script roda de novo. Para conferir as filas:

```sh
docker compose exec localstack awslocal sqs list-queues
```

## Migrations

Ficam em [`migrations/`](migrations), com `.up.sql` e `.down.sql`. O `docker compose up` aplica todas antes de subir a aplicação.

```sh
URL='postgres://wagering:wagering@postgres:5432/wagering?sslmode=disable'

docker compose run --rm migrate                                          # aplica todas
docker compose run --rm migrate -path=/migrations -database="$URL" version
docker compose run --rm migrate -path=/migrations -database="$URL" down 1     # reverte a última
docker compose run --rm migrate -path=/migrations -database="$URL" down -all  # reverte todas
```

No Git Bash (Windows), prefixe com `MSYS_NO_PATHCONV=1`; sem isso, `/migrations` é convertido para um caminho do Windows.

## Rodar a aplicação fora do Docker

```sh
docker compose up -d --wait postgres migrate keycloak localstack
cp .env.example .env
set -a; . ./.env; set +a
go run ./cmd/app
```

## Autenticação

O Keycloak é provisionado automaticamente com estes clients (fluxo `client_credentials`; os segredos só valem localmente):

| Client | Secret | Papel | Pode |
| --- | --- | --- | --- |
| `provider-a` | `provider-a-secret` | `provider` (`provider_id=provider-a`) | Enviar operações e consultar as próprias |
| `provider-b` | `provider-b-secret` | `provider` (`provider_id=provider-b`) | Idem, para `provider-b` |
| `wallet-service` | `wallet-service-secret` | `internal` | Carteiras, extrato, reconciliação e qualquer transação |
| `provider-a-short-lived` | `provider-a-short-lived-secret` | `provider` | Igual a `provider-a`, com token de 2s (usado no teste de expiração) |

Obter um token:

```sh
token() {
  curl -s -X POST http://localhost:8081/realms/wagering/protocol/openid-connect/token \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$1-secret" |
    sed -E 's/.*"access_token":"([^"]+)".*/\1/'
}
INTERNAL=$(token wallet-service)
PROVIDER_A=$(token provider-a)
```

Tokens valem 5 minutos. `/health/*` e `/metrics` são públicos; todo o resto exige `Authorization: Bearer <token>`.

## Exemplos de chamadas

Abrir uma carteira (serviço interno):

```sh
curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -d '{"playerId":"player-123","initialBalance":{"amount":"1000.00","currency":"BRL"}}'
# 201 {"id":"<walletId>","playerId":"player-123","balance":{"amount":"1000.00","currency":"BRL"},"version":1}
WALLET=<walletId da resposta>
```

Apostar (provedor):

```sh
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:round-987:bet-1' \
  -d '{"providerId":"provider-a","externalTransactionId":"bet-1","playerId":"player-123",
       "walletId":"'"$WALLET"'","roundId":"round-987","gameId":"slot-1","kind":"BET",
       "money":{"amount":"25.00","currency":"BRL"}}'
# 200 {"transactionId":"...","status":"PROCESSED","balance":{"amount":"975.00","currency":"BRL"},"idempotentReplay":false}
```

Repetir a mesma chamada devolve o resultado gravado com `"idempotentReplay": true`. Mudar o corpo com a mesma chave dá `409`.

Devolver a aposta (`REFUND` referencia a `BET` pelo id externo):

```sh
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:round-987:refund-1' \
  -d '{"providerId":"provider-a","externalTransactionId":"refund-1","playerId":"player-123",
       "walletId":"'"$WALLET"'","roundId":"round-987","gameId":"slot-1","kind":"REFUND",
       "money":{"amount":"25.00","currency":"BRL"},"referenceExternalTransactionId":"bet-1"}'
```

Consultas:

```sh
curl -s -H "Authorization: Bearer $INTERNAL" http://localhost:8080/wallets/$WALLET
curl -s -H "Authorization: Bearer $INTERNAL" "http://localhost:8080/wallets/$WALLET/ledger?limit=50"
curl -s -H "Authorization: Bearer $PROVIDER_A" http://localhost:8080/providers/provider-a/wagering/transactions/bet-1
curl -s -X POST -H "Authorization: Bearer $INTERNAL" http://localhost:8080/wallets/$WALLET/reconciliation
curl -s http://localhost:8080/health/ready
curl -s http://localhost:8080/metrics
```

Enviar a mesma operação pela fila (o provedor usa as credenciais dele no SQS; no LocalStack, qualquer uma):

```sh
docker compose exec localstack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET" --message-deduplication-id msg-bet-2 \
  --message-body '{"messageId":"msg-bet-2","type":"WagerTransactionRequested","occurredAt":"2026-10-14T12:00:00Z",
    "data":{"providerId":"provider-a","externalTransactionId":"bet-2","idempotencyKey":"provider-a:round-987:bet-2",
    "playerId":"player-123","walletId":"'"$WALLET"'","roundId":"round-987","gameId":"slot-1","kind":"BET",
    "money":{"amount":"10.00","currency":"BRL"}}}'
```

Ler os eventos publicados:

```sh
docker compose exec localstack awslocal sqs receive-message \
  --queue-url http://localhost:4566/000000000000/wager-events.fifo --max-number-of-messages 10
```

## Testes

### Comandos principais

```sh
go vet ./...
go test ./...                    # unitários; os que precisam de infraestrutura são pulados
go test -tags=integration ./...  # tudo, contra o Compose; nada é pulado
```

### `go test -race`

O detector de race precisa de cgo. No Linux e no macOS com um compilador C instalado:

```sh
go test -race ./...
go test -race -tags=integration ./...
```

No Windows, rode dentro de um container, na rede do Compose:

```sh
docker run --rm --network junglegaming_default \
  -e TEST_DATABASE_URL='postgres://wagering:wagering@postgres:5432/wagering?sslmode=disable' \
  -e TEST_SQS_ENDPOINT=http://localstack:4566 -e TEST_KEYCLOAK_URL=http://keycloak:8080 \
  -e TEST_APP_URL=http://app:8080 -e TEST_APP_URLS=http://app:8080,http://app-2:8080,http://app-3:8080 \
  -e TEST_WAGER_QUEUE_URL=http://localstack:4566/000000000000/wager-transactions.fifo \
  -v "$PWD:/src" -v wagering-gomod:/go/pkg/mod -w /src golang:1.27 \
  go test -race -tags=integration ./...
```

A rede se chama `<pasta>_default`; troque `junglegaming` se o checkout estiver em outra pasta. No Git Bash, prefixe com `MSYS_NO_PATHCONV=1`.

### Preparar as dependências dos testes

```sh
docker compose --profile multi up -d --build --wait
```

Isso sobe tudo que os testes usam: PostgreSQL, Keycloak, LocalStack e as três réplicas. Os testes criam seus próprios schemas, filas e carteiras, então podem rodar com dados de execuções anteriores no banco.

| Variável | Padrão com `-tags=integration` | Usada por |
| --- | --- | --- |
| `TEST_DATABASE_URL` | `postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable` | Testes com PostgreSQL |
| `TEST_SQS_ENDPOINT` | `http://localhost:4566` | Testes com SQS |
| `TEST_KEYCLOAK_URL` | `http://localhost:8081` | Tokens reais |
| `TEST_APP_URL` | `http://localhost:8080` | `test/integration` |
| `TEST_APP_URLS` | `http://localhost:8080,http://localhost:8082,http://localhost:8083` | `test/multiinstance` |
| `TEST_WAGER_QUEUE_URL` | fila de entrada do Compose | `test/integration`, `test/multiinstance` |

Sem a tag, um teste cuja variável não está definida é pulado. Com a tag, o padrão do Compose é usado, e um container fora do ar faz o teste falhar.

### Suítes

| Comando | O que roda |
| --- | --- |
| `go test ./internal/...` | Unitários e de componente (PostgreSQL e LocalStack reais, quando configurados) |
| `go test -tags=integration ./test/integration` | Serviço do Compose por HTTP e SQS, com tokens reais: autenticação, autorização e cenários de concorrência |
| `go test -tags=integration ./test/multiinstance` | Os mesmos cenários contra três réplicas; falha se alguma réplica não participou |
| `go test -tags=integration ./test/recovery` | Simulação de falhas com processos reais (cerca de 1 minuto) |

### Simulação de falhas

`test/recovery` compila `cmd/app` e sobe o binário como processo, com schema e filas próprios e o Keycloak do Compose. Os processos morrem em pontos escolhidos:

| Teste | Falha simulada |
| --- | --- |
| `TestConsumerKilledAfterCommitBeforeDeletingTheMessage` | `FAULT_INJECTION=exit-after-commit`: o processo sai depois do commit e antes de apagar a mensagem |
| `TestPublisherKilledAfterSendingRepublishesTheSameEvent` | `FAULT_INJECTION=exit-after-publish`: sai depois de enviar o evento e antes de marcá-lo |
| `TestCompetingPublishersLoseNothingAndKeepEachAggregateInOrder` | Dois processos publicando da mesma outbox |
| `TestReversalArrivingBeforeItsReference` | `REFUND`/`ROLLBACK` antes da referência |
| `TestRestartKeepsIdempotencyPendingWorkAndConsistency` | `kill -9` no meio de 100 mensagens, com um `REFUND` pendente |

Para provocar a falha manualmente:

```sh
docker compose stop app
set -a; . ./.env; set +a
FAULT_INJECTION=exit-after-commit go run ./cmd/app   # sai com código 3 na primeira mensagem processada
```

## Estrutura

```
cmd/app/                      ponto de entrada
internal/domain/              regras puras: Money, Wallet, WagerTransaction, Ledger, eventos
internal/application/         casos de uso e portas (interfaces)
internal/infra/postgres/      repositórios e transação SQL (pgx)
internal/infra/httpapi/       rotas, handlers, middlewares, health checks
internal/infra/sqs/           consumidor, publisher, sondas
internal/infra/auth/          validação de JWT
internal/infra/worker/        workers de referências e outbox
internal/infra/lifecycle/     parada em duas fases dos workers
internal/infra/observability/ logs, métricas, decoradores
internal/infra/faults/        injeção de falha para os testes de recuperação
internal/app/                 composição Fx
migrations/                   SQL versionado
deploy/                       realm do Keycloak, script das filas
test/                         suítes caixa-preta (build tag integration)
specs/                        especificação de cada etapa
```

## Problemas comuns

| Sintoma | Causa e solução |
| --- | --- |
| `port is already allocated` | Outra coisa usa 8080, 8081, 8082, 8083, 4566 ou 5432. Copie `.env.example` para `.env` e troque as portas (`APP_PORT`, `KEYCLOAK_PORT`...); com `-tags=integration`, ajuste também as `TEST_*`. |
| `docker compose up --wait` demora | O Keycloak leva de 1 a 2 minutos na primeira subida. |
| Testes de integração falham com `connection refused` | O Compose não está de pé: `docker compose --profile multi up -d --build --wait`. |
| `gofmt -l .` lista arquivos no Windows | Checkout feito antes do `.gitattributes`; clone de novo (o repositório força LF). |
| `migrate` não acha `/migrations` no Git Bash | Prefixe o comando com `MSYS_NO_PATHCONV=1`. |
| `-race` reclama de cgo no Windows | Use o comando com `docker run` da seção [`go test -race`](#go-test--race). |
