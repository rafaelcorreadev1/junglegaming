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

Estáveis e documentados. Lista inicial:

| Código | Quando | Corrigível? |
| --- | --- | --- |
| `INSUFFICIENT_FUNDS` | `BET` sem saldo | Não |
| `REVERSAL_INSUFFICIENT_FUNDS` | Reversão que debitaria mais que o saldo | Não |
| `CURRENCY_MISMATCH` | Moeda diferente da carteira | Sim |
| `WALLET_NOT_FOUND` / `PLAYER_MISMATCH` | Carteira inexistente ou de outro jogador | Sim |
| `REFERENCE_NOT_FOUND` | Referência não chegou até expirar | Não |
| `REFERENCE_NOT_PROCESSED` | Referência terminou `REJECTED`/`FAILED` | Não |
| `REFERENCE_MISMATCH` | Diverge em provedor/jogador/carteira/moeda/rodada/valor | Não |
| `REFERENCE_KIND_NOT_ALLOWED` | Tipo da referência inválido para a operação | Não |
| `ALREADY_REVERSED` | Referência já tem reversão bem-sucedida | Não |

## Entregáveis

- `internal/domain/`: `wallet.go`, `ledger.go`, `wager_transaction.go`, `errors.go` + testes

## Critérios de aceite

- [ ] Débito além do saldo retorna erro e não altera saldo nem versão.
- [ ] `LOSS` não altera versão nem gera lançamento.
- [ ] Toda transição inválida da máquina de estados é coberta por teste.
- [ ] `internal/domain` importa apenas a biblioteca padrão.

## Para estudar

Métodos com receiver de ponteiro vs valor, erros sentinela e tipos de erro, `iota`/tipos string para enums.
