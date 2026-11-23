ALTER TABLE wallets
    ADD CONSTRAINT wallets_id_currency_unique UNIQUE (id, currency);

CREATE TABLE ledger_accounts (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    account_type VARCHAR(20) NOT NULL,
    currency VARCHAR(3) NOT NULL,
    wallet_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT ledger_accounts_pkey PRIMARY KEY (id),
    CONSTRAINT ledger_accounts_type_check CHECK (account_type IN ('wallet', 'system_funding')),
    CONSTRAINT ledger_accounts_currency_check CHECK (currency IN ('USD', 'EUR', 'JPY')),
    CONSTRAINT ledger_accounts_wallet_type_check CHECK (
        (account_type = 'wallet' AND wallet_id IS NOT NULL)
        OR (account_type = 'system_funding' AND wallet_id IS NULL)
    ),
    CONSTRAINT ledger_accounts_wallet_currency_fkey FOREIGN KEY (wallet_id, currency)
        REFERENCES wallets (id, currency) ON DELETE CASCADE
);

CREATE UNIQUE INDEX ledger_accounts_wallet_id_unique_idx
    ON ledger_accounts (wallet_id)
    WHERE account_type = 'wallet';
CREATE UNIQUE INDEX ledger_accounts_system_currency_unique_idx
    ON ledger_accounts (currency)
    WHERE account_type = 'system_funding';

INSERT INTO ledger_accounts (account_type, currency)
VALUES ('system_funding', 'USD'), ('system_funding', 'EUR'), ('system_funding', 'JPY');

INSERT INTO ledger_accounts (account_type, currency, wallet_id)
SELECT 'wallet', currency, id FROM wallets;

CREATE FUNCTION create_wallet_ledger_account() RETURNS trigger AS $$
BEGIN
    INSERT INTO ledger_accounts (account_type, currency, wallet_id)
    VALUES ('wallet', NEW.currency, NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallets_create_ledger_account
    AFTER INSERT ON wallets
    FOR EACH ROW EXECUTE FUNCTION create_wallet_ledger_account();

CREATE TABLE ledger_transactions (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    transaction_type VARCHAR(20) NOT NULL,
    user_id UUID NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_hash BYTEA NOT NULL,
    wallet_id UUID NOT NULL,
    currency VARCHAR(3) NOT NULL,
    amount BIGINT NOT NULL,
    balance_after BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT ledger_transactions_pkey PRIMARY KEY (id),
    CONSTRAINT ledger_transactions_type_check CHECK (transaction_type = 'deposit'),
    CONSTRAINT ledger_transactions_key_nonempty_check CHECK (length(idempotency_key) > 0),
    CONSTRAINT ledger_transactions_hash_length_check CHECK (octet_length(request_hash) = 32),
    CONSTRAINT ledger_transactions_amount_positive_check CHECK (amount > 0),
    CONSTRAINT ledger_transactions_currency_check CHECK (currency IN ('USD', 'EUR', 'JPY')),
    CONSTRAINT ledger_transactions_user_fkey FOREIGN KEY (user_id)
        REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT ledger_transactions_wallet_fkey FOREIGN KEY (wallet_id)
        REFERENCES wallets (id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT ledger_transactions_user_key_unique UNIQUE (user_id, idempotency_key)
);

CREATE INDEX ledger_transactions_wallet_created_idx
    ON ledger_transactions (wallet_id, created_at DESC, id DESC);

CREATE TABLE ledger_entries (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    transaction_id UUID NOT NULL,
    account_id UUID NOT NULL,
    amount BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT ledger_entries_pkey PRIMARY KEY (id),
    CONSTRAINT ledger_entries_amount_nonzero_check CHECK (amount <> 0),
    CONSTRAINT ledger_entries_transaction_fkey FOREIGN KEY (transaction_id)
        REFERENCES ledger_transactions (id) ON DELETE RESTRICT,
    CONSTRAINT ledger_entries_account_fkey FOREIGN KEY (account_id)
        REFERENCES ledger_accounts (id) ON DELETE RESTRICT
);

CREATE INDEX ledger_entries_transaction_idx ON ledger_entries (transaction_id);
CREATE INDEX ledger_entries_account_created_idx ON ledger_entries (account_id, created_at, id);

CREATE FUNCTION reject_ledger_entry_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'ledger entries are append-only' USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER ledger_entries_append_only
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_entry_mutation();

CREATE FUNCTION check_ledger_transaction_balanced() RETURNS trigger AS $$
DECLARE
    entry_count BIGINT;
    entry_total NUMERIC;
    currency_count BIGINT;
    target_transaction_id UUID;
    transaction_currency VARCHAR(3);
    transaction_amount BIGINT;
    balance_after BIGINT;
    transaction_wallet_id UUID;
    transaction_user_id UUID;
    wallet_total NUMERIC;
    funding_total NUMERIC;
BEGIN
    IF TG_TABLE_NAME = 'ledger_transactions' THEN
        target_transaction_id := NEW.id;
    ELSE
        target_transaction_id := NEW.transaction_id;
    END IF;

    SELECT currency, amount, ledger_transactions.balance_after, wallet_id, user_id
      INTO transaction_currency, transaction_amount, balance_after, transaction_wallet_id, transaction_user_id
      FROM ledger_transactions
     WHERE id = target_transaction_id;

    SELECT count(*), COALESCE(sum(le.amount), 0), count(DISTINCT la.currency),
           COALESCE(sum(le.amount) FILTER (WHERE la.account_type = 'wallet'), 0),
           COALESCE(sum(le.amount) FILTER (WHERE la.account_type = 'system_funding'), 0)
      INTO entry_count, entry_total, currency_count, wallet_total, funding_total
      FROM ledger_entries le
      JOIN ledger_accounts la ON la.id = le.account_id
     WHERE le.transaction_id = target_transaction_id;

    IF entry_count <> 2 OR entry_total <> 0 OR currency_count <> 1
       OR wallet_total <> transaction_amount OR funding_total <> -transaction_amount
       OR balance_after IS NULL
       OR EXISTS (
           SELECT 1 FROM ledger_entries le
           JOIN ledger_accounts la ON la.id = le.account_id
            WHERE le.transaction_id = target_transaction_id
             AND (la.currency <> transaction_currency
                  OR (la.account_type = 'wallet' AND la.wallet_id <> transaction_wallet_id))
       ) OR EXISTS (
           SELECT 1 FROM wallets w
           WHERE w.id = transaction_wallet_id AND w.user_id <> transaction_user_id
       ) THEN
        RAISE EXCEPTION 'ledger transaction % is not balanced', target_transaction_id
            USING ERRCODE = '23514', CONSTRAINT = 'ledger_transactions_balanced_check';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER ledger_transactions_balanced
    AFTER INSERT ON ledger_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_ledger_transaction_balanced();

CREATE CONSTRAINT TRIGGER ledger_entries_transaction_balanced
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_ledger_transaction_balanced();
