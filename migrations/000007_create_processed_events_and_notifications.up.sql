CREATE TABLE processed_events (
    event_id UUID NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT processed_events_pkey PRIMARY KEY (event_id),
    CONSTRAINT processed_events_outbox_event_fkey FOREIGN KEY (event_id)
        REFERENCES outbox_events (id) ON DELETE RESTRICT,
    CONSTRAINT processed_events_type_nonempty_check CHECK (length(event_type) > 0)
);

CREATE TABLE notifications (
    id UUID NOT NULL,
    user_id UUID NOT NULL,
    transfer_id UUID NOT NULL,
    event_id UUID NOT NULL,
    notification_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT notifications_pkey PRIMARY KEY (id),
    CONSTRAINT notifications_user_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT notifications_transfer_fkey FOREIGN KEY (transfer_id) REFERENCES ledger_transactions (id) ON DELETE RESTRICT,
    CONSTRAINT notifications_event_fkey FOREIGN KEY (event_id) REFERENCES processed_events (event_id) ON DELETE RESTRICT,
    CONSTRAINT notifications_type_nonempty_check CHECK (length(notification_type) > 0),
    CONSTRAINT notifications_payload_object_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT notifications_event_user_unique UNIQUE (event_id, user_id)
);

CREATE INDEX notifications_user_created_idx ON notifications (user_id, created_at DESC, id DESC);
CREATE INDEX notifications_transfer_id_idx ON notifications (transfer_id);
