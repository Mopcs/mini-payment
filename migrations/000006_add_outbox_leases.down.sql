DROP INDEX outbox_events_pending_poll_idx;

CREATE INDEX outbox_events_unpublished_poll_idx
    ON outbox_events (created_at, id)
    WHERE published_at IS NULL;

ALTER TABLE outbox_events
    DROP CONSTRAINT outbox_events_lease_fields_check,
    DROP COLUMN next_attempt_at,
    DROP COLUMN locked_until,
    DROP COLUMN locked_by;
