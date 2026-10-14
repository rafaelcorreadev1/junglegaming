//go:build integration

package e2e

import (
	"bufio"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Parallel runs fn n times, all released at once. A Fatal inside one call ends only that call and still fails the test.
func Parallel(t *testing.T, n int, fn func(tb testing.TB, i int)) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn(&goroutineTB{TB: t}, i)
		}()
	}
	close(start)
	wg.Wait()
}

// goroutineTB keeps FailNow off the test goroutine, where the testing package does not allow it.
type goroutineTB struct{ testing.TB }

func (g *goroutineTB) Fatalf(format string, args ...any) {
	g.TB.Errorf(format, args...)
	runtime.Goexit()
}

func (g *goroutineTB) Fatal(args ...any) {
	g.TB.Error(args...)
	runtime.Goexit()
}

func (g *goroutineTB) FailNow() {
	g.TB.Fail()
	runtime.Goexit()
}

// SameBetInParallel is §13 scenario 1: 50 copies of one bet at once make a single debit.
func SameBetInParallel(t *testing.T, c *Client) {
	const copies = 50
	wallet := c.OpenWallet(t, "100.00")
	bet := Bet(wallet, "10.00")

	statuses := make([]int, copies)
	results := make([]WagerResult, copies)
	Parallel(t, copies, func(tb testing.TB, i int) { statuses[i], results[i] = c.Process(tb, bet) })

	fresh := 0
	for i, r := range results {
		if statuses[i] != http.StatusOK || r.Status != "PROCESSED" || r.TransactionID != results[0].TransactionID {
			t.Errorf("copy %d = %d %+v, want 200 PROCESSED for transaction %s", i, statuses[i], r, results[0].TransactionID)
		}
		if !r.IdempotentReplay {
			fresh++
		}
	}
	if fresh != 1 {
		t.Errorf("%d copies were applied, want exactly 1 and %d replays", fresh, copies-1)
	}
	c.AssertFinalState(t, wallet.ID, "90.00", 1, 0)
}

// CompetingBets is §13 scenario 2 (and §8): two bets of 80.00 over 100.00, then both sent again.
func CompetingBets(t *testing.T, c *Client) {
	wallet := c.OpenWallet(t, "100.00")
	bets := []Wager{Bet(wallet, "80.00"), Bet(wallet, "80.00")}
	send := func() ([]int, []WagerResult) {
		statuses, results := make([]int, 2), make([]WagerResult, 2)
		Parallel(t, 2, func(tb testing.TB, i int) { statuses[i], results[i] = c.Process(tb, bets[i]) })
		return statuses, results
	}

	statuses, first := send()
	processed, rejected := 0, 0
	for i, r := range first {
		switch {
		case statuses[i] == http.StatusOK && r.Status == "PROCESSED" && r.Balance != nil && r.Balance.Amount == "20.00":
			processed++
		case statuses[i] == http.StatusUnprocessableEntity && r.Status == "REJECTED" && r.FailureCode == "INSUFFICIENT_FUNDS":
			rejected++
		default:
			t.Errorf("bet %d = %d %+v", i, statuses[i], r)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed %d, rejected %d; want 1 and 1", processed, rejected)
	}

	_, again := send()
	for i, r := range again {
		if !r.IdempotentReplay || r.TransactionID != first[i].TransactionID || r.Status != first[i].Status || r.FailureCode != first[i].FailureCode {
			t.Errorf("resubmitted bet %d = %+v, want a replay of %+v", i, r, first[i])
		}
	}
	c.AssertFinalState(t, wallet.ID, "20.00", 1, 0)
}

// DistinctWallets is §13 scenario 3: bets on many wallets at once all go through.
func DistinctWallets(t *testing.T, c *Client) {
	const wallets, betsEach = 10, 10
	opened := make([]Wallet, wallets)
	for i := range opened {
		opened[i] = c.OpenWallet(t, "100.00")
	}

	started := time.Now()
	Parallel(t, wallets*betsEach, func(tb testing.TB, i int) {
		if status, r := c.Process(tb, Bet(opened[i%wallets], "1.00")); status != http.StatusOK {
			tb.Errorf("bet %d = %d %+v", i, status, r)
		}
	})
	t.Logf("%d bets over %d wallets in %v", wallets*betsEach, wallets, time.Since(started))

	for _, w := range opened {
		c.AssertFinalState(t, w.ID, "90.00", betsEach, 0)
	}
}

// SameOperationOverHTTPAndSQS is §13 scenario 9: one operation sent through both entries has one effect.
func SameOperationOverHTTPAndSQS(t *testing.T, c *Client, q *Queue) {
	wallet := c.OpenWallet(t, "100.00")
	bet := Bet(wallet, "10.00")
	Parallel(t, 2, func(tb testing.TB, i int) {
		if i == 0 {
			c.Process(tb, bet)
			return
		}
		q.Send(tb, "msg-"+uuid.NewString(), bet.ExternalTransactionID, bet)
	})

	WaitForQueue(t, c, q, wallet)
	status, replay := c.Process(t, bet)
	if status != http.StatusOK || !replay.IdempotentReplay {
		t.Errorf("HTTP resend = %d %+v, want a replay", status, replay)
	}
	c.AssertFinalState(t, wallet.ID, "89.00", 2, 0)
}

// RepeatedDeliveryIsAppliedOnce sends one message twice as two SQS deliveries; only the inbox can tell them apart.
func RepeatedDeliveryIsAppliedOnce(t *testing.T, c *Client, q *Queue) {
	wallet := c.OpenWallet(t, "100.00")
	bet := Bet(wallet, "10.00")
	messageID := "msg-" + uuid.NewString()
	first := q.Send(t, messageID, bet.ExternalTransactionID, bet)
	second := q.Send(t, messageID, bet.ExternalTransactionID, bet)
	if first == second {
		t.Fatalf("SQS deduplicated the repeat (message %s), so the test would not exercise the inbox", first)
	}

	WaitForQueue(t, c, q, wallet)
	c.AssertFinalState(t, wallet.ID, "89.00", 2, 0)
}

// WaitForQueue sends a 1.00 marker bet behind everything already queued for the wallet and waits for it.
// Messages of a wallet are handled in order, so once the marker is processed every earlier one was handled too.
func WaitForQueue(t *testing.T, c *Client, q *Queue, wallet Wallet) {
	t.Helper()
	marker := Bet(wallet, "1.00")
	q.Send(t, "msg-"+uuid.NewString(), marker.ExternalTransactionID, marker)
	Eventually(t, 90*time.Second, "the marker message", func() bool {
		tx, ok := c.TransactionByExternalID(t, marker)
		return ok && tx.Status == "PROCESSED"
	})
}

// Metric sums the samples of a Prometheus counter on one instance whose labels contain every given pair.
func (c *Client) Metric(t testing.TB, instance, name string, labels ...string) float64 {
	t.Helper()
	response := c.SendTo(t, instance, Request{Method: http.MethodGet, Path: "/metrics"})
	if response.Status != http.StatusOK {
		t.Fatalf("metrics on %s: %d", instance, response.Status)
	}
	var sum float64
	scanner := bufio.NewScanner(strings.NewReader(string(response.Body)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		matches := true
		for i := 0; i+1 < len(labels); i += 2 {
			matches = matches && strings.Contains(line, labels[i]+`="`+labels[i+1]+`"`)
		}
		if !matches {
			continue
		}
		value, err := strconv.ParseFloat(line[strings.LastIndex(line, " ")+1:], 64)
		if err != nil {
			t.Fatalf("metric line %q: %v", line, err)
		}
		sum += value
	}
	return sum
}
