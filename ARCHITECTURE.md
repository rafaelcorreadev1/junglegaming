# Arquitetura

Decisões, interpretações e limitações da solução. Os números `§N` apontam para as seções de [`docs/DESAFIO.md`](docs/DESAFIO.md); cada tema tem uma spec em [`specs/`](specs) com mais detalhes e a verificação feita.

## Visão geral

```
           HTTP (JWT)                       SQS wager-transactions.fifo
               │                                        │
     ┌─────────▼─────────┐                    ┌─────────▼─────────┐
     │  httpapi handlers │                    │   sqs.Consumer    │──► DLQ
     └─────────┬─────────┘                    └─────────┬─────────┘
               │                                        │ inbox
               └──────────► ProcessWagerTransaction ◄───┘
                                     │  uma transação SQL
              ┌──────────┬───────────┼────────────┬─────────────┐
           wallets   wager_tx    ledger      inbox_messages  outbox_events
                                                                  │
                                       worker.Poller (outbox) ────┴──► SQS wager-events.fifo
                                       worker.Poller (referências pendentes)
```

Um binário único roda HTTP, consumidor SQS e dois workers. Várias instâncias compartilham o mesmo PostgreSQL e as mesmas filas; a coordenação entre elas é feita só pelo banco (locks, constraints e reservas com prazo) e pelo SQS. Nenhuma instância guarda estado que outra precise.

### Camadas

| Pacote | Conhece | Não conhece |
| --- | --- | --- |
| `internal/domain` | Só a biblioteca padrão | Fx, HTTP, SQS, banco, relógio, gerador de id |
| `internal/application` | `domain` e as portas que declara | Fx, pgx, AWS, HTTP |
| `internal/infra/*` | `application` e `domain` | — |
| `internal/app`, `cmd/app` | Tudo; é onde o Fx monta o grafo | — |

O domínio recebe ids e o instante por parâmetro, então os testes dele são determinísticos.

## Dinheiro (§5.1, §6.1)

| Decisão | Detalhe |
| --- | --- |
| Representação | `int64` em centavos + código ISO 4217; no banco, `BIGINT` + `CHAR(3)` |
| Ponto flutuante | Nenhum `float` no repositório |
| Escala | Fixa em 2 casas. Moedas aceitas: `ARS`, `AUD`, `BRL`, `CAD`, `COP`, `EUR`, `GBP`, `MXN`, `PEN`, `USD`. `JPY` e outras sem centavos são rejeitadas |
| Limite | `±9.223.372.036.854.775,807` unidades da moeda (`int64` em centavos). Overflow em parsing, soma, subtração e negação vira erro, nunca dá a volta |
| Formas aceitas | `"25"`, `"25.0"`, `"25.00"` → normalizadas para `"25.00"` antes do hash de idempotência |
| Rejeitadas | Mais de 2 casas, `NaN`, `Infinity`, `1e3`, sinal `+`, zeros à esquerda, `"-0.00"`, `".50"`, `"25."`, espaços, separador de milhar, moeda em minúsculas e `amount` como número JSON |
| Negativo | Permitido em cálculo interno (a `difference` da reconciliação); proibido em entrada financeira e em saldo (as entidades exigem valor positivo ou zero, e o banco tem `CHECK`) |
| Moedas diferentes | Soma, subtração e comparação devolvem erro classificável (`errors.Is`) |
| Valor zero da struct | `Money{}` não inicializado é inválido em toda operação e na serialização |

## Banco e transação SQL (§4, §5)

- **Biblioteca:** `pgx/v5` com SQL escrito à mão. Migrations com `golang-migrate`, `.up.sql` e `.down.sql`.
- **Delimitação da transação:** `TxManager.WithinTx(ctx, fn)` abre a transação e a guarda no `context`; os repositórios pegam a transação de lá e, sem ela, usam o pool. Um `WithinTx` dentro de outro entra na transação existente. O caso de uso decide o limite da transação; os repositórios não fazem commit.
- **Tudo ou nada:** saldo, transação, lançamento do ledger, inbox e eventos da outbox são gravados no mesmo commit.
- **Depois de uma violação de constraint** a transação está abortada no PostgreSQL. O caso de uso não continua nela: desfaz e, quando faz sentido, tenta de novo numa transação nova (até 3 vezes).

