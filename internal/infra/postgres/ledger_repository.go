package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
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

func (r *LedgerRepository) Summarize(ctx context.Context, walletID string) (application.LedgerSummary, error) {
	var summary application.LedgerSummary
	// SUM of bigint is numeric; the cast back fails loudly instead of wrapping if the total ever left the int64 range.
	err := querierFrom(ctx, r.pool).QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)::bigint, count(*)
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1`,
		walletID,
	).Scan(&summary.NetMinorUnits, &summary.Entries)
	if err != nil {
		return application.LedgerSummary{}, translate(err)
	}
	return summary, nil
}

func (r *LedgerRepository) ListByWallet(ctx context.Context, walletID string, afterSequence int64, limit int) ([]application.LedgerRecord, error) {
	rows, err := querierFrom(ctx, r.pool).Query(ctx, `
		SELECT sequence, id::text, wallet_id::text, transaction_id::text, direction, amount_minor, currency,
		       balance_before_minor, balance_after_minor, created_at
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1 AND sequence > $2
		 ORDER BY sequence
		 LIMIT $3`,
		walletID, afterSequence, limit,
	)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var records []application.LedgerRecord
	for rows.Next() {
		var (
			sequence                         int64
			id, entryWalletID, transactionID string
			direction, currency              string
			amount, before, after            int64
			createdAt                        time.Time
		)
		if err := rows.Scan(&sequence, &id, &entryWalletID, &transactionID, &direction, &amount, &currency, &before, &after, &createdAt); err != nil {
			return nil, translate(err)
		}
		entry, err := rehydrateLedgerEntry(id, entryWalletID, transactionID, direction, currency, amount, before, after, createdAt)
		if err != nil {
			return nil, err
		}
		records = append(records, application.LedgerRecord{Sequence: sequence, Entry: entry})
	}
	return records, translate(rows.Err())
}

func rehydrateLedgerEntry(id, walletID, transactionID, direction, currency string, amount, before, after int64, createdAt time.Time) (domain.LedgerEntry, error) {
	amountMoney, err := domain.MoneyFromMinorUnits(amount, currency)
	if err != nil {
		return domain.LedgerEntry{}, err
	}
	beforeMoney, err := domain.MoneyFromMinorUnits(before, currency)
	if err != nil {
		return domain.LedgerEntry{}, err
	}
	afterMoney, err := domain.MoneyFromMinorUnits(after, currency)
	if err != nil {
		return domain.LedgerEntry{}, err
	}
	return domain.NewLedgerEntry(id, walletID, transactionID, domain.LedgerDirection(direction), amountMoney, beforeMoney, afterMoney, createdAt.UTC())
}
