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

type WagerTransactionRepository struct {
	pool *pgxpool.Pool
}

func NewWagerTransactionRepository(pool *pgxpool.Pool) *WagerTransactionRepository {
	return &WagerTransactionRepository{pool: pool}
}

// The domain uses "" for an absent optional field; NULLIF and COALESCE convert to and from NULL at the SQL boundary.
func (r *WagerTransactionRepository) Insert(ctx context.Context, tx *domain.WagerTransaction) error {
	resultMinor, resultCurrency := resultBalanceColumns(tx)
	_, err := querierFrom(ctx, r.pool).Exec(ctx, `
		INSERT INTO wager_transactions (
			id, kind, status, wallet_id, player_id, amount_minor, currency,
			provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			reference_external_transaction_id, reference_transaction_id,
			failure_code, result_balance_minor, result_balance_currency, created_at, updated_at,
			correlation_id, reference_attempts, next_reference_attempt_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''),
			NULLIF($14, ''), NULLIF($15, '')::uuid,
			NULLIF($16, ''), $17, $18, $19, $20,
			NULLIF($21, ''), $22, $23
		)`,
		tx.ID(), string(tx.Kind()), string(tx.Status()), tx.WalletID(), tx.PlayerID(),
		tx.Money().MinorUnits(), tx.Money().Currency(),
		tx.ProviderID(), tx.ExternalTransactionID(), tx.IdempotencyKey(), tx.PayloadHash(), tx.RoundID(), tx.GameID(),
		tx.ReferenceExternalTransactionID(), tx.ReferenceTransactionID(),
		string(tx.FailureCode()), resultMinor, resultCurrency, tx.CreatedAt(), tx.UpdatedAt(),
		tx.CorrelationID(), tx.ReferenceAttempts(), nextReferenceAttemptColumn(tx),
	)
	return translate(err)
}

func (r *WagerTransactionRepository) Update(ctx context.Context, tx *domain.WagerTransaction) error {
	resultMinor, resultCurrency := resultBalanceColumns(tx)
	tag, err := querierFrom(ctx, r.pool).Exec(ctx, `
		UPDATE wager_transactions
		   SET status = $2,
		       reference_transaction_id = NULLIF($3, '')::uuid,
		       failure_code = NULLIF($4, ''),
		       result_balance_minor = $5,
		       result_balance_currency = $6,
		       updated_at = $7,
		       reference_attempts = $8,
		       next_reference_attempt_at = $9
		 WHERE id = $1`,
		tx.ID(), string(tx.Status()), tx.ReferenceTransactionID(),
		string(tx.FailureCode()), resultMinor, resultCurrency, tx.UpdatedAt(),
		tx.ReferenceAttempts(), nextReferenceAttemptColumn(tx),
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return application.ErrTransactionNotFound
	}
	return nil
}

const selectWagerTransaction = `
	SELECT id::text, kind, status, wallet_id::text, player_id, amount_minor, currency,
	       COALESCE(provider_id, ''), COALESCE(external_transaction_id, ''), COALESCE(idempotency_key, ''),
	       COALESCE(payload_hash, ''), COALESCE(round_id, ''), COALESCE(game_id, ''),
	       COALESCE(reference_external_transaction_id, ''), COALESCE(reference_transaction_id::text, ''),
	       COALESCE(failure_code, ''), result_balance_minor, result_balance_currency, created_at, updated_at,
	       COALESCE(correlation_id, ''), reference_attempts, next_reference_attempt_at
	  FROM wager_transactions`

func (r *WagerTransactionRepository) Get(ctx context.Context, id string) (*domain.WagerTransaction, error) {
	return scanWagerTransaction(querierFrom(ctx, r.pool).QueryRow(ctx, selectWagerTransaction+" WHERE id = $1", id))
}

func (r *WagerTransactionRepository) GetByExternalID(ctx context.Context, providerID, externalTransactionID string) (*domain.WagerTransaction, error) {
	return scanWagerTransaction(querierFrom(ctx, r.pool).QueryRow(ctx,
		selectWagerTransaction+" WHERE provider_id = $1 AND external_transaction_id = $2",
		providerID, externalTransactionID,
	))
}

func (r *WagerTransactionRepository) GetByIdempotencyKey(ctx context.Context, providerID, idempotencyKey string) (*domain.WagerTransaction, error) {
	return scanWagerTransaction(querierFrom(ctx, r.pool).QueryRow(ctx,
		selectWagerTransaction+" WHERE provider_id = $1 AND idempotency_key = $2",
		providerID, idempotencyKey,
	))
}

