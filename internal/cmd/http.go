package cmd

import (
	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/server"
)

// HTTPCommand serves the traffic only (REST, gRPC, gateway) plus the technical
// routes: no background workers, no queue consumers. Safe to scale to N
// replicas — nothing in this process polls the database on a timer.
type HTTPCommand struct {
	config.ServerOpts
}

// Execute implements flags.Commander.
func (c *HTTPCommand) Execute(_ []string) error {
	return run(c.ServerOpts,
		server.WithServer(c.HTTP.Addr, server.Install),
		server.WithGRPC(c.GRPC.Addr),
		server.WithGateway(c.Gateway.Addr, c.GRPC.Addr),
		server.WithServer(c.Debug.Addr, server.Debug),
	)
}
