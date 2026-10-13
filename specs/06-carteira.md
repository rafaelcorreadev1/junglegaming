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

- Cursor **opaco** (o cliente não interpreta) e ordenação **estável** pela coluna `sequence` do ledger (spec 04).
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

## Contratos

```http
POST /wallets
{ "playerId": "...", "initialBalance": { "amount": "1000.00", "currency": "BRL" } }

201 Created
Location: /wallets/{id}
{ "id": "...", "playerId": "...", "balance": { "amount": "1000.00", "currency": "BRL" }, "version": 1 }
```

```http
GET /wallets/{walletId}/ledger?cursor=...&limit=50

200 OK
{
  "items": [
    { "id": "...", "walletId": "...", "transactionId": "...", "direction": "CREDIT",
      "money": {...}, "balanceBefore": {...}, "balanceAfter": {...}, "createdAt": "2026-09-08T12:00:00Z" }
  ],
  "nextCursor": "djE6NDI"
}
```

- Itens do mais antigo para o mais novo. `nextCursor` é `null` na última página.
- `limit`: padrão `50`, máximo `200`. Fora disso → `400`.
- Erro de validação inclui `field` quando o problema é de um campo: `{ "error": { "code": "INVALID_REQUEST", "message": "...", "field": "playerId" } }`.

| Código de erro | HTTP |
| --- | --- |
| `INVALID_REQUEST` | `400` |
| `WALLET_NOT_FOUND` | `404` |
| `WALLET_ALREADY_EXISTS` | `409` |
| `SERVICE_UNAVAILABLE` | `503` + `Retry-After: 1` |
| `INTERNAL_ERROR` | `500` (a causa fica só no log) |

## Decisões

- **Eventos de domínio entregues aqui** (`internal/domain/events.go`), porque a abertura já grava dois na outbox. Os quatro tipos do desafio estão definidos; a spec 10 fica com a publicação.
- **`aggregateId` de todo evento é o `walletId`**: é o que mantém a ordem por carteira na fila FIFO.
- **A carteira nasce com o saldo e versão `1`.** O lançamento de abertura é montado no caso de uso, não por `wallet.Credit`, que subiria a versão para `2`.
- **Cursor** = base64url de `v1:<sequence>`. O prefixo permite trocar o formato sem quebrar clientes antigos em silêncio.
- **Paginação busca `limit + 1`** registros: o extra só indica se existe próxima página.
- **`X-Correlation-Id`**: aceito do cliente ou gerado; devolvido na resposta e gravado nos eventos.
- **Corpo JSON estrito**: campo desconhecido, dois objetos ou mais de 1 MB → `400`.
- **`walletId` precisa ser UUID** (`400` se não for); `playerId` é texto livre obrigatório.
- **Relógio e gerador de ID são ports** (`Clock`, `IDGenerator`), implementados em `internal/infra/system` (UUID v7, horário truncado em microssegundo).
- Os endpoints ficam **sem autenticação até a spec 09**.

## Entregáveis

- `internal/domain/events.go`
- `internal/application/`: `open_wallet.go`, `get_wallet.go`, `list_ledger.go`
- `internal/infra/httpapi/`: `wallets.go`, `errors.go`, `middleware.go`
- `internal/infra/system/`: relógio e gerador de UUID v7

## Critérios de aceite

- [x] Abrir com `1000.00` cria 1 carteira, 1 `OPENING`, 1 lançamento e 2 eventos na outbox, tudo ou nada.
- [x] Abrir com `0.00` cria só a carteira.
- [x] Segunda abertura para o mesmo jogador e moeda → `409`.
- [x] Paginar o ledger inteiro não repete nem pula lançamento.

## Status: concluída

- Testes HTTP passam pelo router real, casos de uso reais e PostgreSQL real (pulados sem `TEST_DATABASE_URL`).
- "Tudo ou nada": com a outbox falhando no segundo evento, as quatro tabelas ficam vazias.
- Paginação: 9 lançamentos lidos de 3 em 3, com um lançamento novo inserido entre as páginas; a ordem lida é igual à do banco.
- Conferido também com `curl` no Docker Compose.
- Cobertura somada de `internal/`: 90,6%. `go test -race` passa no container.

## Para estudar

`http.Handler`, middlewares do `chi`, `encoding/json` com struct tags, paginação por cursor (keyset) vs offset.
