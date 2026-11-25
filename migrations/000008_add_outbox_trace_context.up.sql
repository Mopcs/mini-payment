-- Trace context rides alongside the event instead of inside the payload, so the
-- financial event contract stays unchanged. Existing rows get an empty string,
-- which the worker reads as "no parent trace".
ALTER TABLE outbox_events
    ADD COLUMN trace_parent TEXT NOT NULL DEFAULT '',
    ADD COLUMN trace_state TEXT NOT NULL DEFAULT '';
