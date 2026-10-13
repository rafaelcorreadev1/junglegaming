package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

var testMeta = EventMeta{EventID: "event-1", CorrelationID: "corr-1", CausationID: "cause-1", OccurredAt: testNow}

func assertJSON(t *testing.T, event Event, want string) {
	t.Helper()
	got, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("event is not valid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("expected JSON is invalid: %v", err)
	}
	gotNormalized, _ := json.Marshal(gotValue)
	wantNormalized, _ := json.Marshal(wantValue)
	if string(gotNormalized) != string(wantNormalized) {
		t.Errorf("event JSON\n got: %s\nwant: %s", gotNormalized, wantNormalized)
	}
}

func newTestWager(t *testing.T, kind TransactionKind, amount, reference string) *WagerTransaction {
	t.Helper()
	return newTestTransaction(t, func(p *NewWagerTransactionParams) {
		p.Kind = kind
		p.Money = mustMoney(t, amount, "BRL")
		p.ReferenceExternalTransactionID = reference
	})
}

func processedBet(t *testing.T, wallet *Wallet) (*WagerTransaction, LedgerEntry) {
	t.Helper()
	tx := newTestWager(t, KindBet, "25.00", "")
	entry, err := ApplyWager(tx, wallet, Reference{}, "entry-1", testLater)
	if err != nil || entry == nil {
		t.Fatalf("ApplyWager: entry %v, err %v", entry, err)
	}
	return tx, *entry
}

func TestWalletBalanceChangedCarriesTheMovement(t *testing.T) {
	wallet := openTestWallet(t, "100.00")
	_, entry := processedBet(t, wallet)

	event, err := NewWalletBalanceChanged(testMeta, wallet, entry)
	if err != nil {
		t.Fatalf("NewWalletBalanceChanged: %v", err)
	}

	assertJSON(t, event, `{
		"eventId": "event-1",
		"eventType": "WalletBalanceChanged",
		"aggregateId": "wallet-1",
		"correlationId": "corr-1",
		"causationId": "cause-1",
		"occurredAt": "2026-09-08T12:00:00Z",
		"version": 1,
		"data": {
			"walletId": "wallet-1",
			"transactionId": "tx-1",
			"direction": "DEBIT",
			"money": {"amount": "25.00", "currency": "BRL"},
			"balanceBefore": {"amount": "100.00", "currency": "BRL"},
			"balanceAfter": {"amount": "75.00", "currency": "BRL"},
			"walletVersion": 2
		}
	}`)
}

func TestWalletBalanceChangedRejectsAnEntryThatIsNotTheLastMovement(t *testing.T) {
	wallet := openTestWallet(t, "100.00")
	_, entry := processedBet(t, wallet)
	if _, err := wallet.Credit("entry-2", "tx-2", mustMoney(t, "1.00", "BRL"), testLater); err != nil {
		t.Fatalf("Credit: %v", err)
	}

	_, err := NewWalletBalanceChanged(testMeta, wallet, entry)
	assertValidationError(t, err, "entry")

	other, err := OpenWallet("wallet-2", "player-1", mustMoney(t, "75.00", "BRL"), testNow)
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	if _, err := NewWalletBalanceChanged(testMeta, other, entry); !errors.Is(err, ErrWalletMismatch) {
		t.Errorf("error = %v, want ErrWalletMismatch", err)
	}
}

func TestWagerTransactionProcessedForAnExternalBet(t *testing.T) {
	wallet := openTestWallet(t, "100.00")
	tx, _ := processedBet(t, wallet)

	event, err := NewWagerTransactionProcessed(testMeta, tx)
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessed: %v", err)
	}

	assertJSON(t, event, `{
		"eventId": "event-1",
		"eventType": "WagerTransactionProcessed",
		"aggregateId": "wallet-1",
		"correlationId": "corr-1",
		"causationId": "cause-1",
		"occurredAt": "2026-09-08T12:00:00Z",
		"version": 1,
		"data": {
			"transactionId": "tx-1",
			"walletId": "wallet-1",
			"playerId": "player-1",
			"kind": "BET",
			"money": {"amount": "25.00", "currency": "BRL"},
			"providerId": "provider-a",
			"externalTransactionId": "ext-1",
			"roundId": "round-1",
			"gameId": "fortune-chimp",
			"balance": {"amount": "75.00", "currency": "BRL"}
		}
	}`)
}

