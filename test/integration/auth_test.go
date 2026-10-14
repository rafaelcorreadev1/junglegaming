//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wagering-service/test/e2e"
)

func TestTokensThatAreMissingInvalidOrExpiredAreRejectedWithoutEffect(t *testing.T) {
	c := newClient(t)
	wallet := c.OpenWallet(t, "100.00")

	providerA := c.Token(t, e2e.ProviderA)
	providerB := c.Token(t, e2e.ProviderB)
	headerA, _, signatureA := split(t, providerA)
	_, payloadB, _ := split(t, providerB)

	shortLived := c.FreshToken(t, e2e.ProviderAShortLife)
	warmUp := e2e.Bet(wallet, "1.00")
	if status, r := c.ProcessAs(t, shortLived, warmUp.ExternalTransactionID, warmUp); status != http.StatusOK {
		t.Fatalf("a fresh short-lived token was refused (%d %+v), so its expiry would prove nothing", status, r)
	}
	if wait := time.Until(expiry(t, shortLived)) + time.Second; wait > 0 {
		time.Sleep(wait)
	}

	cases := map[string]string{
		"missing":             "",
		"not a JWT":           "not-a-token",
		"forged signature":    headerA + "." + payloadB + "." + signatureA,
		"expired":             shortLived,
		"unsigned (alg none)": base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + payloadB + ".",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			bet := e2e.Bet(wallet, "10.00")
			response := c.Send(t, e2e.Request{Token: token, Method: http.MethodPost, Path: "/wagering/transactions", IdempotencyKey: bet.ExternalTransactionID, Body: bet})
			if response.Status != http.StatusUnauthorized {
				t.Errorf("status = %d %s, want 401", response.Status, response.Body)
			}
			if _, found := c.TransactionByExternalID(t, bet); found {
				t.Error("the rejected bet was stored")
			}
		})
	}
	c.AssertFinalState(t, wallet.ID, "99.00", 1, 0)
}

func TestProvidersAreIsolatedFromEachOther(t *testing.T) {
	c := newClient(t)
	wallet := c.OpenWallet(t, "100.00")
	bet := e2e.Bet(wallet, "10.00")
	_, placed := c.Process(t, bet)
	providerA, providerB := c.Token(t, e2e.ProviderA), c.Token(t, e2e.ProviderB)

	t.Run("reads", func(t *testing.T) {
		paths := []string{
			"/wagering/transactions/" + placed.TransactionID,
			"/providers/provider-a/wagering/transactions/" + bet.ExternalTransactionID,
		}
		for _, path := range paths {
			if own := c.Send(t, e2e.Request{Token: providerA, Method: http.MethodGet, Path: path}); own.Status != http.StatusOK {
				t.Errorf("provider-a reading its own %s = %d", path, own.Status)
			}
			other := c.Send(t, e2e.Request{Token: providerB, Method: http.MethodGet, Path: path})
			if other.Status != http.StatusNotFound || strings.Contains(string(other.Body), wallet.ID) {
				t.Errorf("provider-b reading %s = %d %s, want 404 without the transaction", path, other.Status, other.Body)
			}
		}
	})

	t.Run("sending on behalf of another provider", func(t *testing.T) {
		forged := e2e.Bet(wallet, "10.00")
		response := c.Send(t, e2e.Request{Token: providerB, Method: http.MethodPost, Path: "/wagering/transactions", IdempotencyKey: forged.ExternalTransactionID, Body: forged})
		if response.Status != http.StatusForbidden {
			t.Errorf("status = %d %s, want 403", response.Status, response.Body)
		}
		if _, found := c.TransactionByExternalID(t, forged); found {
			t.Error("the forbidden bet was stored")
		}
	})

	t.Run("replay", func(t *testing.T) {
		// Same idempotency key and external id as provider-a's bet: for provider-b it is a new operation, not a replay.
		own := bet
		own.ProviderID = e2e.ProviderB
		status, r := c.ProcessAs(t, providerB, bet.ExternalTransactionID, own)
		if status != http.StatusOK || r.IdempotentReplay || r.TransactionID == placed.TransactionID {
			t.Errorf("provider-b with provider-a's key = %d %+v, want its own new transaction", status, r)
		}
	})

	c.AssertFinalState(t, wallet.ID, "80.00", 2, 0)
}

func TestInternalOperationsAreDeniedToProviders(t *testing.T) {
	c := newClient(t)
	wallet := c.OpenWallet(t, "100.00")
	provider := c.Token(t, e2e.ProviderA)
	player := "player-" + uuid.NewString()

	requests := []e2e.Request{
		{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": player, "initialBalance": e2e.BRL("50.00")}},
		{Method: http.MethodGet, Path: "/wallets/" + wallet.ID},
		{Method: http.MethodGet, Path: "/wallets/" + wallet.ID + "/ledger"},
		{Method: http.MethodPost, Path: "/wallets/" + wallet.ID + "/reconciliation"},
	}
	for _, r := range requests {
		r.Token = provider
		response := c.Send(t, r)
		if response.Status != http.StatusForbidden || strings.Contains(string(response.Body), "balance") {
			t.Errorf("%s %s by a provider = %d %s, want 403 without wallet data", r.Method, r.Path, response.Status, response.Body)
		}
	}

	// Had the provider's request created the wallet, opening it again would conflict.
	opened := c.Send(t, e2e.Request{Token: c.Token(t, e2e.WalletService), Method: http.MethodPost, Path: "/wallets",
		Body: map[string]any{"playerId": player, "initialBalance": e2e.BRL("50.00")}})
	if opened.Status != http.StatusCreated {
		t.Errorf("opening the player's wallet afterwards = %d %s, want 201", opened.Status, opened.Body)
	}
	c.AssertFinalState(t, wallet.ID, "100.00", 0, 0)
}

func split(t *testing.T, token string) (header, payload, signature string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	return parts[0], parts[1], parts[2]
}

func expiry(t *testing.T, token string) time.Time {
	t.Helper()
	_, payload, _ := split(t, token)
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	return time.Unix(claims.Exp, 0)
}
