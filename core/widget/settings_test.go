package widget_test

import (
	"context"
	"log/slog"
	"testing"
	"uuid"

	"github.com/matryer/is"

	"github.com/assanoff/skit/errs"

	"github.com/assanoff/skit-x/core/widget"
)

// An admin may save an import limit of 1..MaxImport.
func TestSettingsCheck(t *testing.T) {
	for _, tt := range []struct {
		limit int
		ok    bool
	}{
		{widget.MaxImport, true},
		{1, true},
		{0, false},
		{widget.MaxImport + 1, false},
	} {
		err := widget.Settings{ImportLimit: tt.limit}.Check()
		if (err == nil) != tt.ok {
			t.Errorf("Check() of the limit %d = %v, want ok %t", tt.limit, err, tt.ok)
		}
		if err != nil && !errs.Is(err, errs.InvalidArgument) {
			t.Errorf("Check() of the limit %d = %v, want InvalidArgument", tt.limit, err)
		}
	}
}

// While the settings are read-only, every write fails with Unavailable before
// it reaches the store: the Core here has none.
func TestReadOnly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	readOnly := func() widget.Settings {
		return widget.Settings{ReadOnly: true, ImportLimit: widget.MaxImport}
	}
	core := widget.NewCore(slog.New(slog.DiscardHandler), nil, widget.WithSettings(readOnly))

	_, err := core.Create(ctx, widget.NewWidget{Name: "gadget"})
	is.True(errs.Is(err, errs.Unavailable))
	_, err = core.Update(ctx, uuid.New(), widget.UpdateWidget{})
	is.True(errs.Is(err, errs.Unavailable))
	is.True(errs.Is(core.Delete(ctx, uuid.New()), errs.Unavailable))
}