### Invariantes que o próprio banco garante

| Invariante | Mecanismo |
| --- | --- |
| Saldo nunca negativo | `CHECK (balance_minor >= 0)` |
| Uma carteira por jogador e moeda | `UNIQUE (player_id, currency)` |
| Operação externa única por provedor | `UNIQUE (provider_id, external_transaction_id)` |
| Chave de idempotência única por provedor | `UNIQUE (provider_id, idempotency_key)` |
| Ledger coerente | `CHECK balance_after = balance_before ± amount`; FKs compostas para a moeda da carteira e a carteira da transação |
| Ledger imutável | Triggers que recusam `UPDATE`, `DELETE` e `TRUNCATE` |
| Uma só reversão processada por referência | Índice único parcial em `reference_transaction_id` |
| Mensagem única por consumidor | `UNIQUE (consumer_name, message_id)` |
| Coerência por tipo e estado | `CHECK` de valor por tipo, referência por tipo, `failureCode` só em `REJECTED`/`FAILED`, saldo resultante em `PROCESSED` |

`kind`, `status` e `direction` são `TEXT` + `CHECK`, não `ENUM`: acrescentar um valor a um `CHECK` é reversível numa migration. Não há `DEFAULT now()`: o instante vem da aplicação, o mesmo que o domínio usou.

### Erros de banco

| PostgreSQL | Erro de aplicação | Tratamento |
| --- | --- | --- |
| `23505` em constraint conhecida | `ErrWalletAlreadyExists`, `ErrDuplicateExternalTransaction`, `ErrDuplicateIdempotencyKey`, `ErrAlreadyReversed` | Permanente; nas disputas de inserção, nova tentativa vira replay ou conflito |
| Outras violações da classe `23` | `ErrInvariantViolation` | Permanente (indica bug) |
| `40001`, `40P01` | `ErrUnavailable` + `ErrTransactionConflict` | Transitório |
| Classes `08`, `53`, `57`, erro de rede | `ErrUnavailable` | Transitório |
| `context` cancelado | O próprio erro do `context` | — |

O erro original fica encadeado com `%w`, então o log mostra o SQLSTATE.

## Concorrência e locks (§5, §8)

- **Lock pessimista por carteira:** `SELECT ... FOR UPDATE` na linha da carteira, no início da transação. Só a carteira disputada é serializada; carteiras diferentes avançam em paralelo. Não existe lock global.
- **O lock vem antes da checagem de duplicata.** Duas requisições iguais chegam juntas: a segunda espera o lock e, quando passa, já vê a linha gravada pela primeira.
- **Quando o lock não cobre** (mesma chave de idempotência enviada para carteiras diferentes), o `UNIQUE` decide: o `INSERT` perdedor falha, tudo é desfeito e a nova tentativa responde replay ou conflito.
- **Rede de segurança:** `UpdateBalance` só grava com `WHERE version = nova − 1`. Se algum caminho esquecesse o lock, o resultado seria `ErrConcurrentUpdate`, não uma atualização perdida. `GetForUpdate` fora de transação é erro.
- **Ordem de locks:** sempre carteira → transações, nos caminhos HTTP, SQS e no worker de referências. Isso evita deadlock entre eles.

Resultado verificado com três réplicas: a mesma aposta 50 vezes gera um débito; duas apostas de `80.00` sobre `100.00` geram uma `PROCESSED`, uma `INSUFFICIENT_FUNDS`, saldo `20.00` e um débito.

## Idempotência (§5, §7, §9)