func TestWagerTransactionProcessedForAnOpeningHasNoExternalMetadata(t *testing.T) {
	opening, err := NewOpeningTransaction("tx-0", "wallet-1", "player-1", mustMoney(t, "1000.00", "BRL"), testNow)
	if err != nil {
		t.Fatalf("NewOpeningTransaction: %v", err)
	}

	meta := testMeta
	meta.CausationID = ""
	event, err := NewWagerTransactionProcessed(meta, opening)
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessed: %v", err)
	}

	assertJSON(t, event, `{
		"eventId": "event-1",
		"eventType": "WagerTransactionProcessed",
		"aggregateId": "wallet-1",
		"correlationId": "corr-1",
		"occurredAt": "2026-09-08T12:00:00Z",
		"version": 1,
		"data": {
			"transactionId": "tx-0",
			"walletId": "wallet-1",
			"playerId": "player-1",
			"kind": "OPENING",
			"money": {"amount": "1000.00", "currency": "BRL"},
			"balance": {"amount": "1000.00", "currency": "BRL"}
		}
	}`)
}

func TestWagerTransactionRejectedCarriesTheFailureCode(t *testing.T) {
	wallet := openTestWallet(t, "10.00")
	tx := newTestWager(t, KindBet, "25.00", "")
	if _, err := ApplyWager(tx, wallet, Reference{}, "entry-1", testLater); err != nil {
		t.Fatalf("ApplyWager: %v", err)
	}

	event, err := NewWagerTransactionRejected(testMeta, tx)
	if err != nil {
		t.Fatalf("NewWagerTransactionRejected: %v", err)
	}
	if event.Data.FailureCode != FailureInsufficientFunds || event.Data.Balance == nil || event.Data.Balance.Amount() != "10.00" {
		t.Errorf("data = %+v", event.Data)
	}
	if event.EventType != EventWagerTransactionRejected || event.AggregateID != "wallet-1" {
		t.Errorf("envelope = %+v", event.EventEnvelope)
	}

	withoutBalance := newTestWager(t, KindBet, "25.00", "")
	if err := withoutBalance.MarkRejected(FailureReferenceNotFound, Money{}, testLater); err != nil {
		t.Fatalf("MarkRejected: %v", err)
	}
	event, err = NewWagerTransactionRejected(testMeta, withoutBalance)
	if err != nil {
		t.Fatalf("NewWagerTransactionRejected: %v", err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &decoded)
	if _, present := decoded.Data["balance"]; present {
		t.Errorf("balance must be omitted when it was never observed: %s", raw)
	}
}

func TestWagerTransactionPendingReferenceCarriesTheReference(t *testing.T) {
	wallet := openTestWallet(t, "100.00")
	tx := newTestWager(t, KindRefund, "25.00", "ext-bet")
	if _, err := ApplyWager(tx, wallet, Reference{}, "entry-1", testLater); err != nil {
		t.Fatalf("ApplyWager: %v", err)
	}

	event, err := NewWagerTransactionPendingReference(testMeta, tx)
	if err != nil {
		t.Fatalf("NewWagerTransactionPendingReference: %v", err)
	}
	if event.EventType != EventWagerTransactionPendingReference || event.Data.ReferenceExternalTransactionID != "ext-bet" {
		t.Errorf("event = %+v", event)
	}
}

func TestEventsRequireTheMatchingTransactionStatus(t *testing.T) {
	pending := newTestWager(t, KindBet, "25.00", "")

	if _, err := NewWagerTransactionProcessed(testMeta, pending); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("processed from PENDING: error = %v", err)
	}
	if _, err := NewWagerTransactionRejected(testMeta, pending); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("rejected from PENDING: error = %v", err)
	}
	if _, err := NewWagerTransactionPendingReference(testMeta, pending); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("pending reference from PENDING: error = %v", err)
	}
}

func TestEventsRequireMetadata(t *testing.T) {
	wallet := openTestWallet(t, "100.00")
	tx, _ := processedBet(t, wallet)

	tests := []struct {
		field string
		meta  EventMeta
	}{
		{"eventId", EventMeta{CorrelationID: "c", OccurredAt: testNow}},
		{"correlationId", EventMeta{EventID: "e", OccurredAt: testNow}},
		{"occurredAt", EventMeta{EventID: "e", CorrelationID: "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			_, err := NewWagerTransactionProcessed(tt.meta, tx)
			assertValidationError(t, err, tt.field)
		})
	}
}
