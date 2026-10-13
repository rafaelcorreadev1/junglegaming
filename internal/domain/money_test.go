package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func mustMoney(t *testing.T, amount, currency string) Money {
	t.Helper()
	money, err := ParseMoney(amount, currency)
	if err != nil {
		t.Fatalf("ParseMoney(%q, %q): %v", amount, currency, err)
	}
	return money
}

func mustMinor(t *testing.T, minorUnits int64, currency string) Money {
	t.Helper()
	money, err := MoneyFromMinorUnits(minorUnits, currency)
	if err != nil {
		t.Fatalf("MoneyFromMinorUnits(%d, %q): %v", minorUnits, currency, err)
	}
	return money
}

func TestParseMoneyAcceptsAndNormalizes(t *testing.T) {
	tests := []struct {
		amount     string
		wantMinor  int64
		wantAmount string
	}{
		{"25.00", 2500, "25.00"},
		{"25.0", 2500, "25.00"},
		{"25", 2500, "25.00"},
		{"0", 0, "0.00"},
		{"0.00", 0, "0.00"},
		{"0.05", 5, "0.05"},
		{"0.5", 50, "0.50"},
		{"1000.00", 100000, "1000.00"},
		{"-5.25", -525, "-5.25"},
		{"-0.01", -1, "-0.01"},
		{"92233720368547758.07", math.MaxInt64, "92233720368547758.07"},
		{"-92233720368547758.08", math.MinInt64, "-92233720368547758.08"},
	}

	for _, tt := range tests {
		t.Run(tt.amount, func(t *testing.T) {
			money, err := ParseMoney(tt.amount, "BRL")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if money.MinorUnits() != tt.wantMinor {
				t.Errorf("MinorUnits() = %d, want %d", money.MinorUnits(), tt.wantMinor)
			}
			if money.Amount() != tt.wantAmount {
				t.Errorf("Amount() = %q, want %q", money.Amount(), tt.wantAmount)
			}
			if money.Currency() != "BRL" {
				t.Errorf("Currency() = %q, want BRL", money.Currency())
			}
		})
	}
}

func TestParseMoneyRejectsInvalidAmounts(t *testing.T) {
	tests := []struct {
		name   string
		amount string
	}{
		{"empty", ""},
		{"NaN", "NaN"},
		{"Infinity", "Infinity"},
		{"negative infinity", "-Infinity"},
		{"scientific notation", "1e3"},
		{"scientific notation with fraction", "2.5E1"},
		{"three decimal places", "25.000"},
		{"excess scale that would need rounding", "25.005"},
		{"missing whole part", ".50"},
		{"missing fraction digits", "25."},
		{"explicit plus sign", "+25.00"},
		{"lone minus sign", "-"},
		{"negative zero", "-0.00"},
		{"leading zeros", "025.00"},
		{"double zero", "00"},
		{"thousands separator", "1,000.00"},
		{"comma as decimal separator", "25,00"},
		{"leading space", " 25.00"},
		{"trailing space", "25.00 "},
		{"two decimal points", "25.0.0"},
		{"letters", "abc"},
		{"hexadecimal", "0x19"},
		{"non-ASCII digits", "２５.００"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMoney(tt.amount, "BRL")
			if !errors.Is(err, ErrInvalidAmount) {
				t.Errorf("ParseMoney(%q) error = %v, want ErrInvalidAmount", tt.amount, err)
			}
		})
	}
}

func TestParseMoneyDetectsOverflow(t *testing.T) {
	for _, amount := range []string{
		"92233720368547758.08",
		"-92233720368547758.09",
		"92233720368547759",
		"999999999999999999999999999999.99",
	} {
		t.Run(amount, func(t *testing.T) {
			_, err := ParseMoney(amount, "BRL")
			if !errors.Is(err, ErrMoneyOverflow) {
				t.Errorf("ParseMoney(%q) error = %v, want ErrMoneyOverflow", amount, err)
			}
		})
	}
}