| Situação | Resultado |
| --- | --- |
| Mesma chave e mesmo conteúdo | Resultado gravado, mesmo código HTTP, `idempotentReplay: true`, com o saldo **observado no processamento original** |
| Mesma chave, conteúdo diferente | `409 IDEMPOTENCY_KEY_CONFLICT` |
| Mesmo `(providerId, externalTransactionId)` com outra chave | `409 EXTERNAL_TRANSACTION_CONFLICT` |
| Chave de outro provedor | Operação nova: a chave é única por provedor, e o provedor vem do token |

- **Persistência:** no banco (`wager_transactions`), então sobrevive ao reinício de todas as instâncias. O servidor nunca troca a chave recebida por outra calculada.
- **Hash:** SHA-256 em hexadecimal do JSON canônico (chaves em ordem alfabética, sem espaços) dos campos de negócio: `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e `referenceExternalTransactionId` (quando informado).
- **Fora do hash:** a própria chave, correlation ID, `messageId` e metadados de transporte.
- **Normalização:** só o valor monetário (duas casas). Os demais campos são comparados byte a byte, sem `trim` nem mudança de caixa.
- **HTTP × SQS:** as duas entradas montam a mesma entrada do mesmo caso de uso; a chave é o header `Idempotency-Key` ou `data.idempotencyKey`. Mesma operação pelas duas → um efeito só.
- **Chave:** obrigatória, até 255 caracteres.

## Máquina de estados e falhas

```
PENDING ──────────────► PROCESSED | REJECTED | FAILED   (terminais)
   │
   ▼
