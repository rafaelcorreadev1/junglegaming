package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
	"wagering-service/internal/infra/postgres"
)

func (e *testEnv) resolver(policy application.ReferenceRetryPolicy) *application.ResolvePendingReferences {
	return application.NewResolvePendingReferences(e.tx, e.wallets, e.wagers, e.ledger, e.outbox, e.clock, e.ids, policy)
}

func (e *testEnv) resolve(resolver *application.ResolvePendingReferences) int {
	e.t.Helper()
	result, err := resolver.Execute(context.Background())
	if err != nil {
		e.t.Fatalf("resolve pending references: %v", err)
	}
	return result.Claimed
}

func (e *testEnv) transaction(id string) wagerTransactionResponse {
	e.t.Helper()
	response := e.do(http.MethodGet, "/wagering/transactions/"+id, "")
	if response.Code != http.StatusOK {
		e.t.Fatalf("get transaction: status %d, body %s", response.Code, response.Body)
	}
	return decode[wagerTransactionResponse](e.t, response)
}

func (e *testEnv) sendPending(walletID, externalID, kind, amount, reference string) wagerResultResponse {
	e.t.Helper()
	w := newWager(walletID, externalID, kind, amount)
	w.ReferenceID = reference
	return e.sendOK(w, http.StatusAccepted)
}

func (e *testEnv) retryState(transactionID string) (attempts int, wait time.Duration) {
	e.t.Helper()
	var next time.Time
	err := e.pool.QueryRow(context.Background(),
		"SELECT reference_attempts, next_reference_attempt_at FROM wager_transactions WHERE id = $1", transactionID,
	).Scan(&attempts, &next)
	if err != nil {
		e.t.Fatalf("retry state: %v", err)
	}
	return attempts, next.Sub(e.clock.Now()).Round(time.Second)
}

func TestRefundBeforeItsBetIsCompletedByTheResolver(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	resolver := env.resolver(env.retry)

	pending := env.sendPending(wallet.ID, "refund-1", "REFUND", "30.00", "bet-1")
	if claimed := env.resolve(resolver); claimed != 0 {
		t.Fatalf("claimed %d before the first retry was due", claimed)
	}

	env.sendOK(newWager(wallet.ID, "bet-1", "BET", "30.00"), http.StatusOK)
	env.assertWallet(wallet.ID, "70.00", 2)

	// No clock advance: the bet made the refund due at once instead of leaving it to the backoff.
	if claimed := env.resolve(resolver); claimed != 1 {
		t.Fatalf("claimed %d after the bet arrived, want 1", claimed)
	}

	refund := env.transaction(pending.TransactionID)
	if refund.Status != domain.StatusProcessed || amountOf(refund.Balance) != "100.00" {
		t.Errorf("refund = %s with balance %s, want PROCESSED with 100.00", refund.Status, amountOf(refund.Balance))
	}
	env.assertWallet(wallet.ID, "100.00", 3)

	var correlationID string
	if err := env.pool.QueryRow(context.Background(),
		"SELECT correlation_id FROM wager_transactions WHERE id = $1", pending.TransactionID).Scan(&correlationID); err != nil {
		t.Fatalf("correlation id: %v", err)
	}
	events := `SELECT count(*) FROM outbox_events
	            WHERE payload ->> 'causationId' = $1 AND payload ->> 'correlationId' = $2 AND event_type = $3`
	for _, eventType := range []string{"WagerTransactionPendingReference", "WagerTransactionProcessed", "WalletBalanceChanged"} {
		if n := env.count(events, pending.TransactionID, correlationID, eventType); n != 1 {
			t.Errorf("%s: %d events carrying the original correlation id, want 1", eventType, n)
		}
	}

	replay := newWager(wallet.ID, "refund-1", "REFUND", "30.00")
	replay.ReferenceID = "bet-1"
	if result := env.sendOK(replay, http.StatusOK); !result.IdempotentReplay || amountOf(result.Balance) != "100.00" {
		t.Errorf("replay after resolution = %+v", result)
	}
	if claimed := env.resolve(resolver); claimed != 0 {
		t.Errorf("claimed %d after everything was resolved", claimed)
	}
}

