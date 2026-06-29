package deps

import (
	"context"
	"fmt"
	"strings"

	"github.com/assanoff/skit/dim"
	"github.com/assanoff/skit/translation"
	translationpg "github.com/assanoff/skit/translation/postgres"
)

// initTranslation builds the content-translation translator over its Postgres
// store. Canonical widget content is stored in the default language; the
// translationrest middleware (wired in app/server) translates responses into the
// request language whenever a translation exists. The translations table is
// owned by the SDK translation package, so it is provisioned here at startup via
// EnsureSchema (advisory-lock guarded, replica-safe).
var initTranslation = func(c *Deps) (dim.CleanupFunc, error) {
	c.Translation = dim.OnceWithName("Translation", func(ctx context.Context) (*translation.Translator, error) {
		t := c.Opts.Translation

		var supported []translation.Language
		for code := range strings.SplitSeq(t.Supported, ",") {
			if code = strings.TrimSpace(code); code != "" {
				supported = append(supported, translation.Language{Code: code})
			}
		}

		store := translationpg.NewStore(c.Logger, c.DB(ctx))
		if err := store.EnsureSchema(ctx); err != nil {
			return nil, fmt.Errorf("ensure translation schema: %w", err)
		}
		tr, err := translation.New(translation.Config{
			Store:           store,
			DefaultLanguage: translation.Language{Code: t.DefaultLang},
			SupportedLangs:  supported,
		})
		if err != nil {
			return nil, fmt.Errorf("init translation: %w", err)
		}
		return tr, nil
	})
	return nil, nil
}
