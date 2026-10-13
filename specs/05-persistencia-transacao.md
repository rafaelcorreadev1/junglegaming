# 05 — Persistência e transação SQL

**Desafio:** §4 (acesso ao banco), §5 (garantias 6, 7), §8 · **Depende de:** 04

## Objetivo

Repositórios com SQL explícito e um único mecanismo que delimita a transação SQL, compartilhada por todos os repositórios dentro de um caso de uso.

## Requisitos

- `pgx/v5` com SQL escrito à mão. Transações, locks e constraints explícitos.
- O caso de uso abre **uma** transação; todos os repositórios usados dentro dela participam.
- Lock **por carteira** (`SELECT ... FOR UPDATE` na linha da carteira). Lock global é proibido.
- Carteiras diferentes avançam em paralelo.
- Sem lost update: a leitura do saldo e a gravação acontecem sob o mesmo lock.
- Toda operação de I/O recebe `context.Context` e respeita cancelamento/timeout.
- Violação de constraint é traduzida para erro de domínio/aplicação (ex.: unique → conflito).
- Erro transitório (conexão caída, deadlock, serialization failure) é distinguível de erro permanente.

## Desenho

- `application` define as interfaces (ports) e os erros que os casos de uso enxergam. `infra/postgres` implementa.
- `TxManager.WithinTx(ctx, func(ctx) error)`: abre a transação, guarda no `context`, faz commit se a função retornar `nil`, rollback caso contrário.
- Os repositórios pegam a transação do `context`; sem transação, usam o pool.
- `WithinTx` chamado dentro de outro `WithinTx` **entra na transação existente** (não abre outra).

### Interfaces entregues

| Interface | Métodos |
| --- | --- |
| `TxManager` | `WithinTx` |
| `WalletRepository` | `Insert`, `Get`, `GetForUpdate`, `UpdateBalance` |
| `WagerTransactionRepository` | `Insert`, `Update`, `Get`, `GetByExternalID`, `GetByIdempotencyKey` |
| `LedgerRepository` | `Insert` |
| `OutboxRepository` | `Append` |

Ficam para as specs que precisam deles: listagem paginada do ledger (06), consulta de reversão já processada e fila de pendências (08), reserva e publicação da outbox (10), `InboxRepository` (11).

### Tradução de erros

| Erro do PostgreSQL | Erro de aplicação | Tipo |
| --- | --- | --- |
| `23505` em `wallets_player_currency_key` | `ErrWalletAlreadyExists` | Permanente |
| `23505` em `wager_transactions_external_key` | `ErrDuplicateExternalTransaction` | Permanente |
| `23505` em `wager_transactions_idempotency_key` | `ErrDuplicateIdempotencyKey` | Permanente |
| `23505` em `wager_transactions_one_processed_reversal` | `ErrAlreadyReversed` | Permanente |
| Outras violações da classe `23` | `ErrInvariantViolation` | Permanente (bug) |
| `40001`, `40P01`, classes `08`, `53`, `57`, erro de rede | `ErrUnavailable` | **Transitório** |
| Nenhuma linha | `ErrWalletNotFound` / `ErrTransactionNotFound` | Permanente |
| `context` cancelado ou expirado | O próprio erro do `context` | — |

O erro original fica encadeado (`%w`), então o log ainda mostra o SQLSTATE.

## Decisões

- **`UpdateBalance` tem guarda de versão** (`WHERE version = nova - 1`). O lock já serializa; a guarda transforma um esquecimento do lock em `ErrConcurrentUpdate`, em vez de uma atualização perdida.
- **`GetForUpdate` fora de transação retorna erro.** Sem transação o lock seria solto na mesma hora e não protegeria nada.
- **Campo opcional vazio (`""`) vira `NULL`** com `NULLIF` na escrita e `COALESCE` na leitura, direto no SQL.
- **Saldo resultante ganhou coluna de moeda** (`result_balance_currency`). Em uma rejeição por `CURRENCY_MISMATCH` o saldo devolvido está na moeda da carteira, diferente da moeda da transação. A migration `000002` foi ajustada; quem já tinha o banco local precisa de `docker compose down -v`.
- **Depois de uma violação de constraint, a transação SQL está abortada** no PostgreSQL. O caso de uso não continua nela: retorna o erro e, se for o caso, tenta de novo em outra transação (usado na spec 07).
- Horários são lidos em UTC. O PostgreSQL guarda microssegundos; o relógio da aplicação (spec 06) trunca para microssegundo, para que o valor em memória seja igual ao relido.

## Entregáveis

- `internal/application/ports.go`
- `internal/infra/postgres/`: `tx.go`, `errors.go`, `wallet_repository.go`, `wager_transaction_repository.go`, `ledger_repository.go`, `outbox_repository.go`
- `internal/testdb/`: schema descartável e migrado para cada teste

## Critérios de aceite

- [x] Erro dentro de `WithinTx` desfaz tudo que os repositórios gravaram.
- [x] Duas transações na mesma carteira: a segunda espera a primeira; em carteiras diferentes, não esperam.
- [x] `context` cancelado interrompe a query.
- [x] Mapeamento `Money` ⇄ (`BIGINT`, `CHAR(3)`) preserva valor e moeda exatamente.

## Status: concluída

Testes contra PostgreSQL real (pulados sem `TEST_DATABASE_URL`), também com `-race`:

- 20 apostas simultâneas de `1.00` em uma carteira de `20.00`: saldo `0.00`, versão `21`, 20 lançamentos; a 21ª é rejeitada com `INSUFFICIENT_FUNDS`.
- Espera pelo lock cortada pelo prazo do `context`, enquanto outra carteira é travada sem esperar.
- Ida e volta de carteira (inclusive o maior saldo representável) e de transação (abertura, aposta, rejeição em outra moeda, referência pendente resolvida).

Cobertura do pacote `postgres`: 83,6%. O que falta são ramos de erro de I/O e o `NewPool`, exercitado ao subir a aplicação.

## Para estudar

`pgxpool`, `pgx.Tx`, `context.WithValue`, `FOR UPDATE`, códigos SQLSTATE (`23505` unique, `23514` check, `40001`, `40P01`).
