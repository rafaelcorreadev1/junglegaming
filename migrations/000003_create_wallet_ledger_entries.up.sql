CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    -- Insertion order: inserts of one wallet are serialized by its row lock, so this is a stable cursor.
    sequence             BIGINT      GENERATED ALWAYS AS IDENTITY,
    wallet_id            UUID        NOT NULL,
    transaction_id       UUID        NOT NULL,
    direction            TEXT        NOT NULL,
    amount_minor         BIGINT      NOT NULL,
    currency             CHAR(3)     NOT NULL,
    balance_before_minor BIGINT      NOT NULL,
    balance_after_minor  BIGINT      NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallet_ledger_entries_wallet_transaction_key UNIQUE (wallet_id, transaction_id),
    CONSTRAINT wallet_ledger_entries_wallet_sequence_key UNIQUE (wallet_id, sequence),
    CONSTRAINT wallet_ledger_entries_wallet_currency_fkey
        FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency),
    CONSTRAINT wallet_ledger_entries_transaction_wallet_fkey
        FOREIGN KEY (transaction_id, wallet_id) REFERENCES wager_transactions (id, wallet_id),

    CONSTRAINT wallet_ledger_entries_direction_known CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT wallet_ledger_entries_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT wallet_ledger_entries_balances_non_negative
        CHECK (balance_before_minor >= 0 AND balance_after_minor >= 0),
    CONSTRAINT wallet_ledger_entries_balance_equation CHECK (
        (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor)
        OR (direction = 'DEBIT' AND balance_after_minor = balance_before_minor - amount_minor)
    )
);

CREATE FUNCTION reject_wallet_ledger_entry_change() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER wallet_ledger_entries_append_only
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_wallet_ledger_entry_change();

-- TRUNCATE fires no row-level trigger, so it needs its own.
CREATE TRIGGER wallet_ledger_entries_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION reject_wallet_ledger_entry_change();
