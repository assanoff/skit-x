package deps

import (
	"context"
	"fmt"

	"github.com/assanoff/skit/dedup"
	"github.com/assanoff/skit/dim"
)

// initDedup builds the Keeper of the idempotency keys on the store DEDUP_URI
// names: mem:// for one replica, redis://host:port/db for many. dedup.New only
// parses the URI — the Redis client connects on first use — so a bad URI fails
// at startup; the closer closes the client.
var initDedup = func(c *Deps) (dim.CleanupFunc, error) {
	k, err := dedup.New(c.Opts.Dedup.URI, c.Logger)
	if err != nil {
		return nil, fmt.Errorf("init dedup: %w", err)
	}
	c.Dedup = dim.OnceWithName("Dedup", func(context.Context) (*dedup.Keeper, error) {
		return k, nil
	})
	return dim.NamedCleanup("Dedup", k.Close), nil
}
