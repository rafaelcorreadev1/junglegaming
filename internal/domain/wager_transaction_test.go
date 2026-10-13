package domain

import (
	"errors"
	"testing"
	"time"
)

func betParams(t *testing.T) NewWagerTransactionParams {
	t.Helper()
	return NewWagerTransactionParams{
		ID:                    "tx-1",
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-1",
		IdempotencyKey:        "provider-a:ext-1",
		PayloadHash:           "hash-1",
		WalletID:              "wallet-1",
		PlayerID:              "player-1",
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  KindBet,
		Money:                 mustMoney(t, "25.00", "BRL"),
		Now:                   testNow,
	}
}

func newTestTransaction(t *testing.T, mutate func(*NewWagerTransactionParams)) *WagerTransaction {
	t.Helper()
	params := betParams(t)
	if mutate != nil {
		mutate(&params)
	}
	tx, err := NewWagerTransaction(params)
	if err != nil {
		t.Fatalf("NewWagerTransaction: %v", err)
	}
	return tx
}

func TestNewWagerTransactionStartsPending(t *testing.T) {
	tx := newTestTransaction(t, nil)

	if tx.Status() != StatusPending {
		t.Errorf("Status() = %s, want PENDING", tx.Status())
	}
	if tx.Kind() != KindBet || tx.Money().Amount() != "25.00" {
		t.Errorf("kind/money = %s %v", tx.Kind(), tx.Money())
	}
	if tx.ID() != "tx-1" || tx.ProviderID() != "provider-a" || tx.ExternalTransactionID() != "ext-1" {
		t.Errorf("identity = %q/%q/%q", tx.ID(), tx.ProviderID(), tx.ExternalTransactionID())
	}
	if tx.IdempotencyKey() != "provider-a:ext-1" || tx.PayloadHash() != "hash-1" {
		t.Errorf("idempotency = %q/%q", tx.IdempotencyKey(), tx.PayloadHash())
	}
	if tx.WalletID() != "wallet-1" || tx.PlayerID() != "player-1" || tx.RoundID() != "round-1" || tx.GameID() != "fortune-chimp" {
		t.Error("context fields were not kept")
	}
	if tx.HasReference() || tx.ReferenceTransactionID() != "" || tx.FailureCode() != "" {
		t.Error("a new BET must have no reference and no failure code")
	}
	if _, observed := tx.ResultBalance(); observed {
		t.Error("a pending transaction must not report a result balance")
	}
	if !tx.CreatedAt().Equal(testNow) || !tx.UpdatedAt().Equal(testNow) {
		t.Error("timestamps must equal the creation instant")
	}
}

func TestNewWagerTransactionRequiresEveryIdentifier(t *testing.T) {
	tests := []struct {
		field  string
		mutate func(*NewWagerTransactionParams)
	}{
		{"id", func(p *NewWagerTransactionParams) { p.ID = "" }},
		{"providerId", func(p *NewWagerTransactionParams) { p.ProviderID = "" }},
		{"externalTransactionId", func(p *NewWagerTransactionParams) { p.ExternalTransactionID = "" }},
		{"idempotencyKey", func(p *NewWagerTransactionParams) { p.IdempotencyKey = "" }},
		{"payloadHash", func(p *NewWagerTransactionParams) { p.PayloadHash = "" }},
		{"walletId", func(p *NewWagerTransactionParams) { p.WalletID = "" }},
		{"playerId", func(p *NewWagerTransactionParams) { p.PlayerID = "" }},
		{"roundId", func(p *NewWagerTransactionParams) { p.RoundID = "" }},
		{"gameId", func(p *NewWagerTransactionParams) { p.GameID = "" }},
		{"now", func(p *NewWagerTransactionParams) { p.Now = time.Time{} }},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			params := betParams(t)
			tt.mutate(&params)
			_, err := NewWagerTransaction(params)
			assertValidationError(t, err, tt.field)
		})
	}
}

func TestNewWagerTransactionRejectsInternalAndUnknownKinds(t *testing.T) {
	for _, kind := range []TransactionKind{KindOpening, "", "DEPOSIT", "bet"} {
		t.Run(string(kind), func(t *testing.T) {
			params := betParams(t)
			params.Kind = kind
			_, err := NewWagerTransaction(params)
			assertValidationError(t, err, "kind")
		})
	}
}

