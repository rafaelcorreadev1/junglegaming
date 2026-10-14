# 14 — Ciclo de vida Fx e shutdown

**Desafio:** §4 (composição e ciclo de vida), §10 (`SIGTERM`) · **Depende de:** 10, 11 · **Status:** concluída

## Objetivo

Subir e encerrar o processo de forma previsível, sem perder nem duplicar trabalho.

## Componentes gerenciados

Servidor HTTP, consumidor SQS, publisher da outbox, worker de referências pendentes, pool do Postgres, cliente SQS.

## Requisitos

- Tudo composto por `fx.Module` / `fx.Provide` / `fx.Invoke`; start e stop via `fx.Lifecycle`.
- **Start:** valida config e dependências (ping no banco, filas existentes) antes de aceitar trabalho.
- **Workers:** cada um tem `context` cancelável, prazo por execução e término observável (o shutdown espera a goroutine terminar de fato).
- **Stop, nesta ordem:**
  1. Para de aceitar entrada nova (HTTP para de escutar; consumidor para de buscar mensagens).
  2. Conclui o trabalho em andamento dentro do prazo, ou libera (devolve visibilidade da mensagem, solta reserva da outbox).
  3. Fecha as dependências (pool, clientes) depois que quem as usa terminou.

## Como foi implementado

### Composição

| Módulo | Conteúdo |
| --- | --- |
| `fx.Supply(cfg)` | Configuração, já validada por `config.Load` antes do Fx existir |
| `observability` | Logger, registro Prometheus, métricas, relator da reconciliação |
| `system` | Relógio e gerador de ids |
| `postgres` | Pool, `TxManager`, repositórios |
| `sqs` | Cliente, publisher, consumidor, checagem das filas |
| `worker` | Resolvedor de referências, publisher da outbox, consumidor |
| `httpapi` | Verificador de tokens, handlers, roteador, servidor |
| raiz (`app.go`) | Casos de uso, sondas de readiness (value group), decoradores |

Os casos de uso são registrados na raiz, não num módulo próprio: o pacote `application` não importa Fx, e o domínio não conhece nem Fx nem HTTP, SQS ou pgx. A autenticação fica dentro de `httpapi`, o único lugar que a usa.

### Start: nada é aceito antes de as dependências responderem

| Dependência | Checagem no `OnStart` |
| --- | --- |
| Configuração | `config.Load` falha antes de criar a aplicação |
| PostgreSQL | `Ping` no pool |
| SQS | `GetQueueAttributes` nas três filas: fila inexistente impede a subida |
| Keycloak | Busca o JWKS e exige pelo menos uma chave |

O servidor HTTP é o **último** a subir: só passa a escutar quando todo o resto já subiu. Se uma checagem falha, o Fx desfaz o que já tinha subido e o processo termina com erro; o teste confere que a porta HTTP nem chega a abrir.

### Stop: a ordem vem da ordem de subida

O Fx executa os `OnStop` na ordem inversa dos `OnStart`. Registrar o módulo `worker` antes do `httpapi` produz exatamente a ordem pedida:

1. **HTTP:** `server.Shutdown` para de aceitar conexões e espera as requisições em andamento; em seguida fecham as conexões ociosas com o JWKS, que só o HTTP usa.
2. **Workers** (consumidor, publisher da outbox, resolvedor de referências): param de buscar e terminam o que estão fazendo.
3. **Dependências:** pool do Postgres fechado, depois as conexões ociosas do SQS.

O teste da aplicação lê a ordem dos `OnStop` no log do Fx e confere HTTP → workers → pool. Invertendo os módulos, ele falha.

### Workers: parada em duas fases (`internal/infra/lifecycle`)

`lifecycle.Attach(lc, loop)` é a abstração comum. O loop recebe dois contextos:

| Contexto | Cancelado | Efeito |
| --- | --- | --- |
| `pollCtx` | Assim que o stop começa | Nenhuma rodada nova, nenhum receive novo |
| `workCtx` | 3s antes do prazo de parada | Interrompe o que ainda está em andamento |

