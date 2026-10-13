# 10 — Outbox e eventos

**Desafio:** §5 (garantia 4), §6.5, §11 · **Depende de:** 07

> Eliminatório: publicar evento antes do commit.

## Objetivo

Nenhum evento sai antes do commit e nenhum evento confirmado no banco se perde.

## Como funciona

1. O caso de uso grava o evento na tabela `outbox_events` **na mesma transação** da mudança de saldo.
2. Um worker separado lê os pendentes, publica no SQS e marca como publicado.

Se o processo morrer entre 1 e 2, o evento continua no banco e outra instância publica.

## Requisitos — outbox

- Registro: id estável do evento, agregado, tipo, payload, ocorrência, tentativas, próximo envio, publicação.
- Payload é um snapshot imutável.
- Vários publishers em paralelo disputam registros sem publicar o mesmo ao mesmo tempo (`FOR UPDATE SKIP LOCKED` + reserva com prazo).
- Retry com backoff.
- Trabalho abandonado (instância morreu depois de reservar) é retomado por outra instância.
- Republicação mantém o mesmo `eventId` (o consumidor deduplica por ele).
- Destino provisionado: fila `wager-events.fifo`. `MessageGroupId` = `aggregateId`, `MessageDeduplicationId` = `eventId`.

## Eventos

| Evento | Gatilho |
| --- | --- |
| `WagerTransactionProcessed` | Operação concluída com sucesso (inclui `LOSS` e `OPENING`) |
| `WagerTransactionRejected` | Rejeição definitiva |
| `WalletBalanceChanged` | Saldo alterado |
| `WagerTransactionPendingReference` | Operação aguardando referência |

## Envelope

`eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` (opcional), `occurredAt`, `version`, `data`.

- Um tipo concreto por evento; `eventType` e `version` definidos pelo construtor.
- Timestamps UTC em RFC 3339; dinheiro em string decimal.
- `WalletBalanceChanged.data`: `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter`, `walletVersion`.

## Entregáveis

- `internal/domain/events.go`
- `internal/application/publish_outbox.go` + worker
- `internal/infra/sqs/publisher.go`
- Fila de saída no script do LocalStack

## Critérios de aceite

- [ ] Rollback da transação de negócio → nenhum registro na outbox.
- [ ] Processo morto entre commit e publicação → outra instância publica.
- [ ] Processo morto entre publicação e marcação → republica com o mesmo `eventId`.
- [ ] Dois publishers na mesma outbox → cada evento reservado por um só de cada vez.

## Para estudar

Padrão transactional outbox, at-least-once vs exactly-once, por que o consumidor precisa ser idempotente.
