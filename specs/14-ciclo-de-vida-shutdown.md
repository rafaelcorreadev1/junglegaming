# 14 — Ciclo de vida Fx e shutdown

**Desafio:** §4 (composição e ciclo de vida), §10 (`SIGTERM`) · **Depende de:** 10, 11

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
- O Fx já executa os `OnStop` na ordem inversa dos `OnStart`; a ordem de dependência dos construtores garante o item 3.
- Já entregue: o consumidor SQS faz os itens 1 e 2 desde a spec 11 (busca e processamento com contextos separados); falta revisar o conjunto.

## Entregáveis

- Um `fx.Module` por área: `config`, `observability`, `postgres`, `auth`, `sqs`, `application`, `httpapi`, `workers`
- Abstração comum de worker (loop + cancelamento + espera)

## Critérios de aceite

- [ ] Teste de composição: `fx.ValidateApp` passa (grafo de dependências completo).
- [ ] Teste de start/stop: a aplicação sobe e desce sem goroutine sobrando.
- [ ] `SIGTERM` durante o processamento de uma mensagem → ela é concluída ou reentregue, nunca perdida nem duplicada.
- [ ] `docker compose stop` encerra dentro do prazo, sem `SIGKILL`.

## Para estudar

`fx.Lifecycle`, `fx.Hook`, `sync.WaitGroup`, `signal.NotifyContext`, `http.Server.Shutdown`.