func TestMoneyRejectsInvalidCurrencies(t *testing.T) {
	for _, currency := range []string{"", "brl", "BR", "BRLL", "XXX", "JPY", "R$"} {
		t.Run(currency, func(t *testing.T) {
			if _, err := ParseMoney("1.00", currency); !errors.Is(err, ErrInvalidCurrency) {
				t.Errorf("ParseMoney error = %v, want ErrInvalidCurrency", err)
			}
			if _, err := MoneyFromMinorUnits(100, currency); !errors.Is(err, ErrInvalidCurrency) {
				t.Errorf("MoneyFromMinorUnits error = %v, want ErrInvalidCurrency", err)
			}
			if _, err := ZeroMoney(currency); !errors.Is(err, ErrInvalidCurrency) {
				t.Errorf("ZeroMoney error = %v, want ErrInvalidCurrency", err)
			}
		})
	}
}

func TestZeroMoney(t *testing.T) {
	zero, err := ZeroMoney("BRL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !zero.IsValid() || !zero.IsZero() || zero.IsPositive() || zero.IsNegative() {
		t.Errorf("zero money has wrong predicates: %+v", zero)
	}
	if zero.Amount() != "0.00" {
		t.Errorf("Amount() = %q, want 0.00", zero.Amount())
	}
}

func TestMoneyArithmetic(t *testing.T) {
	hundred := mustMoney(t, "100.00", "BRL")
	eighty := mustMoney(t, "80.00", "BRL")

	sum, err := hundred.Add(eighty)
	if err != nil || sum.Amount() != "180.00" {
		t.Errorf("Add = %v, %v; want 180.00", sum, err)
	}

	difference, err := hundred.Sub(eighty)
	if err != nil || difference.Amount() != "20.00" {
		t.Errorf("Sub = %v, %v; want 20.00", difference, err)
	}

	negativeDifference, err := eighty.Sub(hundred)
	if err != nil || negativeDifference.Amount() != "-20.00" || !negativeDifference.IsNegative() {
		t.Errorf("Sub = %v, %v; want -20.00", negativeDifference, err)
	}

	negated, err := hundred.Neg()
	if err != nil || negated.Amount() != "-100.00" {
		t.Errorf("Neg = %v, %v; want -100.00", negated, err)
	}

	if hundred.Amount() != "100.00" || eighty.Amount() != "80.00" {
		t.Error("operands were mutated by arithmetic")
	}
}

func TestMoneyArithmeticDetectsOverflow(t *testing.T) {
	max := mustMinor(t, math.MaxInt64, "BRL")
	min := mustMinor(t, math.MinInt64, "BRL")
	cent := mustMinor(t, 1, "BRL")
	negativeCent := mustMinor(t, -1, "BRL")

	tests := []struct {
		name string
		run  func() (Money, error)
	}{
		{"max + 0.01", func() (Money, error) { return max.Add(cent) }},
		{"min + -0.01", func() (Money, error) { return min.Add(negativeCent) }},
		{"min - 0.01", func() (Money, error) { return min.Sub(cent) }},
		{"max - -0.01", func() (Money, error) { return max.Sub(negativeCent) }},
		{"0.00 - min", func() (Money, error) { return mustMinor(t, 0, "BRL").Sub(min) }},
		{"-(min)", func() (Money, error) { return min.Neg() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.run(); !errors.Is(err, ErrMoneyOverflow) {
				t.Errorf("error = %v, want ErrMoneyOverflow", err)
			}
		})
	}

	if sum, err := max.Add(negativeCent); err != nil || sum.MinorUnits() != math.MaxInt64-1 {
		t.Errorf("max + -0.01 = %v, %v; want no overflow", sum, err)
	}
	if negated, err := max.Neg(); err != nil || negated.MinorUnits() != -math.MaxInt64 {
		t.Errorf("-(max) = %v, %v; want no overflow", negated, err)
	}
}

func TestMoneyRejectsCurrencyMismatch(t *testing.T) {
	brl := mustMoney(t, "10.00", "BRL")
	usd := mustMoney(t, "10.00", "USD")

	if _, err := brl.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Add error = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Sub error = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := brl.Cmp(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Cmp error = %v, want ErrCurrencyMismatch", err)
	}
	if brl.Equal(usd) {
		t.Error("10.00 BRL must not equal 10.00 USD")
	}
}

