CREATE TABLE outbox_events (
    id UUID NOT NULL,
    aggregate_id UUID NOT NULL,
    aggregate_type VARCHAR(100) NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    CONSTRAINT outbox_events_pkey PRIMARY KEY (id),
    CONSTRAINT outbox_events_aggregate_type_nonempty_check CHECK (length(aggregate_type) > 0),
    CONSTRAINT outbox_events_event_type_nonempty_check CHECK (length(event_type) > 0),
    CONSTRAINT outbox_events_payload_object_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_events_attempts_nonnegative_check CHECK (attempts >= 0)
);

CREATE INDEX outbox_events_unpublished_poll_idx
    ON outbox_events (created_at, id)
    WHERE published_at IS NULL;

CREATE INDEX outbox_events_aggregate_id_idx
    ON outbox_events (aggregate_id);
