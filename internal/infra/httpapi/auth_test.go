package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBusinessEndpointsRequireAValidToken(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	bet := env.sendOK(newWager(wallet.ID, "bet-1", "BET", "10.00"), http.StatusOK)

	endpoints := []struct{ method, path, body string }{
		{http.MethodPost, "/wallets", openBody("player-2", "10.00", "BRL")},
		{http.MethodGet, "/wallets/" + wallet.ID, ""},
		{http.MethodGet, "/wallets/" + wallet.ID + "/ledger", ""},
		{http.MethodPost, "/wagering/transactions", `{}`},
		{http.MethodGet, "/wagering/transactions/" + bet.TransactionID, ""},
		{http.MethodGet, "/providers/provider-a/wagering/transactions/bet-1", ""},
	}
	for _, e := range endpoints {
		for name, token := range map[string]string{"no token": "", "invalid token": "forged"} {
			response := env.doAs(token, e.method, e.path, e.body)
			if response.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: status %d, want 401", e.method, e.path, name, response.Code)
				continue
			}
			if !strings.HasPrefix(response.Header().Get("WWW-Authenticate"), "Bearer") {
				t.Errorf("%s %s with %s: missing WWW-Authenticate", e.method, e.path, name)
			}
			assertError(t, response, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		}
	}

	if n := env.count("SELECT count(*) FROM wallets"); n != 1 {
		t.Errorf("unauthenticated requests created %d extra wallets", n-1)
	}
	for _, path := range []string{"/health/live", "/health/ready"} {
		if response := env.doAs("", http.MethodGet, path, ""); response.Code != http.StatusOK {
			t.Errorf("%s without a token: status %d, want 200", path, response.Code)
		}
	}
}

func TestMalformedAuthorizationHeaders(t *testing.T) {
	env := newTestEnv(t)
	for _, header := range []string{"provider-a", "Basic provider-a", "Bearer", "Bearer   "} {
		request := httptest.NewRequest(http.MethodGet, "/wagering/transactions/00000000-0000-7000-8000-000000000001", nil)
		request.Header.Set("Authorization", header)
		response := httptest.NewRecorder()
		env.router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", header, response.Code)
		}
	}
}

func TestRolesDecideWhichEndpointsAreAllowed(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")

	forbidden := []struct{ token, method, path, body string }{
		{"provider-a", http.MethodPost, "/wallets", openBody("player-2", "10.00", "BRL")},
		{"provider-a", http.MethodGet, "/wallets/" + wallet.ID, ""},
		{"provider-a", http.MethodGet, "/wallets/" + wallet.ID + "/ledger", ""},
		{"internal", http.MethodPost, "/wagering/transactions", wagerBody(t, newWager(wallet.ID, "bet-1", "BET", "10.00"))},
	}
	for _, f := range forbidden {
		response := env.doAs(f.token, f.method, f.path, f.body)
		assertError(t, response, http.StatusForbidden, "FORBIDDEN", "")
	}

	if n := env.count("SELECT count(*) FROM wallets"); n != 1 {
		t.Errorf("forbidden requests created %d wallets", n-1)
	}
	if n := env.count("SELECT count(*) FROM wager_transactions WHERE kind <> 'OPENING'"); n != 0 {
		t.Errorf("forbidden requests created %d transactions", n)
	}
}

func TestProviderCannotActForAnotherProvider(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	impersonation := newWager(wallet.ID, "bet-1", "BET", "10.00")
	impersonation.ProviderID = "provider-b"

	request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(wagerBody(t, impersonation)))
	request.Header.Set("Authorization", "Bearer provider-a")
	request.Header.Set(idempotencyKeyHeader, "provider-b:bet-1")
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, request)

	assertError(t, response, http.StatusForbidden, "FORBIDDEN", "")
	if n := env.count("SELECT count(*) FROM wager_transactions WHERE kind <> 'OPENING'"); n != 0 {
		t.Errorf("impersonation persisted %d transactions", n)
	}
	env.assertWallet(wallet.ID, "100.00", 1)
}

func TestProviderSeesOnlyItsOwnTransactions(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	bet := env.sendOK(newWager(wallet.ID, "bet-1", "BET", "10.00"), http.StatusOK)

	missing := env.doAs("provider-b", http.MethodGet, "/wagering/transactions/00000000-0000-7000-8000-000000000001", "")
	paths := []string{
		"/wagering/transactions/" + bet.TransactionID,
		"/providers/provider-a/wagering/transactions/bet-1",
	}
	for _, path := range paths {
		response := env.doAs("provider-b", http.MethodGet, path, "")
		assertError(t, response, http.StatusNotFound, "TRANSACTION_NOT_FOUND", "")
		if response.Body.String() != missing.Body.String() {
			t.Errorf("%s: body %s differs from a missing transaction's %s", path, response.Body, missing.Body)
		}
	}

	for _, token := range []string{"provider-a", "internal"} {
		for _, path := range paths {
			if response := env.doAs(token, http.MethodGet, path, ""); response.Code != http.StatusOK {
				t.Errorf("%s reading %s: status %d, want 200", token, path, response.Code)
			}
		}
	}

	// The wallet opening has no provider: only the internal service may read it.
	var openingID string
	if err := env.pool.QueryRow(t.Context(), "SELECT id::text FROM wager_transactions WHERE kind = 'OPENING'").Scan(&openingID); err != nil {
		t.Fatalf("opening id: %v", err)
	}
	if response := env.doAs("provider-a", http.MethodGet, "/wagering/transactions/"+openingID, ""); response.Code != http.StatusNotFound {
		t.Errorf("provider reading the opening: status %d, want 404", response.Code)
	}
	if response := env.doAs("internal", http.MethodGet, "/wagering/transactions/"+openingID, ""); response.Code != http.StatusOK {
		t.Errorf("internal reading the opening: status %d, want 200", response.Code)
	}
}

func TestReplayWithAnotherProvidersKeyIsNotTheirResult(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	original := env.sendOK(newWager(wallet.ID, "bet-1", "BET", "10.00"), http.StatusOK)

	sameKey := newWager(wallet.ID, "bet-1", "BET", "10.00")
	sameKey.ProviderID = "provider-b"
	response := env.send("provider-a:bet-1", sameKey)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", response.Code, response.Body)
	}
	result := decode[wagerResultResponse](t, response)
	if result.IdempotentReplay || result.TransactionID == original.TransactionID {
		t.Errorf("provider-b got provider-a's result: %+v", result)
	}
	env.assertWallet(wallet.ID, "80.00", 3)
}

func wagerBody(t *testing.T, w wager) string {
	t.Helper()
	body, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}
