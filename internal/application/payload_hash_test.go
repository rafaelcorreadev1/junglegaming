package application

import (
	"testing"

	"wagering-service/internal/domain"
)

func hashInput(t *testing.T, amount string) ProcessWagerInput {
	t.Helper()
	money, err := domain.ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatalf("ParseMoney: %v", err)
	}
	return ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PlayerID:              "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:              "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 money,
		CorrelationID:         "corr-1",
	}
}

func mustHash(t *testing.T, in ProcessWagerInput) string {
	t.Helper()
	hash, err := payloadHash(in)
	if err != nil {
		t.Fatalf("payloadHash: %v", err)
	}
	return hash
}

func TestPayloadHashIsStable(t *testing.T) {
	// SHA-256 of {"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}
	const want = "629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344"

	if got := mustHash(t, hashInput(t, "25.00")); got != want {
		t.Errorf("payloadHash = %s, want %s; changing it invalidates every stored idempotency record", got, want)
	}
}

func TestPayloadHashIgnoresTransportAndNormalizesTheAmount(t *testing.T) {
	base := mustHash(t, hashInput(t, "25.00"))

	same := map[string]func(*ProcessWagerInput){
		"another idempotency key": func(in *ProcessWagerInput) { in.IdempotencyKey = "other" },
		"another correlation id":  func(in *ProcessWagerInput) { in.CorrelationID = "other" },
	}
	for name, change := range same {
		in := hashInput(t, "25.00")
		change(&in)
		if got := mustHash(t, in); got != base {
			t.Errorf("%s changed the hash", name)
		}
	}

	for _, amount := range []string{"25", "25.0"} {
		if got := mustHash(t, hashInput(t, amount)); got != base {
			t.Errorf("amount %q must hash like 25.00", amount)
		}
	}
}

func TestPayloadHashChangesWithEveryBusinessField(t *testing.T) {
	base := mustHash(t, hashInput(t, "25.00"))
	usd, _ := domain.ParseMoney("25.00", "USD")

	different := map[string]func(*ProcessWagerInput){
		"providerId":                     func(in *ProcessWagerInput) { in.ProviderID = "provider-b" },
		"externalTransactionId":          func(in *ProcessWagerInput) { in.ExternalTransactionID = "transaction-124" },
		"playerId":                       func(in *ProcessWagerInput) { in.PlayerID = "other" },
		"walletId":                       func(in *ProcessWagerInput) { in.WalletID = "other" },
		"roundId":                        func(in *ProcessWagerInput) { in.RoundID = "other" },
		"gameId":                         func(in *ProcessWagerInput) { in.GameID = "other" },
		"kind":                           func(in *ProcessWagerInput) { in.Kind = "WIN" },
		"money.amount":                   func(in *ProcessWagerInput) { in.Money = hashInput(t, "25.01").Money },
		"money.currency":                 func(in *ProcessWagerInput) { in.Money = usd },
		"referenceExternalTransactionId": func(in *ProcessWagerInput) { in.ReferenceExternalTransactionID = "bet-1" },
	}
	seen := map[string]string{base: "base"}
	for field, change := range different {
		in := hashInput(t, "25.00")
		change(&in)
		got := mustHash(t, in)
		if other, clash := seen[got]; clash {
			t.Errorf("changing %s produced the same hash as %s", field, other)
		}
		seen[got] = field
	}
}
