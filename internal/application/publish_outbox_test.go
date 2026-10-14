package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
	"wagering-service/internal/infra/postgres"
	"wagering-service/internal/infra/system"
	"wagering-service/internal/testdb"
)

type offsetClock struct{ offset atomic.Int64 }

func (c *offsetClock) Now() time.Time {
	return system.Clock{}.Now().Add(time.Duration(c.offset.Load()))
}

func (c *offsetClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

// recordingPublisher stands in for SQS and keeps every send, including the failed ones.
type recordingPublisher struct {
	mu    sync.Mutex
	sent  []application.OutboxMessage
	tries map[string]int
	fail  func(message application.OutboxMessage, try int) error
	delay time.Duration
}

func (p *recordingPublisher) Publish(_ context.Context, message application.OutboxMessage) error {
	time.Sleep(p.delay)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tries == nil {
		p.tries = map[string]int{}
	}
	p.tries[message.ID]++
	if p.fail != nil {
		if err := p.fail(message, p.tries[message.ID]); err != nil {
			return err
		}
	}
	p.sent = append(p.sent, message)
	return nil
}

func (p *recordingPublisher) sentIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, len(p.sent))
	for i, m := range p.sent {
		ids[i] = m.ID
	}
	return ids
}

type outboxEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	outbox  *postgres.OutboxRepository
	clock   *offsetClock
	open    *application.OpenWallet
	process *application.ProcessWagerTransaction
}

func newOutboxEnv(t *testing.T) *outboxEnv {
	t.Helper()
	pool := testdb.NewPool(t)
	tx, wallets, wagers, ledger := postgres.NewTxManager(pool), postgres.NewWalletRepository(pool),
		postgres.NewWagerTransactionRepository(pool), postgres.NewLedgerRepository(pool)
	outbox := postgres.NewOutboxRepository(pool)
	return &outboxEnv{
		t: t, pool: pool, outbox: outbox, clock: &offsetClock{},
		open: application.NewOpenWallet(tx, wallets, wagers, ledger, outbox, system.Clock{}, system.UUIDGenerator{}),
		process: application.NewProcessWagerTransaction(tx, wallets, wagers, ledger, outbox, system.Clock{}, system.UUIDGenerator{},
			application.DefaultReferenceRetryPolicy()),
	}
}

// walletWithBets opens a wallet and places bets, producing 2 + 2*bets events for that wallet.
func (e *outboxEnv) walletWithBets(player string, bets int) string {
	e.t.Helper()
	ctx := context.Background()
	balance, _ := domain.ParseMoney("1000.00", "BRL")
	wallet, err := e.open.Execute(ctx, application.OpenWalletInput{PlayerID: player, InitialBalance: balance, CorrelationID: "corr"})
	if err != nil {
		e.t.Fatalf("open wallet: %v", err)
	}
	amount, _ := domain.ParseMoney("1.00", "BRL")
	for i := range bets {
		external := fmt.Sprintf("%s-bet-%d", player, i)
		_, err := e.process.Execute(ctx, application.ProcessWagerInput{
			ProviderID: "provider-a", ExternalTransactionID: external, IdempotencyKey: external,
			PlayerID: player, WalletID: wallet.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: "BET", Money: amount, CorrelationID: "corr",
		})
		if err != nil {
			e.t.Fatalf("bet: %v", err)
		}
	}
	return wallet.ID()
}

func (e *outboxEnv) publisher(publisher application.EventPublisher, policy application.OutboxPolicy) *application.PublishOutbox {
	return application.NewPublishOutbox(e.outbox, publisher, e.clock, policy)
}

func (e *outboxEnv) run(uc *application.PublishOutbox) int {
	e.t.Helper()
	claimed, _, err := uc.Execute(context.Background())
	if err != nil {
		e.t.Fatalf("publish: %v", err)
	}
	return claimed
}

// stored returns every event id of the aggregate in creation order.
func (e *outboxEnv) stored(aggregateID string) []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(),
		"SELECT id::text FROM outbox_events WHERE aggregate_id = $1 ORDER BY sequence", aggregateID)
	if err != nil {
		e.t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func (e *outboxEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count: %v", err)
	}
	return n
}

