package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type txContextKey struct{}

// querier is the subset shared by the pool and a transaction, so a repository does not care which one it got.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var errLockOutsideTx = errors.New("row lock requested outside a transaction")

type TxManager struct {
	pool *pgxpool.Pool
}

func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txContextKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return translate(err)
	}
	// After a commit this is a no-op; the detached context lets it run even when ctx was the reason for failing.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(context.WithValue(ctx, txContextKey{}, tx)); err != nil {
		return err
	}
	return translate(tx.Commit(ctx))
}

func querierFrom(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := ctx.Value(txContextKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

func inTx(ctx context.Context) bool {
	_, ok := ctx.Value(txContextKey{}).(pgx.Tx)
	return ok
}
