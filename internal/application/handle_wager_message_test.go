package application_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
	"wagering-service/internal/infra/postgres"
	"wagering-service/internal/infra/system"
)

type inboxEnv struct {
	*outboxEnv
	wallets *postgres.WalletRepository
	handle  *application.HandleWagerMessage
}

func newInboxEnv(t *testing.T) *inboxEnv {
	t.Helper()
	env := newOutboxEnv(t)
	return &inboxEnv{
		outboxEnv: env,
		wallets:   postgres.NewWalletRepository(env.pool),
		handle: application.NewHandleWagerMessage(postgres.NewTxManager(env.pool), postgres.NewInboxRepository(env.pool),
			env.process, system.Clock{}),
	}
}

func (e *inboxEnv) wallet(player, balance string) string {
	e.t.Helper()
	amount, _ := domain.ParseMoney(balance, "BRL")
	wallet, err := e.open.Execute(context.Background(), application.OpenWalletInput{PlayerID: player, InitialBalance: amount, CorrelationID: "corr"})
	if err != nil {
		e.t.Fatalf("open wallet: %v", err)
	}
	return wallet.ID()
}

func (e *inboxEnv) balance(walletID string) string {
	e.t.Helper()
	wallet, err := e.wallets.Get(context.Background(), walletID)
	if err != nil {
		e.t.Fatalf("get wallet: %v", err)
	}
	return wallet.Balance().Amount()
}

func (e *inboxEnv) countRows(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count: %v", err)
	}
	return n
}

func wager(walletID, player, external, kind, amount, reference string) application.ProcessWagerInput {
	money, _ := domain.ParseMoney(amount, "BRL")
	return application.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: external, IdempotencyKey: "provider-a:" + external,
		PlayerID: player, WalletID: walletID, RoundID: "round-1", GameID: "game-1",
		Kind: kind, Money: money, ReferenceExternalTransactionID: reference, CorrelationID: "corr",
	}
}

func message(id string, in application.ProcessWagerInput) application.WagerMessage {
	return application.WagerMessage{MessageID: id, PayloadHash: "hash-of-" + id, Wager: in}
}

func TestRedeliveredMessageMovesTheBalanceOnce(t *testing.T) {
	env := newInboxEnv(t)
	walletID := env.wallet("player-1", "100.00")
	msg := message("msg-1", wager(walletID, "player-1", "bet-1", "BET", "25.00", ""))

	first, err := env.handle.Execute(context.Background(), msg)
	if err != nil || first.AlreadyHandled || first.Transaction.Status() != domain.StatusProcessed {
		t.Fatalf("first delivery = %+v, %v", first, err)
	}
	second, err := env.handle.Execute(context.Background(), msg)
	if err != nil || !second.AlreadyHandled || second.Transaction != nil {
		t.Fatalf("redelivery = %+v, %v; want AlreadyHandled", second, err)
	}

	if got := env.balance(walletID); got != "75.00" {
		t.Errorf("balance = %s, want 75.00", got)
	}
	if n := env.countRows("SELECT count(*) FROM inbox_messages WHERE message_id = 'msg-1' AND completed_at IS NOT NULL"); n != 1 {
		t.Errorf("completed inbox rows = %d, want 1", n)
	}
}

func TestSameMessageIDWithAnotherBodyIsAConflict(t *testing.T) {
	env := newInboxEnv(t)
	walletID := env.wallet("player-1", "100.00")
	if _, err := env.handle.Execute(context.Background(), message("msg-1", wager(walletID, "player-1", "bet-1", "BET", "25.00", ""))); err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	other := message("msg-1", wager(walletID, "player-1", "bet-2", "BET", "50.00", ""))
	other.PayloadHash = "another-hash"
	if _, err := env.handle.Execute(context.Background(), other); !errors.Is(err, application.ErrMessageConflict) {
		t.Fatalf("err = %v, want ErrMessageConflict", err)
	}
	if got := env.balance(walletID); got != "75.00" {
		t.Errorf("balance = %s, want 75.00: the conflicting message must not move money", got)
	}
}