func sentFor(sent []application.OutboxMessage, aggregateID string) []string {
	var ids []string
	for _, m := range sent {
		if m.AggregateID == aggregateID {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPublishSendsEveryEventOnceInWalletOrder(t *testing.T) {
	env := newOutboxEnv(t)
	first, second := env.walletWithBets("player-1", 3), env.walletWithBets("player-2", 2)
	publisher := &recordingPublisher{}
	uc := env.publisher(publisher, application.DefaultOutboxPolicy())

	if claimed := env.run(uc); claimed != 8+6 {
		t.Fatalf("claimed %d events, want 14", claimed)
	}
	for _, wallet := range []string{first, second} {
		if got, want := sentFor(publisher.sent, wallet), env.stored(wallet); !equal(got, want) {
			t.Errorf("wallet %s sent %v, want %v in creation order", wallet, got, want)
		}
	}
	if n := env.count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL OR locked_until IS NOT NULL"); n != 0 {
		t.Errorf("%d events left unpublished or leased", n)
	}
	if claimed := env.run(uc); claimed != 0 {
		t.Errorf("second round claimed %d already published events", claimed)
	}

	sent := publisher.sent[0]
	var stored, published map[string]any
	var payload string
	if err := env.pool.QueryRow(context.Background(), "SELECT payload::text FROM outbox_events WHERE id = $1", sent.ID).Scan(&payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	_ = json.Unmarshal([]byte(payload), &stored)
	if err := json.Unmarshal(sent.Payload, &published); err != nil {
		t.Fatalf("published payload is not JSON: %v", err)
	}
	if published["eventId"] != sent.ID || published["eventType"] != sent.EventType || published["aggregateId"] != sent.AggregateID {
		t.Errorf("published envelope = %v, want it to match id %s type %s aggregate %s", published, sent.ID, sent.EventType, sent.AggregateID)
	}
	if fmt.Sprint(stored) != fmt.Sprint(published) {
		t.Errorf("published payload differs from the stored snapshot")
	}
}

func TestFailedEventBlocksOnlyItsWalletAndIsRetriedWithBackoff(t *testing.T) {
	env := newOutboxEnv(t)
	broken, healthy := env.walletWithBets("player-1", 1), env.walletWithBets("player-2", 1)
	brokenEvents := env.stored(broken)
	publisher := &recordingPublisher{fail: func(m application.OutboxMessage, try int) error {
		if m.ID == brokenEvents[1] && try == 1 {
			return errors.New("sqs throttled")
		}
		return nil
	}}
	uc := env.publisher(publisher, application.DefaultOutboxPolicy())

	if _, _, err := uc.Execute(context.Background()); err == nil {
		t.Fatal("expected the publication failure to be reported")
	}
	if got := sentFor(publisher.sent, broken); !equal(got, brokenEvents[:1]) {
		t.Errorf("broken wallet sent %v, want only its first event and nothing after the failed one", got)
	}
	if got := sentFor(publisher.sent, healthy); !equal(got, env.stored(healthy)) {
		t.Errorf("healthy wallet sent %v, want all its events", got)
	}

	var attempts int
	var lastError string
	var wait time.Duration
	var next time.Time
	if err := env.pool.QueryRow(context.Background(),
		"SELECT attempts, last_error, next_attempt_at FROM outbox_events WHERE id = $1", brokenEvents[1]).Scan(&attempts, &lastError, &next); err != nil {
		t.Fatalf("failed event: %v", err)
	}
	wait = next.Sub(env.clock.Now()).Round(time.Second)
	if attempts != 1 || lastError == "" || wait != time.Second {
		t.Errorf("failed event: attempts %d, last error %q, next in %v; want 1, the cause, 1s", attempts, lastError, wait)
	}
	if n := env.count("SELECT count(*) FROM outbox_events WHERE locked_until IS NOT NULL"); n != 0 {
		t.Errorf("%d events still leased after the failure; the untried ones must be released", n)
	}

	if claimed := env.run(uc); claimed != 0 {
		t.Errorf("claimed %d before the backoff ended", claimed)
	}
	env.clock.advance(time.Second)
	if claimed := env.run(uc); claimed != len(brokenEvents)-1 {
		t.Errorf("claimed %d after the backoff, want the %d remaining events", claimed, len(brokenEvents)-1)
	}
	if got := sentFor(publisher.sent, broken); !equal(got, brokenEvents) {
		t.Errorf("broken wallet sent %v, want %v", got, brokenEvents)
	}
}

func TestLeaseOfADeadPublisherExpires(t *testing.T) {
	env := newOutboxEnv(t)
	wallet := env.walletWithBets("player-1", 1)
	policy := application.DefaultOutboxPolicy()

	now := env.clock.Now()
	claimed, err := env.outbox.ClaimDue(context.Background(), now, now.Add(policy.Lease), 10, 10)
	if err != nil || len(claimed) != 4 {
		t.Fatalf("claim = %d events, %v", len(claimed), err)
	}

	publisher := &recordingPublisher{}
	uc := env.publisher(publisher, policy)
	if n := env.run(uc); n != 0 {
		t.Fatalf("claimed %d events still leased by the dead publisher", n)
	}
	env.clock.advance(policy.Lease)
	if n := env.run(uc); n != 4 {
		t.Fatalf("claimed %d after the lease, want 4", n)
	}
	if got := sentFor(publisher.sent, wallet); !equal(got, env.stored(wallet)) {
		t.Errorf("sent %v, want %v", got, env.stored(wallet))
	}
}

// forgetfulOutbox loses the first MarkPublished, like a process killed right after sending.
type forgetfulOutbox struct {
	application.OutboxRepository
	forgot atomic.Bool
}

func (o *forgetfulOutbox) MarkPublished(ctx context.Context, id string, now time.Time) error {
	if o.forgot.CompareAndSwap(false, true) {
		return errors.New("connection lost")
	}
	return o.OutboxRepository.MarkPublished(ctx, id, now)
}

func TestEventSentButNotMarkedIsSentAgainWithTheSameID(t *testing.T) {
	env := newOutboxEnv(t)
	wallet := env.walletWithBets("player-1", 0)
	events := env.stored(wallet)
	publisher := &recordingPublisher{}
	policy := application.DefaultOutboxPolicy()
	uc := application.NewPublishOutbox(&forgetfulOutbox{OutboxRepository: env.outbox}, publisher, env.clock, policy)

	if _, _, err := uc.Execute(context.Background()); err == nil {
		t.Fatal("expected the lost mark to be reported")
	}
	if got := publisher.sentIDs(); !equal(got, events[:1]) {
		t.Fatalf("sent %v, want only the first event before the failure", got)
	}

	env.clock.advance(policy.Lease)
	env.run(uc)
	want := []string{events[0], events[0], events[1]}
	if got := publisher.sentIDs(); !equal(got, want) {
		t.Errorf("sent %v, want %v: the first event again, with the same id, then the rest", got, want)
	}
	if n := env.count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL"); n != 0 {
		t.Errorf("%d events unpublished", n)
	}
}

func TestCompetingPublishersKeepEachWalletInOrder(t *testing.T) {
	env := newOutboxEnv(t)
	const wallets, bets, publishers = 5, 8, 4
	ids := make([]string, wallets)
	for i := range ids {
		ids[i] = env.walletWithBets(fmt.Sprintf("player-%d", i), bets)
	}

	publisher := &recordingPublisher{delay: time.Millisecond}
	policy := application.DefaultOutboxPolicy()
	policy.Aggregates, policy.PerAggregate = 2, 3

	var wg sync.WaitGroup
	for range publishers {
		uc := env.publisher(publisher, policy)
		wg.Go(func() {
			// A round can claim nothing while the others hold every wallet, so stop only after several empty rounds.
			for idle := 0; idle < 20; {
				claimed, _, err := uc.Execute(context.Background())
				if err != nil {
					t.Errorf("publish: %v", err)
					return
				}
				if claimed == 0 {
					idle++
					time.Sleep(2 * time.Millisecond)
				} else {
					idle = 0
				}
			}
		})
	}
	wg.Wait()

	total := wallets * (2 + 2*bets)
	if len(publisher.sent) != total {
		t.Errorf("sent %d messages, want each of the %d events exactly once", len(publisher.sent), total)
	}
	for _, wallet := range ids {
		if got, want := sentFor(publisher.sent, wallet), env.stored(wallet); !equal(got, want) {
			t.Errorf("wallet %s: sent %d events out of creation order", wallet, len(got))
		}
	}
	if n := env.count("SELECT count(*) FROM outbox_events WHERE published_at IS NULL"); n != 0 {
		t.Errorf("%d events unpublished", n)
	}
}

func TestOutboxPolicyValidation(t *testing.T) {
	if err := application.DefaultOutboxPolicy().Validate(); err != nil {
		t.Fatalf("default policy: %v", err)
	}
	invalid := map[string]func(*application.OutboxPolicy){
		"zero base delay":       func(p *application.OutboxPolicy) { p.BaseDelay = 0 },
		"max below base":        func(p *application.OutboxPolicy) { p.MaxDelay = p.BaseDelay - 1 },
		"zero lease":            func(p *application.OutboxPolicy) { p.Lease = 0 },
		"zero aggregates":       func(p *application.OutboxPolicy) { p.Aggregates = 0 },
		"zero events per batch": func(p *application.OutboxPolicy) { p.PerAggregate = 0 },
	}
	for name, change := range invalid {
		policy := application.DefaultOutboxPolicy()
		change(&policy)
		if policy.Validate() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
