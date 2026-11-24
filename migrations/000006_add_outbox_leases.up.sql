ALTER TABLE outbox_events
    ADD COLUMN locked_by TEXT,
    ADD COLUMN locked_until TIMESTAMPTZ,
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD CONSTRAINT outbox_events_lease_fields_check
        CHECK ((locked_by IS NULL) = (locked_until IS NULL));

DROP INDEX outbox_events_unpublished_poll_idx;

-- Matches the claim query: unpublished events ordered by when they are due next.
CREATE INDEX outbox_events_pending_poll_idx
    ON outbox_events (next_attempt_at, created_at, id)
    WHERE published_at IS NULL;
