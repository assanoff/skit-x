package tests

import (
	"context"
	"log/slog"
	"testing"
	"time"
	"uuid"

	"github.com/matryer/is"

	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/dbx"

	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widgetaudit"
)

// The event log records an event once — a redelivery adds no row — in the
// partition of the month of the event. An event without a time is dropped; one
// of a month without a partition goes back to the queue.
func TestWidgetEventLog(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	is := is.New(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	db := openTestDB(t, startPostgres(ctx, t))

	keeper, err := widgetaudit.Partitions(log, db, 90*24*time.Hour)
	is.NoErr(err)
	is.NoErr(keeper.Maintain(ctx)) // this month and the next
	rec := widgetaudit.New(log, db)

	m := broker.Message{
		ID:   uuid.New().String(),
		Type: widget.EventWidgetCreated,
		Time: time.Now(),
		Data: []byte(`{"id":"1"}`),
	}
	is.Equal(rec.Handle(ctx, m), broker.Ack)
	is.Equal(rec.Handle(ctx, m), broker.Ack) // the redelivery
	month := "widget_event_log_" + m.Time.UTC().Format("2006_01")
	n, err := dbx.QueryOne[int](ctx, db, "SELECT count(*) FROM "+month)
	is.NoErr(err)
	is.Equal(n, 1) // one row, in the partition of the month of the event

	noTime := m
	noTime.ID, noTime.Time = uuid.New().String(), time.Time{}
	is.Equal(rec.Handle(ctx, noTime), broker.Discard)

	nextYear := m
	nextYear.ID, nextYear.Time = uuid.New().String(), m.Time.AddDate(1, 0, 0)
	is.Equal(rec.Handle(ctx, nextYear), broker.Requeue) // no partition yet
}
