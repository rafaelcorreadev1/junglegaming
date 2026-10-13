package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"wagering-service/internal/application"
)

func TestTranslateSeparatesTransientFromPermanentErrors(t *testing.T) {
	plain := errors.New("syntax error in the query")

	tests := []struct {
		name string
		err  error
		want error
	}{
		{"deadlock", &pgconn.PgError{Code: "40P01"}, application.ErrUnavailable},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, application.ErrUnavailable},
		{"connection failure", &pgconn.PgError{Code: "08006"}, application.ErrUnavailable},
		{"too many connections", &pgconn.PgError{Code: "53300"}, application.ErrUnavailable},
		{"server shutting down", &pgconn.PgError{Code: "57P01"}, application.ErrUnavailable},
		{"network error", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, application.ErrUnavailable},
		{"check violation", &pgconn.PgError{Code: "23514", ConstraintName: "wallets_balance_non_negative"}, application.ErrInvariantViolation},
		{"unmapped unique violation", &pgconn.PgError{Code: "23505", ConstraintName: "wallets_pkey"}, application.ErrInvariantViolation},
		{"mapped unique violation", &pgconn.PgError{Code: "23505", ConstraintName: "wager_transactions_one_processed_reversal"}, application.ErrAlreadyReversed},
		{"canceled context", fmt.Errorf("query: %w", context.Canceled), context.Canceled},
		{"expired deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), context.DeadlineExceeded},
		{"anything else is left as is", plain, plain},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translate(tt.err)
			if !errors.Is(got, tt.want) {
				t.Fatalf("translate(%v) = %v, want it to match %v", tt.err, got, tt.want)
			}
			if !errors.Is(got, tt.err) {
				t.Errorf("translate dropped the original error from the chain")
			}
		})
	}

	permanent := []error{
		translate(&pgconn.PgError{Code: "23514"}),
		translate(&pgconn.PgError{Code: "42601"}),
		translate(plain),
		translate(fmt.Errorf("query: %w", context.Canceled)),
	}
	for _, err := range permanent {
		if errors.Is(err, application.ErrUnavailable) {
			t.Errorf("%v must not be classified as transient", err)
		}
	}

	if translate(nil) != nil {
		t.Error("translate(nil) must be nil")
	}
}