O `OnStop` só retorna quando a goroutine do loop termina de fato (canal `done`); se ela ignorar os dois contextos, o stop devolve erro em vez de fingir que acabou. O consumidor SQS (spec 11) e o `Poller` usam o mesmo mecanismo.

O que acontece com o trabalho interrompido:

| Worker | Interrompido no meio |
| --- | --- |
| Consumidor | Transação desfeita, mensagem devolvida com visibilidade 0 |
| Publisher da outbox | Falhas e eventos não tentados são liberados com contexto desligado do cancelamento (spec 10) |
| Resolvedor de referências | Transação desfeita; a reserva vence em 30s e outra instância assume |
| HTTP | `Shutdown` espera. Se o prazo acabar, o processo termina: transação não confirmada é desfeita pelo banco; se já tinha confirmado, o cliente reenvia com a mesma chave e recebe o replay |

### Prazo por rodada

Cada rodada de um `Poller` roda com `context.WithTimeout` igual ao **lease** do worker (30s). Uma rodada mais longa que o lease poderia se sobrepor a outra instância que já assumiu o mesmo trabalho.

### Recursos fechados no stop

O cliente SQS e o verificador de tokens ganharam um `http.Transport` próprio, e o stop fecha as conexões ociosas. Sem isso, as goroutines de keep-alive do `net/http` continuam vivas depois do stop. O `goleak` acusou exatamente isso quando tirei o fechamento.

## Entregáveis

- `internal/infra/lifecycle/background.go`: parada em duas fases
- `internal/infra/worker/poller.go`: dois contextos e prazo por rodada
- Checagens de start: `sqs.checkQueuesOnStart`, `auth.Verifier.CheckKeys`
- `internal/app/lifecycle_test.go`: a aplicação inteira contra Postgres, LocalStack e um JWKS de teste

## Critérios de aceite

- [x] Teste de composição: `fx.ValidateApp` passa (grafo de dependências completo).
- [x] Teste de start/stop: a aplicação sobe e desce sem goroutine sobrando.
- [x] `SIGTERM` durante o processamento de uma mensagem → ela é concluída ou reentregue, nunca perdida nem duplicada.
- [x] `docker compose stop` encerra dentro do prazo, sem `SIGKILL`.

## Verificação

- **`goleak`:** a aplicação inteira sobe, responde `/health/ready`, para em menos de 1s e não deixa goroutine. Tirando o fechamento das conexões do SQS, o teste aponta as goroutines `persistConn`.
- **Start com dependência faltando:** fila inexistente ou JWKS sem chaves → `Start` falha e a porta HTTP não abre.
- **`SIGTERM` com 300 mensagens na fila:** parei a aplicação quando 70 tinham sido processadas. O lote em andamento terminou, a parada levou cerca de 1s com código de saída 0 (sem `SIGKILL`), e depois do restart as 300 foram aplicadas uma vez cada: saldo 700.00 e reconciliação consistente.
- **`SIGTERM` com requisições HTTP em sequência:** cada uma das 17 respostas `200` virou exatamente um débito; depois do stop o cliente recebe conexão recusada.

## Limites conhecidos

- O prazo de parada (`SHUTDOWN_TIMEOUT`, 20s) é um só para todos os `OnStop`, em sequência. Se o HTTP gastar quase tudo esperando uma requisição lenta, os workers ficam com pouco tempo e interrompem o que estão fazendo. Nada se perde, mas o trabalho é refeito.
- Mensagens recebidas no instante do `SIGTERM`, antes de o tratamento começar, voltam só depois do visibility timeout (spec 11).
- A disputa entre processos diferentes fica para a spec 15.

## Para estudar

`fx.Lifecycle`, `fx.Hook`, `sync.WaitGroup`, `signal.NotifyContext`, `http.Server.Shutdown`, `goleak`.