func TestReferenceThatNeverArrivesBacksOffAndExpires(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	policy := env.retry
	policy.BaseDelay, policy.MaxDelay, policy.MaxAttempts = time.Second, 4*time.Second, 5
	resolver := env.resolver(policy)

	pending := env.sendPending(wallet.ID, "refund-1", "REFUND", "30.00", "never")

	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
		attempts, wait := env.retryState(pending.TransactionID)
		if attempts != i+1 || wait != want {
			t.Fatalf("after lookup %d: attempts = %d, next in %v; want %d and %v", i+1, attempts, wait, i+1, want)
		}

		env.clock.advance(want - 100*time.Millisecond)
		if claimed := env.resolve(resolver); claimed != 0 {
			t.Fatalf("lookup %d was claimed before it was due", i+2)
		}
		env.clock.advance(200 * time.Millisecond)
		if claimed := env.resolve(resolver); claimed != 1 {
			t.Fatalf("lookup %d: claimed %d, want 1", i+2, claimed)
		}
	}

	expired := env.transaction(pending.TransactionID)
	if expired.Status != domain.StatusRejected || expired.FailureCode != domain.FailureReferenceNotFound || amountOf(expired.Balance) != "100.00" {
		t.Errorf("expired = %s/%s with balance %s, want REJECTED/REFERENCE_NOT_FOUND with 100.00",
			expired.Status, expired.FailureCode, amountOf(expired.Balance))
	}
	rejected := "SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionRejected' AND payload ->> 'causationId' = $1"
	if n := env.count(rejected, pending.TransactionID); n != 1 {
		t.Errorf("expiry produced %d rejection events, want 1", n)
	}
	env.assertWallet(wallet.ID, "100.00", 1)

	env.clock.advance(time.Hour)
	if claimed := env.resolve(resolver); claimed != 0 {
		t.Errorf("an expired transaction was claimed again")
	}

	// The bet arriving late does not reopen a decision already reported to the provider.
	env.sendOK(newWager(wallet.ID, "never", "BET", "30.00"), http.StatusOK)
	if claimed := env.resolve(resolver); claimed != 0 || env.transaction(pending.TransactionID).Status != domain.StatusRejected {
		t.Errorf("late reference changed an expired transaction")
	}
}

func TestPendingReferenceFollowsTheStateOfItsReference(t *testing.T) {
	t.Run("rejected reference rejects the reversal", func(t *testing.T) {
		env := newTestEnv(t)
		wallet := env.openWallet("10.00")
		resolver := env.resolver(env.retry)

		pending := env.sendPending(wallet.ID, "refund-1", "REFUND", "30.00", "bet-1")
		env.sendOK(newWager(wallet.ID, "bet-1", "BET", "30.00"), http.StatusUnprocessableEntity)
		env.resolve(resolver)

		if refund := env.transaction(pending.TransactionID); refund.Status != domain.StatusRejected || refund.FailureCode != domain.FailureReferenceNotProcessed {
			t.Errorf("refund = %s/%s, want REJECTED/REFERENCE_NOT_PROCESSED", refund.Status, refund.FailureCode)
		}
		env.assertWallet(wallet.ID, "10.00", 1)
	})

	t.Run("rollback of a win debits, and fails apart when the money is gone", func(t *testing.T) {
		env := newTestEnv(t)
		wallet := env.openWallet("100.00")
		resolver := env.resolver(env.retry)
		env.sendOK(newWager(wallet.ID, "bet-1", "BET", "100.00"), http.StatusOK)

		covered := env.sendPending(wallet.ID, "rollback-1", "ROLLBACK", "40.00", "win-1")
		uncovered := env.sendPending(wallet.ID, "rollback-2", "ROLLBACK", "500.00", "win-2")
		env.sendOK(newWager(wallet.ID, "win-1", "WIN", "40.00"), http.StatusOK)
		env.sendOK(newWager(wallet.ID, "win-2", "WIN", "500.00"), http.StatusOK)
		env.sendOK(newWager(wallet.ID, "bet-2", "BET", "300.00"), http.StatusOK)
		if claimed := env.resolve(resolver); claimed != 2 {
			t.Fatalf("claimed %d, want 2", claimed)
		}

		if tx := env.transaction(covered.TransactionID); tx.Status != domain.StatusProcessed {
			t.Errorf("covered rollback = %s/%s, want PROCESSED", tx.Status, tx.FailureCode)
		}
		if tx := env.transaction(uncovered.TransactionID); tx.FailureCode != domain.FailureReversalInsufficientFunds {
			t.Errorf("uncovered rollback = %s/%s, want REVERSAL_INSUFFICIENT_FUNDS", tx.Status, tx.FailureCode)
		}
		if balance, _ := env.balance(wallet.ID); balance != "200.00" {
			t.Errorf("balance = %s, want 200.00", balance)
		}
	})

	t.Run("a chain resolves link by link", func(t *testing.T) {
		env := newTestEnv(t)
		wallet := env.openWallet("100.00")
		resolver := env.resolver(env.retry)

		rollback := env.sendPending(wallet.ID, "rollback-1", "ROLLBACK", "30.00", "refund-1")
		refund := env.sendPending(wallet.ID, "refund-1", "REFUND", "30.00", "bet-1")
		env.sendOK(newWager(wallet.ID, "bet-1", "BET", "30.00"), http.StatusOK)

		if claimed := env.resolve(resolver); claimed != 1 {
			t.Fatalf("first pass claimed %d, want only the refund", claimed)
		}
		if claimed := env.resolve(resolver); claimed != 1 {
			t.Fatalf("second pass claimed %d, want the rollback woken by the refund", claimed)
		}
		for name, id := range map[string]string{"refund": refund.TransactionID, "rollback": rollback.TransactionID} {
			if tx := env.transaction(id); tx.Status != domain.StatusProcessed {
				t.Errorf("%s = %s/%s, want PROCESSED", name, tx.Status, tx.FailureCode)
			}
		}
		env.assertWallet(wallet.ID, "70.00", 4)
	})
}

