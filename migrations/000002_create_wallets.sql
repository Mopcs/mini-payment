CREATE TABLE wallets (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    currency VARCHAR(3) NOT NULL,
    balance BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT wallets_pkey PRIMARY KEY (id),
    CONSTRAINT wallets_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT wallets_balance_nonnegative_check CHECK (balance >= 0),
    CONSTRAINT wallets_currency_check CHECK (currency IN ('USD', 'EUR', 'JPY')),
    CONSTRAINT wallets_user_currency_unique UNIQUE (user_id, currency)
);

COMMENT ON COLUMN wallets.balance IS
    'Amount in the currency''s smallest unit: cents for USD/EUR and yen for JPY.';
