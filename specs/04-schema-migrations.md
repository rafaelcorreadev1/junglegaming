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
| Uma reversão bem-sucedida por referência e tipo | Índice único parcial em `(reference_transaction_id, kind) WHERE status = 'PROCESSED'` |
| Mensagem única por consumidor | `UNIQUE (consumer_name, message_id)` |
| Estados e tipos válidos | `CHECK` com a lista de valores |

## Requisitos

- Migrations versionadas com `up` e `down`.
- Aplicação e reversão documentadas no README.
- Dinheiro em `BIGINT` (centavos) + `CHAR(3)` (moeda).

## Entregáveis

- `migrations/NNNNNN_nome.up.sql` e `.down.sql`
- Serviço `migrate` no Docker Compose (roda antes da aplicação)

## Critérios de aceite

- [ ] `up` seguido de `down` deixa o banco vazio; `up` de novo funciona.
- [ ] `UPDATE` e `DELETE` em `wallet_ledger_entries` falham.
- [ ] `UPDATE wallets SET balance_minor = -1` falha.
- [ ] Inserir a mesma `(provider_id, external_transaction_id)` duas vezes falha.

## Para estudar

Índice único parcial, triggers no PostgreSQL, por que constraint no banco vale mais que validação na aplicação em ambiente com várias instâncias.
