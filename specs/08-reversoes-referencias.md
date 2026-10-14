# 08 — Reversões e referências pendentes

**Desafio:** §3 (reversão antes da referência), §7 · **Depende de:** 07 · **Status:** concluída

## Objetivo

`REFUND` e `ROLLBACK` corretos, inclusive quando chegam antes da operação que referenciam.

## Requisitos — reversão

- `referenceExternalTransactionId` obrigatório; resolvido por `(providerId, referenceExternalTransactionId)`.
- Operação e referência coincidem em provedor, jogador, carteira, moeda e rodada.
- Valor igual ao da referência (sem reversão parcial).
- `REFUND`: só sobre `BET` processada → crédito.
- `ROLLBACK`: sobre `BET` (crédito), `WIN` (débito) ou `REFUND` (débito) processados.
- Uma referência não recebe duas reversões bem-sucedidas do mesmo tipo.
- Reversão que debitaria mais que o saldo → `REJECTED` com `REVERSAL_INSUFFICIENT_FUNDS` (código diferente do de aposta sem saldo).

## Decisão: REFUND + ROLLBACK sobre a mesma BET

Uma transação aceita **uma única** reversão processada, de qualquer tipo. Isso cobre a regra do desafio (sem duas reversões do mesmo tipo) e impede devolver duas vezes o mesmo débito.

| Sequência | Resultado |
| --- | --- |
| `REFUND(BET)` depois `ROLLBACK(BET)` | `ROLLBACK` rejeitado com `ALREADY_REVERSED` |
| `ROLLBACK(BET)` depois `REFUND(BET)` | `REFUND` rejeitado com `ALREADY_REVERSED` |
| `REFUND(BET)` depois `ROLLBACK(REFUND)` | Aceito: debita de novo o valor devolvido. A `BET` volta a valer, mas não aceita nova reversão |
| `ROLLBACK(REFUND)` duas vezes | O segundo é rejeitado com `ALREADY_REVERSED` |
| `ROLLBACK(ROLLBACK)` | Rejeitado com `REFERENCE_KIND_NOT_ALLOWED` |

O domínio (spec 03) aplica essa regra a partir do campo `Reference.Reversed`; o índice único está na spec 04 e a consulta que preenche o campo, na spec 07. O caso de uso da spec 07 já processa reversões cuja referência existe e grava `PENDING_REFERENCE` quando não existe. **Esta spec entrega o que falta:** o worker que retoma as pendentes, o backoff, a expiração e os testes de reversão concorrente.

## Requisitos — referência indisponível

- Referência não encontrada → grava `PENDING_REFERENCE` + evento `WagerTransactionPendingReference`; HTTP responde `202`.
- Worker tenta de novo com **backoff exponencial**; o estado da espera (tentativas, próxima tentativa) fica no banco e sobrevive a reinício.
- Várias instâncias do worker disputam sem processar a mesma pendência duas vezes (`FOR UPDATE SKIP LOCKED`).
- Limite de tentativas esgotado → `REJECTED` com `REFERENCE_NOT_FOUND` + `WagerTransactionRejected`.

| Estado da referência | Comportamento |
| --- | --- |
| Não existe | Aguarda (`PENDING_REFERENCE`) |
| Existe em `PENDING` / `PENDING_REFERENCE` | Continua aguardando |
| `PROCESSED` | Aplica a reversão |
| `REJECTED` / `FAILED` | `REJECTED` com `REFERENCE_NOT_PROCESSED` |

## Como foi implementado

### Estado da espera (migration `000005_add_reference_retry`)

| Coluna em `wager_transactions` | Uso |
| --- | --- |
| `reference_attempts` | Quantas consultas não acharam uma referência utilizável (a primeira é a da chegada) |
| `next_reference_attempt_at` | Quando consultar de novo. Preenchida **se e somente se** o status é `PENDING_REFERENCE` (`CHECK`) |
| `correlation_id` | O correlation ID da requisição original, para os eventos emitidos depois pelo worker |

