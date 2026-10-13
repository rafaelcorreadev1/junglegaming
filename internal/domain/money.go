package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrInvalidMoney     = errors.New("money is not initialized")
	ErrInvalidAmount    = errors.New("invalid money amount")
	ErrInvalidCurrency  = errors.New("invalid currency")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrMoneyOverflow    = errors.New("money amount overflow")
)

const (
	moneyScale         = 2
	minorUnitsPerMajor = 100
)

// Only ISO 4217 currencies with two decimal places fit the fixed scale.
var supportedCurrencies = map[string]struct{}{
	"ARS": {}, "AUD": {}, "BRL": {}, "CAD": {}, "COP": {},
	"EUR": {}, "GBP": {}, "MXN": {}, "PEN": {}, "USD": {},
}

type Money struct {
	minorUnits int64
	currency   string
}

func ParseMoney(amount, currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	minorUnits, err := parseMinorUnits(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{minorUnits: minorUnits, currency: currency}, nil
}

func MoneyFromMinorUnits(minorUnits int64, currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	return Money{minorUnits: minorUnits, currency: currency}, nil
}

func ZeroMoney(currency string) (Money, error) {
	return MoneyFromMinorUnits(0, currency)
}

func (m Money) IsValid() bool     { return m.currency != "" }
func (m Money) MinorUnits() int64 { return m.minorUnits }
func (m Money) Currency() string  { return m.currency }
func (m Money) IsZero() bool      { return m.minorUnits == 0 }
func (m Money) IsPositive() bool  { return m.minorUnits > 0 }
func (m Money) IsNegative() bool  { return m.minorUnits < 0 }

func (m Money) Add(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	a, b := m.minorUnits, other.minorUnits
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return Money{}, fmt.Errorf("%w: %s + %s", ErrMoneyOverflow, m, other)
	}
	return Money{minorUnits: a + b, currency: m.currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	a, b := m.minorUnits, other.minorUnits
	if (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b) {
		return Money{}, fmt.Errorf("%w: %s - %s", ErrMoneyOverflow, m, other)
	}
	return Money{minorUnits: a - b, currency: m.currency}, nil
}

func (m Money) Neg() (Money, error) {
	if !m.IsValid() {
		return Money{}, ErrInvalidMoney
	}
	// int64 has one more negative value than positive ones, so MinInt64 has no counterpart.
	if m.minorUnits == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: -(%s)", ErrMoneyOverflow, m)
	}
	return Money{minorUnits: -m.minorUnits, currency: m.currency}, nil
}

// Cmp returns -1, 0 or 1 when m is less than, equal to or greater than other.
func (m Money) Cmp(other Money) (int, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return 0, err
	}
	switch {
	case m.minorUnits < other.minorUnits:
		return -1, nil
	case m.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) Equal(other Money) bool {
	return m.IsValid() && m == other
}

func (m Money) Amount() string {
	sign := ""
	magnitude := uint64(m.minorUnits)
	if m.minorUnits < 0 {
		sign = "-"
		// Negating in uint64 also yields the right magnitude for MinInt64, which -int64 cannot represent.
		magnitude = -magnitude
	}
	return fmt.Sprintf("%s%d.%02d", sign, magnitude/minorUnitsPerMajor, magnitude%minorUnitsPerMajor)
}

func (m Money) String() string {
	if !m.IsValid() {
		return "<invalid money>"
	}
	return m.Amount() + " " + m.currency
}

type moneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsValid() {
		return nil, ErrInvalidMoney
	}
	return json.Marshal(moneyJSON{Amount: m.Amount(), Currency: m.currency})
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var raw moneyJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: amount and currency must be JSON strings", ErrInvalidAmount)
	}
	parsed, err := ParseMoney(raw.Amount, raw.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

func (m Money) requireSameCurrency(other Money) error {
	if !m.IsValid() || !other.IsValid() {
		return ErrInvalidMoney
	}
	if m.currency != other.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}

func validateCurrency(currency string) error {
	if _, ok := supportedCurrencies[currency]; !ok {
		return fmt.Errorf("%w: %q", ErrInvalidCurrency, currency)
	}
	return nil
}

func parseMinorUnits(amount string) (int64, error) {
	invalid := fmt.Errorf("%w: %q", ErrInvalidAmount, amount)

	unsigned, negative := strings.CutPrefix(amount, "-")
	whole, fraction, hasFraction := strings.Cut(unsigned, ".")

	if !isDigits(whole) || (len(whole) > 1 && whole[0] == '0') {
		return 0, invalid
	}
	if hasFraction && (!isDigits(fraction) || len(fraction) > moneyScale) {
		return 0, invalid
	}
	fraction += strings.Repeat("0", moneyScale-len(fraction))

	// Accumulating as a negative number lets the full int64 range be parsed, MinInt64 included.
	var minorUnits int64
	for _, char := range []byte(whole + fraction) {
		digit := int64(char - '0')
		if minorUnits < (math.MinInt64+digit)/10 {
			return 0, fmt.Errorf("%w: %q", ErrMoneyOverflow, amount)
		}
		minorUnits = minorUnits*10 - digit
	}

	if negative {
		if minorUnits == 0 {
			return 0, invalid
		}
		return minorUnits, nil
	}
	if minorUnits == math.MinInt64 {
		return 0, fmt.Errorf("%w: %q", ErrMoneyOverflow, amount)
	}
	return -minorUnits, nil
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range []byte(value) {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
