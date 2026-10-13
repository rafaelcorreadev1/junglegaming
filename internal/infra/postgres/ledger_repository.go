package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/domain"
)

type LedgerRepository struct {
	pool *pgxpool.Pool
}

func NewLedgerRepository(pool *pgxpool.Pool) *LedgerRepository {
	return &LedgerRepository{pool: pool}
}

func (r *LedgerRepository) Insert(ctx context.Context, entry domain.LedgerEntry) error {
	_, err := querierFrom(ctx, r.pool).Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor, currency,
			balance_before_minor, balance_after_minor, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		entry.ID(), entry.WalletID(), entry.TransactionID(), string(entry.Direction()),
		entry.Amount().MinorUnits(), entry.Amount().Currency(),
		entry.BalanceBefore().MinorUnits(), entry.BalanceAfter().MinorUnits(), entry.CreatedAt(),
	)
	return translate(err)
}
