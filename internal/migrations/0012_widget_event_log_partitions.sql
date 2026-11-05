-- +goose Up
-- widget_event_log is partitioned by the month of occurred_at, the time of the
-- event (CloudEvents time): the partition package of skit creates the
-- partitions ahead and drops the ones past the retention (BROKER_LOG_RETENTION,
-- see the workers in internal/app/server). The migration creates the table
-- alone, without partitions. The key holds the partition key; a redelivery
-- carries the same time, so ON CONFLICT (event_id, occurred_at) still records
-- an event once. The log of the showcase is not carried over.
DROP TABLE IF EXISTS widget_event_log;
CREATE TABLE widget_event_log (
    event_id    UUID NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    type        TEXT NOT NULL,
    payload     BYTEA NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, occurred_at)
) PARTITION BY RANGE (occurred_at);

-- +goose Down
DROP TABLE IF EXISTS widget_event_log;
CREATE TABLE widget_event_log (
    event_id    UUID PRIMARY KEY,
    type        TEXT NOT NULL,
    payload     BYTEA NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