PENDING_REFERENCE ────► PROCESSED | REJECTED | FAILED
```

Transição a partir de estado terminal é erro. Rejeição de negócio **não é erro** no domínio: a transação termina `REJECTED`, com `failureCode`, e é gravada.

| Categoria | Exemplo | HTTP | SQS |
| --- | --- | --- | --- |
| Entrada corrigível | Campo ausente, `BET` de valor zero, `REFUND` sem referência, `kind: OPENING`, carteira inexistente | `400`/`404`, nada gravado | DLQ |
| Rejeição de negócio | `INSUFFICIENT_FUNDS`, `PLAYER_MISMATCH` | `422`, gravada | Mensagem removida (resultado terminal) |
| Aguardando referência | `REFUND` antes da `BET` | `202`, gravada | Mensagem removida; o worker continua |
| Conflito | Chave reutilizada com outro conteúdo | `409` | DLQ |
| Transitória | Banco fora, deadlock | `503` + `Retry-After: 1` | Volta para a fila com backoff |
| Erro inesperado | Bug | `500`, causa só no log | Tratada como transitória até o `maxReceiveCount`, depois DLQ |

### `failureCode`

| Código | Quando |
| --- | --- |
| `INSUFFICIENT_FUNDS` | `BET` sem saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | `ROLLBACK` que debitaria mais que o saldo |
| `BALANCE_LIMIT_EXCEEDED` | Crédito que passaria do limite representável |
| `PLAYER_MISMATCH` | A carteira pertence a outro jogador |
| `CURRENCY_MISMATCH` | Moeda diferente da carteira |
| `REFERENCE_NOT_FOUND` | A referência não chegou até expirar |
| `REFERENCE_NOT_PROCESSED` | A referência terminou `REJECTED`/`FAILED`, ou continuava pendente na expiração |
| `REFERENCE_MISMATCH` | Diverge em provedor, jogador, carteira, moeda ou rodada |
| `REFERENCE_AMOUNT_MISMATCH` | Valor da reversão diferente do referenciado |
| `REFERENCE_KIND_NOT_ALLOWED` | Tipo da referência inválido para a operação |
| `ALREADY_REVERSED` | A referência já tem uma reversão processada |
| `PROCESSING_FAILED` | Reservado para falha permanente de infraestrutura (`FAILED`) |

## Operações e reversões (§7)

| Tipo | Movimento | Valor | Referência |
| --- | --- | --- | --- |
| `BET` | Débito | > 0, saldo suficiente | Proibida |
| `WIN` | Crédito | > 0 | Opcional: uma `BET` |
| `LOSS` | Nenhum | Exatamente `0.00` | Proibida |
| `REFUND` | Crédito | Igual ao da `BET` | Obrigatória: `BET` |
| `ROLLBACK` | Contrário ao original | Igual ao original | Obrigatória: `BET`, `WIN` ou `REFUND` |

Operação e referência precisam coincidir em provedor, jogador, carteira, moeda e rodada. Não há reversão parcial.

**Interpretação adotada:** uma transação aceita **uma única** reversão processada, de qualquer tipo. O desafio proíbe duas do mesmo tipo; esta regra também impede devolver duas vezes o mesmo débito.

| Sequência | Resultado |
| --- | --- |
| `REFUND(BET)` e depois `ROLLBACK(BET)` | `ROLLBACK` rejeitado com `ALREADY_REVERSED` |
| `ROLLBACK(BET)` e depois `REFUND(BET)` | `REFUND` rejeitado com `ALREADY_REVERSED` |
| `REFUND(BET)` e depois `ROLLBACK(REFUND)` | Aceito: debita de novo. A `BET` volta a valer, mas não aceita nova reversão |
| `ROLLBACK(ROLLBACK)` | `REFERENCE_KIND_NOT_ALLOWED` |

Dois `REFUND` distintos da mesma `BET` em paralelo: um `PROCESSED`, outro `ALREADY_REVERSED` (garantido pelo índice único parcial, mesmo entre instâncias).

## Referências pendentes (§7)

| Estado da referência | Comportamento |
| --- | --- |
| Não existe | `PENDING_REFERENCE` + evento `WagerTransactionPendingReference`; HTTP `202` |
| `PENDING` ou `PENDING_REFERENCE` | Continua aguardando |
| `PROCESSED` | Aplica a operação |
| `REJECTED` ou `FAILED` | `REJECTED` com `REFERENCE_NOT_PROCESSED` |

- **Estado da espera no banco** (`reference_attempts`, `next_reference_attempt_at`, `correlation_id`), então sobrevive a reinício.
- **Backoff:** `min(base × 2^(n−1), máximo)`. Padrão: 1, 2, 4, 8, 16, 32, 60, 60, 60s; com 10 tentativas, cerca de 4 minutos até `REJECTED` com `REFERENCE_NOT_FOUND`.
- **Disputa entre instâncias:** um `UPDATE` com `FOR UPDATE SKIP LOCKED` reserva as pendências vencidas, empurrando `next_reference_attempt_at` para agora + `REFERENCE_LEASE`. Cada uma é resolvida numa transação própria, que trava a carteira e relê a transação. Se a instância morre, a reserva vence e outra assume.
- **Despertar:** quando uma transação chega a estado final, a mesma transação SQL antecipa as pendências que a referenciam. O `REFUND` é concluído no próximo ciclo do worker, sem esperar o backoff.
- **Interpretação:** depois de expirada, a transação não é reaberta se a referência chegar tarde, porque a rejeição já foi informada ao provedor.

## Contrato HTTP (§9)

| Método e rota | Quem | Sucesso |
| --- | --- | --- |
| `POST /wallets` | `internal` | `201` + `Location` |
| `GET /wallets/{walletId}` | `internal` | `200` |
| `GET /wallets/{walletId}/ledger?cursor=&limit=` | `internal` | `200`, `items` do mais antigo ao mais novo, `nextCursor` (`null` na última página) |
| `POST /wallets/{walletId}/reconciliation` | `internal` | `200` |
| `POST /wagering/transactions` | `provider` | `200` / `202` / `422` |
| `GET /wagering/transactions/{transactionId}` | `provider` (próprias) ou `internal` | `200` |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | `provider` (próprias) ou `internal` | `200` |
| `GET /health/live`, `GET /health/ready`, `GET /metrics` | Público | `200` (`ready` dá `503` se uma dependência falha) |

- **Erro:** `{"error":{"code":"...","message":"...","field":"..."}}`; `field` aparece só em erro de validação.
- **Códigos de erro:** `INVALID_REQUEST` (400), `UNAUTHENTICATED` (401), `FORBIDDEN` (403), `WALLET_NOT_FOUND`/`TRANSACTION_NOT_FOUND` (404), `WALLET_ALREADY_EXISTS`/`IDEMPOTENCY_KEY_CONFLICT`/`EXTERNAL_TRANSACTION_CONFLICT` (409), `SERVICE_UNAVAILABLE` (503 + `Retry-After: 1`), `INTERNAL_ERROR` (500).
- **Corpo JSON estrito:** campo desconhecido, dois objetos ou mais de 1 MB → `400`. `walletId` e `transactionId` precisam ser UUID.
- **Abertura:** saldo inicial positivo cria, no mesmo commit, a carteira (versão `1`), uma transação `OPENING` processada, um crédito no ledger e os eventos `WagerTransactionProcessed` e `WalletBalanceChanged`. Saldo zero cria só a carteira.
- **Extrato:** cursor opaco (base64url de `v1:<sequence>`), ordenado pela coluna `sequence` do ledger. `limit` padrão 50, máximo 200.
- **Saldo da resposta:** é o observado quando a operação foi decidida; omitido enquanto aguarda referência.
- **`X-Correlation-Id`:** aceito do cliente ou gerado; devolvido na resposta, gravado nos eventos e presente nos logs.

## Reconciliação (§9)

`calculatedBalance` = créditos − débitos do ledger, incluindo a abertura; `difference` = armazenado − calculado. A leitura acontece numa transação `REPEATABLE READ READ ONLY`, então saldo e ledger vêm do mesmo instante, sem travar as escritas. Com `READ COMMITTED`, o teste de concorrência acusou divergências falsas. Uma divergência sai em três lugares: na resposta, num log `ERROR` e na métrica `wagering_reconciliations_total{result="divergent"}`. A reconciliação não corrige nada.

## Outbox e eventos (§6.5, §11)

1. O caso de uso grava o evento em `outbox_events` **na mesma transação** da mudança. Nenhum evento sai antes do commit.
2. `worker.Poller` publica os pendentes no SQS e marca `published_at`.

**Reserva por agregado:** com vários publishers, reservar eventos soltos deixaria o evento 2 de uma carteira sair antes do 1. Um único `UPDATE ... RETURNING` com CTEs faz duas coisas:
- trava, com `FOR UPDATE SKIP LOCKED`, só o evento não publicado mais antigo de cada carteira;
- reserva (`locked_until = agora + OUTBOX_LEASE`) os eventos daquelas carteiras.

Outro publisher não consegue pegar eventos posteriores da mesma carteira.

| Situação | O que acontece |
| --- | --- |
| Envio aceito | Marca publicado |
| Envio falhou | `attempts + 1`, `last_error`, backoff de 1s até 1min, sem limite de tentativas; os eventos seguintes da carteira esperam, as outras carteiras seguem |
| Enviado, mas a marcação falhou ou o processo morreu | A reserva vence e o evento é reenviado com o **mesmo `eventId`** |

A entrega é **at-least-once**; quem consome deduplica por `eventId`.

### Contrato dos eventos

Fila `wager-events.fifo`. `MessageGroupId` = `aggregateId` (o `walletId`, para manter a ordem por carteira), `MessageDeduplicationId` = `eventId`, atributos `eventType` e `eventId`.

```json
{
  "eventId": "uuid", "eventType": "WalletBalanceChanged", "aggregateId": "<walletId>",
  "correlationId": "...", "causationId": "<transactionId>", "occurredAt": "2026-10-14T12:00:00Z", "version": 1,
  "data": { "walletId": "...", "transactionId": "...", "direction": "DEBIT", "money": {...},
            "balanceBefore": {...}, "balanceAfter": {...}, "walletVersion": 2 }
}
```

| `eventType` | Quando | `data` |
| --- | --- | --- |
| `WagerTransactionProcessed` | Operação processada (inclui `LOSS` e `OPENING`) | Snapshot da transação + `balance` |
| `WagerTransactionRejected` | Rejeição definitiva | Snapshot + `failureCode` + `balance` (quando observado) |
| `WalletBalanceChanged` | Saldo alterado | Como no exemplo |
| `WagerTransactionPendingReference` | Aguardando referência | Snapshot da transação |

O snapshot traz `transactionId`, `walletId`, `playerId`, `kind`, `money` e, nas operações externas, `providerId`, `externalTransactionId`, `roundId`, `gameId` e `referenceExternalTransactionId`. Os timestamps são UTC em RFC 3339 e o dinheiro é string decimal.

## Consumidor SQS e inbox (§6.5, §10)

- **Mesmo caso de uso do HTTP**, com inbox na mesma transação: `INSERT ... ON CONFLICT DO NOTHING` em `inbox_messages (consumer_name, message_id, hash)`.
  - Reentrega com o mesmo hash → nada é reprocessado; a mensagem só é removida.
  - Mesmo `messageId` com outro corpo → erro permanente (DLQ).
  - O hash da inbox é o SHA-256 do corpo canônico, então a mesma mensagem com outra formatação não vira conflito.
- **Remoção só depois do commit.** Se o processo morre entre os dois, a reentrega é reconhecida pela inbox.
- **Envelope estrito:** `messageId`, `type = WagerTransactionRequested`, `occurredAt`, `data` e `data.money` são obrigatórios, e campo desconhecido é erro. `correlationId` é opcional, no envelope ou como atributo.

| Resultado | Ação |
| --- | --- |
| Processada, rejeição de negócio, `PENDING_REFERENCE`, já tratada | `DeleteMessage` |
| Envelope inválido, provedor fora de `WAGER_QUEUE_PROVIDERS`, validação, carteira inexistente, conflitos, violação de invariante | Envio explícito para a DLQ com o atributo `failureReason`, depois remove da origem |
| Falha transitória ou desconhecida | `ChangeMessageVisibility` com backoff de 2s × 2ⁿ⁻¹ até 5min, pelo `ApproximateReceiveCount` |
| Esgotou 5 recebimentos | DLQ pela redrive policy |
| Interrompida pelo shutdown | Visibilidade 0: volta para a fila na hora |

| Parâmetro | Valor |
| --- | --- |
| `MessageGroupId` | `walletId`: ordem por carteira, paralelismo entre carteiras |
| `MessageDeduplicationId` | `messageId` |
| Visibility timeout no receive | `WAGER_VISIBILITY_TIMEOUT`, 30s |
| Lote e long polling | 10 mensagens e 20s (máximos do SQS) |
| `maxReceiveCount` | 5 (cobre cerca de 1 minuto de banco fora) |
| Retenção da DLQ | 14 dias |

Dentro de um lote, carteiras diferentes rodam em paralelo e as mensagens de uma carteira rodam em sequência. Se uma falha, as seguintes do mesmo grupo voltam com o mesmo atraso. O motivo: no LocalStack uma mensagem posterior foi entregue enquanto a anterior do grupo estava invisível.

A deduplicação do FIFO dura só 5 minutos e exige o mesmo `MessageDeduplicationId`. A inbox não tem prazo, e os testes de duplicidade mandam cada cópia com um id de deduplicação novo, para provar a deduplicação da aplicação.

## Autenticação e autorização (§2)

- **IdP:** Keycloak, provisionado no `docker compose up` a partir de [`deploy/keycloak/wagering-realm.json`](deploy/keycloak/wagering-realm.json). Fluxo `client_credentials`, sem senha de usuário nem emissão própria de token.
- **Validação** (`go-oidc`): assinatura `RS256` pelo JWKS (em cache, buscado de novo quando aparece um `kid` desconhecido), além de `iss`, `aud` e `exp`. As chaves são conferidas também na subida: sem chave, a aplicação não sobe.
- **Duas URLs:** `AUTH_ISSUER` é o `iss` fixo (`KC_HOSTNAME=http://localhost:8081`); `AUTH_JWKS_URL` busca as chaves pela rede do Docker.