func TestMoneyCmpAndEqual(t *testing.T) {
	small := mustMoney(t, "10.00", "BRL")
	large := mustMoney(t, "10.01", "BRL")

	tests := []struct {
		name string
		a, b Money
		want int
	}{
		{"less", small, large, -1},
		{"greater", large, small, 1},
		{"equal", small, mustMoney(t, "10", "BRL"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.a.Cmp(tt.b)
			if err != nil || got != tt.want {
				t.Errorf("Cmp = %d, %v; want %d", got, err, tt.want)
			}
		})
	}

	if !small.Equal(mustMoney(t, "10.0", "BRL")) {
		t.Error("10.00 must equal 10.0 after normalization")
	}
	if small.Equal(large) {
		t.Error("10.00 must not equal 10.01")
	}
}

func TestUninitializedMoneyIsRejected(t *testing.T) {
	var uninitialized Money
	valid := mustMoney(t, "1.00", "BRL")

	if uninitialized.IsValid() {
		t.Error("zero-value Money must not be valid")
	}
	if uninitialized.Equal(Money{}) {
		t.Error("uninitialized Money must not be equal to anything")
	}

	checks := map[string]error{}
	_, checks["Add receiver"] = uninitialized.Add(valid)
	_, checks["Add argument"] = valid.Add(uninitialized)
	_, checks["Sub receiver"] = uninitialized.Sub(valid)
	_, checks["Sub argument"] = valid.Sub(uninitialized)
	_, checks["Cmp"] = valid.Cmp(uninitialized)
	_, checks["Neg"] = uninitialized.Neg()
	_, checks["MarshalJSON"] = json.Marshal(uninitialized)

	for name, err := range checks {
		if !errors.Is(err, ErrInvalidMoney) {
			t.Errorf("%s error = %v, want ErrInvalidMoney", name, err)
		}
	}
}

func TestMoneyJSONRoundTrip(t *testing.T) {
	encoded, err := json.Marshal(mustMoney(t, "25", "BRL"))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"amount":"25.00","currency":"BRL"}`; string(encoded) != want {
		t.Errorf("Marshal = %s, want %s", encoded, want)
	}

	var decoded Money
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.MinorUnits() != 2500 || decoded.Currency() != "BRL" {
		t.Errorf("Unmarshal = %v, want 25.00 BRL", decoded)
	}
}

func TestMoneyJSONRejectsInvalidPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    error
	}{
		{"amount as JSON number", `{"amount":25.00,"currency":"BRL"}`, ErrInvalidAmount},
		{"amount as integer", `{"amount":25,"currency":"BRL"}`, ErrInvalidAmount},
		{"missing amount", `{"currency":"BRL"}`, ErrInvalidAmount},
		{"null amount", `{"amount":null,"currency":"BRL"}`, ErrInvalidAmount},
		{"excess scale", `{"amount":"25.001","currency":"BRL"}`, ErrInvalidAmount},
		{"missing currency", `{"amount":"25.00"}`, ErrInvalidCurrency},
		{"lowercase currency", `{"amount":"25.00","currency":"brl"}`, ErrInvalidCurrency},
		{"not an object", `"25.00"`, ErrInvalidAmount},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoded := mustMoney(t, "1.00", "BRL")
			err := json.Unmarshal([]byte(tt.payload), &decoded)
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
			if decoded.Amount() != "1.00" {
				t.Errorf("a failed decode changed the value to %v", decoded)
			}
		})
	}
}

func TestMoneyString(t *testing.T) {
	if got := mustMoney(t, "-5.5", "USD").String(); got != "-5.50 USD" {
		t.Errorf("String() = %q, want %q", got, "-5.50 USD")
	}
	if got := (Money{}).String(); got != "<invalid money>" {
		t.Errorf("String() = %q, want %q", got, "<invalid money>")
	}
}
