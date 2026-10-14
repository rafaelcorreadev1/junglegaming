# 13 — Observabilidade

**Desafio:** §9 (health checks), §12 · **Depende de:** 01

## Objetivo

Conseguir diagnosticar uma operação pelos logs e pelas métricas.

## Logs

- Formato JSON (`log/slog`).
- Campos, quando disponíveis: `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`.
- **Não** registrar: tokens, credenciais, dados sensíveis, payload financeiro completo.
- `correlationId`: vem do header `X-Correlation-Id` (ou é gerado) e segue pelo `context` até os eventos.

## Métricas (`GET /metrics`, Prometheus)

O endpoint e o registro próprio já existem desde a spec 12 (`internal/infra/observability/metrics.go`).

| Métrica | Tipo |
| --- | --- |
| Transações por `kind` e `status` | Contador |
| Duplicatas / replays detectados | Contador |
| Retries (por componente) | Contador |
| Mensagens enviadas à DLQ | Contador |
| Conflitos de concorrência | Contador |
| Atraso da outbox (idade do pendente mais antigo) | Gauge |
| Latência de processamento | Histograma |
| Divergências de reconciliação | Contador (já entregue na spec 12: `wagering_reconciliations_total{result}`) |

## Health checks (públicos)

| Rota | Verifica |
| --- | --- |
| `GET /health/live` | Processo vivo |
| `GET /health/ready` | PostgreSQL e SQS respondendo |

## Entregáveis

- `internal/infra/observability/`: logger, métricas, middleware HTTP
- Handlers de health

## Critérios de aceite

- [ ] Uma requisição pode ser seguida do HTTP até o evento publicado pelo `correlationId`.
- [ ] Nenhum log contém `Authorization` nem o corpo da operação.
- [ ] Postgres parado → `/health/ready` responde `503`, `/health/live` responde `200`.

## Para estudar

`log/slog`, `slog.Handler`, tipos de métrica do Prometheus.
