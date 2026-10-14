# 12 — Reconciliação

**Desafio:** §9 (reconciliação) · **Depende de:** 06 · **Status:** concluída

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

## Como foi implementado

### Leitura consistente

`TxManager.WithinSnapshot` abre a transação com `REPEATABLE READ` e `READ ONLY`. Dentro dela, o caso de uso lê a carteira e soma o ledger em duas consultas que enxergam o mesmo estado.

| Isolamento | O que cada leitura vê | Resultado com apostas concorrentes |
| --- | --- | --- |
| `READ COMMITTED` (padrão) | O que estava confirmado quando **aquele comando** começou | O saldo é lido, uma aposta confirma, o ledger já inclui a aposta: divergência falsa de 1.00 |
| `REPEATABLE READ` | O que estava confirmado na **primeira consulta** da transação | Saldo e ledger sempre do mesmo instante |

O teste de concorrência prova isso: com `REPEATABLE READ`, cerca de 650 reconciliações durante 100 apostas paralelas e nenhuma divergência. Trocando para `READ COMMITTED`, o mesmo teste acusou várias divergências falsas.

- Por ser `READ ONLY`, qualquer escrita dentro dela falha no banco: a garantia de "não altera o saldo" não depende só do código.
- `WithinSnapshot` dentro de outra transação é erro: ela herdaria o isolamento da transação de fora sem ninguém perceber.
- Leitura em `REPEATABLE READ` não trava ninguém: as apostas continuam enquanto a reconciliação roda.

### Cálculo

- Uma consulta: `SUM(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END)` e `count(*)` do ledger da carteira. A abertura é um crédito no ledger (spec 06), então entra na conta; carteira aberta com zero tem 0 lançamentos.
- A soma de `bigint` no PostgreSQL é `numeric`; o `::bigint` de volta falha em vez de dar a volta se um dia passar do limite.
- `difference = armazenado − calculado` com `Money.Sub`; `consistent` é `difference == 0`.

### Relato

`application.ReconciliationReporter` é uma porta; a implementação em `internal/infra/observability/metrics.go` faz as duas coisas:

| Resultado | Log | Métrica |
| --- | --- | --- |
| Consistente | `DEBUG` "wallet reconciled" | `wagering_reconciliations_total{result="consistent"}` + 1 |
| Divergente | `ERROR` "wallet balance diverges from its ledger", com carteira, saldos, diferença e lançamentos | `wagering_reconciliations_total{result="divergent"}` + 1 |

- As duas séries existem desde o início com valor 0, para um alerta enxergar a divergência como aumento, não como série nova.
- `GET /metrics` (formato Prometheus) foi criado agora, público como os health checks, com um registro próprio (não o global), que a spec 13 vai encher com as outras métricas.

### Acesso

Rota no grupo do papel `internal`: token de provedor → `403`, sem token → `401`, carteira inexistente → `404`, id inválido → `400`.

## Entregáveis

- `internal/application/reconcile_wallet.go` + `WalletHandler.Reconcile`
- `internal/infra/postgres`: `TxManager.WithinSnapshot`, `LedgerRepository.Summarize`
- `internal/infra/observability/metrics.go`: registro Prometheus e `ReconciliationReporter`
- `GET /metrics`

## Critérios de aceite

- [x] Carteira aberta com `1000.00` + `BET` de `25.00` → `975.00`, `consistent: true`, `checkedEntries: 2`.
- [x] Reconciliar durante escrita concorrente nunca acusa divergência falsa.
- [x] Divergência forçada no teste → `consistent: false`, log de erro e métrica incrementada.

## Limites conhecidos

- A reconciliação compara só o total. Não confere a cadeia `balanceBefore`/`balanceAfter` lançamento a lançamento; as `CHECK` do ledger (spec 04) já garantem cada lançamento isoladamente.
- O log de divergência ainda não traz o `correlationId` da requisição; a propagação para os logs fora do `httpapi` é da spec 13.
- Uma carteira por chamada; não há reconciliação periódica de todas as carteiras.

## Para estudar

Níveis de isolamento do PostgreSQL (`READ COMMITTED` vs `REPEATABLE READ`).
