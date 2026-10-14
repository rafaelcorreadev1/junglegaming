package sqs

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/postgres"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/system"
	"github.com/rafaelcorreadev1/junglegaming/internal/testdb"
)

type wagering struct {
	pool    *pgxpool.Pool
	handler *application.HandleWagerMessage
	wallet  *domain.Wallet
}

// newWagering wires the real use case over a migrated schema and opens a wallet with 100.00 for player-1.
func newWagering(t *testing.T, pool *pgxpool.Pool) wagering {
	t.Helper()
	tx, wallets, wagers, ledger, outbox := postgres.NewTxManager(pool), postgres.NewWalletRepository(pool),
		postgres.NewWagerTransactionRepository(pool), postgres.NewLedgerRepository(pool), postgres.NewOutboxRepository(pool)
	clock, ids := system.Clock{}, system.UUIDGenerator{}
	process := application.NewProcessWagerTransaction(tx, wallets, wagers, ledger, outbox, clock, ids, application.DefaultReferenceRetryPolicy())

	balance, _ := domain.ParseMoney("100.00", "BRL")
	wallet, err := application.NewOpenWallet(tx, wallets, wagers, ledger, outbox, clock, ids).
		Execute(context.Background(), application.OpenWalletInput{PlayerID: "player-1", InitialBalance: balance, CorrelationID: "c"})
	if err != nil {
		t.Fatalf("open wallet: %v", err)
	}
	return wagering{pool: pool, handler: application.NewHandleWagerMessage(tx, postgres.NewInboxRepository(pool), process, clock), wallet: wallet}
}

func (w wagering) balance(t *testing.T) string {
	t.Helper()
	wallet, err := postgres.NewWalletRepository(w.pool).Get(context.Background(), w.wallet.ID())
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	return wallet.Balance().Amount()
}

func (w wagering) betBody(messageID string) string {
	return strings.Replace(body(messageID, "tx-1", "provider-a"), "0192f291-27dd-7d3f-8071-5f8685deef37", w.wallet.ID(), 1)
}

func pollUntilEmpty(t *testing.T, q queuePair, c *Consumer) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for q.visibleInSource(t) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the queue did not drain")
		}
		poll(t, c)
	}
}

func TestRedeliveryAfterACrashBetweenCommitAndDeleteDoesNotDebitTwice(t *testing.T) {
	q := newQueuePair(t, 5)
	w := newWagering(t, testdb.NewPool(t))

	var calls atomic.Int32
	var secondDelivery application.HandleWagerMessageResult
	c := newTestConsumer(t, q, testOptions(q), handlerFunc(func(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
		result, err := w.handler.Execute(ctx, msg)
		if calls.Add(1) == 1 && err == nil {
			// Committed, but the process "dies" before deleting: the message is not removed and comes back.
			return result, errors.New("consumer crashed before deleting the message")
		}
		secondDelivery = result
		return result, err
	}))
	q.send(t, w.wallet.ID(), "d1", w.betBody("msg-1"))

	pollUntilEmpty(t, q, c)

	if calls.Load() != 2 || !secondDelivery.AlreadyHandled {
		t.Errorf("deliveries = %d, second = %+v; want the redelivery recognized by the inbox", calls.Load(), secondDelivery)
	}
	if got := w.balance(t); got != "75.00" {
		t.Errorf("balance = %s, want 75.00 (a single debit)", got)
	}
}

func TestMessageWaitsWhileTheDatabaseIsDownAndIsProcessedWhenItReturns(t *testing.T) {
	q := newQueuePair(t, 5)
	pool := testdb.NewPool(t)
	w := newWagering(t, pool)

	// A pool pointing at a closed port fails like PostgreSQL being down.
	downCfg := pool.Config().Copy()
	downCfg.ConnConfig.Host, downCfg.ConnConfig.Port = "127.0.0.1", 1
	downCfg.ConnConfig.ConnectTimeout = time.Second
	downPool, err := pgxpool.NewWithConfig(context.Background(), downCfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(downPool.Close)
	downTx := postgres.NewTxManager(downPool)
	downHandler := application.NewHandleWagerMessage(downTx, postgres.NewInboxRepository(downPool),
		application.NewProcessWagerTransaction(downTx, postgres.NewWalletRepository(downPool),
			postgres.NewWagerTransactionRepository(downPool), postgres.NewLedgerRepository(downPool), postgres.NewOutboxRepository(downPool),
			system.Clock{}, system.UUIDGenerator{}, application.DefaultReferenceRetryPolicy()),
		system.Clock{})

	var databaseUp atomic.Bool
	var failures atomic.Int32
	c := newTestConsumer(t, q, testOptions(q), handlerFunc(func(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
		if !databaseUp.Load() {
			_, err := downHandler.Execute(ctx, msg)
			if !errors.Is(err, application.ErrUnavailable) {
				t.Errorf("handler with the database down = %v, want ErrUnavailable", err)
			}
			failures.Add(1)
			return application.HandleWagerMessageResult{}, err
		}
		return w.handler.Execute(ctx, msg)
	}))
	q.send(t, w.wallet.ID(), "d1", w.betBody("msg-1"))

	poll(t, c)
	if failures.Load() != 1 || q.visibleInSource(t) != 1 {
		t.Fatalf("failures = %d, queue = %d; want the message kept after the failure", failures.Load(), q.visibleInSource(t))
	}

	databaseUp.Store(true)
	pollUntilEmpty(t, q, c)

	if got := w.balance(t); got != "75.00" {
		t.Errorf("balance = %s, want 75.00", got)
	}
	if letters := q.drainDLQ(t); len(letters) != 0 {
		t.Errorf("dlq = %+v, want empty", letters)
	}
}
