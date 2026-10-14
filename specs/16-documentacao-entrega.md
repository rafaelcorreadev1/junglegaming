# 16 — Documentação e entrega

**Desafio:** §15 · **Depende de:** todas · **Status:** concluída

## Objetivo

Outra pessoa reproduz a solução a partir de um checkout limpo.

## README.md

- [x] Pré-requisitos
- [x] Variáveis de ambiente (tabela com padrão e descrição de cada uma)
- [x] Inicialização das filas
- [x] Aplicar e reverter migrations
- [x] Executar a aplicação (Compose, três réplicas e fora do Docker)
- [x] Obter token no Keycloak e exemplos de chamadas autenticadas (HTTP e SQS)
- [x] Comandos de teste: unitário, integração (build tag), múltiplas instâncias, simulação de falha, `-race` no Windows

## ARCHITECTURE.md

- [x] Money: representação, limites, normalização
- [x] Biblioteca de banco e delimitação da transação SQL entre repositórios
- [x] Idempotência: algoritmo do hash, campos, equivalência HTTP × SQS
- [x] Concorrência: estratégia de lock por carteira
- [x] Máquina de estados; falha transitória × permanente
- [x] Referências pendentes: backoff, limite, casos de referência pendente ou sem sucesso
- [x] Reversões: combinações de `REFUND` e `ROLLBACK`
- [x] Tabela de `failureCode`
- [x] Contrato HTTP: códigos e corpos de erro
- [x] Inbox / outbox; contratos dos eventos e roteamento
- [x] SQS: `MessageGroupId`, `MessageDeduplicationId`, tentativas, visibility timeout, mensagens inválidas
- [x] Autenticação e autorização: IdP, validação, modelo de permissões
- [x] Uso do Fx e shutdown
- [x] Limitações, interpretações adotadas e trabalho não concluído

Também entraram reconciliação, observabilidade e testes, para o documento cobrir a solução inteira.

## Comandos que precisam funcionar

```sh
docker compose up --build
go test ./...
go test -race ./...
go vet ./...
```

## Demais itens

- [x] `.env.example` sem segredos reais (só os valores locais do Compose)
- [x] Keycloak provisionado automaticamente, com identidades de teste
- [x] Código formatado com `gofmt`
- [x] `go.mod` e `go.sum` versionados

## Verificação: checkout limpo

Clonei o repositório do GitHub numa pasta nova e segui o README ao pé da letra:

| Passo | Resultado |
| --- | --- |
| `docker compose up --build` | Tudo sobe; `/health/ready` → `200` com `postgres` e `sqs` `up` |
| Exemplos do README (token, abrir carteira, apostar, repetir, `REFUND`, consultas, reconciliação, `/metrics`) | Saídas iguais às documentadas: `975.00`, replay com `idempotentReplay: true`, `REFUND` volta a `1000.00`, reconciliação consistente com 3 lançamentos |
| Envio pela fila (`awslocal sqs send-message`) | Saldo `990.00` |
| Leitura de `wager-events.fifo` | 8 eventos publicados |
| `migrate ... version` | `6` |
| `go vet ./...`, `gofmt -l .`, `go test ./...` | Sem saída / verde |
| `docker compose --profile multi up` + `go test -tags=integration ./...` | Verde |
| `go test -race -tags=integration ./...` no container, como no README | Verde |

### O que o checkout limpo encontrou

| Problema | Causa | Correção |
| --- | --- | --- |
| `gofmt -l .` listou 98 arquivos | No Windows, `core.autocrlf=true` grava os arquivos com CRLF e o `gofmt` exige LF | `.gitattributes` com `* text=auto eol=lf`: o clone passa a ter LF em qualquer sistema |
| `migrate -path=/migrations` falhou no Git Bash | O MSYS converte `/migrations` em caminho do Windows | Nota no README: prefixar com `MSYS_NO_PATHCONV=1` |
| `create schema: duplicate key` num teste de integração | Nomes de schema e de fila com `time.Now().UnixNano()` repetiram entre testes que começaram no mesmo instante | `testenv.UniqueName`, com sufixo aleatório, nos 6 lugares; duas rodadas completas e uma com `-race` passaram depois |

O caso das filas era o mais perigoso: `CreateQueue` com um nome existente devolve a fila que já existe, então dois testes passariam a dividir a mesma fila sem nenhum erro.

### Auditoria final contra o enunciado

Reli o enunciado no GitHub (idêntico a `docs/DESAFIO.md`) e, a partir de volumes vazios, rodei `go vet`, `gofmt`, `go mod verify`, `go test ./...`, `go test -tags=integration -v ./...` (595 testes, 0 falhas, 0 skips) e `-race` no container (0 races). Cada item do §13 tem teste nomeado; os endpoints de negócio devolvem `401` sem token; os logs são JSON; a fila de entrada tem redrive para a DLQ (`maxReceiveCount` 5).

| Problema | Causa | Correção |
| --- | --- | --- |
| `/metrics` não mostrava retries, DLQ nem conflitos depois de um fluxo normal | `CounterVec` só cria a série no primeiro incremento | As séries conhecidas são criadas em zero na subida; teste `TestFailureSeriesAreExposedBeforeTheFirstFailure` |

## Decisões finais

- **Nome do módulo:** `github.com/rafaelcorreadev1/junglegaming`, o caminho do repositório, como manda a convenção do Go (`go get`/`go install` funcionam). `wagering-service` continua como nome do serviço: audiência do token, imagem e políticas das filas.
- **Guia de estudo:** material pessoal, fora do repositório entregue.

## Para estudar

`.gitattributes` e `core.autocrlf`, Compose profiles e âncoras YAML.
