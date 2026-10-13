package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/application"
)

type OutboxRepository struct {
	pool *pgxpool.Pool
}

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

func (r *OutboxRepository) Append(ctx context.Context, message application.OutboxMessage) error {
	// The payload goes as text so PostgreSQL parses it; a []byte parameter would be sent as bytea.
	_, err := querierFrom(ctx, r.pool).Exec(ctx, `
		INSERT INTO outbox_events (id, aggregate_id, event_type, payload, occurred_at, next_attempt_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $5)`,
		message.ID, message.AggregateID, message.EventType, string(message.Payload), message.OccurredAt,
	)
	return translate(err)
}
