package widget_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/matryer/is"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/assanoff/skit-x/core/widget"
)

// createStore accepts every Create; the other methods are not called here.
type createStore struct {
	widget.Store
}

func (createStore) Create(context.Context, widget.Widget) error { return nil }

func TestCreateCountsWidgets(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	core := widget.NewCore(slog.New(slog.DiscardHandler), createStore{}, widget.WithMetrics(reg))

	for _, name := range []string{"bolt", "nut"} {
		_, err := core.Create(t.Context(), widget.NewWidget{Name: name})
		is.NoErr(err)
	}

	want := `
# HELP skitx_widget_created_total Widgets created.
# TYPE skitx_widget_created_total counter
skitx_widget_created_total 2
`
	is.NoErr(testutil.GatherAndCompare(reg, strings.NewReader(want)))
}

// The gauge reads the cached count on every scrape.
func TestCountGauge(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	count := 3
	widget.CountGauge(reg, func() int { return count })

	count = 5

	want := `
# HELP skitx_widget_count Widgets in the store, as the widget-count poller last read them.
# TYPE skitx_widget_count gauge
skitx_widget_count 5
`
	is.NoErr(testutil.GatherAndCompare(reg, strings.NewReader(want)))
}
