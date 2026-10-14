# 00 — Visão geral e decisões de base

Enunciado completo: [`docs/DESAFIO.md`](../docs/DESAFIO.md). Os números `§N` nas specs apontam para as seções dele.

## O que é o sistema

Um serviço que movimenta carteiras de jogadores a partir de operações de provedores de jogos (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`), recebidas por **HTTP** e por **SQS**, com o mesmo resultado financeiro nas duas entradas, mesmo com várias instâncias rodando e com falhas no meio do processamento.

## Mapa de specs

| # | Spec | Seções do desafio | Depende de |
| --- | --- | --- | --- |
| 01 | Bootstrap: módulo Go, Fx, config, Docker Compose | §4, §15 | — |
| 02 | Money | §5.1, §6.1 | 01 |
| 03 | Domínio: Wallet, Ledger, WagerTransaction, erros | §6, §6.2–6.4, §7 | 02 |
| 04 | Schema e migrations | §4, §5, §6.3–6.5 | 03 |
| 05 | Persistência e transação SQL | §4, §5, §8 | 04 |
| 06 | Carteira: abertura, leitura, ledger paginado | §9 | 05 |
| 07 | Operação: BET/WIN/LOSS + idempotência | §7, §8, §9 | 06 |
| 08 | Reversões e referências pendentes | §7 | 07 |
| 09 | Autenticação e autorização | §2 | 06 |
| 10 | Outbox e eventos | §6.5, §11 | 07 |
| 11 | Consumidor SQS, inbox e DLQ | §6.5, §10 | 07, 10 |
| 12 | Reconciliação | §9 | 06 |
| 13 | Observabilidade | §12, §9 (health) | 01 |
| 14 | Ciclo de vida Fx e shutdown | §4 | 10, 11 |
| 15 | Testes de integração, concorrência e recuperação | §8, §13 | todas |
| 16 | Documentação e entrega | §15 | todas |

## Decisões de base

| Tema | Decisão | Motivo |
| --- | --- | --- |
| Roteador HTTP | `chi` | Compatível com `net/http`, só adiciona rotas com parâmetro e middlewares |
| Banco | `pgx/v5` com SQL explícito | Preferência declarada no §4; locks e transações ficam visíveis |
| Migrations | `golang-migrate` (arquivos `.up.sql` / `.down.sql`) | Aplicação e reversão exigidas no §4 |
| Money | `int64` em centavos + moeda; `BIGINT` + `CHAR(3)` no banco | Sem float, sem dependência externa; overflow tratado à mão |
| Concorrência | Lock pessimista por carteira (`SELECT ... FOR UPDATE`) + `CHECK (balance >= 0)` | Serializa só a carteira disputada; a constraint é a rede de segurança |
| IDs | UUID v7 | Mesmo formato dos exemplos; ordenável por tempo |
| IdP | Keycloak, `client_credentials`, JWT validado por JWKS | Recomendação do §2 |
| Mensageria | `aws-sdk-go-v2` + LocalStack | §4 |
| Logs / métricas | `log/slog` em JSON / Prometheus em `/metrics` | Biblioteca padrão para logs; formato de métrica mais comum |
| Processo | Um binário único (HTTP + workers); 3 réplicas no Compose | Atende "três processos independentes" do §8 |

## Estrutura de pacotes

```
cmd/app/                  ponto de entrada (main)
internal/domain/          regras puras: Money, Wallet, WagerTransaction, Ledger, eventos, erros
internal/application/     casos de uso + interfaces (ports) que a infra implementa
internal/infra/postgres/  repositórios, transação SQL
internal/infra/httpapi/   handlers, middlewares, contratos JSON
internal/infra/sqs/       consumidor, publisher
internal/infra/auth/      validação de JWT
internal/infra/worker/    workers de referências pendentes e outbox
internal/infra/lifecycle/ parada em duas fases dos workers
internal/infra/observability/
internal/infra/system/    relógio e gerador de UUID v7
internal/infra/faults/    injeção de falha para os testes de recuperação
internal/app/             composição Fx
internal/config/
internal/testdb/, internal/testenv/   apoio aos testes com infraestrutura real
migrations/
deploy/                   realm do Keycloak, script de filas do LocalStack
test/                     suítes caixa-preta (build tag integration)
```

Regra de dependência (§4): `domain` não importa nada de fora da biblioteca padrão. `application` importa `domain`. `infra` importa os dois. Só `cmd/app` e os módulos Fx conhecem o Fx.

## Paralelos com o que você já conhece

| Go | C# / TypeScript |
| --- | --- |
| `go.mod` / `go.sum` | `.csproj` / `package.json` + lockfile |
| `internal/` | `internal` do C#: o compilador impede import de fora do módulo |
| `interface` implícita | Interface do TS (estrutural): não existe `implements` |
| `error` como valor de retorno | Não há exceção; todo erro é retornado e checado com `if err != nil` |
| `context.Context` | `CancellationToken` |
| goroutine + channel | `Task` + `Channel<T>` |
| Uber Fx | Container de DI (`IServiceCollection`, NestJS modules) |
| `defer` | `finally` / `using` |
