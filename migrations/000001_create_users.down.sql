DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM users) THEN
        RAISE EXCEPTION 'cannot roll back users migration while users exist';
    END IF;
END;
$$;

DROP TABLE users;
