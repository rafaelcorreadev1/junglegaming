DROP INDEX IF EXISTS wager_transactions_waiting_for_idx;
DROP INDEX IF EXISTS wager_transactions_due_references_idx;

ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_transactions_next_attempt_only_while_waiting,
    DROP CONSTRAINT IF EXISTS wager_transactions_reference_attempts_non_negative,
    DROP COLUMN IF EXISTS next_reference_attempt_at,
    DROP COLUMN IF EXISTS reference_attempts,
    DROP COLUMN IF EXISTS correlation_id;