func TestNewWagerTransactionAmountPolicyPerKind(t *testing.T) {
	tests := []struct {
		kind   TransactionKind
		amount string
		wantOK bool
	}{
		{KindBet, "25.00", true},
		{KindBet, "0.00", false},
		{KindBet, "-25.00", false},
		{KindWin, "0.01", true},
		{KindWin, "0.00", false},
		{KindWin, "-1.00", false},
		{KindLoss, "0.00", true},
		{KindLoss, "0.01", false},
		{KindLoss, "-0.01", false},
		{KindRefund, "25.00", true},
		{KindRefund, "0.00", false},
		{KindRefund, "-25.00", false},
		{KindRollback, "25.00", true},
		{KindRollback, "0.00", false},
		{KindRollback, "-25.00", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind)+" "+tt.amount, func(t *testing.T) {
			params := betParams(t)
			params.Kind = tt.kind
			params.Money = mustMoney(t, tt.amount, "BRL")
			if tt.kind.IsReversal() {
				params.ReferenceExternalTransactionID = "ext-0"
			}

			_, err := NewWagerTransaction(params)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			assertValidationError(t, err, "money")
		})
	}

	params := betParams(t)
	params.Money = Money{}
	if _, err := NewWagerTransaction(params); !errors.Is(err, ErrInvalidMoney) {
		t.Errorf("uninitialized money error = %v, want ErrInvalidMoney", err)
	}
}

func TestNewWagerTransactionReferencePolicyPerKind(t *testing.T) {
	tests := []struct {
		name      string
		kind      TransactionKind
		reference string
		wantOK    bool
	}{
		{"BET without reference", KindBet, "", true},
		{"BET with reference", KindBet, "ext-0", false},
		{"LOSS with reference", KindLoss, "ext-0", false},
		{"WIN without reference", KindWin, "", true},
		{"WIN with reference", KindWin, "ext-0", true},
		{"REFUND without reference", KindRefund, "", false},
		{"REFUND with reference", KindRefund, "ext-0", true},
		{"ROLLBACK without reference", KindRollback, "", false},
		{"ROLLBACK with reference", KindRollback, "ext-0", true},
		{"ROLLBACK of itself", KindRollback, "ext-1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := betParams(t)
			params.Kind = tt.kind
			params.ReferenceExternalTransactionID = tt.reference
			if tt.kind == KindLoss {
				params.Money = mustMoney(t, "0.00", "BRL")
			}

			tx, err := NewWagerTransaction(params)
			if !tt.wantOK {
				assertValidationError(t, err, "referenceExternalTransactionId")
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tx.ReferenceExternalTransactionID() != tt.reference || tx.HasReference() != (tt.reference != "") {
				t.Errorf("reference = %q", tx.ReferenceExternalTransactionID())
			}
		})
	}
}

func TestNewOpeningTransactionIsBornProcessedWithoutExternalMetadata(t *testing.T) {
	opening, err := NewOpeningTransaction("tx-open", "wallet-1", "player-1", mustMoney(t, "1000.00", "BRL"), testNow)
	if err != nil {
		t.Fatalf("NewOpeningTransaction: %v", err)
	}

	if opening.Kind() != KindOpening || opening.Status() != StatusProcessed {
		t.Errorf("kind/status = %s/%s, want OPENING/PROCESSED", opening.Kind(), opening.Status())
	}
	if balance, observed := opening.ResultBalance(); !observed || balance.Amount() != "1000.00" {
		t.Errorf("ResultBalance() = %v, %v; want 1000.00", balance, observed)
	}
	external := []string{
		opening.ProviderID(), opening.ExternalTransactionID(), opening.IdempotencyKey(), opening.PayloadHash(),
		opening.RoundID(), opening.GameID(), opening.ReferenceExternalTransactionID(), opening.ReferenceTransactionID(),
	}
	for _, value := range external {
		if value != "" {
			t.Errorf("OPENING carries external metadata %q", value)
		}
	}
	if opening.Kind().IsExternal() {
		t.Error("OPENING must not be an external kind")
	}
}

func TestNewOpeningTransactionRejectsInvalidInput(t *testing.T) {
	positive := mustMoney(t, "10.00", "BRL")

	tests := []struct {
		name               string
		id, wallet, player string
		money              Money
		now                time.Time
		wantField          string
	}{
		{"missing id", "", "w", "p", positive, testNow, "id"},
		{"missing wallet", "t", "", "p", positive, testNow, "walletId"},
		{"missing player", "t", "w", "", positive, testNow, "playerId"},
		{"missing instant", "t", "w", "p", positive, time.Time{}, "now"},
		{"zero balance creates no opening", "t", "w", "p", mustMoney(t, "0.00", "BRL"), testNow, "money"},
		{"negative balance", "t", "w", "p", mustMoney(t, "-1.00", "BRL"), testNow, "money"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewOpeningTransaction(tt.id, tt.wallet, tt.player, tt.money, tt.now)
			assertValidationError(t, err, tt.wantField)
		})
	}

	if _, err := NewOpeningTransaction("t", "w", "p", Money{}, testNow); !errors.Is(err, ErrInvalidMoney) {
		t.Errorf("uninitialized money error = %v, want ErrInvalidMoney", err)
	}
}

