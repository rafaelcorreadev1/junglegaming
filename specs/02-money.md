# 02 — Money

**Desafio:** §5 (garantia 1), §6.1 · **Depende de:** 01

## Objetivo

Value object imutável com valor + moeda, sem nenhum `float` em nenhum ponto.

## Requisitos

- Representação: `int64` em unidades mínimas (centavos) + código ISO 4217. Escala fixa de 2 casas.
- Operações: criar a partir de string decimal, zero por moeda, soma, subtração, negação, comparação, serialização.
- Contrato externo: `{"amount":"25.00","currency":"BRL"}`.
- Rejeitar no parsing: vazio, `NaN`, `Infinity`, notação científica (`1e3`), mais de 2 casas, moeda inválida.
- Não arredondar entrada inválida.
- Negativo: permitido em cálculo interno (diferenças), **proibido** em entrada financeira externa e em saldo.
- Soma, subtração e comparação exigem a mesma moeda → erro de moeda incompatível.
- Overflow de `int64` tratado em parsing, soma, subtração e negação.
- Valor zero de `Money` (não inicializado, sem moeda) é inválido e deve ser rejeitado.

## Decisões a documentar no ARCHITECTURE.md

- Limite: `int64` em centavos → máximo ≈ 92 quatrilhões na moeda.
- Formas aceitas e normalização antes do hash de idempotência: `"25"`, `"25.0"` e `"25.00"` são aceitas e normalizadas para `"25.00"`; `"25.000"` é rejeitada.

## Entregáveis

- `internal/domain/money.go` + `money_test.go`

## Critérios de aceite

- [ ] Nenhuma ocorrência de `float32`/`float64` no repositório.
- [ ] `"25.00"` → 2500 centavos → serializa de volta `"25.00"`.
- [ ] Tabela de testes cobrindo cada rejeição da lista acima.
- [ ] `BRL + USD` retorna erro classificável com `errors.Is`.
- [ ] `MaxInt64 + 1 centavo` retorna erro de overflow, não dá a volta.

## Para estudar

Struct com campos não exportados (imutabilidade), table-driven tests, `errors.Is`, `json.Marshaler`/`json.Unmarshaler`.
