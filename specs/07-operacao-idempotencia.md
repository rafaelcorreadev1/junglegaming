# 07 — Operação: BET / WIN / LOSS e idempotência

**Desafio:** §5 (garantias 2, 6, 7), §7, §8, §9 (envio de operação) · **Depende de:** 06

## Objetivo

Um único caso de uso `ProcessWagerTransaction`, usado por HTTP e por SQS, que aplica a operação exatamente uma vez.

## Endpoints

| Método | Rota |
| --- | --- |
| `POST` | `/wagering/transactions` (header `Idempotency-Key` obrigatório) |
| `GET` | `/wagering/transactions/:transactionId` |
| `GET` | `/providers/:providerId/wagering/transactions/:externalTransactionId` |

Resposta do `POST`: `transactionId`, `status`, `balance`, `idempotentReplay`.

## Requisitos — idempotência

- Persistente no banco (sobrevive a reinício de todos os processos).
- Hash determinístico dos **campos de negócio** em JSON canônico (chaves ordenadas). Fora do hash: a chave de idempotência e metadados de transporte.
- Mesmo hash para HTTP e SQS com o mesmo conteúdo.
- O servidor **não** troca a chave recebida por outra calculada.

| Situação | Resultado |
| --- | --- |
| Mesma chave + mesmo hash | Resultado persistido, `idempotentReplay: true` |
| Mesma chave + hash diferente | `409` |
| Mesmo `(providerId, externalTransactionId)` com outra chave | `409`, sem reaplicar |
| Replay de operação concluída | Saldo **observado no processamento original**, não o saldo atual |

## Decisões a documentar

- Algoritmo: SHA-256 do JSON canônico.
- Campos no hash: `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount` (normalizado), `money.currency`, `referenceExternalTransactionId`.

## Requisitos — processamento

- Sequência dentro de **uma** transação SQL: lock da carteira → checa duplicata → aplica regra do domínio → grava transação, saldo, ledger e eventos na outbox.
- Rejeição de negócio também é gravada (`REJECTED` + `failureCode`) e gera `WagerTransactionRejected`.
- `BET`, `WIN`, `LOSS` são concluídas de forma síncrona (sem `PENDING` intermediário).
- `OPENING` recebido de fora → entrada inválida.
- `LOSS`: sem ledger, sem mudança de versão, gera só `WagerTransactionProcessed`.

## Contrato de resposta

| Situação | HTTP | `status` |
| --- | --- | --- |
| Processada | `200` | `PROCESSED` |
| Rejeição de negócio | `422` | `REJECTED` + `failureCode` |
| Aguardando referência | `202` | `PENDING_REFERENCE` |
| Chave/conteúdo em conflito | `409` | — |
| Entrada inválida | `400` | — |
| Banco indisponível | `503` | — |

## Critérios de aceite

- [ ] Mesma aposta 50 vezes em paralelo → um único débito.
- [ ] Carteira com `100.00`, duas apostas distintas de `80.00` ao mesmo tempo → uma `PROCESSED`, uma `REJECTED` (`INSUFFICIENT_FUNDS`), saldo `20.00`, um débito no ledger.
- [ ] Reenviar qualquer uma das duas não muda o resultado.
- [ ] Replay depois de outras movimentações devolve o saldo original da operação.

## Para estudar

`crypto/sha256`, ordenação de chaves em `encoding/json`, por que o lock vem antes da checagem de duplicata.