func TestMessageForAnOperationAlreadySentByHTTPIsAReplay(t *testing.T) {
	env := newInboxEnv(t)
	walletID := env.wallet("player-1", "100.00")
	in := wager(walletID, "player-1", "bet-1", "BET", "25.00", "")
	if _, err := env.process.Execute(context.Background(), in); err != nil {
		t.Fatalf("http: %v", err)
	}

	result, err := env.handle.Execute(context.Background(), message("msg-1", in))
	if err != nil || !result.Replay || result.AlreadyHandled {
		t.Fatalf("message = %+v, %v; want a replay of the HTTP operation", result, err)
	}
	if got := env.balance(walletID); got != "75.00" {
		t.Errorf("balance = %s, want 75.00", got)
	}
}

func TestHTTPAndSQSRacingForTheSameOperationDebitOnce(t *testing.T) {
	env := newInboxEnv(t)
	walletID := env.wallet("player-1", "1000.00")
	const rounds = 20

	for i := range rounds {
		in := wager(walletID, "player-1", fmt.Sprintf("bet-%d", i), "BET", "10.00", "")
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for j := range 4 {
			wg.Go(func() {
				if j%2 == 0 {
					_, errs[j] = env.process.Execute(context.Background(), in)
				} else {
					// Two deliveries of the same SQS message, as after a visibility timeout.
					_, errs[j] = env.handle.Execute(context.Background(), message(fmt.Sprintf("msg-%d", i), in))
				}
			})
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
		}
	}

	if got := env.balance(walletID); got != "800.00" {
		t.Errorf("balance = %s, want 800.00 (one debit of 10.00 per operation)", got)
	}
	if n := env.countRows("SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'BET'", walletID); n != rounds {
		t.Errorf("bets stored = %d, want %d", n, rounds)
	}
}

func TestBusinessRejectionAndPendingReferenceCompleteTheMessage(t *testing.T) {
	env := newInboxEnv(t)
	walletID := env.wallet("player-1", "10.00")

	rejected, err := env.handle.Execute(context.Background(), message("msg-1", wager(walletID, "player-1", "bet-1", "BET", "50.00", "")))
	if err != nil || rejected.Transaction.Status() != domain.StatusRejected || rejected.Transaction.FailureCode() != domain.FailureInsufficientFunds {
		t.Fatalf("rejected = %+v, %v", rejected, err)
	}
	pending, err := env.handle.Execute(context.Background(), message("msg-2", wager(walletID, "player-1", "refund-1", "REFUND", "5.00", "bet-later")))
	if err != nil || pending.Transaction.Status() != domain.StatusPendingReference {
		t.Fatalf("pending = %+v, %v", pending, err)
	}

	if n := env.countRows("SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL"); n != 2 {
		t.Errorf("completed inbox rows = %d, want 2: both outcomes are final for the message", n)
	}
}

func TestFailedHandlingLeavesNoInboxRecord(t *testing.T) {
	env := newInboxEnv(t)
	missingWallet := "00000000-0000-7000-8000-000000000999"

	_, err := env.handle.Execute(context.Background(), message("msg-1", wager(missingWallet, "player-1", "bet-1", "BET", "5.00", "")))
	if !errors.Is(err, application.ErrWalletNotFound) {
		t.Fatalf("err = %v, want ErrWalletNotFound", err)
	}
	if n := env.countRows("SELECT count(*) FROM inbox_messages"); n != 0 {
		t.Errorf("inbox rows = %d, want 0: the record shares the rolled back transaction", n)
	}
}

func TestMessageWithoutIdentityIsInvalid(t *testing.T) {
	env := newInboxEnv(t)
	walletID := env.wallet("player-1", "10.00")
	msg := message("", wager(walletID, "player-1", "bet-1", "BET", "5.00", ""))

	var validation *domain.ValidationError
	if _, err := env.handle.Execute(context.Background(), msg); !errors.As(err, &validation) || validation.Field != "messageId" {
		t.Errorf("err = %v, want a messageId validation error", err)
	}
}
