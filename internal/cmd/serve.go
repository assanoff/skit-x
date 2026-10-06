package cmd

import (
	"github.com/assanoff/skit-x/internal/app/config"
	"github.com/assanoff/skit-x/internal/app/server"
)

// ServeCommand runs everything in one process: the REST API, the gRPC server
// and its gateway, the technical routes, the background workers and the queue
// consumers. This is the default for local development and single-pod
// deployments; the http / worker / consumer commands split the same binary
// across pods.
type ServeCommand struct {
	config.ServerOpts
}

// Execute implements flags.Commander.
func (c *ServeCommand) Execute(_ []string) error {
	return run(c.ServerOpts,
		server.WithServer(c.HTTP.Addr, server.Install),
		server.WithGRPC(c.GRPC.Addr),
		server.WithGateway(c.Gateway.Addr, c.GRPC.Addr),
		server.WithServer(c.Debug.Addr, server.Debug),
		server.WithWorkers(),
		server.WithConsumers(),
	)
}
