# 09 — Autenticação e autorização

**Desafio:** §2, §13 (autenticação e autorização) · **Depende de:** 06

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

## Entregáveis

- `internal/infra/auth/` — verificador de JWT + middleware
- `deploy/keycloak/realm.json` — realm, clients e claims importados automaticamente no `docker compose up`
- Política de acesso às filas no script do LocalStack

## Critérios de aceite

- [ ] Sem token → `401` em todos os endpoints de negócio.
- [ ] Token expirado ou com assinatura inválida → `401`.
- [ ] `provider-a` enviando `providerId: "provider-b"` → `403`, nada gravado.
- [ ] `provider-a` consultando transação do `provider-b` → `404`.
- [ ] `provider-a` em `POST /wallets` → `403`.
- [ ] Replay com a chave de outro provedor não devolve o resultado dele.

## Para estudar

JWT e JWKS, `client_credentials`, middleware em Go, passar a identidade pelo `context`.
