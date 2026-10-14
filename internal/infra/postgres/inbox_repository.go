package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/application"
)

type InboxRepository struct {
	pool *pgxpool.Pool
}

func NewInboxRepository(pool *pgxpool.Pool) *InboxRepository {
	return &InboxRepository{pool: pool}
}

func (r *InboxRepository) Receive(ctx context.Context, consumerName, messageID, payloadHash string, now time.Time) (application.InboxRecord, bool, error) {
	q := querierFrom(ctx, r.pool)
	// DO NOTHING keeps the surrounding transaction usable; a concurrent first delivery makes this wait for its outcome.
	tag, err := q.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		consumerName, messageID, payloadHash, now,
	)
	if err != nil {
		return application.InboxRecord{}, false, translate(err)
	}
	if tag.RowsAffected() == 1 {
		return application.InboxRecord{PayloadHash: payloadHash}, true, nil
	}

	var stored application.InboxRecord
	err = q.QueryRow(ctx, `
		SELECT payload_hash, completed_at IS NOT NULL
		  FROM inbox_messages
		 WHERE consumer_name = $1 AND message_id = $2`,
		consumerName, messageID,
	).Scan(&stored.PayloadHash, &stored.Completed)
	if err != nil {
		return application.InboxRecord{}, false, translate(err)
	}
	return stored, false, nil
}

func (r *InboxRepository) Complete(ctx context.Context, consumerName, messageID string, now time.Time) error {
	tag, err := querierFrom(ctx, r.pool).Exec(ctx, `
		UPDATE inbox_messages
		   SET completed_at = $3
		 WHERE consumer_name = $1 AND message_id = $2 AND completed_at IS NULL`,
		consumerName, messageID, now,
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrInvariantViolation
	}
	return nil
}
