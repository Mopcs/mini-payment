CREATE TABLE users (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    email VARCHAR(255) NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT users_pkey PRIMARY KEY (id),
    CONSTRAINT users_email_normalized_check CHECK (email = lower(btrim(email)))
);

-- Uniqueness is case-insensitive. The CHECK above keeps the stored value lowercase.
CREATE UNIQUE INDEX users_email_lower_unique_idx ON users (lower(email));
