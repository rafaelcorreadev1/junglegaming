CREATE TABLE inbox_messages (
    consumer_name TEXT        NOT NULL,
    message_id    TEXT        NOT NULL,
    payload_hash  TEXT        NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL,
    completed_at  TIMESTAMPTZ,

    CONSTRAINT inbox_messages_pkey PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_messages_identity_not_blank CHECK (consumer_name <> '' AND message_id <> ''),
    CONSTRAINT inbox_messages_completed_not_before_received
        CHECK (completed_at IS NULL OR completed_at >= received_at)
);

CREATE TABLE outbox_events (
    id              UUID        PRIMARY KEY,
    -- Publication order within an aggregate.
    sequence        BIGINT      GENERATED ALWAYS AS IDENTITY,
    aggregate_id    UUID        NOT NULL,
    event_type      TEXT        NOT NULL,
    payload         JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    -- A publisher holds the event until this instant; after it, another instance may take over.
    locked_until    TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    last_error      TEXT,

    CONSTRAINT outbox_events_event_type_not_blank CHECK (event_type <> ''),
    CONSTRAINT outbox_events_attempts_non_negative CHECK (attempts >= 0)
);

CREATE INDEX outbox_events_pending_idx
    ON outbox_events (next_attempt_at, sequence)
    WHERE published_at IS NULL;
