package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"wagering-service/internal/application"
)

const (
	sqlStateUniqueViolation = "23505"
	sqlStateDeadlock        = "40P01"
	sqlStateSerialization   = "40001"
)

var uniqueConstraintErrors = map[string]error{
	"wallets_player_currency_key":               application.ErrWalletAlreadyExists,
	"wager_transactions_external_key":           application.ErrDuplicateExternalTransaction,
	"wager_transactions_idempotency_key":        application.ErrDuplicateIdempotencyKey,
	"wager_transactions_one_processed_reversal": application.ErrAlreadyReversed,
}

// transientClasses are SQLSTATE classes where retrying the same statement later can succeed.
var transientClasses = []string{
	"08", // connection exception
	"53", // insufficient resources
	"57", // operator intervention: shutdown, query canceled by admin
}

// translate keeps the original error in the chain, so logs still show the SQLSTATE.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return translatePgError(pgErr)
	}

	var connectErr *pgconn.ConnectError
	var netErr net.Error
	if errors.As(err, &connectErr) || errors.As(err, &netErr) {
		return fmt.Errorf("%w: %w", application.ErrUnavailable, err)
	}
	return err
}

func translatePgError(pgErr *pgconn.PgError) error {
	switch {
	case pgErr.Code == sqlStateUniqueViolation:
		if mapped, ok := uniqueConstraintErrors[pgErr.ConstraintName]; ok {
			return fmt.Errorf("%w: %w", mapped, pgErr)
		}
		return fmt.Errorf("%w: %w", application.ErrInvariantViolation, pgErr)
	case pgErr.Code == sqlStateDeadlock || pgErr.Code == sqlStateSerialization:
		return fmt.Errorf("%w: %w: %w", application.ErrUnavailable, application.ErrTransactionConflict, pgErr)
	case strings.HasPrefix(pgErr.Code, "23"):
		return fmt.Errorf("%w: %w", application.ErrInvariantViolation, pgErr)
	}
	for _, class := range transientClasses {
		if strings.HasPrefix(pgErr.Code, class) {
			return fmt.Errorf("%w: %w", application.ErrUnavailable, pgErr)
		}
	}
	return pgErr
}
