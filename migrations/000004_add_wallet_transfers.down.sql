DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ledger_transactions WHERE transaction_type = 'transfer') THEN
        RAISE EXCEPTION 'cannot roll back transfer support while transfer transactions exist';
    END IF;
END;
$$;

DROP INDEX ledger_transactions_destination_wallet_idx;
ALTER TABLE ledger_transactions
    DROP CONSTRAINT ledger_transactions_transfer_fields_check,
    DROP CONSTRAINT ledger_transactions_type_check,
    ADD CONSTRAINT ledger_transactions_type_check CHECK (transaction_type = 'deposit');

CREATE OR REPLACE FUNCTION check_ledger_transaction_balanced() RETURNS trigger AS $$
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

ALTER TABLE ledger_transactions
    DROP CONSTRAINT ledger_transactions_destination_wallet_fkey,
    DROP COLUMN destination_balance_after,
    DROP COLUMN destination_wallet_id;
