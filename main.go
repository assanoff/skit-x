// Command skit-x is the showcase service built on the skit SDK: REST, gRPC,
// background workers and queue consumers in one binary.
package main

import (
	"os"

	"github.com/assanoff/skit-x/internal/cmd"
)

// @title		skit-x API
// @version	1.0
// @description	Widget service — the REST contract consumed by other services.
func main() {
	os.Exit(cmd.Execute())
}
