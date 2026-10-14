ALTER TABLE wager_transactions
    ADD COLUMN correlation_id            TEXT,
    ADD COLUMN reference_attempts        INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN next_reference_attempt_at TIMESTAMPTZ;

UPDATE wager_transactions
   SET reference_attempts = 1,
       next_reference_attempt_at = updated_at
 WHERE status = 'PENDING_REFERENCE';

ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_transactions_reference_attempts_non_negative CHECK (reference_attempts >= 0),
    -- A waiting transaction without a next attempt would never be picked up again.
    ADD CONSTRAINT wager_transactions_next_attempt_only_while_waiting CHECK (
        (status = 'PENDING_REFERENCE') = (next_reference_attempt_at IS NOT NULL)
    );

CREATE INDEX wager_transactions_due_references_idx
    ON wager_transactions (next_reference_attempt_at)
    WHERE status = 'PENDING_REFERENCE';

CREATE INDEX wager_transactions_waiting_for_idx
    ON wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PENDING_REFERENCE';
