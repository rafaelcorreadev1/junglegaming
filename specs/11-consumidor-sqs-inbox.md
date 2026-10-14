# 11 — Consumidor SQS, inbox e DLQ

**Desafio:** §3, §6.5, §10 · **Depende de:** 07, 10

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

## Decisões a documentar

- `MessageGroupId` = `walletId` (ordem por carteira, paralelismo entre carteiras).
- `MessageDeduplicationId` = `messageId`.
- Valores de `maxReceiveCount`, visibility timeout, tamanho do lote e long polling.
- A deduplicação do FIFO (janela de 5 min) **não** substitui a inbox.

## Entregáveis

- `internal/infra/sqs/consumer.go`
- `internal/application/handle_wager_message.go` (inbox + caso de uso na mesma transação)
- `deploy/localstack/init-queues.sh`, incluindo a política de acesso às filas (veio da spec 09)

## Critérios de aceite

- [ ] Mesma mensagem entregue duas vezes → um único efeito financeiro.
- [ ] Consumidor morto depois do commit e antes de remover a mensagem → reentrega não duplica.
- [ ] Mesma operação por HTTP e por SQS ao mesmo tempo → um único efeito.
- [ ] Mensagem com JSON inválido termina na DLQ.
- [ ] Banco fora do ar → mensagem volta para a fila e é processada quando o banco volta.

## Para estudar

SQS FIFO, visibility timeout, long polling, redrive policy, padrão inbox.
