CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     TEXT        NOT NULL,
    currency      CHAR(3)     NOT NULL,
    balance_minor BIGINT      NOT NULL,
    version       BIGINT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_player_currency_key UNIQUE (player_id, currency),
    -- Target of the composite foreign key that pins a ledger entry to the wallet currency.
    CONSTRAINT wallets_id_currency_key UNIQUE (id, currency),
    CONSTRAINT wallets_player_id_not_blank CHECK (player_id <> ''),
    CONSTRAINT wallets_currency_format CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT wallets_balance_non_negative CHECK (balance_minor >= 0),
    CONSTRAINT wallets_version_positive CHECK (version >= 1),
    CONSTRAINT wallets_updated_not_before_created CHECK (updated_at >= created_at)
);
