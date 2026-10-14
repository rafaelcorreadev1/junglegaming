package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
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
			failure_code, result_balance_minor, result_balance_currency, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''),
			NULLIF($14, ''), NULLIF($15, '')::uuid,
			NULLIF($16, ''), $17, $18, $19, $20
		)`,
		tx.ID(), string(tx.Kind()), string(tx.Status()), tx.WalletID(), tx.PlayerID(),
		tx.Money().MinorUnits(), tx.Money().Currency(),
		tx.ProviderID(), tx.ExternalTransactionID(), tx.IdempotencyKey(), tx.PayloadHash(), tx.RoundID(), tx.GameID(),
		tx.ReferenceExternalTransactionID(), tx.ReferenceTransactionID(),
		string(tx.FailureCode()), resultMinor, resultCurrency, tx.CreatedAt(), tx.UpdatedAt(),
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
		       updated_at = $7
		 WHERE id = $1`,
		tx.ID(), string(tx.Status()), tx.ReferenceTransactionID(),
		string(tx.FailureCode()), resultMinor, resultCurrency, tx.UpdatedAt(),
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
	       COALESCE(failure_code, ''), result_balance_minor, result_balance_currency, created_at, updated_at
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
	)
	err := row.Scan(
		&state.ID, &kind, &status, &state.WalletID, &state.PlayerID, &amountMinor, &currency,
		&state.ProviderID, &state.ExternalTransactionID, &state.IdempotencyKey,
		&state.PayloadHash, &state.RoundID, &state.GameID,
		&state.ReferenceExternalTransactionID, &state.ReferenceTransactionID,
		&failureCode, &resultMinor, &resultCurrency, &createdAt, &updatedAt,
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
