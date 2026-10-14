# 08 — Reversões e referências pendentes

**Desafio:** §3 (reversão antes da referência), §7 · **Depende de:** 07

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
- Limite de tentativas/TTL esgotado → `REJECTED` com `REFERENCE_NOT_FOUND` + `WagerTransactionRejected`.

| Estado da referência | Comportamento |
| --- | --- |
| Não existe | Aguarda (`PENDING_REFERENCE`) |
| Existe em `PENDING` / `PENDING_REFERENCE` | Continua aguardando |
| `PROCESSED` | Aplica a reversão |
| `REJECTED` / `FAILED` | `REJECTED` com `REFERENCE_NOT_PROCESSED` |

## Entregáveis

- Regras de reversão em `internal/domain`
- `internal/application/resolve_pending_references.go` + worker em `internal/infra`
- Colunas de retry na transação (ou tabela própria) na migration

## Critérios de aceite

- [ ] `REFUND` chega antes da `BET` → fica pendente; a `BET` chega → o worker conclui o `REFUND`.
- [ ] Referência que nunca chega → `REJECTED` / `REFERENCE_NOT_FOUND` após o limite.
- [ ] Dois `REFUND` distintos da mesma `BET` em paralelo → um `PROCESSED`, um `REJECTED`.
- [ ] Reiniciar a aplicação com pendências não perde nenhuma.

## Para estudar

`SKIP LOCKED` como fila no Postgres, backoff exponencial, `time.Ticker`, goroutine com `context` para cancelamento.
