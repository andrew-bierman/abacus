// Command beaddeck (working name) is the umbrella CLI/TUI for the Beads tool family.
package main

import (
	"os"

	"github.com/andrew-bierman/beaddeck/internal/cli"
	"github.com/andrew-bierman/beaddeck/internal/lambe"
	"github.com/andrew-bierman/beaddeck/internal/core"
	"github.com/andrew-bierman/beaddeck/internal/tui"
)

// version is overridden at release time with -ldflags.
var version = "dev"

func build(o cli.Options) *core.Registry {
	if o.Demo {
		return core.NewRegistry(lambe.NewDemo())
	}
	cfg := o.LambeConfig
	if cfg == "" {
		cfg = os.Getenv("BEADDECK_LAMBE_CONFIG")
	}
	return core.NewRegistry(lambe.New(lambe.Options{
		Bin:        os.Getenv("BEADDECK_LAMBE_BIN"),
		StateDir:   os.Getenv("BEADDECK_LAMBE_STATE_DIR"),
		ConfigPath: cfg,
		Selector:   lambe.Selector{ConfigPath: cfg},
	}))
}

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Deps{
		Out: os.Stdout, Err: os.Stderr, Version: version, Build: build, RunTUI: tui.Run,
	}))
}
