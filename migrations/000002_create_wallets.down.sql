DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM wallets) THEN
        RAISE EXCEPTION 'cannot roll back wallets migration while wallets exist';
    END IF;
END;
$$;

DROP TABLE wallets;
