package deps

import (
	"context"
	"fmt"

	"github.com/assanoff/skit/auditlog"
	"github.com/assanoff/skit/auditlog/auditbus"
	auditdb "github.com/assanoff/skit/auditlog/db"
	"github.com/assanoff/skit/dim"
)

// initAuditLog builds the audit-log core over its Postgres store and subscribes it
// to audit events on the in-process bus. Recording happens at the domain layer:
// the widget Core publishes an auditbus.Event on each mutation, the consumer wired
// here records a versioned snapshot — so every path (REST, gRPC, workers) is
// audited uniformly, without transport middleware. The read API is exposed
// separately via auditrest.NewHandlers (see app/server).
var initAuditLog = func(c *Deps) (dim.CleanupFunc, error) {
	c.AuditLog = dim.OnceWithName("AuditLog", func(ctx context.Context) (*auditlog.Core, error) {
		a := c.Opts.Audit
		// The audit_log table is owned by the SDK auditlog package, so it is
		// provisioned here at startup via EnsureSchema (advisory-lock guarded).
		store := auditdb.NewStore(c.Logger, c.DB(ctx))
		if err := store.EnsureSchema(ctx); err != nil {
			return nil, fmt.Errorf("ensure auditlog schema: %w", err)
		}
		// Opportunistic inline compaction: every AutoCompactEvery versions, Create
		// thins that model's history (best-effort) so it stays bounded without a
		// separate sweep. The same options back the POST /auditlog/compact endpoint.
		return auditlog.NewCore(
			c.Logger, store,
			auditlog.WithAutoCompact(a.AutoCompactEvery, auditlog.CompactOptions{
				Factor:      a.Factor,
				KeepRecent:  a.KeepRecent,
				MaxVersions: a.MaxVersions,
			}),
		), nil
	})

	ctx := context.Background()
	auditbus.Register(c.Bus(ctx), c.AuditLog(ctx))
	return nil, nil
}
