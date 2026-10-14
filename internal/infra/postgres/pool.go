package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
)

var Module = fx.Module("postgres",
	fx.Provide(
		NewPool,
		fx.Annotate(NewTxManager, fx.As(new(application.TxManager))),
		fx.Annotate(NewWalletRepository, fx.As(new(application.WalletRepository))),
		fx.Annotate(NewWagerTransactionRepository, fx.As(new(application.WagerTransactionRepository))),
		fx.Annotate(NewLedgerRepository, fx.As(new(application.LedgerRepository))),
		fx.Annotate(NewOutboxRepository, fx.As(new(application.OutboxRepository))),
		fx.Annotate(NewInboxRepository, fx.As(new(application.InboxRepository))),
	),
)

func NewPool(lc fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	// NewWithConfig does not open a connection; the ping in OnStart is what validates the database.
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("ping postgres: %w", err)
			}
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})

	return pool, nil
}
