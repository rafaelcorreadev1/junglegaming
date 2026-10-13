package domain

import (
	"errors"
	"testing"
	"time"
)

var (
	testNow   = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	testLater = testNow.Add(time.Minute)
)

func openTestWallet(t *testing.T, balance string) *Wallet {
	t.Helper()
	wallet, err := OpenWallet("wallet-1", "player-1", mustMoney(t, balance, "BRL"), testNow)
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	return wallet
}

func assertValidationError(t *testing.T, err error, field string) {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a *ValidationError", err)
	}
	if validation.Field != field {
		t.Errorf("validation field = %q, want %q", validation.Field, field)
	}
}

func TestValidationErrorNamesTheField(t *testing.T) {
	err := invalid("playerId", "is required")
	if got, want := err.Error(), "invalid playerId: is required"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestOpenWalletStartsAtVersionOne(t *testing.T) {
	for _, balance := range []string{"0.00", "1000.00"} {
		t.Run(balance, func(t *testing.T) {
			wallet := openTestWallet(t, balance)

			if wallet.Version() != 1 {
				t.Errorf("Version() = %d, want 1", wallet.Version())
			}
			if wallet.Balance().Amount() != balance || wallet.Currency() != "BRL" {
				t.Errorf("Balance() = %v, want %s BRL", wallet.Balance(), balance)
			}
			if wallet.ID() != "wallet-1" || wallet.PlayerID() != "player-1" {
				t.Errorf("identity = %q/%q", wallet.ID(), wallet.PlayerID())
			}
			if !wallet.CreatedAt().Equal(testNow) || !wallet.UpdatedAt().Equal(testNow) {
				t.Error("timestamps must equal the opening instant")
			}
		})
	}
}

func TestOpenWalletRejectsInvalidInput(t *testing.T) {
	valid := mustMoney(t, "10.00", "BRL")

	tests := []struct {
		name      string
		id        string
		playerID  string
		balance   Money
		now       time.Time
		wantField string
		wantErr   error
	}{
		{name: "missing id", playerID: "p", balance: valid, now: testNow, wantField: "id"},
		{name: "missing player", id: "w", balance: valid, now: testNow, wantField: "playerId"},
		{name: "negative balance", id: "w", playerID: "p", balance: mustMoney(t, "-0.01", "BRL"), now: testNow, wantField: "balance"},
		{name: "missing instant", id: "w", playerID: "p", balance: valid, wantField: "timestamps"},
		{name: "uninitialized balance", id: "w", playerID: "p", now: testNow, wantErr: ErrInvalidMoney},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := OpenWallet(tt.id, tt.playerID, tt.balance, tt.now)
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

func TestRehydrateWalletRestoresStateWithoutSideEffects(t *testing.T) {
	wallet, err := RehydrateWallet("wallet-1", "player-1", mustMoney(t, "42.50", "BRL"), 7, testNow, testLater)
	if err != nil {
		t.Fatalf("RehydrateWallet: %v", err)
	}
	if wallet.Version() != 7 || wallet.Balance().Amount() != "42.50" {
		t.Errorf("state = v%d %v, want v7 42.50", wallet.Version(), wallet.Balance())
	}
	if !wallet.CreatedAt().Equal(testNow) || !wallet.UpdatedAt().Equal(testLater) {
		t.Error("timestamps were not restored as given")
	}

	_, err = RehydrateWallet("wallet-1", "player-1", mustMoney(t, "1.00", "BRL"), 0, testNow, testNow)
	assertValidationError(t, err, "version")
}

func TestWalletDebitAndCredit(t *testing.T) {
	wallet := openTestWallet(t, "100.00")

	debit, err := wallet.Debit("entry-1", "tx-1", mustMoney(t, "25.00", "BRL"), testLater)
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if wallet.Balance().Amount() != "75.00" || wallet.Version() != 2 {
		t.Errorf("after debit: %v v%d, want 75.00 v2", wallet.Balance(), wallet.Version())
	}
	if !wallet.UpdatedAt().Equal(testLater) {
		t.Error("UpdatedAt must move with the balance")
	}
	if debit.Direction() != LedgerDebit || debit.BalanceBefore().Amount() != "100.00" || debit.BalanceAfter().Amount() != "75.00" {
		t.Errorf("debit entry = %s %v -> %v", debit.Direction(), debit.BalanceBefore(), debit.BalanceAfter())
	}
	if debit.WalletID() != "wallet-1" || debit.TransactionID() != "tx-1" || debit.ID() != "entry-1" {
		t.Errorf("debit entry identity = %q/%q/%q", debit.ID(), debit.WalletID(), debit.TransactionID())
	}

	credit, err := wallet.Credit("entry-2", "tx-2", mustMoney(t, "10.50", "BRL"), testLater)
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if wallet.Balance().Amount() != "85.50" || wallet.Version() != 3 {
		t.Errorf("after credit: %v v%d, want 85.50 v3", wallet.Balance(), wallet.Version())
	}
	if credit.Direction() != LedgerCredit || credit.Amount().Amount() != "10.50" || !credit.CreatedAt().Equal(testLater) {
		t.Errorf("credit entry = %s %v at %v", credit.Direction(), credit.Amount(), credit.CreatedAt())
	}
}

func TestWalletDebitCanEmptyTheBalance(t *testing.T) {
	wallet := openTestWallet(t, "80.00")

	if _, err := wallet.Debit("entry-1", "tx-1", mustMoney(t, "80.00", "BRL"), testLater); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if !wallet.Balance().IsZero() {
		t.Errorf("Balance() = %v, want 0.00", wallet.Balance())
	}
}

func TestWalletRejectedMovementsLeaveStateUntouched(t *testing.T) {
	tests := []struct {
		name      string
		move      func(*Wallet) error
		wantErr   error
		wantField string
	}{
		{
			name: "debit above balance",
			move: func(w *Wallet) error {
				_, err := w.Debit("e", "tx", mustMoney(t, "100.01", "BRL"), testLater)
				return err
			},
			wantErr: ErrInsufficientFunds,
		},
		{
			name: "debit in another currency",
			move: func(w *Wallet) error {
				_, err := w.Debit("e", "tx", mustMoney(t, "1.00", "USD"), testLater)
				return err
			},
			wantErr: ErrCurrencyMismatch,
		},
		{
			name: "credit in another currency",
			move: func(w *Wallet) error {
				_, err := w.Credit("e", "tx", mustMoney(t, "1.00", "USD"), testLater)
				return err
			},
			wantErr: ErrCurrencyMismatch,
		},
		{
			name: "uninitialized amount",
			move: func(w *Wallet) error {
				_, err := w.Credit("e", "tx", Money{}, testLater)
				return err
			},
			wantErr: ErrInvalidMoney,
		},
		{
			name: "zero debit",
			move: func(w *Wallet) error {
				_, err := w.Debit("e", "tx", mustMoney(t, "0.00", "BRL"), testLater)
				return err
			},
			wantField: "amount",
		},
		{
			name: "negative debit would act as a credit",
			move: func(w *Wallet) error {
				_, err := w.Debit("e", "tx", mustMoney(t, "-5.00", "BRL"), testLater)
				return err
			},
			wantField: "amount",
		},
		{
			name: "negative credit would act as a debit",
			move: func(w *Wallet) error {
				_, err := w.Credit("e", "tx", mustMoney(t, "-5.00", "BRL"), testLater)
				return err
			},
			wantField: "amount",
		},
		{
			name: "missing transaction id",
			move: func(w *Wallet) error {
				_, err := w.Credit("e", "", mustMoney(t, "5.00", "BRL"), testLater)
				return err
			},
			wantField: "transactionId",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wallet := openTestWallet(t, "100.00")

			err := tt.move(wallet)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("error = %v, want %v", err, tt.wantErr)
				}
			} else {
				assertValidationError(t, err, tt.wantField)
			}

			if wallet.Balance().Amount() != "100.00" || wallet.Version() != 1 || !wallet.UpdatedAt().Equal(testNow) {
				t.Errorf("wallet changed after a rejected movement: %v v%d", wallet.Balance(), wallet.Version())
			}
		})
	}
}

func TestWalletCreditDetectsBalanceOverflow(t *testing.T) {
	wallet, err := RehydrateWallet("wallet-1", "player-1", mustMoney(t, "92233720368547758.07", "BRL"), 1, testNow, testNow)
	if err != nil {
		t.Fatalf("RehydrateWallet: %v", err)
	}

	if _, err := wallet.Credit("e", "tx", mustMoney(t, "0.01", "BRL"), testLater); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("error = %v, want ErrMoneyOverflow", err)
	}
	if wallet.Version() != 1 {
		t.Errorf("Version() = %d, want 1", wallet.Version())
	}
}
