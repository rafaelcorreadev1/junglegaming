# 11 — Consumidor SQS, inbox e DLQ

**Desafio:** §2 (política do broker), §3, §6.5, §10 · **Depende de:** 07, 10 · **Status:** concluída

## Objetivo

Receber `WagerTransactionRequested` pela fila com as mesmas garantias do HTTP, tolerando reentrega.

## Filas

- `wager-transactions.fifo` (entrada)
- `wager-transactions-dlq.fifo` (mensagens que esgotaram tentativas), com redrive configurado.

## Requisitos

- Chama o **mesmo** caso de uso do HTTP. A chave de idempotência é `data.idempotencyKey`.
- **Inbox:** grava `(consumerName, messageId)` + hash do corpo, na mesma transação SQL das mudanças de domínio, ledger e outbox.
  - Reentrega com mesmo hash e já concluída → não reprocessa, só remove da fila.
  - Reentrega com mesmo `messageId` e hash diferente → erro permanente.
- Remover a mensagem da fila **só depois** do commit.
- `PENDING_REFERENCE` persistido conta como tratamento concluído: a mensagem é removida e o worker de referências continua.

| Resultado | Ação na fila |
| --- | --- |
| Processada | Remove |
| Rejeição de negócio confirmada | Remove (é terminal) |
| Falha transitória (banco fora) | Não remove; reentrega com backoff via visibility timeout |
| Mensagem inválida / erro permanente | Vai para a DLQ |
| Tentativas esgotadas (`maxReceiveCount`) | DLQ pelo redrive |

- `SIGTERM`: para de buscar mensagens, termina o que está em andamento dentro do prazo, ou devolve a visibilidade para reentrega.
- O consumidor valida o `providerId` e as regras de domínio mesmo com a fila protegida por política.

## Como foi implementado

### Caso de uso com inbox (`internal/application/handle_wager_message.go`)

Numa única transação SQL:

1. `INSERT ... ON CONFLICT DO NOTHING` em `inbox_messages` com `(consumerName, messageId, hash, received_at)`.
2. Se a linha já existia: hash diferente → `ErrMessageConflict`; hash igual → `AlreadyHandled`, nada é reprocessado.
3. Se é a primeira entrega: roda os mesmos passos do caso de uso HTTP (trava a carteira, idempotência por `(providerId, idempotencyKey)`, regras, ledger, outbox).
4. Marca `completed_at`.

Se qualquer passo falha, o rollback leva junto a linha da inbox, e a próxima entrega tenta do zero. Como recebimento e conclusão estão na mesma transação, nunca se vê uma linha recebida e não concluída.

O laço que repete a transação quando duas requisições disputam a mesma chave única (spec 07) virou `withinTxRetryingRaces`, compartilhado pelos dois caminhos: depois de uma violação de unicidade a transação está abortada, então a nova tentativa começa uma transação nova, com a inbox incluída.

**Mesma operação por HTTP e SQS:** as duas entradas calculam o mesmo hash de negócio (spec 07) e usam a mesma chave; quem chega depois pega o lock da carteira, encontra a operação gravada e devolve replay. Uma movimentação só.

### Envelope e hash (`internal/infra/sqs/message.go`)

- O envelope é validado na entrada: `messageId`, `type = WagerTransactionRequested`, `occurredAt`, `data` e `data.money` obrigatórios; campo desconhecido é erro (igual ao HTTP).
- `correlationId` opcional no envelope ou como atributo da mensagem; sem ele, um novo é gerado.
- O hash da inbox é o SHA-256 do corpo **canônico** (JSON re-serializado com chaves ordenadas): o mesmo corpo com outra formatação não vira conflito.

### Consumidor (`internal/infra/sqs/consumer.go`)

| Resultado do tratamento | Ação |
| --- | --- |
| Sucesso (inclui rejeição de negócio, `PENDING_REFERENCE` e `AlreadyHandled`) | `DeleteMessage` depois do commit |
| Envelope inválido, provedor fora da lista, `ValidationError`, dinheiro inválido, carteira inexistente, conflito de idempotência, conflito de `messageId`, violação de invariante | Envia para a DLQ com o atributo `failureReason` e remove da origem |
| Qualquer outro erro (banco fora, deadlock, erro desconhecido) | `ChangeMessageVisibility` com backoff: 2s, 4s, 8s... até 5min, pelo `ApproximateReceiveCount` |
| Interrompido pelo shutdown | Visibilidade 0: volta para a fila na hora |

