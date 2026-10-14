package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
)

type WalletRepository struct {
	pool *pgxpool.Pool
}

func NewWalletRepository(pool *pgxpool.Pool) *WalletRepository {
	return &WalletRepository{pool: pool}
}

func (r *WalletRepository) Insert(ctx context.Context, wallet *domain.Wallet) error {
	_, err := querierFrom(ctx, r.pool).Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		wallet.ID(), wallet.PlayerID(), wallet.Currency(), wallet.Balance().MinorUnits(),
		wallet.Version(), wallet.CreatedAt(), wallet.UpdatedAt(),
	)
	return translate(err)
}

const selectWallet = `
	SELECT id::text, player_id, currency, balance_minor, version, created_at, updated_at
	  FROM wallets
	 WHERE id = $1`

func (r *WalletRepository) Get(ctx context.Context, id string) (*domain.Wallet, error) {
	return scanWallet(querierFrom(ctx, r.pool).QueryRow(ctx, selectWallet, id))
}

func (r *WalletRepository) GetForUpdate(ctx context.Context, id string) (*domain.Wallet, error) {
	if !inTx(ctx) {
		return nil, errLockOutsideTx
	}
	return scanWallet(querierFrom(ctx, r.pool).QueryRow(ctx, selectWallet+" FOR UPDATE", id))
}

func (r *WalletRepository) UpdateBalance(ctx context.Context, wallet *domain.Wallet) error {
	// The version predicate turns a write made without the row lock into an error instead of a lost update.
	tag, err := querierFrom(ctx, r.pool).Exec(ctx, `
		UPDATE wallets
		   SET balance_minor = $2, version = $3, updated_at = $4
		 WHERE id = $1 AND version = $3 - 1`,
		wallet.ID(), wallet.Balance().MinorUnits(), wallet.Version(), wallet.UpdatedAt(),
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return application.ErrConcurrentUpdate
	}
	return nil
}

func scanWallet(row pgx.Row) (*domain.Wallet, error) {
	var (
		id, playerID, currency string
		balanceMinor, version  int64
		createdAt, updatedAt   time.Time
	)
	err := row.Scan(&id, &playerID, &currency, &balanceMinor, &version, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrWalletNotFound
	}
	if err != nil {
		return nil, translate(err)
	}

	balance, err := domain.MoneyFromMinorUnits(balanceMinor, currency)
	if err != nil {
		return nil, err
	}
	return domain.RehydrateWallet(id, playerID, balance, version, createdAt.UTC(), updatedAt.UTC())
}
