# 15 — Testes de integração, concorrência e recuperação

**Desafio:** §8, §13 · **Depende de:** todas · **Status:** concluída

> Eliminatório: substituir PostgreSQL, SQS e IdP por mocks.

Os testes unitários de cada spec foram escritos junto com ela. Esta spec cobre o que precisa de infraestrutura real e fecha os cenários do §13.

## Requisitos

- PostgreSQL, Keycloak e LocalStack em containers reais.
- Integração separada por build tag: `go test -tags=integration ./...`.
- `go test -race` nos testes aplicáveis.
- Testes de duplicidade exercitam a deduplicação **da aplicação**, com recebimento repetido comprovado (não a do SQS FIFO).
- Ao final de cada cenário financeiro: saldo armazenado = créditos − débitos do ledger.

## Como foi implementado

### Três camadas de teste

| Camada | Onde | Contra o quê |
| --- | --- | --- |
| Unitário | `internal/...` | Nada externo: domínio, regras, fakes de porta |
| Componente | `internal/...` (mesmos pacotes) | PostgreSQL e LocalStack reais, um schema e filas por teste; código chamado em processo |
| Caixa-preta | `test/integration`, `test/multiinstance`, `test/recovery` | O serviço rodando, por HTTP e SQS, com tokens reais do Keycloak |

Os containers vêm do próprio Compose, não do `testcontainers-go`: é o mesmo ambiente que roda a aplicação, e cada teste se isola com schema e filas próprios.

### A build tag `integration`

O pacote `internal/testenv` decide o que fazer quando falta infraestrutura:

| Comando | Variável `TEST_*` ausente |
| --- | --- |
| `go test ./...` | O teste é pulado (`t.Skip`), com a mensagem dizendo como ativá-lo |
| `go test -tags=integration ./...` | Usa o endereço do Compose; se o container não estiver de pé, o teste **falha** |

Com a tag, nada é pulado em silêncio: a execução completa reportou 0 testes pulados. Os pacotes de `test/` só compilam com a tag.

### Cenários escritos uma vez, rodados em uma ou três instâncias

`test/e2e` tem o cliente (tokens do Keycloak, chamadas HTTP, envio ao SQS, leitura de `/metrics`) e os cenários. `test/integration` roda cada cenário contra uma instância; `test/multiinstance` roda os mesmos contra três réplicas (`docker compose --profile multi up -d`), alternando as requisições entre elas.

Todo cenário financeiro termina em `AssertFinalState`: saldo esperado, número de débitos e créditos no ledger e reconciliação consistente (saldo armazenado = créditos − débitos).

### Ponto de injeção de falha

`FAULT_INJECTION` faz o processo terminar com código 3 num ponto escolhido:

| Valor | Onde o processo morre |
| --- | --- |
| `exit-after-commit` | Depois do commit da mensagem, antes de apagá-la da fila |
| `exit-after-publish` | Depois de enviar o evento ao SQS, antes de marcá-lo na outbox |

É um decorador Fx (`internal/infra/faults`) em volta do handler do consumidor e do publisher; sem a variável, ele devolve a dependência intacta. O caso de uso não sabe que ele existe.

### Recuperação com processos reais

`test/recovery` compila `cmd/app` e sobe o binário como processo, com schema e filas próprios e o Keycloak de verdade. Assim dá para matar o processo (pela injeção ou com `kill -9`) e subir outro. Os prazos ficam curtos por configuração (`OUTBOX_LEASE`, `REFERENCE_LEASE`, `WAGER_VISIBILITY_TIMEOUT`, `REFERENCE_*`), que também passou a valer para produção, com padrão de 30s.

### Prova de que cada instância participou

Ter três réplicas não basta: o teste precisa mostrar que todas trabalharam. Ele lê `wagering_transactions_total` (por canal) e o novo `wagering_outbox_published_total` no `/metrics` de cada instância antes e depois, e falha se alguma não fez nada.

## Cobertura dos itens

### Integração

| Item | Teste |
| --- | --- |
| Migrations `up`/`down` | `migrations: TestMigrationsApplyRevertAndApplyAgain` |
| Constraints e imutabilidade do ledger | `migrations: TestSchemaEnforcesFinancialInvariants`; `postgres: TestConstraintViolationsBecomeApplicationErrors` |
| Atomicidade | `postgres: TestWithinTxCommitsOrRollsBackEveryRepositoryTogether`; `application: TestOpenWalletIsAllOrNothing` |
| Inbox e reentrega | `test/integration: TestRepeatedSQSDeliveryIsAppliedOnce`; `sqs: TestRedeliveryAfterACrashBetweenCommitAndDeleteDoesNotDebitTwice` |
| Outbox concorrente, retry e DLQ | `test/recovery: TestCompetingPublishersLoseNothingAndKeepEachAggregateInOrder`; `application: TestFailedEventBlocksOnlyItsWalletAndIsRetriedWithBackoff`; `sqs: TestRedrivePolicyDeadLettersAMessageThatKeepsFailing` |
| Recuperação após reinicialização | `test/recovery: TestRestartKeepsIdempotencyPendingWorkAndConsistency` |
| Composição Fx | `app: TestApplicationStartsServesAndStopsWithoutLeakingGoroutines`, `TestStartFailsWhenADependencyIsMissing` |

