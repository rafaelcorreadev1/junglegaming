package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// logLines returns the captured log lines with the given message.
func (e *testEnv) logLines(msg string) []map[string]any {
	e.t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(e.logs.String()), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			e.t.Fatalf("log line is not JSON: %q", raw)
		}
		if line["msg"] == msg {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestCorrelationIDFollowsTheOperationFromRequestToEvent(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")

	body, _ := json.Marshal(newWager(wallet.ID, "bet-1", "BET", "10.00"))
	request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer provider-a")
	request.Header.Set(idempotencyKeyHeader, "key-1")
	request.Header.Set(correlationIDHeader, "trace-123")
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body)
	}
	result := decode[wagerResultResponse](t, response)

	var access map[string]any
	for _, line := range env.logLines("http request") {
		if line["route"] == "/wagering/transactions" {
			access = line
		}
	}
	for key, want := range map[string]any{
		"correlationId": "trace-123", "providerId": "provider-a", "walletId": wallet.ID,
		"transactionId": result.TransactionID, "clientId": "provider-a", "method": "POST", "status": float64(200),
	} {
		if access[key] != want {
			t.Errorf("access log %s = %v, want %v (line %v)", key, access[key], want, access)
		}
	}

	events := env.count("SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND payload->>'correlationId' = 'trace-123'", wallet.ID)
	if events != 2 {
		t.Errorf("events carrying the correlation id = %d, want 2 (processed and balance changed)", events)
	}
}

func TestLogsCarryNoCredentialsNorOperationBody(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")

	env.doAs("super-secret-token-xyz", http.MethodGet, "/wallets/"+wallet.ID, "")
	w := newWager(wallet.ID, "bet-1", "BET", "12.34")
	w.GameID = "game-secret-marker"
	env.sendOK(w, http.StatusOK)
	env.send("bad-key", wager{ProviderID: "provider-a", WalletID: wallet.ID, Kind: "BET", GameID: "game-secret-marker"})

	logs := env.logs.String()
	for _, secret := range []string{"super-secret-token-xyz", "Bearer", "game-secret-marker", "12.34", "Authorization"} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain %q:\n%s", secret, logs)
		}
	}
}

func TestWagerMetricsSeparateDecisionsFromReplays(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	bet := newWager(wallet.ID, "bet-1", "BET", "10.00")
	env.sendOK(bet, http.StatusOK)
	env.sendOK(bet, http.StatusOK)
	env.sendOK(newWager(wallet.ID, "bet-2", "BET", "500.00"), http.StatusUnprocessableEntity)
	env.do(http.MethodGet, "/wallets/"+wallet.ID, "")

	metrics := env.doAs("", http.MethodGet, "/metrics", "").Body.String()
	for _, want := range []string{
		`wagering_transactions_total{channel="http",kind="BET",status="PROCESSED"} 1`,
		`wagering_transactions_total{channel="http",kind="BET",status="REJECTED"} 1`,
		`wagering_duplicates_total{channel="http",kind="idempotent_replay"} 1`,
		`wagering_processing_duration_seconds_count{channel="http"} 3`,
		`wagering_http_requests_total{method="POST",route="/wagering/transactions",status="200"} 2`,
		`wagering_http_requests_total{method="GET",route="/wallets/{walletId}",status="200"} 1`,
		"wagering_outbox_oldest_pending_age_seconds",
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
	if strings.Contains(metrics, wallet.ID) {
		t.Error("a wallet id ended up in a metric label; routes must be recorded as patterns")
	}
}

type fakeProbe struct {
	name string
	err  error
}

func (p fakeProbe) Name() string               { return p.name }
func (p fakeProbe) Ping(context.Context) error { return p.err }

func TestReadinessReportsEachDependency(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name   string
		probes []ReadinessProbe
		status int
		checks map[string]string
	}{
		{"all up", []ReadinessProbe{fakeProbe{name: "postgres"}, fakeProbe{name: "sqs"}}, http.StatusOK,
			map[string]string{"postgres": "up", "sqs": "up"}},
		{"postgres down", []ReadinessProbe{fakeProbe{name: "postgres", err: errors.New("connection refused")}, fakeProbe{name: "sqs"}},
			http.StatusServiceUnavailable, map[string]string{"postgres": "down", "sqs": "up"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			health := NewHealthHandler(tc.probes, logger)

			ready := httptest.NewRecorder()
			health.Ready(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
			var body struct {
				Checks map[string]string `json:"checks"`
			}
			_ = json.Unmarshal(ready.Body.Bytes(), &body)
			if ready.Code != tc.status || len(body.Checks) != len(tc.checks) {
				t.Fatalf("ready = %d %s", ready.Code, ready.Body)
			}
			for name, state := range tc.checks {
				if body.Checks[name] != state {
					t.Errorf("%s = %s, want %s", name, body.Checks[name], state)
				}
			}

			live := httptest.NewRecorder()
			health.Live(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
			if live.Code != http.StatusOK {
				t.Errorf("live = %d; liveness must not depend on the database", live.Code)
			}
		})
	}
}