- **Erro desconhecido conta como transitório:** na dúvida, tenta de novo; o `maxReceiveCount` impede que tente para sempre.
- **Envio para a DLQ:** como `MessageDeduplicationId` vai o id da mensagem no SQS, que se repete na reentrega; se a remoção da origem falhar, a DLQ descarta a segunda cópia.
- **Ordem por carteira:** o lote é agrupado por `MessageGroupId`; carteiras diferentes rodam em paralelo, e as mensagens de uma carteira rodam em sequência. Se uma falha, as seguintes do mesmo grupo voltam para a fila com o mesmo atraso, sem passar na frente. No teste, o LocalStack entregou uma mensagem posterior enquanto a anterior do grupo ainda estava invisível; a AWS documenta que segura o grupo nesse caso. Por isso as seguintes não são liberadas na hora.
- **Remoção falhou depois do commit:** a mensagem volta, a inbox reconhece e ela é removida sem segundo efeito.
- **Lista de provedores (`WAGER_QUEUE_PROVIDERS`):** mesmo com a política da fila, um `providerId` fora da lista vai para a DLQ. O caso de uso continua validando tudo como no HTTP.

### Valores escolhidos

| Parâmetro | Valor | Motivo |
| --- | --- | --- |
| `maxReceiveCount` | 5 | Com o backoff (2+4+8+16+32s), cobre cerca de 1 minuto de banco fora antes da DLQ |
| Visibility timeout (no receive) | 30s | Bem acima do tempo de uma transação; se o processo morre, a mensagem volta em até 30s |
| Lote | 10 | Máximo do SQS |
| Long polling | 20s | Máximo do SQS; menos chamadas vazias |
| Backoff de reentrega | 2s × 2ⁿ⁻¹, máx. 5min | Pelo número de recebimentos da mensagem |
| Retenção da DLQ | 14 dias | Máximo do SQS, tempo para investigar |

- `MessageGroupId` = `walletId`: ordem por carteira, paralelismo entre carteiras.
- `MessageDeduplicationId` = `messageId`.
- A deduplicação do FIFO vale só por 5 minutos e só para o mesmo `MessageDeduplicationId`. A inbox não tem prazo e também detecta o mesmo `messageId` com outro corpo.

### Shutdown

O consumidor tem dois contextos: um para **buscar** e um para **processar**.

1. `SIGTERM` cancela a busca na hora (o long polling é interrompido).
2. As mensagens em andamento continuam até 3s antes do prazo de parada do Fx.
3. Se não terminarem, o contexto de processamento é cancelado: a transação faz rollback e a mensagem volta com visibilidade 0.

As chamadas ao SQS feitas depois do tratamento (remover, devolver, mandar para a DLQ) usam um contexto desligado do cancelamento, com 5s de limite.

### Política de acesso às filas (`deploy/localstack/init-queues.sh`)

| Fila | Quem pode o quê |
| --- | --- |
| `wager-transactions.fifo` | `provider-a` e `provider-b` enviam; `wagering-service` recebe, remove e muda a visibilidade |
| `wager-transactions-dlq.fifo` | `wagering-service` envia; `wagering-operator` lê e redireciona |
| `wager-events.fifo` | `wagering-service` envia; `wallet-service` consome |

A DLQ também tem `RedriveAllowPolicy` aceitando só a fila de entrada como origem. As políticas ficam gravadas nas filas, mas o LocalStack, na configuração usada aqui, não as aplica: na AWS, sim. Por isso o consumidor não confia só nelas e valida provedor e regras de domínio.

### Ambiente

- `WAGER_QUEUE_URL`, `WAGER_DLQ_URL` e `WAGER_QUEUE_PROVIDERS` (lista separada por vírgula), todas obrigatórias.
- `.gitattributes` força LF nos `.sh`, para o script do LocalStack funcionar quando o repositório é clonado no Windows.

## Entregáveis

- `internal/infra/sqs/consumer.go` e `message.go`
- `internal/application/handle_wager_message.go` (inbox + caso de uso na mesma transação)
- `internal/infra/postgres/inbox_repository.go`
- `deploy/localstack/init-queues.sh`, com DLQ, redrive e a política de acesso às filas (veio da spec 09)

## Critérios de aceite

- [x] Mesma mensagem entregue duas vezes → um único efeito financeiro.
- [x] Consumidor morto depois do commit e antes de remover a mensagem → reentrega não duplica.
- [x] Mesma operação por HTTP e por SQS ao mesmo tempo → um único efeito.
- [x] Mensagem com JSON inválido termina na DLQ.
- [x] Banco fora do ar → mensagem volta para a fila e é processada quando o banco volta.

## Limites conhecidos

- Cada recebimento conta para o `maxReceiveCount`, inclusive o das mensagens que voltaram sem tentar porque uma anterior do grupo falhou. Se uma carteira falha por muito tempo, as seguintes podem ir para a DLQ junto.
- Um consumidor por processo, lote a lote: o próximo receive só sai quando o lote inteiro termina. A vazão cresce com mais instâncias.
- Mensagens recebidas no instante do `SIGTERM`, antes do tratamento começar, ficam invisíveis até o visibility timeout (30s) e então voltam.
- No LocalStack a ordem dentro de um grupo pode quebrar se a mesma mensagem falhar várias vezes seguidas; o resultado financeiro não depende da ordem (reversões antes da referência ficam pendentes, spec 08).
- A disputa HTTP × SQS foi testada no mesmo processo; com três instâncias, na spec 15.

## Para estudar

SQS FIFO, visibility timeout, long polling, redrive policy, padrão inbox.
