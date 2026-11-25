ALTER TABLE outbox_events
    DROP COLUMN trace_state,
    DROP COLUMN trace_parent;
