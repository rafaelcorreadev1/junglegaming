# 16 — Documentação e entrega

**Desafio:** §15 · **Depende de:** todas

## Objetivo

Outra pessoa reproduz a solução a partir de um checkout limpo.

## README.md

- [ ] Pré-requisitos
- [ ] Variáveis de ambiente
- [ ] Inicialização das filas
- [ ] Aplicar e reverter migrations
- [ ] Executar a aplicação
- [ ] Obter token no Keycloak e exemplos de chamadas autenticadas
- [ ] Comandos de teste: unitário, integração (build tag), múltiplas instâncias, simulação de falha

## ARCHITECTURE.md

- [ ] Money: representação, limites, normalização
- [ ] Biblioteca de banco e delimitação da transação SQL entre repositórios
- [ ] Idempotência: algoritmo do hash, campos, equivalência HTTP × SQS
- [ ] Concorrência: estratégia de lock por carteira
- [ ] Máquina de estados; falha transitória × permanente
- [ ] Referências pendentes: backoff, limite, casos de referência pendente ou sem sucesso
- [ ] Reversões: combinações de `REFUND` e `ROLLBACK`
- [ ] Tabela de `failureCode`
- [ ] Contrato HTTP: códigos e corpos de erro
- [ ] Inbox / outbox; contratos dos eventos e roteamento
- [ ] SQS: `MessageGroupId`, `MessageDeduplicationId`, tentativas, visibility timeout, mensagens inválidas
- [ ] Autenticação e autorização: IdP, validação, modelo de permissões
- [ ] Uso do Fx e shutdown
- [ ] Limitações, interpretações adotadas e trabalho não concluído

## Comandos que precisam funcionar

```sh
docker compose up --build
go test ./...
go test -race ./...
go vet ./...
```

## Demais itens

- [ ] `.env.example` sem segredos reais
- [ ] Keycloak provisionado automaticamente, com identidades de teste
- [ ] Código formatado com `gofmt`
- [ ] `go.mod` e `go.sum` versionados
