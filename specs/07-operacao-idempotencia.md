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

## Hash do payload

- **Algoritmo:** SHA-256 (hexadecimal) do JSON canônico dos campos de negócio.
- **JSON canônico:** um objeto com as chaves em ordem alfabética, sem espaços. O `encoding/json` do Go já escreve as chaves de um `map` ordenadas.
- **Campos:** `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e `referenceExternalTransactionId` (só quando informado).
- **Fora do hash:** chave de idempotência, correlation ID, `messageId` e qualquer metadado de transporte.
- **Normalização:** só o valor monetário, na forma de duas casas (`"25"`, `"25.0"` e `"25.00"` → `"25.00"`). Os demais campos são comparados byte a byte: sem `trim`, sem mudança de caixa.
- HTTP e SQS montam a mesma entrada do caso de uso, então geram o mesmo hash.

Exemplo: a aposta do enunciado gera `629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344`. Um teste fixa esse valor: mudar o algoritmo invalidaria os registros já gravados.

## Como o caso de uso processa

Dentro de **uma** transação SQL:

1. `SELECT ... FOR UPDATE` na carteira.
2. Procura duplicata pela chave: mesmo hash → replay; hash diferente → conflito. Depois pelo ID externo: se existe com outra chave → conflito.
3. Resolve a referência, se houver (transação referenciada e se já foi revertida).
4. `domain.ApplyWager` decide o resultado.
5. Grava a transação; se o saldo mudou, grava o saldo e o lançamento.
6. Grava os eventos na outbox.

**Por que o lock vem antes da checagem de duplicata:** duas requisições iguais chegam juntas. A segunda fica parada no lock e, quando passa, já enxerga a linha que a primeira gravou. Se a checagem viesse antes, as duas diriam "não existe".

**Quando o lock não cobre:** a mesma chave enviada com `walletId` diferentes trava carteiras diferentes. Aí quem decide é o `UNIQUE` do banco: o `INSERT` perdedor falha, a transação inteira é desfeita, e o caso de uso tenta de novo (até 3 vezes). Na nova tentativa a checagem encontra a linha e responde replay ou conflito.

## Decisões

- **Um só caso de uso para os cinco tipos.** A resolução de referência já está aqui, porque `WIN` pode referenciar uma `BET`. `REFUND` e `ROLLBACK` já funcionam quando a referência existe; a spec 08 entrega o worker das pendentes, o backoff e a expiração.
- **Carteira inexistente → `404`, nada gravado.** É entrada corrigível, não rejeição de negócio.
- **`PLAYER_MISMATCH` e `CURRENCY_MISMATCH` → `422`, gravados** como `REJECTED`. O saldo devolvido é o da carteira.
- **Replay devolve o mesmo código HTTP do original** (`200`, `422` ou `202`) com `idempotentReplay: true`.
- **Chave de idempotência:** obrigatória, até 255 caracteres, única por provedor. Dois provedores podem usar a mesma chave.
- **`balance` é omitido** quando nenhum saldo foi observado (operação aguardando referência).
- Endpoints ainda **sem autenticação** (spec 09): por enquanto qualquer chamador informa qualquer `providerId`.

## Contrato de resposta

| Situação | HTTP | Corpo |
| --- | --- | --- |
| Processada | `200` | `status: PROCESSED`, `balance` |
| Rejeição de negócio | `422` | `status: REJECTED`, `failureCode`, `balance` |
| Aguardando referência | `202` | `status: PENDING_REFERENCE` |
| Chave reutilizada com outro conteúdo | `409` | `error.code: IDEMPOTENCY_KEY_CONFLICT` |
| Mesma operação com outra chave | `409` | `error.code: EXTERNAL_TRANSACTION_CONFLICT` |
| Entrada inválida | `400` | `error.code: INVALID_REQUEST`, `field` |
| Carteira inexistente | `404` | `error.code: WALLET_NOT_FOUND` |
| Indisponibilidade transitória | `503` + `Retry-After` | `error.code: SERVICE_UNAVAILABLE` |

Consultas (`GET`) devolvem a transação completa: tipo, estado, `failureCode`, valor, saldo resultante, identificadores e horários.

## Entregáveis

- `internal/application/`: `process_wager_transaction.go`, `payload_hash.go`, `get_wager_transaction.go`
- `internal/infra/httpapi/wagering.go`
- `WagerTransactionRepository.HasProcessedReversal`

## Critérios de aceite

- [x] Mesma aposta 50 vezes em paralelo → um único débito.
- [x] Carteira com `100.00`, duas apostas distintas de `80.00` ao mesmo tempo → uma `PROCESSED`, uma `REJECTED` (`INSUFFICIENT_FUNDS`), saldo `20.00`, um débito no ledger.
- [x] Reenviar qualquer uma das duas não muda o resultado.
- [x] Replay depois de outras movimentações devolve o saldo original da operação.

## Status: concluída

- Testes pelo router real e PostgreSQL real, também com `-race` (os de concorrência repetidos 5 vezes).
- 50 envios simultâneos da mesma aposta: 50 respostas `200` com o mesmo `transactionId`, exatamente uma com `idempotentReplay: false`, um débito.
- Corrida entre carteiras diferentes simulada de forma determinística: a tentativa perdedora é desfeita por inteiro e vira replay ou conflito.
- 5 carteiras com 10 apostas simultâneas cada: todas terminam com `90.00`.
- 19 entradas inválidas → `400`/`404`, nenhuma transação gravada.
- Conferido com `curl` no Docker Compose, incluindo replay depois de reiniciar a aplicação.
- Cobertura somada de `internal/`: 90,4%.

Os testes de concorrência usam várias conexões em **um** processo; a prova com três processos fica na spec 15.

## Para estudar

`crypto/sha256`, ordenação de chaves em `encoding/json`, por que o lock vem antes da checagem de duplicata.