### Autenticação e autorização (tokens reais, `test/integration/auth_test.go`)

| Item | Resultado verificado |
| --- | --- |
| Token ausente, malformado, assinatura forjada, `alg: none`, expirado | 401, nada gravado, saldo intacto |
| Isolamento entre provedores | Leitura do outro provedor → 404 sem dados; envio em nome do outro → 403; mesma chave usada por outro provedor → transação nova, não replay |
| Operações internas negadas a provedores | 403 em abrir, ler, extrato e reconciliação, sem dados no corpo; a carteira não é criada |

O token expirado é real: o cliente `provider-a-short-lived` do realm emite tokens de 2s. O teste primeiro confirma que o token funciona e só depois espera a expiração.

### Concorrência e recuperação (§13)

| # | Cenário | Teste | Resultado |
| --- | --- | --- | --- |
| 1 | Mesma aposta 50× em paralelo | `SameBetInParallel` | 50 × 200, mesma transação, 1 aplicada, 49 replays, 1 débito |
| 2 | Duas de `80.00` sobre `100.00` | `CompetingBets` | 1 processada, 1 `INSUFFICIENT_FUNDS`, saldo `20.00`, 1 débito; reenvio devolve os mesmos resultados |
| 3 | Carteiras distintas | `DistinctWallets` | 100 apostas em 10 carteiras em ~0,75s, todas aplicadas |
| 4 | 1–3 com 3 instâncias | `test/multiinstance` | Mesmos resultados; cada réplica decidiu ~1/3 das requisições e 60 de 180 mensagens |
| 5 | Morte após commit, antes do delete | `TestConsumerKilledAfterCommitBeforeDeletingTheMessage` | Processo morre com 1 débito e a mensagem ainda na fila; o novo processo reconhece a reentrega pela inbox; 1 débito |
| 6 | Dois publishers na mesma outbox | `TestCompetingPublishersLoseNothingAndKeepEachAggregateInOrder`, `TestPublisherKilledAfterSendingRepublishesTheSameEvent` | 200 eventos, nenhum perdido, ordem por agregado mantida, os dois publicaram; após a morte entre envio e marcação, o evento é reenviado com o mesmo `eventId` |
| 7 | Reversão antes da referência | `TestReversalArrivingBeforeItsReference` | `REFUND` fica `PENDING_REFERENCE` e é processado quando a aposta chega; `ROLLBACK` sem referência termina `REJECTED REFERENCE_NOT_FOUND` |
| 8 | Reinício | `TestRestartKeepsIdempotencyPendingWorkAndConsistency` | `kill -9` com 65 de 100 mensagens tratadas e um refund pendente; depois: replay idêntico, refund resolvido, 100 mensagens aplicadas uma vez, saldo `39.00` |
| 9 | Mesma operação por HTTP e SQS | `SameOperationOverHTTPAndSQS` | Um único débito, em uma e em três instâncias |

## Verificação

- `go test ./...` passa sem infraestrutura (93 testes pulados, com o motivo).
- `go test -tags=integration ./...` passa com o Compose (`--profile multi`), sem nenhum teste pulado.
- `go test -race -tags=integration ./...` passa num container `golang:1.27` na rede do Compose.
- As três suítes caixa-preta passaram 3 vezes seguidas (`-count=3`).
- **Os testes pegam os defeitos** (cada mutação foi desfeita depois):

| Defeito introduzido | Teste que falhou |
| --- | --- |
| Consumidor apaga a mensagem antes do commit | Cenário 5: "messages left in the queue = 0, want 1" |
| Publisher inverte a ordem dentro do agregado | Cenário 6: ordem recebida diferente da gravada |
| Verificador ignora a expiração (`SkipExpiryCheck`) | Token expirado aceito: 200, aposta gravada, saldo 89.00 em vez de 99.00 |

## Limites conhecidos

- O SQS FIFO descarta o reenvio do evento (mesmo `MessageDeduplicationId` em 5 minutos). O teste prova o reenvio pelo contador do publisher (3 envios, um deles repetido), não pela fila.
- O cenário 3 mostra que as carteiras avançam juntas, mas não mede ausência de bloqueio. Isso fica com `postgres: TestWalletLockMakesTheSecondWriterWaitOnlyOnTheSameWallet`.
- A suíte de recuperação leva cerca de 1 minuto: cada teste compila e sobe processos e espera prazos reais (lease e visibility de 3s).

## Para estudar

`testing.T`, `testing.TB`, build tags (`//go:build`), `os/exec`, detector de race, `runtime.Goexit`, Prometheus text format.
