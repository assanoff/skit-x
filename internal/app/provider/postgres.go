// Package provider holds resource constructors used by app/deps. Each returns a
// dim factory func — (value, cleanup, error) — mirroring the lavka-promoaction
// app/provider layout: deps declares WHAT to build, provider declares HOW.
package provider

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/assanoff/servicekit/dim"
	"github.com/assanoff/servicekit/logger"
	"github.com/assanoff/servicekit/sqldb"

	"github.com/assanoff/service-kit-x/internal/app/config"
)

// Postgres returns a factory that opens and verifies a Postgres connection pool.
func Postgres(opts *config.ServerOpts, log *logger.Logger) func(ctx context.Context) (*sqlx.DB, dim.CleanupFunc, error) {
	return func(ctx context.Context) (*sqlx.DB, dim.CleanupFunc, error) {
		db, err := sqldb.Open(sqldb.Config{
			User:         opts.DB.User,
			Password:     opts.DB.Password,
			Host:         opts.DB.Host,
			Name:         opts.DB.Name,
			Schema:       opts.DB.Schema,
			MaxIdleConns: opts.DB.MaxIdleConns,
			MaxOpenConns: opts.DB.MaxOpenConns,
			DisableTLS:   opts.DB.DisableTLS,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("open postgres: %w", err)
		}
		if err := sqldb.StatusCheck(ctx, db); err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("postgres status check: %w", err)
		}

		cleanup := func() error { return db.Close() }
		return db, cleanup, nil
	}
}