func (r *WagerTransactionRepository) HasProcessedReversal(ctx context.Context, transactionID string) (bool, error) {
	var exists bool
	err := querierFrom(ctx, r.pool).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM wager_transactions
			 WHERE reference_transaction_id = $1 AND status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK')
		)`, transactionID).Scan(&exists)
	return exists, translate(err)
}

func (r *WagerTransactionRepository) ClaimPendingReferences(ctx context.Context, now, leaseUntil time.Time, limit int) ([]application.PendingReference, error) {
	// SKIP LOCKED keeps instances from waiting on each other; moving the next attempt forward is the lease that outlives this statement.
	rows, err := querierFrom(ctx, r.pool).Query(ctx, `
		UPDATE wager_transactions
		   SET next_reference_attempt_at = $2
		 WHERE id IN (
			SELECT id FROM wager_transactions
			 WHERE status = 'PENDING_REFERENCE' AND next_reference_attempt_at <= $1
			 ORDER BY next_reference_attempt_at
			 LIMIT $3
			   FOR UPDATE SKIP LOCKED
		 )
		RETURNING id::text, wallet_id::text`, now, leaseUntil, limit)
	if err != nil {
		return nil, translate(err)
	}
	claimed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (application.PendingReference, error) {
		var pending application.PendingReference
		err := row.Scan(&pending.TransactionID, &pending.WalletID)
		return pending, err
	})
	return claimed, translate(err)
}

func (r *WagerTransactionRepository) WakePendingReferences(ctx context.Context, providerID, externalTransactionID string, now time.Time) error {
	_, err := querierFrom(ctx, r.pool).Exec(ctx, `
		UPDATE wager_transactions
		   SET next_reference_attempt_at = $3
		 WHERE provider_id = $1 AND reference_external_transaction_id = $2
		   AND status = 'PENDING_REFERENCE' AND next_reference_attempt_at > $3`,
		providerID, externalTransactionID, now)
	return translate(err)
}

func nextReferenceAttemptColumn(tx *domain.WagerTransaction) *time.Time {
	next, waiting := tx.NextReferenceAttemptAt()
	if !waiting {
		return nil
	}
	return &next
}

func resultBalanceColumns(tx *domain.WagerTransaction) (*int64, *string) {
	balance, observed := tx.ResultBalance()
	if !observed {
		return nil, nil
	}
	minor, currency := balance.MinorUnits(), balance.Currency()
	return &minor, &currency
}

func scanWagerTransaction(row pgx.Row) (*domain.WagerTransaction, error) {
	var (
		state          domain.WagerTransactionState
		kind, status   string
		failureCode    string
		amountMinor    int64
		currency       string
		resultMinor    *int64
		resultCurrency *string
		createdAt      time.Time
		updatedAt      time.Time
		nextAttemptAt  *time.Time
	)
	err := row.Scan(
		&state.ID, &kind, &status, &state.WalletID, &state.PlayerID, &amountMinor, &currency,
		&state.ProviderID, &state.ExternalTransactionID, &state.IdempotencyKey,
		&state.PayloadHash, &state.RoundID, &state.GameID,
		&state.ReferenceExternalTransactionID, &state.ReferenceTransactionID,
		&failureCode, &resultMinor, &resultCurrency, &createdAt, &updatedAt,
		&state.CorrelationID, &state.ReferenceAttempts, &nextAttemptAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrTransactionNotFound
	}
	if err != nil {
		return nil, translate(err)
	}

	state.Kind = domain.TransactionKind(kind)
	state.Status = domain.TransactionStatus(status)
	state.FailureCode = domain.FailureCode(failureCode)
	state.CreatedAt = createdAt.UTC()
	state.UpdatedAt = updatedAt.UTC()
	if nextAttemptAt != nil {
		state.NextReferenceAttemptAt = nextAttemptAt.UTC()
	}

	if state.Money, err = domain.MoneyFromMinorUnits(amountMinor, currency); err != nil {
		return nil, err
	}
	if resultMinor != nil && resultCurrency != nil {
		if state.ResultBalance, err = domain.MoneyFromMinorUnits(*resultMinor, *resultCurrency); err != nil {
			return nil, err
		}
	}
	return domain.RehydrateWagerTransaction(state)
}
