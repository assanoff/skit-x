package widget

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The widget metrics are business metrics, named
// <service>_<subsystem>_<name>_<unit>: skitx_widget_*.

// metrics are the metrics of the widget core.
type metrics struct {
	created prometheus.Counter
}

// newMetrics builds the set once, on reg. With a nil reg the counter works but
// is not registered, so neither the core nor its tests check for nil.
func newMetrics(reg prometheus.Registerer) metrics {
	return metrics{
		created: promauto.With(reg).NewCounter(prometheus.CounterOpts{
			Name: "skitx_widget_created_total",
			Help: "Widgets created.",
		}),
	}
}

// WithMetrics records skitx_widget_created_total on reg. The Core is built once
// per registry: it is a singleton of the process.
func WithMetrics(reg prometheus.Registerer) Option {
	return func(c *Core) {
		c.m = newMetrics(reg)
	}
}

// CountGauge exposes count, the widget total a poller caches, as
// skitx_widget_count on reg. The gauge reads count on every scrape, so a scrape
// never reaches the store: the poller already reads it in the background.
// Register it where the poller runs, or a process without the poller reports
// its initial value.
func CountGauge(reg prometheus.Registerer, count func() int) {
	promauto.With(reg).NewGaugeFunc(prometheus.GaugeOpts{
		Name: "skitx_widget_count",
		Help: "Widgets in the store, as the widget-count poller last read them.",
	}, func() float64 { return float64(count()) })
}
