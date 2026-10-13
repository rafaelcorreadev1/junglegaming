package domain

import (
	"errors"
	"testing"
	"time"
)

func TestNewLedgerEntryHoldsTheBalanceEquation(t *testing.T) {
	amount := mustMoney(t, "25.00", "BRL")
	hundred := mustMoney(t, "100.00", "BRL")

	tests := []struct {
		name      string
		direction LedgerDirection
		after     string
		wantOK    bool
	}{
		{"debit subtracts", LedgerDebit, "75.00", true},
		{"credit adds", LedgerCredit, "125.00", true},
		{"debit with the credit result", LedgerDebit, "125.00", false},
		{"credit with the debit result", LedgerCredit, "75.00", false},
		{"unchanged balance", LedgerDebit, "100.00", false},
		{"off by one cent", LedgerCredit, "125.01", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := NewLedgerEntry("e", "w", "tx", tt.direction, amount, hundred, mustMoney(t, tt.after, "BRL"), testNow)
			if !tt.wantOK {
				assertValidationError(t, err, "balanceAfter")
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if entry.BalanceAfter().Amount() != tt.after {
				t.Errorf("BalanceAfter() = %v, want %s", entry.BalanceAfter(), tt.after)
			}
		})
	}
}

func TestNewLedgerEntryRejectsInvalidInput(t *testing.T) {
	amount := mustMoney(t, "25.00", "BRL")
	before := mustMoney(t, "100.00", "BRL")
	after := mustMoney(t, "75.00", "BRL")

	type input struct {
		id, walletID, transactionID string
		direction                   LedgerDirection
		amount, before, after       Money
		createdAt                   time.Time
	}
	valid := input{"e", "w", "tx", LedgerDebit, amount, before, after, testNow}

	tests := []struct {
		name      string
		mutate    func(*input)
		wantField string
		wantErr   error
	}{
		{name: "missing id", mutate: func(i *input) { i.id = "" }, wantField: "id"},
		{name: "missing wallet", mutate: func(i *input) { i.walletID = "" }, wantField: "walletId"},
		{name: "missing transaction", mutate: func(i *input) { i.transactionID = "" }, wantField: "transactionId"},
		{name: "missing instant", mutate: func(i *input) { i.createdAt = time.Time{} }, wantField: "createdAt"},
		{name: "unknown direction", mutate: func(i *input) { i.direction = "TRANSFER" }, wantField: "direction"},
		{name: "zero amount", mutate: func(i *input) { i.amount = mustMoney(t, "0.00", "BRL") }, wantField: "amount"},
		{name: "negative amount", mutate: func(i *input) { i.amount = mustMoney(t, "-25.00", "BRL") }, wantField: "amount"},
		{name: "uninitialized amount", mutate: func(i *input) { i.amount = Money{} }, wantField: "amount"},
		{name: "negative balance before", mutate: func(i *input) { i.before = mustMoney(t, "-1.00", "BRL") }, wantField: "balanceBefore"},
		{name: "negative balance after", mutate: func(i *input) { i.after = mustMoney(t, "-1.00", "BRL") }, wantField: "balanceAfter"},
		{name: "amount in another currency", mutate: func(i *input) { i.amount = mustMoney(t, "25.00", "USD") }, wantErr: ErrCurrencyMismatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.mutate(&in)

			_, err := NewLedgerEntry(in.id, in.walletID, in.transactionID, in.direction, in.amount, in.before, in.after, in.createdAt)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			assertValidationError(t, err, tt.wantField)
		})
	}
}
