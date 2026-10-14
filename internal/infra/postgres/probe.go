package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Probe struct {
	pool *pgxpool.Pool
}

func NewProbe(pool *pgxpool.Pool) *Probe {
	return &Probe{pool: pool}
}

func (p *Probe) Name() string { return "postgres" }

func (p *Probe) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}
