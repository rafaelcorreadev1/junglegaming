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

- `application` define as interfaces: `WalletRepository`, `WagerTransactionRepository`, `LedgerRepository`, `InboxRepository`, `OutboxRepository` e `TxManager`.
- `TxManager.WithinTx(ctx, func(ctx) error)`: abre a transação, guarda no `context`, faz commit se a função retornar `nil`, rollback caso contrário.
- Os repositórios pegam a transação do `context`; sem transação, usam o pool.

## Entregáveis

- `internal/application/ports.go`
- `internal/infra/postgres/`: `tx.go`, um arquivo por repositório, `errors.go` (tradução de códigos do Postgres)

## Critérios de aceite

- [ ] Erro dentro de `WithinTx` desfaz tudo que os repositórios gravaram.
- [ ] Duas transações na mesma carteira: a segunda espera a primeira; em carteiras diferentes, não esperam.
- [ ] `context` cancelado interrompe a query.
- [ ] Mapeamento `Money` ⇄ (`BIGINT`, `CHAR(3)`) preserva valor e moeda exatamente.

## Para estudar

`pgxpool`, `pgx.Tx`, `context.WithValue`, `FOR UPDATE`, códigos SQLSTATE (`23505` unique, `23514` check, `40001`, `40P01`).