| Papel (`realm_access.roles`) | Clients | Pode |
| --- | --- | --- |
| `provider` + claim `provider_id` | `provider-a`, `provider-b` | Enviar operações (o `providerId` do corpo precisa ser o do token) e consultar as próprias |
| `internal` | `wallet-service` | Carteiras, extrato, reconciliação, qualquer transação |

| Situação | Resposta |
| --- | --- |
| Sem token, malformado, assinatura inválida, `alg: none`, expirado, `iss`/`aud` errados | `401` + `WWW-Authenticate: Bearer` |
| Sem o papel exigido, ou `providerId` de outro provedor no corpo | `403`, nada gravado |
| Transação de outro provedor | `404`, corpo idêntico ao de inexistente |

- **Isolamento em replay:** a chave de idempotência é única por provedor, e o provedor vem do token.
- **Filas:** a política de acesso fica gravada em cada fila: provedores só enviam, o serviço consome e manda para a DLQ, operadores inspecionam a DLQ e o `wallet-service` lê os eventos. O LocalStack não aplica essas políticas; por isso o consumidor valida o provedor e todas as regras de domínio, como no HTTP.

## Fx e ciclo de vida (§4)

| Módulo | Conteúdo |
| --- | --- |
| `fx.Supply(cfg)` | Configuração, validada antes de o Fx existir |
| `observability` | Logger JSON, registro Prometheus, métricas |
| `system` | Relógio e gerador de UUID v7 |
| `postgres` | Pool, `TxManager`, repositórios |
| `sqs` | Cliente, publisher, consumidor, checagem das filas |
| `worker` | Resolvedor de referências, publicador da outbox, consumidor |
| `httpapi` | Verificador de tokens, handlers, servidor |
| Raiz (`app.Options`) | Casos de uso, sondas de readiness (value group), decoradores (métricas, injeção de falha) |

