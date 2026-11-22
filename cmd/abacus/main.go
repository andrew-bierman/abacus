// Command abacus (working name) is the fleet cockpit CLI/TUI for Beads-powered agent fleets.
package main

import (
	"os"

	"github.com/andrew-bierman/abacus/internal/app"
	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/tui"
)

// version is overridden at release time with -ldflags.
var version = "dev"

func build(o cli.Options) *core.Registry { return app.Build(o, app.DefaultEnv()) }

func main() {
	code := cli.Run(os.Args[1:], cli.Deps{
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Version: version, Build: build, RunTUI: tui.Run,
	})
	core.CleanupSidecarEnv()
	os.Exit(code)
}
