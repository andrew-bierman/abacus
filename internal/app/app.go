// Package app wires the real modules together: which binary each one runs, which tracker Tasks
// reads and which config Queues reads. cmd/abacus only calls it; the precedence rules live here
// so they can be tested.
package app

import (
	"fmt"
	"os"

	"github.com/andrew-bierman/abacus/internal/bd"
	"github.com/andrew-bierman/abacus/internal/beadslease"
	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/bv"
	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// Env is everything Build reads from the outside world, so tests can supply their own.
type Env struct {
	Getenv    func(string) string
	Cwd       string
	Config    core.ConfigStore
	Workspace string // the managed workspace directory
}

// DefaultEnv is the real process environment and the standard abacus paths.
func DefaultEnv() Env {
	cwd, _ := os.Getwd()
	cfg, _ := core.DefaultConfigPath()
	ws, _ := core.DefaultWorkspaceDir()
	return Env{Getenv: os.Getenv, Cwd: cwd, Config: core.ConfigStore{Path: cfg}, Workspace: ws}
}

// saveTracker remembers a new workspace. It refuses to replace a tracker_dir the user already
// saved: that path is theirs to clear on purpose (`config set tracker_dir ""`).
func saveTracker(store core.ConfigStore) func(string) error {
	return func(dir string) error {
		return store.Update(func(c *core.Config) error {
			if c.TrackerDir != "" && c.TrackerDir != dir {
				return fmt.Errorf("refusing to replace the saved tracker_dir %s (clear it first with: %s config set tracker_dir \"\")", c.TrackerDir, brand.Name)
			}
			return c.Set("tracker_dir", dir)
		})
	}
}

// Build constructs the registry. Binary precedence is each module's own: an ABACUS_*_BIN
// override, then a verified bundled sidecar, then PATH. The tracker is ABACUS_BD_DIR, a tracker
// at or above the working directory, the saved tracker_dir, then an existing managed workspace;
// failing all of those the managed workspace is offered, and a saved tracker_dir that has gone
// missing is reported rather than replaced. Queues reads, in order, --lambe-config,
// ABACUS_LAMBE_CONFIG, then the lambe_config setup saved (if the file is still there).
func Build(o cli.Options, e Env) *core.Registry {
	if o.Demo {
		return core.NewRegistry(lambe.NewDemo())
	}
	cfg, _ := e.Config.Load() // an unreadable file reads as empty here; Doctor reports it
	choice := core.ResolveTracker(e.Getenv("ABACUS_BD_DIR"), cfg, e.Cwd, e.Workspace)
	ref := core.NewTrackerRef(choice.Dir)
	lambeCfg := o.LambeConfig
	if lambeCfg == "" {
		lambeCfg = e.Getenv("ABACUS_LAMBE_CONFIG")
	}
	if lambeCfg == "" && cfg.LambeConfig != "" {
		if fi, err := os.Stat(cfg.LambeConfig); err == nil && fi.Mode().IsRegular() {
			lambeCfg = cfg.LambeConfig
		}
	}
	return core.NewRegistry(
		lambe.New(lambe.Options{
			Bin:        e.Getenv("ABACUS_LAMBE_BIN"),
			StateDir:   e.Getenv("ABACUS_LAMBE_STATE_DIR"),
			ConfigPath: lambeCfg,
			Selector:   lambe.Selector{ConfigPath: lambeCfg},
		}),
		beadslease.New(beadslease.Options{
			Bin:   e.Getenv("ABACUS_BEADSLEASE_BIN"),
			Actor: func() string { c, _ := e.Config.Load(); return c.Actor },
		}),
		bd.New(bd.Options{
			Bin: e.Getenv("ABACUS_BD_BIN"), Ref: ref, InitDir: choice.Offer, ConfigMissing: choice.Missing,
			OnInit: saveTracker(e.Config),
		}),
		bv.New(bv.Options{Bin: e.Getenv("ABACUS_BV_BIN"), Ref: ref}),
		core.NewConfigCheck(e.Config),
	)
}