Os casos de uso são registrados na raiz porque `application` não importa Fx. Os decoradores (`fx.Decorate`) também ficam na raiz: um decorador dentro de um módulo só vale para aquele módulo, e o Fx decora cada tipo uma vez por escopo.

**Start:** o Fx roda os `OnStart` na ordem de registro.
1. A configuração é validada.
2. O Postgres responde ao `Ping`.
3. As três filas existem.
4. O JWKS tem chaves.
5. Só então o servidor HTTP passa a escutar.

Se algo falha, o Fx desfaz o que subiu e o processo termina com erro, sem nunca abrir a porta.

**Stop** (`SIGTERM`, prazo `SHUTDOWN_TIMEOUT`): a ordem inversa do start.
1. O HTTP para de aceitar e espera as requisições em andamento (`Shutdown`).
2. Os workers param em duas fases (`internal/infra/lifecycle`). Primeiro nenhuma rodada ou receive novo começa. O que está em andamento tem até 3s antes do prazo; depois disso é cancelado: a transação é desfeita e a mensagem volta com visibilidade 0. O stop só retorna quando a goroutine terminou de fato.
3. O pool do Postgres fecha e as conexões HTTP ociosas (SQS e JWKS) são fechadas.

Cada rodada de worker tem prazo igual ao lease, para não continuar trabalhando em algo que outra instância já assumiu. Um teste sobe a aplicação inteira, para e confere com `goleak` que nenhuma goroutine sobra.

