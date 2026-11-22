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
	Version  string
	Build    func(Options) *core.Registry
	RunTUI   func(entries []core.Entry) error
}

func usage(d Deps) {
	fmt.Fprintf(d.Err, "usage: %s <command> [flags]\n\ncommands:\n  version   print versions and which tools were detected\n  doctor    health checks across detected tools\n  status    state summary across detected tools\n  tui       interactive view, one section per detected tool\n\nflags: --json (version, doctor, status), --lambe-config FILE, --demo (tui)\n", brand.Name)
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
	ctx := context.Background()
	entries := d.Build(o).Detect(ctx)
	if cmd == "tui" {
		if d.RunTUI == nil {
			return core.ExitFail
		}
		if err := d.RunTUI(core.ReadyOnly(entries)); err != nil {
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
		r = core.StatusReport(ctx, d.Version, entries)
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
	if len(r.Modules) == 0 {
		fmt.Fprintln(w, "no Beads tools detected")
		return
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
