// Command wallapop is a terminal client for Wallapop. See docs/cli-spec.md.
package main

import (
	"os"

	"github.com/Microck/wallapop-cli/internal/cli"
)

// version is set by goreleaser through -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
