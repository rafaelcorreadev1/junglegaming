# 09 — Autenticação e autorização

**Desafio:** §2, §13 (autenticação e autorização) · **Depende de:** 06 · **Status:** concluída

> Eliminatório: endpoint de negócio sem autenticação efetiva ou acesso a transação de outro provedor.

## Objetivo

Todo endpoint de negócio exige um token emitido pelo Keycloak, e a identidade do token decide o que pode ser acessado.

## Requisitos

- IdP externo OAuth 2.0 / OIDC. Sem cadastro de senha e sem emissão própria de token.
- Fluxo `client_credentials` (serviço para serviço).
- A identidade autenticada determina o `providerId` autorizado: o `providerId` do corpo/rota precisa bater com o do token.
- Provedor só enxerga as próprias transações, inclusive em replay.
- Operações de carteira são restritas ao serviço interno.
- Health checks são públicos.
- Mensageria: acesso controlado por credenciais e políticas do broker; o consumidor mantém as validações de domínio.

## Modelo de permissões

| Client no Keycloak | Papel | Claim | Pode |
| --- | --- | --- | --- |
| `provider-a`, `provider-b` | `provider` | `provider_id` | `POST /wagering/transactions` e consultas das próprias transações |
| `wallet-service` | `internal` | — | `/wallets/**`, reconciliação, consulta de qualquer transação |

## Validação do token

- Assinatura via JWKS do Keycloak (chaves em cache, com renovação).
- Checar `iss`, `aud`, `exp`.
- Token ausente, inválido ou expirado → `401`. Token válido sem permissão → `403`.
- Acesso negado não pode gerar efeito financeiro nem revelar se a transação existe (consulta de transação de outro provedor → `404`).

## Como foi implementado

### Keycloak (`deploy/keycloak/wagering-realm.json`)

Importado no `docker compose up` (`start-dev --import-realm`). Porta `8081` no host.

| Client | Secret (só local) | Papel no realm | Mappers |
| --- | --- | --- | --- |
| `provider-a` | `provider-a-secret` | `provider` | audiência `wagering-service`, claim fixo `provider_id=provider-a` |
| `provider-b` | `provider-b-secret` | `provider` | audiência `wagering-service`, claim fixo `provider_id=provider-b` |
| `wallet-service` | `wallet-service-secret` | `internal` | audiência `wagering-service` |

Os papéis vão para o token em `realm_access.roles` (escopo padrão `roles` do Keycloak). Só o fluxo `client_credentials` está habilitado; token vale 5 minutos.

Obter um token:

```bash
curl -s -X POST http://localhost:8081/realms/wagering/protocol/openid-connect/token   -d grant_type=client_credentials -d client_id=provider-a -d client_secret=provider-a-secret
```

### Emissor fixo, chaves pela rede interna

`KC_HOSTNAME=http://localhost:8081` fixa o `iss` de todo token, não importa por qual endereço o Keycloak foi chamado. A aplicação confere `iss` contra esse valor, mas busca as chaves em `http://keycloak:8080/.../certs`, pela rede do Docker. Por isso a configuração tem duas URLs:

| Variável | Uso |
| --- | --- |
| `AUTH_ISSUER` | Valor esperado em `iss` |
| `AUTH_JWKS_URL` | De onde buscar as chaves públicas |
| `AUTH_AUDIENCE` | Valor esperado em `aud` (padrão `wagering-service`) |

`AUTH_ISSUER` e `AUTH_JWKS_URL` são obrigatórias: não existe modo sem autenticação.

### Validação (`internal/infra/auth`)

- `github.com/coreos/go-oidc/v3`: confere assinatura (só `RS256`), `iss`, `aud` e `exp`.
- As chaves são buscadas na primeira requisição, ficam em cache e são buscadas de novo quando chega um token com `kid` desconhecido (rotação de chave).
- `Principal{ClientID, ProviderID, Roles}` vai no `context` da requisição.
- Token com papel `provider` sem `provider_id` é recusado.

### Regras por rota (`internal/infra/httpapi/auth.go`, `server.go`)

| Rota | Exige |
| --- | --- |
| `/health/*` | Nada |
| `/wallets/**` | Papel `internal` |
| `POST /wagering/transactions` | Papel `provider` e `providerId` do corpo = `provider_id` do token |
| `GET /wagering/transactions/{id}` e `GET /providers/{providerId}/...` | `provider` (só as próprias) ou `internal` (qualquer) |

| Situação | Resposta |
| --- | --- |
| Sem token, token malformado, assinatura inválida, expirado, `iss`/`aud` errados | `401 UNAUTHENTICATED` + `WWW-Authenticate: Bearer` |
| Token válido sem o papel exigido, ou `providerId` de outro provedor no corpo | `403 FORBIDDEN`, nada gravado |
| Transação de outro provedor | `404 TRANSACTION_NOT_FOUND`, corpo idêntico ao de transação inexistente |

A consulta por ID externo confere o `providerId` da rota antes de ir ao banco.

### Replay entre provedores

A chave de idempotência é única por provedor (`UNIQUE (provider_id, idempotency_key)`), e o provedor da busca agora vem do token. `provider-b` usando a mesma chave de `provider-a` cria uma operação dele, nunca recebe o resultado de `provider-a`.

## Entregáveis

- `internal/infra/auth/` — `Principal` e verificador de JWT
- `internal/infra/httpapi/auth.go` — middlewares `authenticate` e `requireRole`
- `deploy/keycloak/wagering-realm.json` — realm, clients e claims importados no `docker compose up`
- Política de acesso às filas: **movida para a spec 11**, junto com o script do LocalStack, que ainda não existe

## Critérios de aceite

- [x] Sem token → `401` em todos os endpoints de negócio.
- [x] Token expirado ou com assinatura inválida → `401`.
- [x] `provider-a` enviando `providerId: "provider-b"` → `403`, nada gravado.
- [x] `provider-a` consultando transação do `provider-b` → `404`.
- [x] `provider-a` em `POST /wallets` → `403`.
- [x] Replay com a chave de outro provedor não devolve o resultado dele.

## Limites conhecidos

- Se o Keycloak estiver fora do ar e chegar um token com chave ainda não vista, a resposta é `401`, não `503`.
- O token expirado foi testado com tokens assinados no teste; contra o Keycloak real, só assinatura inválida, segredo errado e permissões.
- Provedor não tem vínculo com carteira: qualquer provedor autenticado pode apostar em qualquer carteira, desde que o `playerId` confira. O desafio não define essa relação.

## Para estudar

JWT e JWKS, `client_credentials`, middleware em Go, passar a identidade pelo `context`.
