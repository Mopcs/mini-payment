ALTER TABLE ledger_transactions
    ADD COLUMN destination_wallet_id UUID,
    ADD COLUMN destination_balance_after BIGINT;

ALTER TABLE ledger_transactions
    ADD CONSTRAINT ledger_transactions_destination_wallet_fkey
        FOREIGN KEY (destination_wallet_id) REFERENCES wallets (id)
        ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    DROP CONSTRAINT ledger_transactions_type_check,
    ADD CONSTRAINT ledger_transactions_type_check
        CHECK (transaction_type IN ('deposit', 'transfer')),
    ADD CONSTRAINT ledger_transactions_transfer_fields_check CHECK (
        (transaction_type = 'deposit'
            AND destination_wallet_id IS NULL
            AND destination_balance_after IS NULL)
        OR (transaction_type = 'transfer'
            AND destination_wallet_id IS NOT NULL
            AND destination_wallet_id <> wallet_id
            AND (destination_balance_after IS NULL OR balance_after IS NOT NULL))
    );

CREATE INDEX ledger_transactions_destination_wallet_idx
    ON ledger_transactions (destination_wallet_id, created_at DESC, id DESC)
    WHERE destination_wallet_id IS NOT NULL;

CREATE OR REPLACE FUNCTION check_ledger_transaction_balanced() RETURNS trigger AS $$
DECLARE
    entry_count BIGINT;
    entry_total NUMERIC;
    currency_count BIGINT;
    source_count BIGINT;
    destination_count BIGINT;
    funding_count BIGINT;
    source_total NUMERIC;
    destination_total NUMERIC;
    funding_total NUMERIC;
    target_transaction_id UUID;
    transaction_type VARCHAR(20);
    transaction_currency VARCHAR(3);
    transaction_amount BIGINT;
    source_balance_after BIGINT;
    destination_balance_after BIGINT;
    source_wallet_id UUID;
    destination_wallet_id UUID;
    transaction_user_id UUID;
BEGIN
    IF TG_TABLE_NAME = 'ledger_transactions' THEN
        target_transaction_id := NEW.id;
    ELSE
        target_transaction_id := NEW.transaction_id;
    END IF;

    SELECT ledger_transactions.transaction_type, ledger_transactions.currency,
           ledger_transactions.amount, ledger_transactions.balance_after,
           ledger_transactions.destination_balance_after, ledger_transactions.wallet_id,
           ledger_transactions.destination_wallet_id, ledger_transactions.user_id
      INTO transaction_type, transaction_currency, transaction_amount, source_balance_after,
           destination_balance_after, source_wallet_id, destination_wallet_id, transaction_user_id
      FROM ledger_transactions
     WHERE ledger_transactions.id = target_transaction_id;

    SELECT count(*), COALESCE(sum(le.amount), 0), count(DISTINCT la.currency),
           count(*) FILTER (WHERE la.account_type = 'wallet' AND la.wallet_id = source_wallet_id),
           count(*) FILTER (WHERE la.account_type = 'wallet' AND la.wallet_id = destination_wallet_id),
           count(*) FILTER (WHERE la.account_type = 'system_funding'),
           COALESCE(sum(le.amount) FILTER (WHERE la.account_type = 'wallet' AND la.wallet_id = source_wallet_id), 0),
           COALESCE(sum(le.amount) FILTER (WHERE la.account_type = 'wallet' AND la.wallet_id = destination_wallet_id), 0),
           COALESCE(sum(le.amount) FILTER (WHERE la.account_type = 'system_funding'), 0)
      INTO entry_count, entry_total, currency_count, source_count, destination_count,
           funding_count, source_total, destination_total, funding_total
      FROM ledger_entries le
      JOIN ledger_accounts la ON la.id = le.account_id
     WHERE le.transaction_id = target_transaction_id;

    IF entry_count <> 2 OR entry_total <> 0 OR currency_count <> 1
       OR source_balance_after IS NULL
       OR EXISTS (
           SELECT 1 FROM ledger_entries le
           JOIN ledger_accounts la ON la.id = le.account_id
           WHERE le.transaction_id = target_transaction_id
             AND la.currency <> transaction_currency
       )
       OR EXISTS (
           SELECT 1 FROM wallets w
           WHERE w.id = source_wallet_id AND w.user_id <> transaction_user_id
       ) THEN
        RAISE EXCEPTION 'ledger transaction % is invalid', target_transaction_id
            USING ERRCODE = '23514', CONSTRAINT = 'ledger_transactions_balanced_check';
    END IF;

    IF transaction_type = 'deposit' THEN
        IF destination_wallet_id IS NOT NULL OR destination_balance_after IS NOT NULL
           OR source_count <> 1 OR source_total <> transaction_amount
           OR destination_count <> 0 OR funding_count <> 1
           OR funding_total <> -transaction_amount THEN
            RAISE EXCEPTION 'deposit ledger transaction % is invalid', target_transaction_id
                USING ERRCODE = '23514', CONSTRAINT = 'ledger_transactions_balanced_check';
        END IF;
    ELSIF transaction_type = 'transfer' THEN
        IF destination_wallet_id IS NULL OR destination_wallet_id = source_wallet_id
           OR destination_balance_after IS NULL
           OR source_count <> 1 OR source_total <> -transaction_amount
           OR destination_count <> 1 OR destination_total <> transaction_amount
           OR funding_count <> 0 OR funding_total <> 0 THEN
            RAISE EXCEPTION 'transfer ledger transaction % is invalid', target_transaction_id
                USING ERRCODE = '23514', CONSTRAINT = 'ledger_transactions_balanced_check';
        END IF;
    ELSE
        RAISE EXCEPTION 'unsupported ledger transaction type %', transaction_type
            USING ERRCODE = '23514', CONSTRAINT = 'ledger_transactions_balanced_check';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
