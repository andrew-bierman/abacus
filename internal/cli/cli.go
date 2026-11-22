// Package cli is a thin front-end over core: it parses flags, asks core for a typed
// report, and renders it as text or JSON. Exit codes come from core.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
)

// Options are the cross-command flags that shape module construction.
type Options struct {
	LambeConfig string
	Demo        bool
}

// Deps are the process edges, injectable for tests.
type Deps struct {
	Out, Err io.Writer
	In       io.Reader  // confirm prompts; nil means no input (a write then needs --yes)
	Audit    core.Audit // nil means the default file audit log
	Version  string
	Build    func(Options) *core.Registry
	RunTUI   func(entries []core.Entry) error
	// Setup builds the first-run setup over the detected modules; nil means the real one.
	Setup func(entries []core.Entry) *core.Setup
	// Config is the abacus config file; the zero value means the default path.
	Config core.ConfigStore
}

func usage(d Deps) {
	fmt.Fprintf(d.Err, "usage: %s <command> [flags]\n\nfleet:\n  board     the board\n  tasks     task summary; tasks create|update|close change tasks\n  leases    held, stale and free leases\n  queues    runs, queue, attention and history\n  tui       interactive cockpit\n\nfirst run:\n  setup     report and (with --yes) perform first-run setup; --dry-run, --json\n  config    show|get|set|path: abacus settings (actor, tracker_dir, lambe_config)\n\ndiagnostics:\n  version   print versions and which providers were detected\n  doctor    health checks across providers\n  status    state summary across providers and fleet domains\n\nflags: --json, --lambe-config FILE, --demo (tui); tasks writes: --yes, --dry-run\n", brand.Name)
}

// Run dispatches a command and returns the process exit code.
func Run(args []string, d Deps) int {
	if len(args) == 0 {
		usage(d)
		return core.ExitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "doctor", "status", "tui":
	case "tasks", "leases", "queues", "board":
		return runDomain(cmd, rest, d)
	case "setup":
		return runSetup(rest, d)
	case "config":
		return runConfig(rest, d)
	default:
		fmt.Fprintf(d.Err, "unknown command %q\n", cmd)
		usage(d)
		return core.ExitUsage
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	asJSON := fs.Bool("json", false, "print JSON")
	fs.StringVar(&o.LambeConfig, "lambe-config", "", "Lambe config file selecting the daemon")
	fs.BoolVar(&o.Demo, "demo", false, "render from fixtures (tui)")
	if err := fs.Parse(rest); err != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "%s: unexpected argument %q\n", cmd, fs.Arg(0))
		return core.ExitUsage
	}
	ctx := context.Background()
	if cmd != "tui" { // the TUI is interactive and has no overall deadline
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cliTimeout)
		defer cancel()
	}
	entries := d.Build(o).Detect(ctx)
	if cmd == "tui" {
		if d.RunTUI == nil {
			return core.ExitFail
		}
		if err := d.RunTUI(tuiEntries(entries)); err != nil {
			fmt.Fprintf(d.Err, "tui: %v\n", err)
			return core.ExitFail
		}
		return core.ExitOK
	}
	var r core.Report
	switch cmd {
	case "version":
		r = core.VersionReport(d.Version, entries)
	case "doctor":
		r = core.DoctorReport(ctx, d.Version, entries)
	case "status":
		r = core.FleetStatusReport(ctx, d.Version, entries)
	}
	if *asJSON {
		enc := json.NewEncoder(d.Out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	} else {
		renderText(d.Out, cmd, r)
	}
	return r.ExitCode()
}

func renderText(w io.Writer, cmd string, r core.Report) {
	if cmd == "version" {
		fmt.Fprintf(w, "%s %s\n", brand.Name, r.Version)
		for _, m := range r.Modules {
			fmt.Fprintf(w, "  %-10s %s %s\n", m.ID, m.Detection.State, strings.TrimSpace(m.Detection.Version+" "+m.Detection.Detail))
		}
		return
	}
	if len(r.Modules) == 0 && r.Domains == nil {
		fmt.Fprintln(w, "no Beads tools detected")
		return
	}
	if r.Domains != nil {
		renderDomains(w, *r.Domains)
		if len(r.Modules) > 0 {
			fmt.Fprintln(w, "\nProviders")
		} else {
			fmt.Fprintln(w, "\nno Beads tools detected")
		}
	}
	for _, m := range r.Modules {
		fmt.Fprintf(w, "%s\n", m.Name)
		if cmd == "doctor" {
			for _, c := range m.Checks {
				fmt.Fprintf(w, "  %-4s  %-12s %s\n", c.Status, c.Name, c.Detail)
				if c.Fix != "" {
					fmt.Fprintf(w, "        fix: %s\n", c.Fix)
				}
			}
			continue
		}
		switch {
		case m.Status == nil:
			fmt.Fprintf(w, "  %s: %s\n", m.Detection.State, m.Detection.Detail)
		case m.Status.Unavailable != "":
			fmt.Fprintf(w, "  state unavailable: %s\n", m.Status.Unavailable)
		default:
			if m.Status.Source != "" {
				fmt.Fprintf(w, "  source: %s\n", m.Status.Source)
			}
			fmt.Fprintf(w, "  %s\n", m.Status.Summary)
			for _, l := range m.Status.Lines {
				fmt.Fprintf(w, "    %s\n", l)
			}
		}
	}
}

// tuiEntries is what the TUI is handed: every detected provider. Domains never disappear, so
// with nothing ready the TUI shows each one as not connected.
func tuiEntries(entries []core.Entry) []core.Entry { return entries }
