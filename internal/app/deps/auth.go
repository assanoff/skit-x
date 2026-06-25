package deps

import (
	"context"
	"fmt"

	"github.com/assanoff/servicekit/auth"
	"github.com/assanoff/servicekit/dim"
	"github.com/assanoff/servicekit/provider"

	"github.com/assanoff/service-kit-x/internal/app/locale"
)

// initTranslator builds the i18n translator from the embedded catalogs via the
// SDK's language-agnostic provider.Translator. It is always available — error
// responses are localized by Accept-Language.
var initTranslator = func(c *Deps) (cleanup dim.CleanupFunc, err error) {
	c.Translator, cleanup = dim.NewResource("Translator",
		provider.Translator(locale.DefaultLang, locale.FS, locale.Files...))
	return cleanup, nil
}

// initAuth builds the JWT verifier when auth is enabled. Disabled => provider
// stays nil and widget writes are public.
var initAuth = func(c *Deps) (dim.CleanupFunc, error) {
	if !c.Opts.Auth.Enabled {
		return nil, nil
	}
	if c.Opts.Auth.JWTSecret == "" {
		return nil, fmt.Errorf("init auth: auth enabled but jwt-secret is empty")
	}
	verifier, err := auth.NewJWTVerifier(auth.JWTConfig{
		HMACSecret: []byte(c.Opts.Auth.JWTSecret),
		Issuer:     c.Opts.Auth.Issuer,
		Audience:   c.Opts.Auth.Audience,
	})
	if err != nil {
		return nil, fmt.Errorf("init auth: %w", err)
	}
	c.Verifier = dim.OnceWithName("Verifier", func(context.Context) (auth.Verifier, error) {
		return verifier, nil
	})
	return nil, nil
}
