// Package app wires the real modules together: which binary each one runs, which tracker Tasks
// reads and which config Queues reads. cmd/abacus only calls it; the precedence rules live here
// so they can be tested.
package app

import (
	"fmt"
	"os"

	"github.com/andrew-bierman/abacus/internal/bd"
	"github.com/andrew-bierman/abacus/internal/beadslease"
	"github.com/andrew-bierman/abacus/internal/board"
	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/graph"
	"github.com/andrew-bierman/abacus/internal/lambe"
	"github.com/andrew-bierman/abacus/internal/rex"
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

// boardURL is the board the CLI is pointed at, with where it came from: ABACUS_BOARD_URL, else
// the saved board_url, else the shell's BOARD_URL, else "" (the CLI's own default: a linked
// Vercel project, or the login's team board). BOARD_URL is validated like the others and then
// passed as --url, never inherited. An invalid value is an error, never a silent fall back to
// the default board.
func boardURL(e Env, cfg core.Config) (url, source, bad string) {
	for _, c := range []struct{ v, src string }{{e.Getenv("ABACUS_BOARD_URL"), "ABACUS_BOARD_URL"}, {cfg.BoardURL, "board_url (config)"}, {e.Getenv("BOARD_URL"), "BOARD_URL"}} {
		if c.v == "" {
			continue
		}
		if err := core.ValidateBoardURL(c.v); err != nil {
			return "", "", c.src + ": " + err.Error()
		}
		return c.v, c.src, ""
	}
	return "", "", ""
}

// Build constructs the registry. Binary precedence is each module's own: an ABACUS_*_BIN
// override, then a verified bundled sidecar, then PATH. The tracker is ABACUS_BD_DIR, a tracker
// at or above the working directory, the saved tracker_dir, then an existing managed workspace;
// failing all of those the managed workspace is offered, and a saved tracker_dir that has gone
// missing is reported rather than replaced. Queues reads, in order, --lambe-config,
// ABACUS_LAMBE_CONFIG, then the lambe_config setup saved (if the file is still there).
func Build(o cli.Options, e Env) *core.Registry {
	if o.Demo {
		return core.NewRegistry(lambe.NewDemo(), demo.NewLeases(), demo.NewTasks(), demo.NewBoard())
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
		insights(ref),
		board.New(boardOptions(e, cfg)),
		// Rex is optional: ABACUS_REX_BIN overrides the lookup, and an absent Rex stays out of the
		// tool list and Doctor. In --demo the demo Leases provider supplies the workspace instead.
		// ABACUS_REX_HOSTS lists the only other machines a read may contact with -S, and
		// ABACUS_REX_PASS_ENV the extra variables (such as SSH_AUTH_SOCK) rex is given.
		rex.New(rex.Options{Bin: e.Getenv("ABACUS_REX_BIN"), Getenv: e.Getenv,
			Hosts: rex.ParseHosts(e.Getenv("ABACUS_REX_HOSTS")), PassEnv: rex.ParsePassEnv(e.Getenv("ABACUS_REX_PASS_ENV"))}),
		core.NewConfigCheck(e.Config),
	)
}

// insights is the Tasks graph-insights provider: bv's analysis package run in this process over
// the same tracker the bd adapter reads. It is the only engine; --demo never gets here.
func insights(ref *core.TrackerRef) core.Module {
	return graph.NewModule(graph.Options{Source: graph.FileSource{Ref: ref}, Analyzer: graph.NewBVAnalyzer()})
}

func boardOptions(e Env, cfg core.Config) board.Options {
	u, src, bad := boardURL(e, cfg)
	return board.Options{Bin: e.Getenv("ABACUS_BOARD_BIN"), URL: u, URLSource: src, URLErr: bad, Getenv: e.Getenv}
}
