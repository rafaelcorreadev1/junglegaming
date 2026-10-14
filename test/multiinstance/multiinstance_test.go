//go:build integration

// Package multiinstance repeats the concurrency scenarios against three replicas started by
// docker compose --profile multi up -d: separate processes, each with its own connections and memory.
package multiinstance

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"wagering-service/internal/testenv"
	"wagering-service/test/e2e"
)

func newClient(t *testing.T) *e2e.Client {
	t.Helper()
	urls := strings.Split(testenv.Lookup(t, "TEST_APP_URLS", "http://localhost:8080,http://localhost:8082,http://localhost:8083"), ",")
	if len(urls) < 3 {
		t.Fatalf("TEST_APP_URLS lists %d instances, want at least 3", len(urls))
	}
	return e2e.NewClient(t, urls)
}

// handledBy fails unless every instance decided at least one of the transactions run makes, proving all of them took part.
func handledBy(t *testing.T, c *e2e.Client, channel string, run func()) {
	t.Helper()
	before := make([]float64, len(c.Instances))
	for i, instance := range c.Instances {
		before[i] = c.Metric(t, instance, "wagering_transactions_total", "channel", channel)
	}
	run()
	for i, instance := range c.Instances {
		decided := c.Metric(t, instance, "wagering_transactions_total", "channel", channel) - before[i]
		t.Logf("%s decided %v transactions over %s", instance, decided, channel)
		if decided == 0 {
			t.Errorf("%s decided nothing, so the scenario did not run on every instance", instance)
		}
	}
}

func TestSameBetSentFiftyTimesAcrossInstancesDebitsOnce(t *testing.T) {
	c := newClient(t)
	e2e.SameBetInParallel(t, c)
}

func TestTwoBetsCompetingAcrossInstances(t *testing.T) {
	c := newClient(t)
	for range 5 {
		e2e.CompetingBets(t, c)
	}
}

func TestDistinctWalletsAdvanceOnEveryInstance(t *testing.T) {
	c := newClient(t)
	handledBy(t, c, "http", func() { e2e.DistinctWallets(t, c) })
}

func TestSameOperationOverHTTPAndSQSAcrossInstances(t *testing.T) {
	e2e.SameOperationOverHTTPAndSQS(t, newClient(t), e2e.NewQueue(t))
}

// Every replica consumes the same queue; wallets are message groups, so each one is handled in order by one consumer at a time.
func TestQueueIsSharedByEveryInstance(t *testing.T) {
	c, q := newClient(t), e2e.NewQueue(t)
	const wallets, betsEach = 30, 5
	opened := make([]e2e.Wallet, wallets)
	for i := range opened {
		opened[i] = c.OpenWallet(t, "100.00")
	}
	handledBy(t, c, "sqs", func() {
		for _, w := range opened {
			for range betsEach {
				bet := e2e.Bet(w, "1.00")
				q.Send(t, "msg-"+uuid.NewString(), bet.ExternalTransactionID, bet)
			}
		}
		for _, w := range opened {
			e2e.WaitForQueue(t, c, q, w)
		}
	})
	for _, w := range opened {
		c.AssertFinalState(t, w.ID, "94.00", betsEach+1, 0)
	}
}
