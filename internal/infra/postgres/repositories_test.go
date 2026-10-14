package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
	"wagering-service/internal/testdb"
)

var testNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

type fixture struct {
	pool    *pgxpool.Pool
	tx      *TxManager
	wallets *WalletRepository
	wagers  *WagerTransactionRepository
	ledger  *LedgerRepository
	outbox  *OutboxRepository
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := testdb.NewPool(t)
	return fixture{
		pool:    pool,
		tx:      NewTxManager(pool),
		wallets: NewWalletRepository(pool),
		wagers:  NewWagerTransactionRepository(pool),
		ledger:  NewLedgerRepository(pool),
		outbox:  NewOutboxRepository(pool),
	}
}

func id(n int) string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", n)
}

func money(t *testing.T, amount, currency string) domain.Money {
	t.Helper()
	m, err := domain.ParseMoney(amount, currency)
	if err != nil {
		t.Fatalf("ParseMoney(%q, %q): %v", amount, currency, err)
	}
	return m
}

func (f fixture) openWallet(t *testing.T, n int, balance domain.Money) *domain.Wallet {
	t.Helper()
	wallet, err := domain.OpenWallet(id(n), fmt.Sprintf("player-%d", n), balance, testNow)
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	if err := f.wallets.Insert(context.Background(), wallet); err != nil {
		t.Fatalf("insert wallet: %v", err)
	}
	return wallet
}

func newWager(t *testing.T, n int, wallet *domain.Wallet, kind domain.TransactionKind, amount domain.Money, reference string) *domain.WagerTransaction {
	t.Helper()
	tx, err := domain.NewWagerTransaction(domain.NewWagerTransactionParams{
		ID:                             id(n),
		ProviderID:                     "provider-a",
		ExternalTransactionID:          fmt.Sprintf("ext-%d", n),
		IdempotencyKey:                 fmt.Sprintf("key-%d", n),
		PayloadHash:                    "hash",
		WalletID:                       wallet.ID(),
		PlayerID:                       wallet.PlayerID(),
		RoundID:                        "round-1",
		GameID:                         "game-1",
		Kind:                           kind,
		Money:                          amount,
		ReferenceExternalTransactionID: reference,
		Now:                            testNow,
	})
	if err != nil {
		t.Fatalf("NewWagerTransaction: %v", err)
	}
	return tx
}

