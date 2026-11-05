package widget

import "github.com/assanoff/skit/errs"

// MaxImport is the most widgets one import takes at all: the settings may
// lower the limit, not raise it.
const MaxImport = 1000

// Settings are the widget settings an admin changes while the service runs
// (PUT /admin/widget-settings). Every replica keeps them in memory and rereads
// them; the Core and the importer read them on each write.
type Settings struct {
	// ReadOnly pauses the writes of widgets: a create, an update, a delete
	// and an import fail with errs.Unavailable.
	ReadOnly bool `json:"read_only"`
	// ImportLimit is the most widgets one import takes, 1..MaxImport.
	ImportLimit int `json:"import_limit"`
}

// DefaultSettings are the settings before an admin saves any: the writes on,
// imports of up to MaxImport widgets.
func DefaultSettings() Settings {
	return Settings{ImportLimit: MaxImport}
}

// Check reports whether an admin may save s.
func (s Settings) Check() error {
	if s.ImportLimit < 1 || s.ImportLimit > MaxImport {
		return errs.Newf(errs.InvalidArgument, "import_limit must be within 1..%d", MaxImport)
	}
	return nil
}

// Writable fails while s pause the writes of widgets.
func (s Settings) Writable() error {
	if s.ReadOnly {
		return errs.Newf(errs.Unavailable, "widgets are read-only").
			WithMessageID("widget.read_only")
	}
	return nil
}

// WithSettings makes the Core read its settings from current on every write,
// such as the Current of the settings the app keeps; without it the writes
// run on DefaultSettings.
func WithSettings(current func() Settings) Option {
	return func(c *Core) { c.settings = current }
}
