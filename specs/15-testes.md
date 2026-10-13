# 15 — Testes de integração, concorrência e recuperação

**Desafio:** §8, §13 · **Depende de:** todas

> Eliminatório: substituir PostgreSQL, SQS e IdP por mocks.

Os testes unitários de cada spec são escritos junto com ela. Esta spec cobre o que precisa de infraestrutura real.

## Requisitos

- PostgreSQL, Keycloak e LocalStack em containers reais.
- Integração separada por build tag: `go test -tags=integration ./...`.
- `go test -race` nos testes aplicáveis.
- Testes de duplicidade exercitam a deduplicação **da aplicação**, com recebimento repetido comprovado (não a do SQS FIFO).
- Ao final de cada cenário financeiro: saldo armazenado = créditos − débitos do ledger.

## Integração

- [ ] Migrations `up`/`down`.
- [ ] Constraints: saldo negativo, unicidades, imutabilidade do ledger.
- [ ] Atomicidade: falha no meio não deixa saldo sem ledger nem ledger sem evento.
- [ ] Inbox e reentrega.
- [ ] Outbox concorrente, retry e DLQ.
- [ ] Recuperação após reinicialização.
- [ ] Composição Fx: start, stop e liberação dos workers.

## Autenticação e autorização

- [ ] Token real do Keycloak; ausente, inválido e expirado são rejeitados.
- [ ] Isolamento entre provedores em envio, consulta e replay.
- [ ] Operações internas negadas a provedores.
- [ ] Acesso negado não gera efeito financeiro nem expõe dados.

## Concorrência e recuperação (§13)

| # | Cenário | Resultado esperado |
| --- | --- | --- |
| 1 | Mesma aposta 50× em paralelo | Um único débito |
| 2 | Duas apostas de `80.00` sobre `100.00` | 1 processada, 1 rejeitada, saldo `20.00`, 1 débito |
| 3 | Carteiras distintas em paralelo | Todas avançam, sem bloqueio mútuo |
| 4 | Cenários 1–3 com 3 instâncias independentes | Mesmo resultado |
| 5 | Consumidor morto após commit, antes de remover a mensagem | Reentrega sem duplicar |
| 6 | Dois publishers na mesma outbox | Sem perda; republicação mantém `eventId` |
| 7 | `REFUND`/`ROLLBACK` antes da referência | Resolve depois, ou rejeita por expiração |
| 8 | Reinício da aplicação | Idempotência, pendências e consistência preservadas |
| 9 | Mesma operação por HTTP e SQS | Um único efeito |

## Entregáveis

- `test/integration/` — suíte com containers
- `test/multiinstance/` — cenários contra 3 réplicas do Compose
- Ponto de injeção de falha (para matar o processo entre commit e ack / entre publicação e marcação)

## Para estudar

`testing.T`, `t.Parallel`, build tags, detector de race, `testcontainers-go`, `sync.WaitGroup` + `errgroup`.
