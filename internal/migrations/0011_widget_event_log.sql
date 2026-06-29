-- +goose Up
-- widget_event_log is an application-owned table: the widgetaudit consumer
-- records each widget.created event it receives from the broker here. It is
-- distinct from the SDK-owned tables (outbox/queue/auditlog/translation), which
-- are provisioned at startup via EnsureSchema rather than by a migration.
CREATE TABLE IF NOT EXISTS widget_event_log (
    event_id    UUID PRIMARY KEY,
    type        TEXT NOT NULL,
    payload     BYTEA NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS widget_event_log;
