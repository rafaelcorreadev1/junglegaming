# 10 — Outbox e eventos

**Desafio:** §5 (garantia 4), §6.5, §11 · **Depende de:** 07 · **Status:** concluída

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

## Como foi implementado

### Reserva que preserva a ordem por carteira

A fila FIFO só mantém a ordem de um grupo se os envios saírem em ordem. Com vários publishers, reservar eventos soltos permitiria que o evento 2 de uma carteira saísse antes do 1. Por isso a reserva é por **agregado**:

1. Trava (`FOR UPDATE SKIP LOCKED`) só o evento **mais antigo não publicado** de cada carteira, desde que esteja vencido e sem reserva. `NOT EXISTS` garante que é mesmo o mais antigo.
2. Reserva (`locked_until = agora + 30s`) todos os eventos não publicados dessas carteiras, até `PerAggregate` por carteira.

Outro publisher pula a cabeça travada e não consegue pegar eventos posteriores da mesma carteira, porque o `NOT EXISTS` ainda vê a cabeça não publicada. Depois do comando, a reserva gravada faz o mesmo papel. Tudo é um único `UPDATE ... RETURNING` com CTEs; migration `000006` cria o índice `(aggregate_id, sequence) WHERE published_at IS NULL`.

### Publicação

| Situação | O que acontece |
| --- | --- |
| Envio aceito | `published_at = agora`, reserva liberada |
| Envio falhou | `attempts + 1`, `last_error`, `next_attempt_at = agora + backoff`; os eventos seguintes da mesma carteira são liberados sem tentar; outras carteiras seguem |
| Enviado, mas a marcação falhou (ou o processo morreu) | A reserva vence e o evento é enviado de novo com o mesmo `eventId` |
| Processo morreu depois de reservar | A reserva vence e outra instância assume |

- Backoff: 1s, 2s, 4s... até 1min. **Sem limite de tentativas**: evento confirmado não pode ser descartado; o erro fica em `last_error`.
- A falha é registrada com um contexto desligado do cancelamento, para que um desligamento no meio não deixe a reserva presa por 30s.
- O worker repete a rodada enquanto o lote volta cheio (todas as carteiras permitidas tinham eventos) e espera `OUTBOX_POLL_INTERVAL` (padrão 500ms) caso contrário.

### Mensagem no SQS (`internal/infra/sqs/publisher.go`)

| Campo | Valor |
| --- | --- |
| Corpo | Envelope JSON do evento, como gravado na outbox |
| `MessageGroupId` | `aggregateId` (carteira) |
| `MessageDeduplicationId` | `eventId` |
| Atributos | `eventType`, `eventId` |

A deduplicação do SQS cobre só 5 minutos; reenvio mais tardio chega duplicado e o consumidor deduplica por `eventId`. A entrega é **at-least-once**.

### Worker genérico

O laço com `time.Ticker` da spec 08 virou `worker.Poller`, usado pelo resolvedor de referências e pelo publicador da outbox. Cada um roda em sua goroutine e para com o ciclo de vida do Fx.

### Ambiente

- LocalStack 4.9 no Compose (só SQS), porta `4566`. `deploy/localstack/init-queues.sh` cria `wager-events.fifo`; o healthcheck só fica verde depois que a fila existe.
- Variáveis: `OUTBOX_QUEUE_URL` (obrigatória), `SQS_ENDPOINT`, `AWS_REGION`, `OUTBOX_POLL_INTERVAL`; credenciais pela cadeia padrão da AWS (`AWS_ACCESS_KEY_ID`...).
- O LocalStack não guarda as filas entre reinícios.

## Entregáveis

- `internal/domain/events.go` (já entregue na spec 06, junto com `OutboxRepository.Append`)
- `internal/application/publish_outbox.go` + `internal/infra/worker/poller.go` e `module.go`
- `internal/infra/postgres/outbox_repository.go`: `ClaimDue`, `MarkPublished`, `Reschedule`, `Release`
- `internal/infra/sqs/publisher.go`
- `deploy/localstack/init-queues.sh` com a fila de saída
- `migrations/000006_add_outbox_aggregate_index`

## Critérios de aceite

- [x] Rollback da transação de negócio → nenhum registro na outbox.
- [x] Processo morto entre commit e publicação → outra instância publica.
- [x] Processo morto entre publicação e marcação → republica com o mesmo `eventId`.
- [x] Dois publishers na mesma outbox → cada evento reservado por um só de cada vez.
- [x] Quatro publishers concorrentes → cada carteira publicada na ordem de criação.

## Limites conhecidos

- Uma carteira com evento falhando fica parada até ele sair (é o preço da ordem); as outras seguem.
- Mensagem com `MessageGroupId` por carteira: uma carteira muito ativa é publicada em série.
- A disputa entre publishers foi testada no mesmo processo, com conexões separadas; com três processos, na spec 15.

## Para estudar

Padrão transactional outbox, at-least-once vs exactly-once, por que o consumidor precisa ser idempotente.
