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

- Moedas aceitas: só ISO 4217 com duas casas (`ARS`, `AUD`, `BRL`, `CAD`, `COP`, `EUR`, `GBP`, `MXN`, `PEN`, `USD`). Moedas sem centavos, como `JPY`, não cabem na escala fixa e são rejeitadas. Código em minúsculas é rejeitado, não normalizado.
- Também rejeitados: sinal `+`, zeros à esquerda (`"025.00"`), `"-0.00"`, `".50"`, `"25."`, espaços, separador de milhar.
- `ParseMoney` aceita valor negativo, porque diferenças negativas são serializadas (ex.: `difference` da reconciliação). A proibição de negativo em entrada financeira externa é regra das entidades (spec 03), que exigem `IsPositive()` ou não negativo.
- No JSON, `amount` precisa ser string. Número JSON (`25.00` sem aspas) é rejeitado, porque passaria por ponto flutuante no cliente ou no parser.

## Entregáveis

- `internal/domain/money.go` + `money_test.go`

## Critérios de aceite

- [x] Nenhuma ocorrência de `float32`/`float64` no repositório.
- [x] `"25.00"` → 2500 centavos → serializa de volta `"25.00"`.
- [x] Tabela de testes cobrindo cada rejeição da lista acima.
- [x] `BRL + USD` retorna erro classificável com `errors.Is`.
- [x] `MaxInt64 + 1 centavo` retorna erro de overflow, não dá a volta.
- [x] `Money{}` (não inicializado) é rejeitado em toda operação e na serialização.

## Status: concluída

Cobertura de 100% das instruções do pacote `domain`. `go test`, `go vet` e `go test -race` (em container) passam.

## Para estudar

Struct com campos não exportados (imutabilidade), table-driven tests, `errors.Is`, `json.Marshaler`/`json.Unmarshaler`.