## Observabilidade (§12)

- **Logs** JSON (`log/slog`). Campos guardados no `context` e adicionados a todo log: `correlationId`, `clientId`, `walletId`, `providerId`, `transactionId`, `messageId`, `sqsMessageId`. Uma linha de access log por requisição, com a rota no padrão do chi. Headers, tokens e corpos nunca são registrados (há teste para isso).
- **Métricas** em `/metrics`:

| Métrica | O que mede |
| --- | --- |
| `wagering_transactions_total{channel,kind,status}` | Transações decididas |
| `wagering_duplicates_total` | Replays e reentregas já na inbox |
| `wagering_retries_total{component}` | Retentativas |
| `wagering_dlq_messages_total{reason}` | Mensagens mandadas para a DLQ |
| `wagering_concurrency_conflicts_total{reason}` | Conflitos de concorrência |
| `wagering_outbox_published_total` | Eventos publicados |
| `wagering_outbox_oldest_pending_age_seconds` | Idade do evento pendente mais antigo |
| `wagering_processing_duration_seconds{channel}` | Latência de processamento |
| `wagering_http_requests_total`, `wagering_http_request_duration_seconds` | Requisições HTTP |
| `wagering_reconciliations_total{result}` | Reconciliações por resultado |

  Os rótulos têm cardinalidade baixa: nunca ids.
