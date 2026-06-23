package cmd

import "fmt"

// version is the build version. It defaults to "dev" and is stamped at build
// time via the linker, e.g.
//
//	go build -ldflags "-X github.com/assanoff/service-kit-x/internal/cmd.version=v0.1.0"
//
// The Makefile derives it from `git describe`.
var version = "dev"

// VersionCommand prints the build version and exits.
type VersionCommand struct{}

// Execute implements flags.Commander.
func (VersionCommand) Execute(_ []string) error {
	fmt.Println(version)
	return nil
}