func TestTransactionStateMachine(t *testing.T) {
	balance := mustMoney(t, "75.00", "BRL")

	transitions := map[TransactionStatus]func(*WagerTransaction) error{
		StatusPendingReference: func(tx *WagerTransaction) error { return tx.MarkPendingReference(testLater) },
		StatusProcessed:        func(tx *WagerTransaction) error { return tx.MarkProcessed(balance, testLater) },
		StatusRejected: func(tx *WagerTransaction) error {
			return tx.MarkRejected(FailureInsufficientFunds, balance, testLater)
		},
		StatusFailed: func(tx *WagerTransaction) error { return tx.MarkFailed(FailureProcessingFailed, testLater) },
	}

	allowed := map[TransactionStatus][]TransactionStatus{
		StatusPending:          {StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed},
		StatusPendingReference: {StatusProcessed, StatusRejected, StatusFailed},
		StatusProcessed:        {},
		StatusRejected:         {},
		StatusFailed:           {},
	}

	for from, allowedTargets := range allowed {
		for to, apply := range transitions {
			t.Run(string(from)+" to "+string(to), func(t *testing.T) {
				tx := newTestTransaction(t, func(p *NewWagerTransactionParams) {
					p.Kind = KindRefund
					p.ReferenceExternalTransactionID = "ext-0"
				})
				if from != StatusPending {
					if err := transitions[from](tx); err != nil {
						t.Fatalf("setup transition to %s: %v", from, err)
					}
				}

				err := apply(tx)

				wantAllowed := false
				for _, target := range allowedTargets {
					if target == to {
						wantAllowed = true
					}
				}
				if wantAllowed {
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if tx.Status() != to {
						t.Errorf("Status() = %s, want %s", tx.Status(), to)
					}
					return
				}
				if !errors.Is(err, ErrInvalidTransition) {
					t.Errorf("error = %v, want ErrInvalidTransition", err)
				}
				if tx.Status() != from {
					t.Errorf("Status() changed to %s after a refused transition", tx.Status())
				}
			})
		}
	}
}

func TestTransitionsRecordTheirOutcome(t *testing.T) {
	balance := mustMoney(t, "75.00", "BRL")

	processed := newTestTransaction(t, nil)
	if err := processed.MarkProcessed(balance, testLater); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if got, observed := processed.ResultBalance(); !observed || got.Amount() != "75.00" {
		t.Errorf("processed ResultBalance() = %v, %v", got, observed)
	}
	if !processed.UpdatedAt().Equal(testLater) || !processed.CreatedAt().Equal(testNow) {
		t.Error("a transition must move UpdatedAt and keep CreatedAt")
	}
	if !processed.Status().IsTerminal() {
		t.Error("PROCESSED must be terminal")
	}

	rejected := newTestTransaction(t, nil)
	if err := rejected.MarkRejected(FailureInsufficientFunds, balance, testLater); err != nil {
		t.Fatalf("MarkRejected: %v", err)
	}
	if rejected.FailureCode() != FailureInsufficientFunds {
		t.Errorf("FailureCode() = %q", rejected.FailureCode())
	}
	if got, observed := rejected.ResultBalance(); !observed || got.Amount() != "75.00" {
		t.Errorf("rejected ResultBalance() = %v, %v", got, observed)
	}

	rejectedWithoutBalance := newTestTransaction(t, nil)
	if err := rejectedWithoutBalance.MarkRejected(FailureReferenceNotFound, Money{}, testLater); err != nil {
		t.Fatalf("MarkRejected without balance: %v", err)
	}
	if _, observed := rejectedWithoutBalance.ResultBalance(); observed {
		t.Error("a rejection without a wallet read must not report a balance")
	}

	failed := newTestTransaction(t, nil)
	if err := failed.MarkFailed(FailureProcessingFailed, testLater); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if failed.FailureCode() != FailureProcessingFailed || failed.Status() != StatusFailed {
		t.Errorf("failed = %s/%q", failed.Status(), failed.FailureCode())
	}
}

func TestTransitionsRejectInvalidArguments(t *testing.T) {
	balance := mustMoney(t, "75.00", "BRL")

	tests := []struct {
		name      string
		apply     func(*WagerTransaction) error
		wantField string
	}{
		{"processed without balance", func(tx *WagerTransaction) error { return tx.MarkProcessed(Money{}, testLater) }, "resultBalance"},
		{"processed with negative balance", func(tx *WagerTransaction) error {
			return tx.MarkProcessed(mustMoney(t, "-1.00", "BRL"), testLater)
		}, "resultBalance"},
		{"processed without instant", func(tx *WagerTransaction) error { return tx.MarkProcessed(balance, time.Time{}) }, "now"},
		{"rejected without code", func(tx *WagerTransaction) error { return tx.MarkRejected("", balance, testLater) }, "failureCode"},
		{"failed without code", func(tx *WagerTransaction) error { return tx.MarkFailed("", testLater) }, "failureCode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := newTestTransaction(t, nil)
			assertValidationError(t, tt.apply(tx), tt.wantField)
			if tx.Status() != StatusPending {
				t.Errorf("Status() = %s, want PENDING", tx.Status())
			}
		})
	}
}

