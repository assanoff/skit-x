package cmd

import (
	"context"
	"fmt"

	"github.com/assanoff/skit/dbx"
	"github.com/assanoff/skit/migrate"

	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/migrations"
)

// MigrateCommand applies database migrations. Usage:
//
//	migrate          # up (default)
//	migrate up
//	migrate down     # roll back one step
//	migrate status
type MigrateCommand struct {
	config.ServerOpts
}

// Execute implements flags.Commander. The trailing arg selects the direction.
func (c *MigrateCommand) Execute(args []string) error {
	cfg := c.ServerOpts
	log := buildLogger(cfg)

	direction := "up"
	if len(args) > 0 {
		direction = args[0]
	}

	db, err := dbx.Open(dbx.Config{
		User:         cfg.DB.User,
		Password:     cfg.DB.Password,
		Host:         cfg.DB.Host,
		Name:         cfg.DB.Name,
		Schema:       cfg.DB.Schema,
		MaxIdleConns: cfg.DB.MaxIdleConns,
		MaxOpenConns: cfg.DB.MaxOpenConns,
		DisableTLS:   cfg.DB.DisableTLS,
	})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if err := dbx.StatusCheck(ctx, db); err != nil {
		return fmt.Errorf("db status check: %w", err)
	}

	m, err := migrate.New(migrate.Postgres, db.DB, migrations.FS)
	if err != nil {
		return fmt.Errorf("build migrator: %w", err)
	}
	defer func() { _ = m.Close() }()

	log.Info(ctx, "running migrations", "direction", direction)
	switch direction {
	case "up":
		return m.Up(ctx)
	case "down":
		return m.Down(ctx)
	case "status":
		statuses, err := m.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			state := "pending"
			if s.Applied {
				state = "applied"
			}
			log.Info(ctx, "migration", "version", s.Version, "state", state, "source", s.Source)
		}
		return nil
	default:
		return fmt.Errorf("unknown migrate direction %q (want: up | down | status)", direction)
	}
}
