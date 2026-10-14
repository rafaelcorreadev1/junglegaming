# 13 — Observabilidade

**Desafio:** §9 (health checks), §12 · **Depende de:** 01 · **Status:** concluída

## Objetivo

Conseguir diagnosticar uma operação pelos logs e pelas métricas.

## Logs

- Formato JSON (`log/slog`).
- Campos, quando disponíveis: `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`.
- **Não** registrar: tokens, credenciais, dados sensíveis, payload financeiro completo.
- `correlationId`: vem do header `X-Correlation-Id` (ou é gerado) e segue pelo `context` até os eventos.

## Métricas (`GET /metrics`, Prometheus)

| Métrica | Tipo |
| --- | --- |
| Transações por `kind` e `status` | Contador |
| Duplicatas / replays detectados | Contador |
| Retries (por componente) | Contador |
| Mensagens enviadas à DLQ | Contador |
| Conflitos de concorrência | Contador |
| Atraso da outbox (idade do pendente mais antigo) | Gauge |
| Latência de processamento | Histograma |
| Divergências de reconciliação | Contador |

## Health checks (públicos)

| Rota | Verifica |
| --- | --- |
| `GET /health/live` | Processo vivo |
| `GET /health/ready` | PostgreSQL e SQS respondendo |

## Como foi implementado

### Campos de log no `context` (`internal/infra/observability/logger.go`)

- `WithFields(ctx, "correlationId", id)` cria no contexto um conjunto de campos; `AddFields(ctx, ...)` acrescenta a ele.
- `ContextHandler` envolve o handler JSON e junta esses campos a **todo** log escrito com aquele contexto. Quem loga passa só o que é específico da mensagem.
- O conjunto é compartilhado por ponteiro: o handler HTTP acrescenta `transactionId` lá dentro, e o access log, escrito depois pelo middleware de fora, já o inclui. `WithFields` num contexto filho copia os campos, e o que o filho acrescenta não volta para o pai.

| Onde | Campos |
| --- | --- |
| Middleware de correlation ID | `correlationId` |
| Autenticação | `clientId` |
| Rotas de carteira | `walletId` |
| `POST /wagering/transactions` | `providerId`, `walletId`, depois `transactionId` |
| Consumidor SQS | `sqsMessageId`, `messageId`, `providerId`, `walletId`, `correlationId`, `transactionId` |

O `correlationId` da requisição já ia para o envelope dos eventos (spec 06/07); o teste confere que os dois eventos gravados levam o valor enviado em `X-Correlation-Id`.

### Access log

Uma linha por requisição: `method`, `route` (o padrão do chi, como `/wallets/{walletId}`), `status`, `durationMs` e os campos do contexto. `/health/*` e `/metrics` saem em `DEBUG` para não poluir. Nenhum header e nenhum corpo são registrados; o teste procura o token, `Bearer`, `Authorization`, o `gameId` e o valor da aposta nos logs e não encontra. Para provar que o teste pega o problema, registrei os headers de propósito e ele falhou; voltei o código.

### Métricas (`internal/infra/observability/metrics.go`)

| Métrica | Rótulos | Onde é medida |
| --- | --- | --- |
| `wagering_transactions_total` | `channel` (`http`, `sqs`, `reference_worker`), `kind`, `status` | Handler HTTP, consumidor, worker de referências (depois do commit) |
| `wagering_duplicates_total` | `channel`, `kind` (`idempotent_replay`, `inbox`) | Replays e mensagens já na inbox; não contam em `transactions_total` |
| `wagering_retries_total` | `component` (`sqs_consumer`, `reference_resolver`, `outbox_publisher`) | Mensagem devolvida com backoff, pendência reagendada, publicação que falhou |
| `wagering_dlq_messages_total` | `reason` (`invalid_message`, `permanent_error`) | Envio explícito do consumidor para a DLQ |
| `wagering_concurrency_conflicts_total` | `reason` (`duplicate_race`, `concurrent_update`, `deadlock_or_serialization`) | Decorador do `TxManager` |
| `wagering_outbox_oldest_pending_age_seconds` | — | Consulta a cada scrape |
| `wagering_processing_duration_seconds` | `channel` | Histograma por operação, replays incluídos |
| `wagering_http_requests_total` e `wagering_http_request_duration_seconds` | `method`, `route`, `status` | Middleware HTTP |
| `wagering_reconciliations_total` | `result` | Spec 12 |

- **Rótulos com cardinalidade baixa:** rota é o padrão, nunca o caminho com ids; o teste confere que nenhum id de carteira aparece em `/metrics`.
- **Decoradores do Fx (`fx.Decorate`):** `InstrumentTxManager` olha o erro de cada transação e conta os conflitos; `InstrumentPublisher` conta as publicações que falharam. A aplicação não sabe que é medida. Ficam na raiz do `app.Options`, porque um `fx.Decorate` dentro de um módulo só vale para aquele módulo.
- **Novo erro `ErrTransactionConflict`:** deadlock e falha de serialização agora levam também esse erro, além de `ErrUnavailable` (Go aceita vários `%w` num `fmt.Errorf`). Assim dá para separar "o banco caiu" de "perdi uma disputa".
- **Atraso da outbox:** `min(occurred_at)` dos não publicados, calculado a cada scrape com limite de 2s. Sem pendentes, 0. Se a consulta falha, o valor é `NaN`, que o Prometheus guarda como "sem valor" em vez de um 0 falso.
- O worker de referências passou a devolver `ResolveResult` (reservadas, decididas, reagendadas) em vez de um número, para o worker poder medir.

### Health checks (`internal/infra/httpapi/health.go`)

- `ReadinessProbe` (`Name`, `Ping`). Postgres faz `Ping` no pool; SQS consulta os atributos das três filas (não basta o LocalStack responder, as filas precisam existir).
- As sondas são entregues por um **value group** do Fx (`group:"readiness"`), e o handler recebe a lista. Uma dependência nova é só mais um provider no grupo.
- Rodam em paralelo, com 2s no total. Qualquer uma fora → `503` com o estado de cada uma. `/health/live` não consulta nada.

## Entregáveis

- `internal/infra/observability/`: `logger.go` (campos no contexto), `metrics.go`, `instrument.go` (decoradores)
- `internal/infra/httpapi/middleware.go`: access log e métricas HTTP; `health.go` com sondas
- `internal/infra/postgres/probe.go`, `internal/infra/sqs/probe.go`

## Critérios de aceite

- [x] Uma requisição pode ser seguida do HTTP até o evento publicado pelo `correlationId`.
- [x] Nenhum log contém `Authorization` nem o corpo da operação.
- [x] Postgres parado → `/health/ready` responde `503`, `/health/live` responde `200`.

## Limites conhecidos

- Os logs do worker de referências e do publisher da outbox não trazem `correlationId`; ele está no evento e na transação, mas esses workers tratam lotes, não requisições.
- `wagering_dlq_messages_total` só conta o que o consumidor mandou para a DLQ. O que o SQS move pelo redrive é contado no próprio SQS (`ApproximateNumberOfMessages` da DLQ), não aqui.
- `/metrics` é público, como os health checks; numa implantação real ficaria numa porta interna.
- Sem tracing (OpenTelemetry) nem dashboards, que o desafio deixa como opcionais.

## Para estudar

`log/slog`, `slog.Handler`, tipos de métrica do Prometheus, `fx.Decorate`, value groups do Fx.
