CREATE TABLE wager_transactions (
    id           UUID        PRIMARY KEY,
    kind         TEXT        NOT NULL,
    status       TEXT        NOT NULL,
    wallet_id    UUID        NOT NULL REFERENCES wallets (id),
    player_id    TEXT        NOT NULL,
    amount_minor BIGINT      NOT NULL,
    currency     CHAR(3)     NOT NULL,

    provider_id             TEXT,
    external_transaction_id TEXT,
    idempotency_key         TEXT,
    payload_hash            TEXT,
    round_id                TEXT,
    game_id                 TEXT,

    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID REFERENCES wager_transactions (id),

    failure_code            TEXT,
    -- The balance reported to the provider; on a currency mismatch it is in the wallet currency, not the transaction one.
    result_balance_minor    BIGINT,
    result_balance_currency CHAR(3),

    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,

    CONSTRAINT wager_transactions_external_key UNIQUE (provider_id, external_transaction_id),
    CONSTRAINT wager_transactions_idempotency_key UNIQUE (provider_id, idempotency_key),
    -- Target of the composite foreign key that pins a ledger entry to the wallet of its transaction.
    CONSTRAINT wager_transactions_id_wallet_key UNIQUE (id, wallet_id),

    CONSTRAINT wager_transactions_kind_known
        CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wager_transactions_status_known
        CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wager_transactions_currency_format CHECK (currency ~ '^[A-Z]{3}$'),

    CONSTRAINT wager_transactions_origin CHECK (
        (
            kind = 'OPENING'
            AND status = 'PROCESSED'
            AND num_nonnulls(provider_id, external_transaction_id, idempotency_key, payload_hash,
                             round_id, game_id, reference_external_transaction_id,
                             reference_transaction_id) = 0
        )
        OR (
            kind <> 'OPENING'
            AND num_nulls(provider_id, external_transaction_id, idempotency_key, payload_hash,
                          round_id, game_id) = 0
        )
    ),
    CONSTRAINT wager_transactions_amount_by_kind CHECK (
        (kind = 'LOSS' AND amount_minor = 0) OR (kind <> 'LOSS' AND amount_minor > 0)
    ),
    CONSTRAINT wager_transactions_reference_by_kind CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR kind = 'WIN'
        OR (kind IN ('OPENING', 'BET', 'LOSS') AND reference_external_transaction_id IS NULL)
    ),
    CONSTRAINT wager_transactions_resolved_reference CHECK (
        reference_transaction_id IS NULL
        OR (reference_external_transaction_id IS NOT NULL AND reference_transaction_id <> id)
    ),
    CONSTRAINT wager_transactions_failure_code_by_status CHECK (
        (status IN ('REJECTED', 'FAILED')) = (failure_code IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_result_balance CHECK (
        (result_balance_minor IS NULL) = (result_balance_currency IS NULL)
        AND (result_balance_minor IS NULL OR result_balance_minor >= 0)
        AND (result_balance_currency IS NULL OR result_balance_currency ~ '^[A-Z]{3}$')
        AND (status <> 'PROCESSED' OR result_balance_minor IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_updated_not_before_created CHECK (updated_at >= created_at)
);

CREATE UNIQUE INDEX wager_transactions_one_opening_per_wallet
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wager_transactions_one_processed_reversal
    ON wager_transactions (reference_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

CREATE INDEX wager_transactions_wallet_idx ON wager_transactions (wallet_id, created_at);
