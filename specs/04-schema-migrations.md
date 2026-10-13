# 04 — Schema e migrations

**Desafio:** §4, §5 (garantias 3, 5, 8), §6.3, §6.4, §6.5 · **Depende de:** 03

## Objetivo

O banco impõe as invariantes financeiras sozinho: mesmo que a aplicação tenha um bug, o PostgreSQL recusa saldo negativo, duplicidade e edição do ledger.

## Tabelas

| Tabela | Conteúdo |
| --- | --- |
| `wallets` | Carteira e saldo atual |
| `wager_transactions` | Operações internas e externas |
| `wallet_ledger_entries` | Lançamentos append-only |
| `inbox_messages` | Mensagens SQS já recebidas |
| `outbox_events` | Eventos a publicar |

## Invariantes impostas pelo banco

| Invariante | Mecanismo |
| --- | --- |
| Saldo nunca negativo | `CHECK (balance_minor >= 0)` |
| Uma carteira por jogador e moeda | `UNIQUE (player_id, currency)` |
| Operação externa única | `UNIQUE (provider_id, external_transaction_id)` |
| Chave de idempotência única | `UNIQUE (provider_id, idempotency_key)` |
| Um lançamento por transação e carteira | `UNIQUE (wallet_id, transaction_id)` |
| Ledger coerente | `CHECK` de `balance_after = balance_before ± amount` conforme a direção |
| Ledger imutável | Trigger que rejeita `UPDATE` e `DELETE` |
| Um único crédito de abertura por carteira | Índice único parcial `WHERE kind = 'OPENING'` |
| Interno × externo | `CHECK`: `OPENING` tem campos externos nulos; os demais tipos têm todos preenchidos |
| Uma única reversão processada por referência | Índice único parcial em `(reference_transaction_id) WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK')` |
| Mensagem única por consumidor | `UNIQUE (consumer_name, message_id)` |
| Estados e tipos válidos | `CHECK` com a lista de valores |

## Requisitos

- Migrations versionadas com `up` e `down`.
- Aplicação e reversão documentadas no README (o README da solução é escrito na spec 16; os comandos estão abaixo).
- Dinheiro em `BIGINT` (centavos) + `CHAR(3)` (moeda).

## Invariantes acrescentadas na implementação

| Invariante | Mecanismo |
| --- | --- |
| Lançamento na moeda da carteira | FK composta `(wallet_id, currency)` → `wallets (id, currency)` |
| Lançamento na carteira da sua transação | FK composta `(transaction_id, wallet_id)` → `wager_transactions (id, wallet_id)` |
| Valor por tipo | `CHECK`: `LOSS` = 0; os demais > 0 |
| Referência por tipo | `CHECK`: obrigatória em `REFUND`/`ROLLBACK`, proibida em `OPENING`/`BET`/`LOSS` |
| Código de falha só em `REJECTED`/`FAILED`, e sempre neles | `CHECK` |
| `PROCESSED` tem saldo resultante | `CHECK` |
| `TRUNCATE` no ledger | Trigger próprio (o trigger por linha não dispara em `TRUNCATE`) |

## Decisões

- **Uma migration por grupo de tabelas** (4 arquivos), na ordem das dependências.
- **IDs internos em `UUID`; identificadores vindos de fora em `TEXT`** (`player_id`, `provider_id`, `external_transaction_id`, `round_id`, `game_id`, `message_id`). O formato deles é do provedor, não nosso.
- **`kind`, `status` e `direction` em `TEXT` + `CHECK`**, não `ENUM` do Postgres: acrescentar um valor a um `CHECK` é reversível em uma migration; remover valor de `ENUM` não é.
- **`sequence` (identity) no ledger e na outbox.** No ledger é a ordem estável do cursor de paginação (spec 06): as inserções de uma carteira acontecem sob o lock da linha dela, então a sequência cresce na ordem real. `created_at` pode empatar.
- **Sem `DEFAULT now()`**: o instante vem da aplicação, o mesmo que o domínio usou.
- **Campos externos anuláveis + `CHECK` de origem**: `OPENING` não tem provedor. `UNIQUE` ignora linhas com `NULL`, então as aberturas não colidem entre si.
- **Outbox com `locked_until`**: é a "reserva com prazo" da spec 10.
- As colunas de retry de `PENDING_REFERENCE` entram em uma migration da spec 08.

## Como aplicar e reverter

```bash
docker compose up -d postgres
docker compose run --rm migrate                      # aplica tudo (é o padrão do serviço)
docker compose run --rm migrate -path=/migrations -database=<url> down 1     # reverte a última
docker compose run --rm migrate -path=/migrations -database=<url> down -all  # reverte tudo
docker compose run --rm migrate -path=/migrations -database=<url> version
```

`docker compose up` já roda o `migrate` antes da aplicação: o serviço `app` só sobe depois que ele termina com sucesso.

## Entregáveis

- `migrations/000001_create_wallets` … `000004_create_inbox_and_outbox` (`.up.sql` e `.down.sql`)
- Serviço `migrate` no Docker Compose (roda antes da aplicação)
- `migrations/migrations_test.go`

## Critérios de aceite

- [x] `up` seguido de `down` deixa o banco vazio; `up` de novo funciona.
- [x] `UPDATE` e `DELETE` em `wallet_ledger_entries` falham.
- [x] `UPDATE wallets SET balance_minor = -1` falha.
- [x] Inserir a mesma `(provider_id, external_transaction_id)` duas vezes falha.

## Status: concluída

O teste aplica as migrations em um schema descartável e confere 21 violações (pelo SQLSTATE e pelo nome da constraint) e 5 casos que devem ser aceitos. Ele precisa de um PostgreSQL real e é pulado sem `TEST_DATABASE_URL`:

```bash
TEST_DATABASE_URL='postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' go test ./migrations/
```

O ciclo `up` → `down -all` → `up` também foi conferido com o `golang-migrate` do Compose: depois do `down` sobra apenas a tabela de controle `schema_migrations`.

## Para estudar

Índice único parcial, triggers no PostgreSQL, por que constraint no banco vale mais que validação na aplicação em ambiente com várias instâncias.
