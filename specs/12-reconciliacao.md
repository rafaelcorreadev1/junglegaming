# 12 — Reconciliação

**Desafio:** §9 (reconciliação) · **Depende de:** 06

## Objetivo

Provar que o saldo armazenado bate com a soma do ledger.

## Endpoint

`POST /wallets/:walletId/reconciliation` (restrito ao serviço interno)

```json
{
  "walletId": "...",
  "storedBalance": { "amount": "975.00", "currency": "BRL" },
  "calculatedBalance": { "amount": "975.00", "currency": "BRL" },
  "difference": { "amount": "0.00", "currency": "BRL" },
  "consistent": true,
  "checkedEntries": 2
}
```

## Requisitos

- `calculatedBalance` = soma dos créditos − soma dos débitos do ledger, incluindo a abertura.
- `difference` = armazenado − calculado (pode ser negativo).
- Saldo e ledger lidos em uma **visão consistente** (mesmo snapshot): transação `REPEATABLE READ` somente leitura.
- Não altera o saldo.
- Divergência é reportada em três lugares: resposta, log e métrica.

## Entregáveis

- `internal/application/reconcile_wallet.go` + handler

## Critérios de aceite

- [ ] Carteira aberta com `1000.00` + `BET` de `25.00` → `975.00`, `consistent: true`, `checkedEntries: 2`.
- [ ] Reconciliar durante escrita concorrente nunca acusa divergência falsa.
- [ ] Divergência forçada no teste → `consistent: false`, log de erro e métrica incrementada.

## Para estudar

Níveis de isolamento do PostgreSQL (`READ COMMITTED` vs `REPEATABLE READ`).
