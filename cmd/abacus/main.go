// Command abacus (working name) is the fleet cockpit CLI/TUI for Beads-powered agent fleets.
package main

import (
	"os"

	"github.com/andrew-bierman/abacus/internal/bd"
	"github.com/andrew-bierman/abacus/internal/beadslease"
	"github.com/andrew-bierman/abacus/internal/bv"
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
	cfgPath, _ := core.DefaultConfigPath()
	store := core.ConfigStore{Path: cfgPath}
	cfg, _ := store.Load() // an unreadable file reads as empty here; `abacus config show` reports it
	cwd, _ := os.Getwd()
	ws, _ := core.DefaultWorkspaceDir()
	choice := core.ResolveTracker(os.Getenv("ABACUS_BD_DIR"), cfg, cwd, ws)
	ref := core.NewTrackerRef(choice.Dir)
	lambeCfg := o.LambeConfig
	if lambeCfg == "" {
		lambeCfg = os.Getenv("ABACUS_LAMBE_CONFIG")
	}
	if lambeCfg == "" && cfg.LambeConfig != "" {
		if _, err := os.Stat(cfg.LambeConfig); err == nil {
			lambeCfg = cfg.LambeConfig
		}
	}
	return core.NewRegistry(
		lambe.New(lambe.Options{
			Bin:        os.Getenv("ABACUS_LAMBE_BIN"),
			StateDir:   os.Getenv("ABACUS_LAMBE_STATE_DIR"),
			ConfigPath: lambeCfg,
			Selector:   lambe.Selector{ConfigPath: lambeCfg},
		}),
		beadslease.New(beadslease.Options{
			Bin:   os.Getenv("ABACUS_BEADSLEASE_BIN"),
			Actor: func() string { c, _ := store.Load(); return c.Actor },
		}),
		bd.New(bd.Options{
			Bin: os.Getenv("ABACUS_BD_BIN"), Ref: ref, InitDir: choice.Offer,
			OnInit: func(dir string) error {
				return store.Update(func(c *core.Config) error { return c.Set("tracker_dir", dir) })
			},
		}),
		bv.New(bv.Options{Bin: os.Getenv("ABACUS_BV_BIN"), Ref: ref}),
	)
}

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Deps{
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Version: version, Build: build, RunTUI: tui.Run,
	}))
}