func (f fixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// applyBet runs the whole write path of one bet under the wallet lock.
func (f fixture) applyBet(ctx context.Context, t *testing.T, n int, walletID string, amount domain.Money) (*domain.WagerTransaction, error) {
	var tx *domain.WagerTransaction
	err := f.tx.WithinTx(ctx, func(ctx context.Context) error {
		wallet, err := f.wallets.GetForUpdate(ctx, walletID)
		if err != nil {
			return err
		}
		tx = newWager(t, n, wallet, domain.KindBet, amount, "")
		entry, err := domain.ApplyWager(tx, wallet, domain.Reference{}, id(1_000_000+n), testNow)
		if err != nil {
			return err
		}
		if err := f.wagers.Insert(ctx, tx); err != nil {
			return err
		}
		if entry == nil {
			return nil
		}
		if err := f.wallets.UpdateBalance(ctx, wallet); err != nil {
			return err
		}
		return f.ledger.Insert(ctx, *entry)
	})
	return tx, err
}

func TestWithinTxCommitsOrRollsBackEveryRepositoryTogether(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	errBoom := errors.New("boom")

	write := func(result error) error {
		return f.tx.WithinTx(ctx, func(ctx context.Context) error {
			balance := money(t, "100.00", "BRL")
			wallet, err := domain.OpenWallet(id(1), "player-1", balance, testNow)
			if err != nil {
				return err
			}
			opening, err := domain.NewOpeningTransaction(id(2), wallet.ID(), wallet.PlayerID(), balance, testNow)
			if err != nil {
				return err
			}
			zero, _ := domain.ZeroMoney("BRL")
			entry, err := domain.NewLedgerEntry(id(3), wallet.ID(), opening.ID(), domain.LedgerCredit, balance, zero, balance, testNow)
			if err != nil {
				return err
			}

			if err := f.wallets.Insert(ctx, wallet); err != nil {
				return err
			}
			if err := f.wagers.Insert(ctx, opening); err != nil {
				return err
			}
			if err := f.ledger.Insert(ctx, entry); err != nil {
				return err
			}
			err = f.outbox.Append(ctx, application.OutboxMessage{
				ID: id(4), AggregateID: wallet.ID(), EventType: "WalletBalanceChanged",
				Payload: []byte(`{"walletId":"w"}`), OccurredAt: testNow,
			})
			if err != nil {
				return err
			}
			return result
		})
	}

	tables := []string{"wallets", "wager_transactions", "wallet_ledger_entries", "outbox_events"}

	if err := write(errBoom); !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want the one returned by fn", err)
	}
	for _, table := range tables {
		if n := f.count(t, table); n != 0 {
			t.Errorf("%s has %d rows after a rollback", table, n)
		}
	}

	if err := write(nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	for _, table := range tables {
		if n := f.count(t, table); n != 1 {
			t.Errorf("%s has %d rows after a commit, want 1", table, n)
		}
	}
}

func TestNestedWithinTxJoinsTheOuterTransaction(t *testing.T) {
	f := newFixture(t)
	errBoom := errors.New("boom")

	err := f.tx.WithinTx(context.Background(), func(ctx context.Context) error {
		inner := f.tx.WithinTx(ctx, func(ctx context.Context) error {
			wallet, err := domain.OpenWallet(id(1), "player-1", money(t, "1.00", "BRL"), testNow)
			if err != nil {
				return err
			}
			return f.wallets.Insert(ctx, wallet)
		})
		if inner != nil {
			return inner
		}
		return errBoom
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want boom", err)
	}
	if n := f.count(t, "wallets"); n != 0 {
		t.Errorf("inner write survived the outer rollback: %d wallets", n)
	}
}

func TestWalletLockMakesTheSecondWriterWaitOnlyOnTheSameWallet(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.openWallet(t, 1, money(t, "100.00", "BRL"))
	f.openWallet(t, 2, money(t, "100.00", "BRL"))

	locked := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- f.tx.WithinTx(ctx, func(ctx context.Context) error {
			if _, err := f.wallets.GetForUpdate(ctx, id(1)); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	lock := func(ctx context.Context, walletID string) error {
		return f.tx.WithinTx(ctx, func(ctx context.Context) error {
			_, err := f.wallets.GetForUpdate(ctx, walletID)
			return err
		})
	}

	waiting, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := lock(waiting, id(1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("same wallet: error = %v, want the lock wait to be cut by the deadline", err)
	}

	if err := lock(ctx, id(2)); err != nil {
		t.Errorf("another wallet must not wait: %v", err)
	}

	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder: %v", err)
	}
	if err := lock(ctx, id(1)); err != nil {
		t.Errorf("after release: %v", err)
	}
}

func TestConcurrentBetsOnOneWalletLoseNoUpdate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const bets = 20
	f.openWallet(t, 1, money(t, "20.00", "BRL"))

	var wg sync.WaitGroup
	errs := make(chan error, bets)
	for n := range bets {
		wg.Go(func() {
			tx, err := f.applyBet(ctx, t, 100+n, id(1), money(t, "1.00", "BRL"))
			if err == nil && tx.Status() != domain.StatusProcessed {
				err = fmt.Errorf("bet %d ended %s", n, tx.Status())
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("bet: %v", err)
		}
	}

	wallet, err := f.wallets.Get(ctx, id(1))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !wallet.Balance().IsZero() || wallet.Version() != bets+1 {
		t.Errorf("wallet = %v v%d, want 0.00 v%d", wallet.Balance(), wallet.Version(), bets+1)
	}
	if n := f.count(t, "wallet_ledger_entries"); n != bets {
		t.Errorf("ledger has %d entries, want %d", n, bets)
	}

	rejected, err := f.applyBet(ctx, t, 999, id(1), money(t, "0.01", "BRL"))
	if err != nil {
		t.Fatalf("bet on an empty wallet: %v", err)
	}
	if rejected.Status() != domain.StatusRejected || rejected.FailureCode() != domain.FailureInsufficientFunds {
		t.Errorf("bet on an empty wallet = %s/%s", rejected.Status(), rejected.FailureCode())
	}
	if n := f.count(t, "wallet_ledger_entries"); n != bets {
		t.Errorf("a rejected bet wrote to the ledger: %d entries", n)
	}
}

func TestUpdateBalanceRefusesAWriteMadeWithoutTheLock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.openWallet(t, 1, money(t, "100.00", "BRL"))

	first, _ := f.wallets.Get(ctx, id(1))
	second, _ := f.wallets.Get(ctx, id(1))
	for _, wallet := range []*domain.Wallet{first, second} {
		if _, err := wallet.Debit(id(9), id(8), money(t, "80.00", "BRL"), testNow); err != nil {
			t.Fatalf("Debit: %v", err)
		}
	}

	if err := f.wallets.UpdateBalance(ctx, first); err != nil {
		t.Fatalf("first writer: %v", err)
	}
	if err := f.wallets.UpdateBalance(ctx, second); !errors.Is(err, application.ErrConcurrentUpdate) {
		t.Errorf("second writer: error = %v, want ErrConcurrentUpdate", err)
	}
}

func TestGetForUpdateRequiresATransaction(t *testing.T) {
	f := newFixture(t)
	f.openWallet(t, 1, money(t, "1.00", "BRL"))

	if _, err := f.wallets.GetForUpdate(context.Background(), id(1)); !errors.Is(err, errLockOutsideTx) {
		t.Errorf("error = %v, want errLockOutsideTx", err)
	}
}

func TestWalletRoundTripPreservesMoneyExactly(t *testing.T) {
	f := newFixture(t)
	balances := []domain.Money{
		money(t, "0.00", "BRL"),
		money(t, "0.01", "USD"),
		money(t, "1234567.89", "EUR"),
		money(t, "92233720368547758.07", "BRL"),
	}

	for n, balance := range balances {
		t.Run(balance.String(), func(t *testing.T) {
			stored := f.openWallet(t, n+1, balance)

			loaded, err := f.wallets.Get(context.Background(), stored.ID())
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !loaded.Balance().Equal(balance) {
				t.Errorf("balance = %v, want %v", loaded.Balance(), balance)
			}
			if loaded.PlayerID() != stored.PlayerID() || loaded.Version() != 1 ||
				!loaded.CreatedAt().Equal(testNow) || !loaded.UpdatedAt().Equal(testNow) {
				t.Errorf("loaded = %q v%d %v/%v", loaded.PlayerID(), loaded.Version(), loaded.CreatedAt(), loaded.UpdatedAt())
			}
		})
	}
}

func assertSameTransaction(t *testing.T, got, want *domain.WagerTransaction) {
	t.Helper()
	gotBalance, gotObserved := got.ResultBalance()
	wantBalance, wantObserved := want.ResultBalance()

	checks := []struct {
		field     string
		got, want any
	}{
		{"id", got.ID(), want.ID()},
		{"kind", got.Kind(), want.Kind()},
		{"status", got.Status(), want.Status()},
		{"walletId", got.WalletID(), want.WalletID()},
		{"playerId", got.PlayerID(), want.PlayerID()},
		{"money", got.Money(), want.Money()},
		{"providerId", got.ProviderID(), want.ProviderID()},
		{"externalTransactionId", got.ExternalTransactionID(), want.ExternalTransactionID()},
		{"idempotencyKey", got.IdempotencyKey(), want.IdempotencyKey()},
		{"payloadHash", got.PayloadHash(), want.PayloadHash()},
		{"roundId", got.RoundID(), want.RoundID()},
		{"gameId", got.GameID(), want.GameID()},
		{"referenceExternalTransactionId", got.ReferenceExternalTransactionID(), want.ReferenceExternalTransactionID()},
		{"referenceTransactionId", got.ReferenceTransactionID(), want.ReferenceTransactionID()},
		{"failureCode", got.FailureCode(), want.FailureCode()},
		{"resultBalance", gotBalance, wantBalance},
		{"resultBalance observed", gotObserved, wantObserved},
		{"createdAt", got.CreatedAt(), want.CreatedAt()},
		{"updatedAt", got.UpdatedAt(), want.UpdatedAt()},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

func TestWagerTransactionRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	later := testNow.Add(time.Minute)
	wallet := f.openWallet(t, 1, money(t, "100.00", "BRL"))

	opening, err := domain.NewOpeningTransaction(id(10), wallet.ID(), wallet.PlayerID(), money(t, "100.00", "BRL"), testNow)
	if err != nil {
		t.Fatalf("NewOpeningTransaction: %v", err)
	}

	bet := newWager(t, 11, wallet, domain.KindBet, money(t, "25.00", "BRL"), "")
	if _, err := domain.ApplyWager(bet, wallet, domain.Reference{}, id(50), later); err != nil {
		t.Fatalf("apply bet: %v", err)
	}

	inAnotherCurrency := newWager(t, 12, wallet, domain.KindBet, money(t, "5.00", "USD"), "")
	if _, err := domain.ApplyWager(inAnotherCurrency, wallet, domain.Reference{}, id(51), later); err != nil {
		t.Fatalf("apply bet in another currency: %v", err)
	}

	stored := map[string]*domain.WagerTransaction{
		"internal opening":                       opening,
		"processed bet":                          bet,
		"rejection reporting the wallet balance": inAnotherCurrency,
	}
	for name, tx := range stored {
		t.Run(name, func(t *testing.T) {
			if err := f.wagers.Insert(ctx, tx); err != nil {
				t.Fatalf("Insert: %v", err)
			}
			loaded, err := f.wagers.Get(ctx, tx.ID())
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			assertSameTransaction(t, loaded, tx)
		})
	}

	t.Run("lookups by provider", func(t *testing.T) {
		byExternal, err := f.wagers.GetByExternalID(ctx, "provider-a", "ext-11")
		if err != nil {
			t.Fatalf("GetByExternalID: %v", err)
		}
		assertSameTransaction(t, byExternal, bet)

		byKey, err := f.wagers.GetByIdempotencyKey(ctx, "provider-a", "key-11")
		if err != nil {
			t.Fatalf("GetByIdempotencyKey: %v", err)
		}
		assertSameTransaction(t, byKey, bet)

		if _, err := f.wagers.GetByExternalID(ctx, "provider-b", "ext-11"); !errors.Is(err, application.ErrTransactionNotFound) {
			t.Errorf("another provider: error = %v, want ErrTransactionNotFound", err)
		}
	})

	t.Run("update after a pending reference is resolved", func(t *testing.T) {
		refund := newWager(t, 13, wallet, domain.KindRefund, money(t, "25.00", "BRL"), "ext-11")
		if _, err := domain.ApplyWager(refund, wallet, domain.Reference{}, id(52), later); err != nil {
			t.Fatalf("apply refund without reference: %v", err)
		}
		if err := f.wagers.Insert(ctx, refund); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		pending, err := f.wagers.Get(ctx, refund.ID())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertSameTransaction(t, pending, refund)

		if _, err := domain.ApplyWager(refund, wallet, domain.Reference{Transaction: bet}, id(52), later.Add(time.Minute)); err != nil {
			t.Fatalf("apply refund with reference: %v", err)
		}
		if err := f.wagers.Update(ctx, refund); err != nil {
			t.Fatalf("Update: %v", err)
		}
		resolved, err := f.wagers.Get(ctx, refund.ID())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if resolved.Status() != domain.StatusProcessed || resolved.ReferenceTransactionID() != bet.ID() {
			t.Errorf("resolved = %s referencing %q", resolved.Status(), resolved.ReferenceTransactionID())
		}
		assertSameTransaction(t, resolved, refund)
	})
}

func TestConstraintViolationsBecomeApplicationErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	wallet := f.openWallet(t, 1, money(t, "100.00", "BRL"))
	bet := newWager(t, 10, wallet, domain.KindBet, money(t, "1.00", "BRL"), "")
	if err := f.wagers.Insert(ctx, bet); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	sameWallet, _ := domain.OpenWallet(id(2), wallet.PlayerID(), money(t, "1.00", "BRL"), testNow)

	sameExternal := newWager(t, 11, wallet, domain.KindBet, money(t, "1.00", "BRL"), "")
	sameKey := newWager(t, 12, wallet, domain.KindBet, money(t, "1.00", "BRL"), "")
	state := func(tx *domain.WagerTransaction, externalID, key string) *domain.WagerTransaction {
		rehydrated, err := domain.RehydrateWagerTransaction(domain.WagerTransactionState{
			ID: tx.ID(), Kind: tx.Kind(), Status: tx.Status(), WalletID: tx.WalletID(), PlayerID: tx.PlayerID(),
			Money: tx.Money(), ProviderID: tx.ProviderID(), ExternalTransactionID: externalID, IdempotencyKey: key,
			PayloadHash: tx.PayloadHash(), RoundID: tx.RoundID(), GameID: tx.GameID(),
			CreatedAt: tx.CreatedAt(), UpdatedAt: tx.UpdatedAt(),
		})
		if err != nil {
			t.Fatalf("RehydrateWagerTransaction: %v", err)
		}
		return rehydrated
	}

	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{"second wallet for the player and currency", func() error { return f.wallets.Insert(ctx, sameWallet) }, application.ErrWalletAlreadyExists},
		{"same external transaction", func() error { return f.wagers.Insert(ctx, state(sameExternal, "ext-10", "key-11")) }, application.ErrDuplicateExternalTransaction},
		{"same idempotency key", func() error { return f.wagers.Insert(ctx, state(sameKey, "ext-12", "key-10")) }, application.ErrDuplicateIdempotencyKey},
		{"unknown wallet", func() error { _, err := f.wallets.Get(ctx, id(404)); return err }, application.ErrWalletNotFound},
		{"unknown transaction", func() error { _, err := f.wagers.Get(ctx, id(404)); return err }, application.ErrTransactionNotFound},
		{"update of an unknown transaction", func() error { return f.wagers.Update(ctx, state(sameKey, "ext-12", "key-12")) }, application.ErrTransactionNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestOutboxAppendSchedulesTheFirstAttemptAtTheOccurrence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	err := f.outbox.Append(ctx, application.OutboxMessage{
		ID: id(1), AggregateID: id(2), EventType: "WalletBalanceChanged",
		Payload: []byte(`{"money":{"amount":"25.00","currency":"BRL"}}`), OccurredAt: testNow,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	var (
		amount        string
		attempts      int
		nextAttemptAt time.Time
		published     bool
	)
	err = f.pool.QueryRow(ctx, `
		SELECT payload #>> '{money,amount}', attempts, next_attempt_at, published_at IS NOT NULL
		  FROM outbox_events WHERE id = $1`, id(1)).Scan(&amount, &attempts, &nextAttemptAt, &published)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if amount != "25.00" || attempts != 0 || !nextAttemptAt.Equal(testNow) || published {
		t.Errorf("outbox row = amount %q, attempts %d, next %v, published %v", amount, attempts, nextAttemptAt, published)
	}
}

func TestInboxReceiveTellsTheFirstDeliveryFromALaterOne(t *testing.T) {
	f := newFixture(t)
	inbox := NewInboxRepository(f.pool)
	ctx := context.Background()

	err := f.tx.WithinTx(ctx, func(ctx context.Context) error {
		stored, first, err := inbox.Receive(ctx, "consumer", "msg-1", "hash-1", testNow)
		if err != nil || !first || stored.PayloadHash != "hash-1" {
			t.Errorf("first Receive = %+v, %v, %v", stored, first, err)
		}
		return inbox.Complete(ctx, "consumer", "msg-1", testNow.Add(time.Second))
	})
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	stored, first, err := inbox.Receive(ctx, "consumer", "msg-1", "hash-2", testNow.Add(time.Minute))
	if err != nil || first || stored != (application.InboxRecord{PayloadHash: "hash-1", Completed: true}) {
		t.Errorf("redelivery = %+v, %v, %v; want the stored record of the first delivery", stored, first, err)
	}
	if err := inbox.Complete(ctx, "consumer", "msg-1", testNow.Add(time.Minute)); !errors.Is(err, application.ErrInvariantViolation) {
		t.Errorf("completing twice: %v, want ErrInvariantViolation", err)
	}

	if _, first, err := inbox.Receive(ctx, "other-consumer", "msg-1", "hash-1", testNow); err != nil || !first {
		t.Errorf("another consumer = %v, %v; the message id is unique per consumer only", first, err)
	}
}

func TestInboxRowDisappearsWithARolledBackTransaction(t *testing.T) {
	f := newFixture(t)
	inbox := NewInboxRepository(f.pool)
	ctx := context.Background()
	failure := errors.New("use case failed")

	err := f.tx.WithinTx(ctx, func(ctx context.Context) error {
		if _, _, err := inbox.Receive(ctx, "consumer", "msg-1", "hash-1", testNow); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("WithinTx = %v", err)
	}
	if n := f.count(t, "inbox_messages"); n != 0 {
		t.Errorf("inbox has %d rows after rollback, want 0", n)
	}
}

func TestLedgerSummarizeNetsCreditsAndDebits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	wallet := f.openWallet(t, 1, money(t, "100.00", "BRL"))
	if _, err := f.applyBet(ctx, t, 2, wallet.ID(), money(t, "30.00", "BRL")); err != nil {
		t.Fatalf("bet: %v", err)
	}

	if _, err := f.applyBet(ctx, t, 3, wallet.ID(), money(t, "20.00", "BRL")); err != nil {
		t.Fatalf("bet: %v", err)
	}

	// The fixture opens wallets without an opening entry, so the ledger holds only the two debits.
	summary, err := f.ledger.Summarize(ctx, wallet.ID())
	if err != nil || summary != (application.LedgerSummary{NetMinorUnits: -5000, Entries: 2}) {
		t.Errorf("Summarize = %+v, %v; want -50.00 over 2 entries", summary, err)
	}
	empty, err := f.ledger.Summarize(ctx, id(99))
	if err != nil || empty != (application.LedgerSummary{}) {
		t.Errorf("Summarize of an unknown wallet = %+v, %v; want zero", empty, err)
	}
}

func TestWithinSnapshotIsReadOnlyAndCannotNest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	wallet := f.openWallet(t, 1, money(t, "100.00", "BRL"))

	err := f.tx.WithinSnapshot(ctx, func(ctx context.Context) error {
		locked, err := f.wallets.GetForUpdate(ctx, wallet.ID())
		if err != nil {
			return err
		}
		if _, err := locked.Debit(id(50), id(51), money(t, "1.00", "BRL"), testNow); err != nil {
			return err
		}
		return f.wallets.UpdateBalance(ctx, locked)
	})
	if err == nil {
		t.Error("a write inside the snapshot succeeded; it must be read-only")
	}

	err = f.tx.WithinTx(ctx, func(ctx context.Context) error {
		return f.tx.WithinSnapshot(ctx, func(context.Context) error { return nil })
	})
	if err == nil {
		t.Error("a snapshot inside a transaction would silently get the outer isolation level")
	}
}
