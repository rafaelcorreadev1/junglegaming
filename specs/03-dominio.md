# 03 — Domínio: Wallet, Ledger, WagerTransaction e erros

**Desafio:** §6, §6.2, §6.3, §6.4, §7 · **Depende de:** 02

## Objetivo

Entidades com estado encapsulado e regras financeiras puras, sem nenhuma dependência de Fx, HTTP, SQS ou banco.

## Regras gerais (§6)

- Construtores com validação; campos não exportados; mudança de estado só por método.
- **Criação** e **reidratação** são funções separadas. Reidratar (carregar do banco) não movimenta saldo, não transiciona estado, não emite evento.
- Erros de domínio classificáveis por `errors.Is` / `errors.As`. Sem `panic` para regra de negócio.

## Wallet (§6.2)

- Campos: id, playerId, moeda, saldo, versão, criadoEm, atualizadoEm.
- `(playerId, currency)` identifica uma única carteira.
- `Debit` exige saldo resultante ≥ 0; `Credit` soma. Ambos exigem a moeda da carteira.
- Versão inicial `1`; incrementa **só** quando o saldo muda.
- Débito/crédito devolvem o `WalletLedgerEntry` correspondente (saldo e lançamento nascem juntos).

## WalletLedgerEntry (§6.4)

- Campos: id, walletId, transactionId, direção (`DEBIT`/`CREDIT`), valor, saldoAntes, saldoDepois, criadoEm.
- Imutável. O construtor valida `saldoDepois = saldoAntes ± valor`.
- `LOSS` e operações rejeitadas não geram lançamento.

## WagerTransaction (§6.3)

- Tipos: `OPENING` (interno), `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`.
- Campos (externa): id interno, id externo, provedor, chave de idempotência, hash do payload, carteira, jogador, rodada, jogo, tipo, `Money`, referência externa opcional, referência interna resolvida, estado, código de falha, saldo resultante, timestamps.
- `OPENING`: só id, carteira, jogador, moeda, valor, estado, timestamps. Os campos externos não se aplicam. Rejeitado quando vem por HTTP ou SQS.

### Máquina de estados

```
PENDING ──────────────► PROCESSED   (terminal)
   │  ├───────────────► REJECTED    (terminal)
   │  └───────────────► FAILED      (terminal)
   ▼
PENDING_REFERENCE ────► PROCESSED | REJECTED | FAILED
```

Transição a partir de estado terminal retorna erro.

### Regras por tipo (§7)

| Tipo | Movimento | Valor | Referência |
| --- | --- | --- | --- |
| `BET` | Débito | > 0, saldo suficiente | — |
| `WIN` | Crédito | > 0 | Opcional: uma `BET` da mesma rodada |
| `LOSS` | Nenhum | exatamente `0.00`, moeda da carteira | — |
| `REFUND` | Crédito | > 0, igual ao da `BET` | Obrigatória: `BET` processada |
| `ROLLBACK` | Contrário ao original | > 0, igual ao original | Obrigatória: `BET`, `WIN` ou `REFUND` processada |

Operação e referência devem coincidir em provedor, jogador, carteira, moeda e rodada.

## Códigos de falha (`failureCode`)

Estáveis e documentados. Todo código abaixo é um **resultado definitivo**: a transação fica `REJECTED` (ou `FAILED`), é persistida e não muda mais.

| Código | Quando |
| --- | --- |
| `INSUFFICIENT_FUNDS` | `BET` sem saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | `ROLLBACK` que debitaria mais que o saldo |
| `BALANCE_LIMIT_EXCEEDED` | Crédito que passaria do limite representável do saldo |
| `PLAYER_MISMATCH` | A carteira pertence a outro jogador |
| `CURRENCY_MISMATCH` | Moeda diferente da carteira |
| `REFERENCE_NOT_FOUND` | Referência não chegou até expirar (spec 08) |
| `REFERENCE_NOT_PROCESSED` | Referência terminou `REJECTED` ou `FAILED` |
| `REFERENCE_MISMATCH` | Diverge em provedor, jogador, carteira, moeda ou rodada |
| `REFERENCE_AMOUNT_MISMATCH` | Valor da reversão diferente do valor referenciado |
| `REFERENCE_KIND_NOT_ALLOWED` | Tipo da referência inválido para a operação |
| `ALREADY_REVERSED` | A referência já tem uma reversão processada |
| `PROCESSING_FAILED` | Falha permanente de infraestrutura (`FAILED`) |

**Entrada corrigível** é outra coisa: é o erro de validação (`ValidationError`, com o nome do campo). Nada é persistido, a resposta é `400` e o provedor pode reenviar corrigido. Exemplos: campo obrigatório ausente, `BET` com valor zero, `REFUND` sem referência, `kind: "OPENING"`.

## Como as regras são aplicadas

`ApplyWager(tx, wallet, ref, entryID, now)` decide o resultado e o registra na transação e na carteira.

- Rejeição de negócio **não é erro**: a função retorna sem erro e a transação fica `REJECTED` com `failureCode`.
- Erro de retorno é reservado a uso incorreto (transação já terminal, carteira errada).
- O lançamento retornado é `nil` quando o saldo não muda (`LOSS`, rejeição, espera por referência).
- `ref` traz a transação referenciada (ou `nil`, se ainda não chegou) e se ela já foi revertida. Quem busca isso no banco é o caso de uso (specs 07 e 08).

Ordem das verificações: jogador → moeda → referência (contexto, tipo, estado, valor, já revertida) → movimentação.

## Decisões

- IDs e o instante (`now`) entram como parâmetro. O domínio não gera UUID nem lê o relógio, então os testes são determinísticos e o pacote fica só com a biblioteca padrão.
- `WIN` com referência informada segue a mesma regra de espera: se a `BET` não chegou, fica `PENDING_REFERENCE`.
- `BET` e `LOSS` não aceitam referência (erro de validação).
- `LedgerEntry` tem um construtor só, usado na criação e na reidratação: o lançamento não tem comportamento a reaplicar, apenas a equação do saldo a conferir.

## Entregáveis

- `internal/domain/`: `errors.go`, `ledger.go`, `wallet.go`, `wager_transaction.go`, `wager_rules.go` + testes

## Critérios de aceite

- [x] Débito além do saldo retorna erro e não altera saldo nem versão.
- [x] `LOSS` não altera versão nem gera lançamento.
- [x] Toda transição da máquina de estados, válida e inválida, é coberta por teste (5 estados × 4 destinos).
- [x] Política de valor zero e de referência de cada tipo coberta por teste.
- [x] Duas apostas de `80.00` sobre `100.00`, em sequência: uma processada, uma rejeitada, saldo `20.00`.
- [x] `internal/domain` importa apenas a biblioteca padrão.

## Status: concluída

243 casos de teste no pacote `domain`, 99,7% das instruções cobertas. `go test`, `go vet` e `go test -race` (em container) passam.

A disputa **simultânea** das duas apostas depende do lock no banco e é provada nas specs 05, 07 e 15; aqui a regra é provada em sequência.

## Para estudar

Métodos com receiver de ponteiro vs valor, erros sentinela e tipos de erro, `iota`/tipos string para enums.
