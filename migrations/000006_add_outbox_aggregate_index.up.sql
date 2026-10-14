-- Publishers look for the oldest unpublished event of each aggregate to keep per-wallet order.
CREATE INDEX outbox_events_unpublished_by_aggregate_idx
    ON outbox_events (aggregate_id, sequence)
    WHERE published_at IS NULL;
