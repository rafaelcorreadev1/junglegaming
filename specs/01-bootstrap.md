# 01 — Bootstrap: módulo Go, Fx, config e Docker Compose

**Desafio:** §4, §15 · **Depende de:** —

## Objetivo

Ter um processo Go que sobe com Fx, lê e valida configuração, conecta no PostgreSQL e responde `GET /health/live`, com toda a infraestrutura local no Docker Compose.

## Requisitos

- Versão do Go declarada em `go.mod` **e** no `Dockerfile`.
- `go.mod` e `go.sum` versionados.
- Composição com `fx.Module`, `fx.Provide` e `fx.Invoke`; injeção por construtores.
- Configuração por variáveis de ambiente, validada na inicialização (processo não sobe com config inválida).
- Docker Compose com PostgreSQL e a aplicação. Keycloak entra na spec 09 e o emulador de SQS na spec 10.
- `.env.example` com valores locais, sem segredos reais.

## Entregáveis

- `go.mod`, `go.sum`, `cmd/app/main.go`
- `internal/config/` — struct de config + validação
- `internal/app/` — composição Fx (separada do `main` para poder ser testada)
- Módulos Fx iniciais: logger, pool do Postgres, servidor HTTP
- `Dockerfile` (multi-stage), `docker-compose.yml`, `.env.example`, `.gitignore`

## Critérios de aceite

- [x] `docker compose up --build` sobe tudo e `GET /health/live` responde `200`.
- [x] Variável obrigatória ausente → processo encerra com erro claro, sem subir o servidor.
- [x] Postgres parado → `/health/ready` responde `503` e `/health/live` continua `200`.
- [x] `SIGTERM` encerra com código `0`: servidor HTTP para antes do pool fechar.
- [x] `go vet ./...` e `gofmt -l .` sem saída.

## Status: concluída

Versão do Go: `1.27.0`. Módulo: `github.com/rafaelcorreadev1/junglegaming` (o caminho do repositório; até a spec 16 era `wagering-service`).

`go test -race` exige cgo, que não vem com o Go no Windows. Rodar em container:

```sh
docker run --rm -v "${PWD}:/src" -v wagering-gomod:/go/pkg/mod -w /src golang:1.27 go test -race ./...
```

## Para estudar

`go mod`, pacotes e visibilidade (maiúscula = exportado), `fx.New`, `fx.Provide` vs `fx.Invoke`, `fx.Lifecycle`.
