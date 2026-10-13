# 06 — Carteira: abertura, leitura e ledger paginado

**Desafio:** §9 (abertura, leitura) · **Depende de:** 05

## Objetivo

Primeiro fluxo ponta a ponta: HTTP → caso de uso → domínio → banco.

## Endpoints

| Método | Rota | Resposta |
| --- | --- | --- |
| `POST` | `/wallets` | `201` com `id`, `playerId`, `balance`, `version` |
| `GET` | `/wallets/:walletId` | `200` com a carteira |
| `GET` | `/wallets/:walletId/ledger?cursor=...&limit=50` | `200` com itens + próximo cursor |

## Requisitos — abertura

- Saldo inicial **positivo**, no mesmo commit:
  - carteira com versão `1`;
  - transação `OPENING` em `PROCESSED`;
  - um lançamento `CREDIT` (saldoAntes `0.00`, saldoDepois = inicial);
  - eventos `WagerTransactionProcessed` e `WalletBalanceChanged` na outbox.
- Saldo inicial **zero**: só a carteira. Sem `OPENING`, sem ledger, sem eventos.
- Saldo inicial negativo → entrada inválida.
- Mesmo `(playerId, currency)` de novo → `409`.

## Requisitos — ledger

- Cursor **opaco** (o cliente não interpreta) e ordenação **estável** (`created_at, id`).
- `limit` com padrão `50` e teto definido.

## Padrão de erro HTTP (válido para todas as specs)

```json
{ "error": { "code": "WALLET_ALREADY_EXISTS", "message": "..." } }
```

| Situação | HTTP |
| --- | --- |
| Entrada inválida | `400` |
| Não autenticado / sem permissão | `401` / `403` |
| Não encontrado | `404` |
| Conflito | `409` |
| Rejeição de negócio | `422` |
| Indisponibilidade transitória | `503` + `Retry-After` |

## Entregáveis

- `internal/application/`: `open_wallet.go`, `get_wallet.go`, `list_ledger.go`
- `internal/infra/httpapi/`: router, handlers de carteira, tradução de erro → HTTP

## Critérios de aceite

- [ ] Abrir com `1000.00` cria 1 carteira, 1 `OPENING`, 1 lançamento e 2 eventos na outbox, tudo ou nada.
- [ ] Abrir com `0.00` cria só a carteira.
- [ ] Segunda abertura para o mesmo jogador e moeda → `409`.
- [ ] Paginar o ledger inteiro não repete nem pula lançamento.

## Para estudar

`http.Handler`, middlewares do `chi`, `encoding/json` com struct tags, paginação por cursor (keyset) vs offset.
