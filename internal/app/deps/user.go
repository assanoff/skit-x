package deps

import (
	"context"

	"github.com/assanoff/servicekit/dim"

	userapi "github.com/assanoff/service-kit-x/api/user"
	"github.com/assanoff/service-kit-x/core/user"
	"github.com/assanoff/service-kit-x/core/user/userdb"
)

// initUserCore builds the user business core over the Postgres store.
var initUserCore = func(c *Deps) (dim.CleanupFunc, error) {
	c.UserCore = dim.OnceWithName("UserCore", func(ctx context.Context) (*user.Core, error) {
		return user.NewCore(c.Logger, userdb.NewStore(c.Logger, c.DB(ctx))), nil
	})
	return nil, nil
}

// initUserHandler builds the REST handler for users.
var initUserHandler = func(c *Deps) (dim.CleanupFunc, error) {
	c.UserHandler = dim.OnceWithName("UserHandler", func(ctx context.Context) (*userapi.Handler, error) {
		return userapi.New(c.UserCore(ctx), c.AuthVerifier(ctx), c.Opts.Auth.RequiredRole), nil
	})
	return nil, nil
}
