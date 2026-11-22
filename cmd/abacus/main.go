// Command abacus (working name) is the fleet cockpit CLI/TUI for Beads-powered agent fleets.
package main

import (
	"os"

	"github.com/andrew-bierman/abacus/internal/bd"
	"github.com/andrew-bierman/abacus/internal/beadslease"
	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
	"github.com/andrew-bierman/abacus/internal/tui"
)

// version is overridden at release time with -ldflags.
var version = "dev"

func build(o cli.Options) *core.Registry {
	if o.Demo {
		return core.NewRegistry(lambe.NewDemo())
	}
	cfg := o.LambeConfig
	if cfg == "" {
		cfg = os.Getenv("ABACUS_LAMBE_CONFIG")
	}
	return core.NewRegistry(
		lambe.New(lambe.Options{
			Bin:        os.Getenv("ABACUS_LAMBE_BIN"),
			StateDir:   os.Getenv("ABACUS_LAMBE_STATE_DIR"),
			ConfigPath: cfg,
			Selector:   lambe.Selector{ConfigPath: cfg},
		}),
		beadslease.New(beadslease.Options{Bin: os.Getenv("ABACUS_BEADSLEASE_BIN")}),
		bd.New(bd.Options{Bin: os.Getenv("ABACUS_BD_BIN"), Dir: os.Getenv("ABACUS_BD_DIR")}),
	)
}

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Deps{
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Version: version, Build: build, RunTUI: tui.Run,
	}))
}
