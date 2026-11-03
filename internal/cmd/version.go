package cmd

import (
	"fmt"

	"github.com/assanoff/skit/buildinfo"
)

// VersionCommand prints the build and exits: the version, the VCS revision and
// time, and the Go version. go build stamps them from git; the Makefile and the
// release workflow stamp the version from the tag as well:
//
//	go build -ldflags "-X github.com/assanoff/skit/buildinfo.version=v0.1.0"
type VersionCommand struct{}

// Execute implements flags.Commander.
func (VersionCommand) Execute(_ []string) error {
	fmt.Println(buildinfo.Read())
	return nil
}
