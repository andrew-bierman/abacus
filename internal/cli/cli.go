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
	"time"

	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
)

// Options are the cross-command flags that shape module construction.
type Options struct {
	// AllUsers lists agents run by other users too (agents --all); the default is the current user only.
	AllUsers    bool
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
	RunTUI   func(entries []core.Entry, tab string) error
	// Setup builds the first-run setup over the detected modules; nil means the real one.
	Setup func(entries []core.Entry) *core.Setup
	// Config is the abacus config file; the zero value means the default path.
	Config core.ConfigStore
	// Exe is this program's path (what the layout's tabs run); nil means os.Executable.
	Exe func() (string, error)
	// Now is the clock for ages in the task views; nil means time.Now.
	Now func() time.Time
}

func usage(d Deps) {
	fmt.Fprintf(d.Err, "usage: %s <command> [flags]\n\nfleet:\n  board     channels, recent threads and tasks; board post|reply send a message\n  tasks     task summary; tasks list|show|board browse; tasks create|update|close|label|assign|priority|dep|comment change tasks; tasks bulk changes up to 25 at once\n  leases    held, stale and free leases\n  queues    runs, queue, attention and history\n  agents    every running coding agent; --harness, --state, --repo, --all, --json\n  tui       interactive cockpit\n  search    find tasks, board threads, leases and runs; --domain, --limit, --json\n\nfirst run:\n  rex       layout: open the cockpit tabs in a terminal workspace (optional); --new NAME, --yes, --dry-run, --json\n  setup     report and (with --yes) perform first-run setup; --dry-run, --json; --repair fixes a half-created workspace\n  config    show|get|set|path: abacus settings (actor, tracker_dir, lambe_config, board_url)\n\ndiagnostics:\n  version   print versions and which providers were detected\n  doctor    health checks across providers\n  status    state summary across providers and fleet domains\n  licenses  print the third-party notices and license texts carried in this file\n\nflags: --json, --lambe-config FILE, --demo and --tab NAME (tui); tasks writes: --yes, --dry-run\n", brand.Name)
}

// Run dispatches a command and returns the process exit code.
func Run(args []string, d Deps) int {
	if len(args) == 0 {
		usage(d)
		return core.ExitUsage
	}
	cmd, rest := args[0], args[1:]
	// The first start unpacks the built-in tools (a few seconds): say so on stderr rather than
	// leaving a blank terminal. Nothing is printed when they are already unpacked.
	core.PrepareNotice = func() { fmt.Fprintln(d.Err, "Preparing built-in tools (first run only)…") }
	defer func() { core.PrepareNotice = nil }()
	switch cmd {
	case "version", "doctor", "status", "tui":
	case "tasks", "leases", "queues", "board":
		return runDomain(cmd, rest, d)
	case "agents":
		return runAgents(rest, d)
	case "search":
		return runSearch(rest, d)
	case "setup":
		return runSetup(rest, d)
	case "rex":
		return runRex(rest, d)
	case "config":
		return runConfig(rest, d)
	case "licenses":
		return runLicenses(rest, d)
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
	tab := fs.String("tab", "", "tab to open first: board, tasks, leases, queues or doctor (tui)")
	if err := fs.Parse(rest); err != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "%s: unexpected argument %q\n", cmd, fs.Arg(0))
		return core.ExitUsage
	}
	if *tab != "" && (cmd != "tui" || !core.ValidTab(*tab)) {
		fmt.Fprintf(d.Err, "%s: --tab is for tui and takes board, tasks, leases, queues or doctor\n", cmd)
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
		if err := d.RunTUI(tuiEntries(entries), strings.ToLower(*tab)); err != nil {
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
			detail := m.Detection.Detail
			state := string(m.Detection.State)
			if m.Detection.State == core.Absent {
				detail = core.AbsentSentence(m.ID, detail)
				if core.IsBundledTool(m.ID) {
					state = "unavailable"
				}
			}
			fmt.Fprintf(w, "  %-10s %s %s\n", m.ID, state, strings.TrimSpace(m.Detection.Version+" "+detail))
		}
		if r.Bundled != nil {
			fmt.Fprintf(w, "%s\n", r.Bundled.Line)
		}
		return
	}
	if cmd == "doctor" && r.Overview != nil {
		renderOverview(w, *r.Overview)
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

// renderOverview is how doctor leads: ready or needs attention, then each domain and tool.
func renderOverview(w io.Writer, o core.Overview) {
	fmt.Fprintln(w, o.Headline)
	for _, a := range o.Attention {
		fmt.Fprintf(w, "  - %s\n", a)
	}
	fmt.Fprintln(w, "\n"+o.Bundled.Line)
	fmt.Fprintln(w, "\nAreas")
	for _, d := range o.Domains {
		fmt.Fprintf(w, "  %-7s %s\n", d.Title, d.Line)
	}
	fmt.Fprintln(w, "\nTools")
	for _, t := range o.Tools {
		fmt.Fprintf(w, "  %-12s %-8s %s\n", t.ID, t.Label(), strings.TrimSpace(t.Version+"  "+t.Path))
		if t.Note != "" {
			fmt.Fprintf(w, "    %s\n", t.Note)
		}
	}
	fmt.Fprintln(w)
}
