package application_test

import (
	"context"
	"errors"
	"testing"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
	"wagering-service/internal/infra/postgres"
	"wagering-service/internal/infra/system"
	"wagering-service/internal/testdb"
)

var errOutboxDown = errors.New("outbox down")

// failingOutbox accepts the first event and fails on the second, after everything else was written.
type failingOutbox struct {
	application.OutboxRepository
	calls int
}

func (o *failingOutbox) Append(ctx context.Context, message application.OutboxMessage) error {
	o.calls++
	if o.calls == 2 {
		return errOutboxDown
	}
	return o.OutboxRepository.Append(ctx, message)
}

func TestOpenWalletIsAllOrNothing(t *testing.T) {
	pool := testdb.NewPool(t)
	outbox := &failingOutbox{OutboxRepository: postgres.NewOutboxRepository(pool)}
	openWallet := application.NewOpenWallet(
		postgres.NewTxManager(pool),
		postgres.NewWalletRepository(pool),
		postgres.NewWagerTransactionRepository(pool),
		postgres.NewLedgerRepository(pool),
		outbox,
		system.Clock{},
		system.UUIDGenerator{},
	)
	balance, err := domain.ParseMoney("1000.00", "BRL")
	if err != nil {
		t.Fatalf("ParseMoney: %v", err)
	}

	_, err = openWallet.Execute(context.Background(), application.OpenWalletInput{
		PlayerID: "player-1", InitialBalance: balance, CorrelationID: "corr-1",
	})

	if !errors.Is(err, errOutboxDown) {
		t.Fatalf("error = %v, want the outbox failure", err)
	}
	if outbox.calls != 2 {
		t.Fatalf("outbox was called %d times, want 2", outbox.calls)
	}
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "outbox_events"} {
		var n int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s kept %d rows after the failure", table, n)
		}
	}
}
