// Package cli is the scriptable surface: version, doctor, status and tui.
// Exit codes: 0 healthy, 1 a check failed, 2 usage error.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/andrew-bierman/beaddeck/internal/brand"
	"github.com/andrew-bierman/beaddeck/internal/module"
)

const (
	ExitOK    = 0
	ExitFail  = 1
	ExitUsage = 2
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
	Build    func(Options) *module.Registry
	RunTUI   func(entries []module.Entry) error
}

type moduleReport struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Detection module.Detection `json:"detection"`
	Checks    []module.Check   `json:"checks,omitempty"`
	Status    *module.Status   `json:"status,omitempty"`
}

func usage(d Deps) {
	fmt.Fprintf(d.Err, "usage: %s <command> [flags]\n\ncommands:\n  version   print versions and which tools were detected\n  doctor    health checks across detected tools\n  status    state summary across detected tools\n  tui       interactive view, one section per detected tool\n\nflags: --json (version, doctor, status), --lambe-config FILE, --demo (tui)\n", brand.Name)
}

// Run dispatches a command and returns the process exit code.
func Run(args []string, d Deps) int {
	if len(args) == 0 {
		usage(d)
		return ExitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "doctor", "status", "tui":
	default:
		fmt.Fprintf(d.Err, "unknown command %q\n", cmd)
		usage(d)
		return ExitUsage
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	asJSON := fs.Bool("json", false, "print JSON")
	fs.StringVar(&o.LambeConfig, "lambe-config", "", "Lambe config file selecting the daemon")
	fs.BoolVar(&o.Demo, "demo", false, "render from fixtures (tui)")
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	ctx := context.Background()
	entries := d.Build(o).Detect(ctx)
	switch cmd {
	case "version":
		return runVersion(d, entries, *asJSON)
	case "doctor":
		return runDoctor(ctx, d, entries, *asJSON)
	case "status":
		return runStatus(ctx, d, entries, *asJSON)
	}
	if d.RunTUI == nil {
		return ExitFail
	}
	if err := d.RunTUI(module.ReadyOnly(entries)); err != nil {
		fmt.Fprintf(d.Err, "tui: %v\n", err)
		return ExitFail
	}
	return ExitOK
}

func emit(d Deps, v any) {
	enc := json.NewEncoder(d.Out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func runVersion(d Deps, entries []module.Entry, asJSON bool) int {
	reports := make([]moduleReport, 0, len(entries))
	for _, e := range entries {
		reports = append(reports, moduleReport{ID: e.Module.ID(), Name: e.Module.Name(), Detection: e.Detection})
	}
	if asJSON {
		emit(d, map[string]any{"version": d.Version, "modules": reports})
		return ExitOK
	}
	fmt.Fprintf(d.Out, "%s %s\n", brand.Name, d.Version)
	for _, r := range reports {
		fmt.Fprintf(d.Out, "  %-10s %s %s\n", r.ID, r.Detection.State, strings.TrimSpace(r.Detection.Version+" "+r.Detection.Detail))
	}
	return ExitOK
}

// present drops absent tools: they are reported by `version`, not as findings.
func present(entries []module.Entry) []module.Entry {
	var out []module.Entry
	for _, e := range entries {
		if e.Detection.State != module.Absent {
			out = append(out, e)
		}
	}
	return out
}

func runDoctor(ctx context.Context, d Deps, entries []module.Entry, asJSON bool) int {
	reports := []moduleReport{}
	failed := false
	for _, e := range present(entries) {
		r := moduleReport{ID: e.Module.ID(), Name: e.Module.Name(), Detection: e.Detection}
		switch e.Detection.State {
		case module.Incompatible:
			r.Checks = []module.Check{{Name: "compatibility", Status: module.StatusWarn, Detail: e.Detection.Detail}}
		default:
			r.Checks = e.Module.Doctor(ctx)
		}
		for _, c := range r.Checks {
			failed = failed || c.Status == module.StatusFail
		}
		reports = append(reports, r)
	}
	if asJSON {
		emit(d, map[string]any{"version": d.Version, "modules": reports})
	} else if len(reports) == 0 {
		fmt.Fprintln(d.Out, "no Beads tools detected")
	} else {
		for _, r := range reports {
			fmt.Fprintf(d.Out, "%s\n", r.Name)
			for _, c := range r.Checks {
				fmt.Fprintf(d.Out, "  %-4s  %-12s %s\n", c.Status, c.Name, c.Detail)
				if c.Fix != "" {
					fmt.Fprintf(d.Out, "        fix: %s\n", c.Fix)
				}
			}
		}
	}
	if failed {
		return ExitFail
	}
	return ExitOK
}

func runStatus(ctx context.Context, d Deps, entries []module.Entry, asJSON bool) int {
	reports := []moduleReport{}
	for _, e := range present(entries) {
		r := moduleReport{ID: e.Module.ID(), Name: e.Module.Name(), Detection: e.Detection}
		if e.Detection.State == module.Ready {
			st := e.Module.Status(ctx)
			r.Status = &st
		}
		reports = append(reports, r)
	}
	if asJSON {
		emit(d, map[string]any{"version": d.Version, "modules": reports})
		return ExitOK
	}
	if len(reports) == 0 {
		fmt.Fprintln(d.Out, "no Beads tools detected")
		return ExitOK
	}
	for _, r := range reports {
		fmt.Fprintf(d.Out, "%s\n", r.Name)
		switch {
		case r.Status == nil:
			fmt.Fprintf(d.Out, "  %s: %s\n", r.Detection.State, r.Detection.Detail)
		case r.Status.Unavailable != "":
			fmt.Fprintf(d.Out, "  state unavailable: %s\n", r.Status.Unavailable)
		default:
			fmt.Fprintf(d.Out, "  %s\n", r.Status.Summary)
			for _, l := range r.Status.Lines {
				fmt.Fprintf(d.Out, "    %s\n", l)
			}
		}
	}
	return ExitOK
}
