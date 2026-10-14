package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func (e *testEnv) reconcile(walletID string) reconciliationResponse {
	e.t.Helper()
	response := e.do(http.MethodPost, "/wallets/"+walletID+"/reconciliation", "")
	if response.Code != http.StatusOK {
		e.t.Fatalf("reconcile: status %d, body %s", response.Code, response.Body)
	}
	return decode[reconciliationResponse](e.t, response)
}

func (e *testEnv) reconciliations(result string) float64 {
	e.t.Helper()
	counter, err := e.registry.Gather()
	if err != nil {
		e.t.Fatalf("gather: %v", err)
	}
	for _, family := range counter {
		if family.GetName() != "wagering_reconciliations_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metric.GetLabel()[0].GetValue() == result {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestReconciliationOfTheChallengeExample(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("1000.00")
	env.sendOK(newWager(wallet.ID, "bet-1", "BET", "25.00"), http.StatusOK)

	got := env.reconcile(wallet.ID)

	if got.WalletID != wallet.ID || got.StoredBalance.Amount() != "975.00" || got.CalculatedBalance.Amount() != "975.00" ||
		got.Difference.Amount() != "0.00" || got.Difference.Currency() != "BRL" || !got.Consistent || got.CheckedEntries != 2 {
		t.Errorf("reconciliation = %+v", got)
	}
	if env.reconciliations("consistent") != 1 || env.reconciliations("divergent") != 0 {
		t.Errorf("metric consistent/divergent = %v/%v, want 1/0", env.reconciliations("consistent"), env.reconciliations("divergent"))
	}
}

func TestReconciliationOfAWalletOpenedEmpty(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("0.00")

	got := env.reconcile(wallet.ID)

	if got.CalculatedBalance.Amount() != "0.00" || !got.Consistent || got.CheckedEntries != 0 {
		t.Errorf("reconciliation = %+v", got)
	}
}

func TestReconciliationReportsADivergenceWithoutFixingIt(t *testing.T) {
	tests := []struct {
		name       string
		tamper     string
		stored     string
		difference string
	}{
		{"wallet holds more than the ledger", "balance_minor + 500", "980.00", "5.00"},
		{"wallet holds less than the ledger", "balance_minor - 500", "970.00", "-5.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			wallet := env.openWallet("1000.00")
			env.sendOK(newWager(wallet.ID, "bet-1", "BET", "25.00"), http.StatusOK)
			// Straight to the table, bypassing the domain: the only way to produce a divergence.
			if _, err := env.pool.Exec(t.Context(), "UPDATE wallets SET balance_minor = "+tc.tamper+" WHERE id = $1", wallet.ID); err != nil {
				t.Fatalf("tamper: %v", err)
			}

			got := env.reconcile(wallet.ID)

			if got.Consistent || got.StoredBalance.Amount() != tc.stored || got.CalculatedBalance.Amount() != "975.00" ||
				got.Difference.Amount() != tc.difference {
				t.Errorf("reconciliation = %+v", got)
			}
			if env.reconciliations("divergent") != 1 {
				t.Errorf("divergent metric = %v, want 1", env.reconciliations("divergent"))
			}
			logs := env.logs.String()
			if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, "diverges") || !strings.Contains(logs, wallet.ID) {
				t.Errorf("logs = %s, want an error naming the wallet", logs)
			}
			if balance, _ := env.balance(wallet.ID); balance != tc.stored {
				t.Errorf("balance after reconciliation = %s, want it untouched at %s", balance, tc.stored)
			}
		})
	}
}

func TestReconciliationDuringConcurrentBetsNeverReportsAFalseDivergence(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("10000.00")
	const writers, betsEach = 4, 25

	var done atomic.Bool
	var writersWG sync.WaitGroup
	for w := range writers {
		writersWG.Go(func() {
			for i := range betsEach {
				env.sendOK(newWager(wallet.ID, fmt.Sprintf("bet-%d-%d", w, i), "BET", "1.00"), http.StatusOK)
			}
		})
	}

	var reconcilers sync.WaitGroup
	var checks atomic.Int32
	for range 2 {
		reconcilers.Go(func() {
			for !done.Load() {
				if got := env.reconcile(wallet.ID); !got.Consistent {
					t.Errorf("false divergence while writing: %+v", got)
				}
				checks.Add(1)
			}
		})
	}
	writersWG.Wait()
	done.Store(true)
	reconcilers.Wait()

	final := env.reconcile(wallet.ID)
	if !final.Consistent || final.StoredBalance.Amount() != "9900.00" || final.CheckedEntries != 1+writers*betsEach {
		t.Errorf("final reconciliation = %+v", final)
	}
	t.Logf("%d reconciliations ran while %d bets were written", checks.Load(), writers*betsEach)
}

func TestReconciliationAccessAndErrors(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("10.00")

	response := env.doAs("provider-a", http.MethodPost, "/wallets/"+wallet.ID+"/reconciliation", "")
	assertError(t, response, http.StatusForbidden, "FORBIDDEN", "")

	response = env.do(http.MethodPost, "/wallets/00000000-0000-7000-8000-000000000999/reconciliation", "")
	assertError(t, response, http.StatusNotFound, "WALLET_NOT_FOUND", "")

	response = env.do(http.MethodPost, "/wallets/not-a-uuid/reconciliation", "")
	assertError(t, response, http.StatusBadRequest, "INVALID_REQUEST", "walletId")
}

func TestMetricsEndpointIsPublicAndExposesReconciliations(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("10.00")
	env.reconcile(wallet.ID)

	response := env.doAs("", http.MethodGet, "/metrics", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status %d", response.Code)
	}
	body := response.Body.String()
	for _, want := range []string{`wagering_reconciliations_total{result="consistent"} 1`, `wagering_reconciliations_total{result="divergent"} 0`} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	if n, err := testutil.GatherAndCount(env.registry, "wagering_reconciliations_total"); err != nil || n != 2 {
		t.Errorf("series = %d, %v; want 2", n, err)
	}
}