Dois índices parciais (`WHERE status = 'PENDING_REFERENCE'`): um por `next_reference_attempt_at` (fila do worker) e um por `(provider_id, reference_external_transaction_id)` (despertar).

### Backoff e expiração

`espera(n) = min(base × 2^(n-1), máximo)`. Com os padrões (base 1s, máximo 1min, 10 tentativas): 1, 2, 4, 8, 16, 32, 60, 60, 60 segundos — cerca de 4 minutos até expirar.

| Variável | Padrão |
| --- | --- |
| `REFERENCE_POLL_INTERVAL` | `1s` |
| `REFERENCE_RETRY_BASE_DELAY` | `1s` |
| `REFERENCE_RETRY_MAX_DELAY` | `1m` |
| `REFERENCE_MAX_ATTEMPTS` | `10` |

Ao expirar: `REFERENCE_NOT_FOUND` se a referência nunca chegou; `REFERENCE_NOT_PROCESSED` se ela existe mas também ficou pendente.

### Disputa entre instâncias: reserva com prazo

O worker roda em dois passos:

1. **Reservar** (um único `UPDATE`): escolhe as vencidas com `FOR UPDATE SKIP LOCKED` e empurra `next_reference_attempt_at` para `agora + 30s`. O lock dura só esse comando; o que protege a pendência depois é a data empurrada.
2. **Resolver** (uma transação por pendência): trava a carteira, relê a transação, e só continua se ela ainda está `PENDING_REFERENCE`.

Se a instância morre entre os dois passos, a reserva vence em 30s e outra instância assume. Se duas instâncias chegam a pegar a mesma pendência, o lock da carteira as põe em fila e a segunda encontra a transação já resolvida.

A ordem de locks é sempre **carteira → transações**, a mesma do caminho HTTP. Segurar o lock da linha da transação e depois pedir o da carteira causaria deadlock com uma requisição que tem a carteira e quer acordar essa linha.

### Despertar

Quando uma transação chega a um estado final, a mesma transação SQL antecipa para "agora" as pendências que a referenciam. O `REFUND` não espera o backoff: é concluído no próximo ciclo do worker (até `REFERENCE_POLL_INTERVAL`). O backoff continua valendo para a referência que não chegou.

## Entregáveis

- `migrations/000005_add_reference_retry.{up,down}.sql`
- `internal/domain/wager_transaction.go`: `ScheduleReferenceAttempt`, `ReferenceAttempts`, `NextReferenceAttemptAt`, `CorrelationID`
- `internal/application/resolve_pending_references.go`: `ReferenceRetryPolicy` e o caso de uso `ResolvePendingReferences`
- `internal/infra/postgres`: `ClaimPendingReferences` e `WakePendingReferences`
- `internal/infra/worker`: laço com `time.Ticker`, iniciado e parado pelo ciclo de vida do Fx (na spec 10 virou o `Poller` genérico de `poller.go`, compartilhado com o publicador da outbox)

## Critérios de aceite

- [x] `REFUND` chega antes da `BET` → fica pendente; a `BET` chega → o worker conclui o `REFUND`.
- [x] Referência que nunca chega → `REJECTED` / `REFERENCE_NOT_FOUND` após o limite.
- [x] Dois `REFUND` distintos da mesma `BET` em paralelo → um `PROCESSED`, um `REJECTED`.
- [x] Reiniciar a aplicação com pendências não perde nenhuma.
- [x] Quatro instâncias do caso de uso disputando 40 pendências: cada uma resolvida exatamente uma vez.

## Limites conhecidos

- A disputa entre instâncias foi testada com vários resolvedores no mesmo processo e conexões separadas; com três processos, na spec 15.
- Depois de expirada, a transação não é reaberta se a referência chegar tarde: a rejeição já foi informada ao provedor.

## Para estudar

`SKIP LOCKED` como fila no Postgres, backoff exponencial, `time.Ticker`, goroutine com `context` para cancelamento.
