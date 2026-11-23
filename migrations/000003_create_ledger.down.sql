DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ledger_entries) THEN
        RAISE EXCEPTION 'cannot roll back ledger schema while ledger entries exist';
    END IF;
END;
$$;

DROP TRIGGER ledger_entries_transaction_balanced ON ledger_entries;
DROP TRIGGER ledger_transactions_balanced ON ledger_transactions;
DROP FUNCTION check_ledger_transaction_balanced();
DROP TRIGGER ledger_entries_append_only ON ledger_entries;
DROP FUNCTION reject_ledger_entry_mutation();
DROP TABLE ledger_entries;
DROP TABLE ledger_transactions;
DROP TRIGGER wallets_create_ledger_account ON wallets;
DROP FUNCTION create_wallet_ledger_account();
DROP TABLE ledger_accounts;
ALTER TABLE wallets DROP CONSTRAINT wallets_id_currency_unique;