func TestTwoReversalsOfTheSameBetInParallel(t *testing.T) {
	for _, kinds := range [][2]string{{"REFUND", "REFUND"}, {"REFUND", "ROLLBACK"}} {
		t.Run(kinds[0]+" and "+kinds[1], func(t *testing.T) {
			env := newTestEnv(t)
			wallet := env.openWallet("100.00")
			env.sendOK(newWager(wallet.ID, "bet-1", "BET", "30.00"), http.StatusOK)

			responses := make([]*httptest.ResponseRecorder, len(kinds))
			var wg sync.WaitGroup
			for i, kind := range kinds {
				reversal := newWager(wallet.ID, fmt.Sprintf("reversal-%d", i), kind, "30.00")
				reversal.ReferenceID = "bet-1"
				wg.Go(func() { responses[i] = env.send(reversal.ProviderID+":"+reversal.ExternalID, reversal) })
			}
			wg.Wait()

			outcome := map[domain.TransactionStatus]int{}
			for _, response := range responses {
				result := decode[wagerResultResponse](t, response)
				outcome[result.Status]++
				if result.Status == domain.StatusRejected && result.FailureCode != domain.FailureAlreadyReversed {
					t.Errorf("rejection code = %s, want ALREADY_REVERSED", result.FailureCode)
				}
			}
			if outcome[domain.StatusProcessed] != 1 || outcome[domain.StatusRejected] != 1 {
				t.Errorf("outcomes = %v, want one PROCESSED and one REJECTED", outcome)
			}
			env.assertWallet(wallet.ID, "100.00", 3)
		})
	}
}

func TestPendingReversalsOfTheSameBetResolvedByCompetingInstances(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	first := env.sendPending(wallet.ID, "refund-1", "REFUND", "30.00", "bet-1")
	second := env.sendPending(wallet.ID, "refund-2", "REFUND", "30.00", "bet-1")
	env.sendOK(newWager(wallet.ID, "bet-1", "BET", "30.00"), http.StatusOK)

	policy := env.retry
	policy.BatchSize = 1
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := env.resolver(policy).Execute(context.Background()); err != nil {
				t.Errorf("resolve: %v", err)
			}
		})
	}
	wg.Wait()

	outcome := map[domain.TransactionStatus]int{}
	for _, id := range []string{first.TransactionID, second.TransactionID} {
		tx := env.transaction(id)
		outcome[tx.Status]++
		if tx.Status == domain.StatusRejected && tx.FailureCode != domain.FailureAlreadyReversed {
			t.Errorf("rejection code = %s, want ALREADY_REVERSED", tx.FailureCode)
		}
	}
	if outcome[domain.StatusProcessed] != 1 || outcome[domain.StatusRejected] != 1 {
		t.Errorf("outcomes = %v, want one PROCESSED and one REJECTED", outcome)
	}
	env.assertWallet(wallet.ID, "100.00", 3)
}

