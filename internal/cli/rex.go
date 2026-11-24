package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrew-bierman/abacus/internal/core"
)

// runRex handles `rex layout`: open the cockpit tabs (Board, Tasks, Leases, Queues, each running
// `tui --tab X`) in the current terminal-workspace session, or in a new one with --new NAME. It
// goes through the same plan, confirm, single-use and audit gate as every other write, and only
// opens tabs: nothing is typed into an existing terminal.
func runRex(args []string, d Deps) int {
	if len(args) == 0 || args[0] != "layout" {
		fmt.Fprintf(d.Err, "usage: abacus rex layout [--new NAME] [--yes] [--dry-run] [--json]\n")
		return core.ExitUsage
	}
	fs := flag.NewFlagSet("rex layout", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var yes, dry, asJSON bool
	var name string
	fs.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&dry, "dry-run", false, "print the plan and the commands; run nothing")
	jsonVar(d, fs, &asJSON, "print JSON")
	fs.StringVar(&name, "new", "", "create a session with this name for the tabs (default: add them to the current session)")
	if fs.Parse(args[1:]) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "rex layout: unexpected argument %q\n", fs.Arg(0))
		return core.ExitUsage
	}
	if asJSON && !yes && !dry {
		fmt.Fprintf(d.Err, "rex layout: --json needs --yes or --dry-run (it cannot prompt)\n")
		return core.ExitUsage
	}
	exe, err := selfPath(d)
	if err != nil {
		fmt.Fprintf(d.Err, "rex layout: cannot tell where this program is: %v\n", err)
		return core.ExitFail
	}
	cwd, _ := os.Getwd()
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	w := core.ResolveProviders(d.Build(Options{}).Detect(ctx)).Placer
	if w == nil {
		return failf(d, core.CodeNotConnected, "rex layout: not connected to a terminal workspace. Rex is optional; when a Rex server is running, or abacus runs inside a Rex terminal, this opens the cockpit tabs in it.\n")
	}
	plan, err := core.PlanPlacement(ctx, w, core.PlacementRequest{Action: core.PlaceLayout, Exe: exe, Cwd: cwd, NewSession: name})
	if err != nil {
		fmt.Fprintf(d.Err, "rex layout: %v\n", err)
		return core.ExitUsage
	}
	return runWrite(d, writeFlow{label: "rex layout", lines: plan.Lines, argv: plan.Steps[0], more: plan.Steps[1:], yes: yes, dry: dry, asJSON: asJSON, prompt: "Open these tabs? [y/N] ",
		apply: func(au core.Audit) (any, string, error) {
			res, err := core.NewPlacementApplier(w, au).Apply(ctx, plan.Confirm())
			return res, fmt.Sprintf("done: opened %d tabs", res.Steps), err
		}})
}

// selfPath is this program's own absolute path: what each tab runs.
func selfPath(d Deps) (string, error) {
	f := d.Exe
	if f == nil {
		f = os.Executable
	}
	p, err := f()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p), nil
}