- **Health:** `/health/live` não consulta nada. `/health/ready` consulta Postgres e as três filas em paralelo, com limite de 2s.

## Testes (§13)

| Camada | Onde | Contra |
| --- | --- | --- |
| Unitário | `internal/...` | Nada externo |
| Componente | `internal/...` | PostgreSQL e LocalStack reais (schema e filas por teste) |
| Caixa-preta | `test/integration`, `test/multiinstance`, `test/recovery` | O serviço rodando, por HTTP e SQS, com tokens reais |

Os testes que precisam de infraestrutura ficam atrás da build tag `integration`. Sem a tag, são pulados quando a variável `TEST_*` falta. Com a tag, usam o Compose e falham se ele não estiver de pé. Os nove cenários de concorrência e recuperação estão mapeados um a um em [`specs/15-testes.md`](specs/15-testes.md). A recuperação mata processos reais através de `FAULT_INJECTION`, um decorador Fx que é inerte sem a variável.

## Limitações, interpretações e trabalho não concluído

### Interpretações

- Uma única reversão processada por transação, de qualquer tipo.
- Reversão expirada não é reaberta se a referência chegar depois.
- `WIN` com referência espera a `BET` como uma reversão espera.
- Provedor não tem vínculo com carteira: qualquer provedor autenticado aposta em qualquer carteira, desde que o `playerId` confira. O desafio não define essa relação.
- Evento publicado sem limite de tentativas: evento confirmado não pode ser descartado.

### Limitações conhecidas

- **Ordem por carteira:**
  - Uma carteira com evento falhando na publicação fica parada até ele sair; as outras seguem.
  - No consumidor, mensagens que voltam porque uma anterior do grupo falhou também gastam recebimentos, e numa falha longa podem ir para a DLQ junto.
- **Shutdown:**
  - O prazo de parada é um só para todos os `OnStop`, em sequência; uma requisição HTTP lenta reduz o tempo dos workers. Nada se perde, mas o trabalho é refeito.
  - Mensagens recebidas no instante do `SIGTERM`, antes do tratamento, só voltam depois do visibility timeout.
- **Keycloak fora do ar** com um token de chave ainda não vista responde `401`, não `503`.
- **Vazão do consumidor:** um consumidor por processo, lote a lote; a vazão cresce com mais instâncias.
- **Recursos não incluídos:**
  - `/metrics` é público; numa implantação real ficaria numa porta interna.
  - Não há tracing (OpenTelemetry) nem dashboards, que o desafio deixa como opcionais.
  - Não há reconciliação periódica de todas as carteiras: é uma por chamada.
- **Logs dos workers:** os workers de referência e de outbox tratam lotes, então seus logs não trazem `correlationId` (ele está no evento e na transação).
- **LocalStack:** não aplica as políticas das filas e não as guarda entre reinícios.

### Não concluído

Nenhum item obrigatório do enunciado ficou de fora. `PROCESSING_FAILED` / `FAILED` existe no modelo, no banco e nos contratos, mas nenhum caminho atual leva a ele: toda falha de infraestrutura é tratada como transitória e repetida (HTTP `503`, SQS com backoff e DLQ).