func TestCompetingInstancesResolveEachPendingReferenceOnce(t *testing.T) {
	env := newTestEnv(t)
	const pendings, instances = 40, 4

	wallets := make([]walletResponse, 4)
	for i := range wallets {
		response := env.do(http.MethodPost, "/wallets", openBody(fmt.Sprintf("player-%d", i), "1000.00", "BRL"))
		wallets[i] = decode[walletResponse](t, response)
	}
	send := func(i int, externalID, kind, reference string) {
		w := newWager(wallets[i%len(wallets)].ID, externalID, kind, "10.00")
		w.PlayerID, w.ReferenceID = fmt.Sprintf("player-%d", i%len(wallets)), reference
		status := http.StatusOK
		if reference != "" {
			status = http.StatusAccepted
		}
		env.sendOK(w, status)
	}
	for i := range pendings {
		send(i, fmt.Sprintf("refund-%d", i), "REFUND", fmt.Sprintf("bet-%d", i))
	}
	for i := range pendings {
		send(i, fmt.Sprintf("bet-%d", i), "BET", "")
	}

	policy := env.retry
	policy.BatchSize = 5
	claims := make(chan int, instances)
	var wg sync.WaitGroup
	for range instances {
		wg.Go(func() {
			resolver, total := env.resolver(policy), 0
			for {
				result, err := resolver.Execute(context.Background())
				if err != nil {
					t.Errorf("resolve: %v", err)
				}
				// Counting decisions rather than claims: each pending transaction must be decided by exactly one instance.
				total += len(result.Decided)
				if result.Claimed == 0 {
					claims <- total
					return
				}
			}
		})
	}
	wg.Wait()
	close(claims)

	total := 0
	for claimed := range claims {
		total += claimed
	}
	if total != pendings {
		t.Errorf("instances decided %d transactions in total, want each of the %d exactly once", total, pendings)
	}
	if n := env.count("SELECT count(*) FROM wager_transactions WHERE kind = 'REFUND' AND status = 'PROCESSED'"); n != pendings {
		t.Errorf("%d refunds processed, want %d", n, pendings)
	}
	if n := env.count("SELECT count(*) FROM wallet_ledger_entries WHERE direction = 'CREDIT' AND amount_minor = 1000"); n != pendings {
		t.Errorf("%d refund credits in the ledger, want %d", n, pendings)
	}
	if n := env.count("SELECT count(*) FROM wallets WHERE balance_minor <> 100000"); n != 0 {
		t.Errorf("%d wallets did not return to the opening balance", n)
	}
}

func TestClaimedReferenceIsRetriedAfterTheLeaseWhenTheInstanceDies(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	pending := env.sendPending(wallet.ID, "refund-1", "REFUND", "30.00", "bet-1")
	env.clock.advance(env.retry.BaseDelay)

	// Claiming without resolving is what an instance killed mid-work leaves behind.
	wagers := postgres.NewWagerTransactionRepository(env.pool)
	now := env.clock.Now()
	claimed, err := wagers.ClaimPendingReferences(context.Background(), now, now.Add(env.retry.Lease), 10)
	if err != nil || len(claimed) != 1 || claimed[0].TransactionID != pending.TransactionID || claimed[0].WalletID != wallet.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}

	// A new use case over the same database stands for the process that starts after the crash.
	restarted := env.resolver(env.retry)
	if n := env.resolve(restarted); n != 0 {
		t.Fatalf("claimed %d while another instance held the lease", n)
	}
	env.clock.advance(env.retry.Lease)
	if n := env.resolve(restarted); n != 1 {
		t.Fatalf("claimed %d after the lease expired, want 1", n)
	}
	if attempts, _ := env.retryState(pending.TransactionID); attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if status := env.transaction(pending.TransactionID).Status; status != domain.StatusPendingReference {
		t.Errorf("status = %s, want it still waiting", status)
	}
}
