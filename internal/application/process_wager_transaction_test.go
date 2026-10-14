package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
	"wagering-service/internal/infra/postgres"
	"wagering-service/internal/infra/system"
	"wagering-service/internal/testdb"
)

// racingWagers simulates another instance committing the same operation between this one's duplicate check and its insert.
type racingWagers struct {
	application.WagerTransactionRepository
	pool  *pgxpool.Pool
	raced bool
	twin  func(*domain.WagerTransaction) *domain.WagerTransaction
}

func (r *racingWagers) Insert(ctx context.Context, tx *domain.WagerTransaction) error {
	if !r.raced {
		r.raced = true
		// context.Background() carries no transaction, so the twin is committed on its own connection.
		if err := postgres.NewWagerTransactionRepository(r.pool).Insert(context.Background(), r.twin(tx)); err != nil {
			return err
		}
	}
	return r.WagerTransactionRepository.Insert(ctx, tx)
}

// The twin lives in another wallet: on the same wallet the row lock would have made the other instance wait.
func twinOf(t *testing.T, id, walletID string, payloadHash func(*domain.WagerTransaction) string) func(*domain.WagerTransaction) *domain.WagerTransaction {
	return func(tx *domain.WagerTransaction) *domain.WagerTransaction {
		balance, _ := tx.ResultBalance()
		twin, err := domain.RehydrateWagerTransaction(domain.WagerTransactionState{
			ID: id, Kind: tx.Kind(), Status: tx.Status(), WalletID: walletID, PlayerID: tx.PlayerID(), Money: tx.Money(),
			ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(), IdempotencyKey: tx.IdempotencyKey(),
			PayloadHash: payloadHash(tx), RoundID: tx.RoundID(), GameID: tx.GameID(),
			ResultBalance: balance, CreatedAt: tx.CreatedAt(), UpdatedAt: tx.UpdatedAt(),
		})
		if err != nil {
			t.Fatalf("RehydrateWagerTransaction: %v", err)
		}
		return twin
	}
}

func TestProcessRecoversWhenAnotherInstanceWinsTheInsert(t *testing.T) {
	const twinID = "00000000-0000-7000-8000-000000000001"

	tests := []struct {
		name        string
		payloadHash func(*domain.WagerTransaction) string
		wantErr     error
	}{
		{"same payload becomes a replay", func(tx *domain.WagerTransaction) string { return tx.PayloadHash() }, nil},
		{"another payload becomes a conflict", func(*domain.WagerTransaction) string { return "another-hash" }, application.ErrIdempotencyConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testdb.NewPool(t)
			ctx := context.Background()
			wallets := postgres.NewWalletRepository(pool)
			balance, _ := domain.ParseMoney("100.00", "BRL")
			openWallet := func(player string) *domain.Wallet {
				wallet, err := domain.OpenWallet(system.UUIDGenerator{}.NewID(), player, balance, system.Clock{}.Now())
				if err != nil {
					t.Fatalf("OpenWallet: %v", err)
				}
				if err := wallets.Insert(ctx, wallet); err != nil {
					t.Fatalf("insert wallet: %v", err)
				}
				return wallet
			}
			wallet, otherWallet := openWallet("player-1"), openWallet("player-2")

			wagers := &racingWagers{
				WagerTransactionRepository: postgres.NewWagerTransactionRepository(pool),
				pool:                       pool,
				twin:                       twinOf(t, twinID, otherWallet.ID(), tt.payloadHash),
			}
			process := application.NewProcessWagerTransaction(
				postgres.NewTxManager(pool), wallets, wagers,
				postgres.NewLedgerRepository(pool), postgres.NewOutboxRepository(pool),
				system.Clock{}, system.UUIDGenerator{}, application.DefaultReferenceRetryPolicy(),
			)

			bet, _ := domain.ParseMoney("25.00", "BRL")

			result, err := process.Execute(ctx, application.ProcessWagerInput{
				ProviderID: "provider-a", ExternalTransactionID: "transaction-123", IdempotencyKey: "key-1",
				PlayerID: "player-1", WalletID: wallet.ID(), RoundID: "round-1", GameID: "game-1",
				Kind: "BET", Money: bet, CorrelationID: "corr-1",
			})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (!result.Replay || result.Transaction.ID() != twinID) {
				t.Errorf("result = replay %v, transaction %s; want a replay of the twin", result.Replay, result.Transaction.ID())
			}

			// The losing attempt was rolled back whole: the wallet was not debited and nothing but the twin exists.
			stored, err := wallets.Get(ctx, wallet.ID())
			if err != nil {
				t.Fatalf("Get wallet: %v", err)
			}
			if stored.Balance().Amount() != "100.00" || stored.Version() != 1 {
				t.Errorf("wallet = %v v%d, want 100.00 v1", stored.Balance(), stored.Version())
			}
			for table, want := range map[string]int{"wager_transactions": 1, "wallet_ledger_entries": 0, "outbox_events": 0} {
				var n int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
					t.Fatalf("count %s: %v", table, err)
				}
				if n != want {
					t.Errorf("%s has %d rows, want %d", table, n, want)
				}
			}
		})
	}
}
