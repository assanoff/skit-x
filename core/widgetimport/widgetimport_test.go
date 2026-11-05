package widgetimport_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/matryer/is"

	"github.com/assanoff/skit/errs"

	"github.com/assanoff/skit-x/core/widget"
	"github.com/assanoff/skit-x/core/widgetimport"
)

// The settings decide before the queue: a read-only Schedule fails with
// Unavailable, a batch over the limit with InvalidArgument. The Importer here
// has no queue.
func TestScheduleChecksTheSettings(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	news := []widget.NewWidget{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	importer := func(s widget.Settings) *widgetimport.Importer {
		current := func() widget.Settings { return s }
		return widgetimport.New(log, nil, nil, widgetimport.WithSettings(current))
	}

	_, err := importer(widget.Settings{ReadOnly: true, ImportLimit: 10}).Schedule(ctx, "", news)
	is.True(errs.Is(err, errs.Unavailable)) // read-only

	_, err = importer(widget.Settings{ImportLimit: 2}).Schedule(ctx, "", news)
	is.True(errs.Is(err, errs.InvalidArgument)) // 3 widgets over the limit of 2
}