func TestOnlyTransactionsWithReferenceCanWaitForOne(t *testing.T) {
	bet := newTestTransaction(t, nil)
	if err := bet.MarkPendingReference(testLater); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("error = %v, want ErrInvalidTransition", err)
	}
	if bet.Status() != StatusPending {
		t.Errorf("Status() = %s, want PENDING", bet.Status())
	}
}

func TestRehydrateWagerTransactionRestoresStateWithoutTransitions(t *testing.T) {
	state := WagerTransactionState{
		ID:                             "tx-9",
		Kind:                           KindRefund,
		Status:                         StatusRejected,
		WalletID:                       "wallet-1",
		PlayerID:                       "player-1",
		Money:                          mustMoney(t, "25.00", "BRL"),
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "ext-9",
		IdempotencyKey:                 "provider-a:ext-9",
		PayloadHash:                    "hash-9",
		RoundID:                        "round-1",
		GameID:                         "fortune-chimp",
		ReferenceExternalTransactionID: "ext-1",
		ReferenceTransactionID:         "tx-1",
		FailureCode:                    FailureAlreadyReversed,
		ResultBalance:                  mustMoney(t, "75.00", "BRL"),
		CreatedAt:                      testNow,
		UpdatedAt:                      testLater,
	}

	tx, err := RehydrateWagerTransaction(state)
	if err != nil {
		t.Fatalf("RehydrateWagerTransaction: %v", err)
	}

	if tx.Status() != StatusRejected || tx.FailureCode() != FailureAlreadyReversed {
		t.Errorf("status/code = %s/%q", tx.Status(), tx.FailureCode())
	}
	if tx.ReferenceTransactionID() != "tx-1" || tx.ReferenceExternalTransactionID() != "ext-1" {
		t.Error("reference was not restored")
	}
	if balance, observed := tx.ResultBalance(); !observed || balance.Amount() != "75.00" {
		t.Errorf("ResultBalance() = %v, %v", balance, observed)
	}
	if !tx.UpdatedAt().Equal(testLater) {
		t.Error("rehydration must not touch UpdatedAt")
	}
	if err := tx.MarkProcessed(mustMoney(t, "1.00", "BRL"), testLater); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("a rehydrated terminal transaction accepted a transition: %v", err)
	}
}

func TestRehydrateWagerTransactionRejectsCorruptState(t *testing.T) {
	valid := WagerTransactionState{
		ID: "tx-9", Kind: KindBet, Status: StatusProcessed, WalletID: "wallet-1", PlayerID: "player-1",
		Money: mustMoney(t, "25.00", "BRL"), ProviderID: "provider-a", ExternalTransactionID: "ext-9",
		CreatedAt: testNow, UpdatedAt: testNow,
	}

	tests := []struct {
		name      string
		mutate    func(*WagerTransactionState)
		wantField string
	}{
		{"missing id", func(s *WagerTransactionState) { s.ID = "" }, "id"},
		{"unknown kind", func(s *WagerTransactionState) { s.Kind = "DEPOSIT" }, "kind"},
		{"unknown status", func(s *WagerTransactionState) { s.Status = "DONE" }, "status"},
		{"missing wallet", func(s *WagerTransactionState) { s.WalletID = "" }, "walletId/playerId"},
		{"external without provider", func(s *WagerTransactionState) { s.ProviderID = "" }, "providerId/externalTransactionId"},
		{"missing timestamps", func(s *WagerTransactionState) { s.CreatedAt = time.Time{} }, "timestamps"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := valid
			tt.mutate(&state)
			_, err := RehydrateWagerTransaction(state)
			assertValidationError(t, err, tt.wantField)
		})
	}

	state := valid
	state.Money = Money{}
	if _, err := RehydrateWagerTransaction(state); !errors.Is(err, ErrInvalidMoney) {
		t.Errorf("uninitialized money error = %v, want ErrInvalidMoney", err)
	}

	opening := WagerTransactionState{
		ID: "tx-open", Kind: KindOpening, Status: StatusProcessed, WalletID: "wallet-1", PlayerID: "player-1",
		Money: mustMoney(t, "10.00", "BRL"), CreatedAt: testNow, UpdatedAt: testNow,
	}
	if _, err := RehydrateWagerTransaction(opening); err != nil {
		t.Errorf("OPENING without external metadata must rehydrate: %v", err)
	}
}
