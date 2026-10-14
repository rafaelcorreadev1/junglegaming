package postgres

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
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

func (r *OutboxRepository) OldestPending(ctx context.Context) (time.Time, bool, error) {
	var oldest *time.Time
	err := querierFrom(ctx, r.pool).QueryRow(ctx,
		"SELECT min(occurred_at) FROM outbox_events WHERE published_at IS NULL").Scan(&oldest)
	if err != nil {
		return time.Time{}, false, translate(err)
	}
	if oldest == nil {
		return time.Time{}, false, nil
	}
	return *oldest, true, nil
}

func (r *OutboxRepository) ClaimDue(ctx context.Context, now, leaseUntil time.Time, aggregates, perAggregate int) ([]application.OutboxRecord, error) {
	// Only the oldest unpublished event of an aggregate is locked; a competing publisher skips it with SKIP LOCKED
	// and cannot pick a later event of the same aggregate either, because NOT EXISTS still sees the head unpublished.
	rows, err := querierFrom(ctx, r.pool).Query(ctx, `
		WITH heads AS (
			SELECT o.aggregate_id
			  FROM outbox_events o
			 WHERE o.published_at IS NULL
			   AND o.next_attempt_at <= $1
			   AND (o.locked_until IS NULL OR o.locked_until <= $1)
			   AND NOT EXISTS (
				SELECT 1 FROM outbox_events p
				 WHERE p.aggregate_id = o.aggregate_id AND p.published_at IS NULL AND p.sequence < o.sequence
			   )
			 ORDER BY o.sequence
			 LIMIT $3
			   FOR UPDATE SKIP LOCKED
		), claimed AS (
			SELECT id FROM (
				SELECT e.id, row_number() OVER (PARTITION BY e.aggregate_id ORDER BY e.sequence) AS position
				  FROM outbox_events e
				  JOIN heads h ON h.aggregate_id = e.aggregate_id
				 WHERE e.published_at IS NULL
			) ranked
			 WHERE position <= $4
		)
		UPDATE outbox_events
		   SET locked_until = $2
		 WHERE id IN (SELECT id FROM claimed)
		RETURNING id::text, aggregate_id::text, event_type, payload::text, occurred_at, sequence, attempts`,
		now, leaseUntil, aggregates, perAggregate)
	if err != nil {
		return nil, translate(err)
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (application.OutboxRecord, error) {
		var (
			record  application.OutboxRecord
			payload string
		)
		err := row.Scan(&record.Message.ID, &record.Message.AggregateID, &record.Message.EventType, &payload,
			&record.Message.OccurredAt, &record.Sequence, &record.Attempts)
		record.Message.Payload = []byte(payload)
		record.Message.OccurredAt = record.Message.OccurredAt.UTC()
		return record, err
	})
	if err != nil {
		return nil, translate(err)
	}
	// RETURNING has no defined order.
	slices.SortFunc(records, func(a, b application.OutboxRecord) int { return cmp.Compare(a.Sequence, b.Sequence) })
	return records, nil
}

func (r *OutboxRepository) MarkPublished(ctx context.Context, id string, now time.Time) error {
	_, err := querierFrom(ctx, r.pool).Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = $2, locked_until = NULL, last_error = NULL
		 WHERE id = $1 AND published_at IS NULL`, id, now)
	return translate(err)
}

func (r *OutboxRepository) Reschedule(ctx context.Context, id string, next time.Time, lastError string) error {
	_, err := querierFrom(ctx, r.pool).Exec(ctx, `
		UPDATE outbox_events
		   SET attempts = attempts + 1, next_attempt_at = $2, locked_until = NULL, last_error = $3
		 WHERE id = $1 AND published_at IS NULL`, id, next, lastError)
	return translate(err)
}

func (r *OutboxRepository) Release(ctx context.Context, ids []string) error {
	_, err := querierFrom(ctx, r.pool).Exec(ctx, `
		UPDATE outbox_events SET locked_until = NULL WHERE id = ANY($1::uuid[]) AND published_at IS NULL`, ids)
	return translate(err)
}
