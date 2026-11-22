package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

func setupFor(d Deps, entries []core.Entry) *core.Setup {
	if d.Setup != nil {
		return d.Setup(entries)
	}
	o := core.DefaultSetupOptions()
	o.Audit = d.Audit
	return core.NewSetup(entries, o)
}

type setupResult struct {
	Step    core.StepID `json:"step"`
	Applied bool        `json:"applied"`
	Message string      `json:"message,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// runSetup is `abacus setup [--json] [--yes] [--dry-run]`: report the first-run steps and,
// only when asked (--yes, or a y at the prompt), perform them through the same confirmed,
// single-use, audited gate the TUI uses. It starts nothing.
func runSetup(rest []string, d Deps) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var yes, dry, asJSON bool
	fs.BoolVar(&yes, "yes", false, "apply every step that is needed without asking")
	fs.BoolVar(&dry, "dry-run", false, "report only; change nothing")
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.StringVar(&o.LambeConfig, "lambe-config", "", "Lambe config file selecting the queues daemon")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "setup: unexpected argument %q\n", fs.Arg(0))
		return core.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	entries := d.Build(o).Detect(ctx)
	s := setupFor(d, entries)
	steps := s.Steps(ctx)
	var results []setupResult
	failed := false
	// Applying needs an explicit yes: --yes, or a y at the prompt (which needs input and text mode).
	canAsk := !asJSON && d.In != nil && interactive(d.In)
	if !dry && (yes || canAsk) {
		in := bufio.NewReader(orEmpty(d.In))
		for _, st := range steps {
			if st.State != "needed" && st.State != "blocked" {
				continue
			}
			// Re-plan now: an earlier step (the workspace) can unblock a later one (Queues).
			if s.Guide(ctx, st.Domain) == nil {
				continue
			}
			p, err := s.Plan(st.Step)
			if err != nil {
				results = append(results, setupResult{Step: st.Step, Error: err.Error()})
				continue
			}
			if !asJSON {
				fmt.Fprintf(d.Out, "\n%s\n", st.Message)
				for _, l := range p.Lines {
					fmt.Fprintf(d.Out, "  %s\n", l)
				}
				fmt.Fprintf(d.Out, "  command: %s\n", core.DisplayArgv(p.Argv))
			}
			if !yes {
				fmt.Fprint(d.Out, "Apply this step? [y/N] ")
				line, _ := in.ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
					fmt.Fprintln(d.Out)
					results = append(results, setupResult{Step: st.Step, Error: "not confirmed"})
					continue
				}
			}
			msg, err := s.Apply(ctx, p.Confirm())
			if err != nil {
				failed = true
				results = append(results, setupResult{Step: st.Step, Error: core.DescribeWriteError(err)})
				continue
			}
			results = append(results, setupResult{Step: st.Step, Applied: true, Message: msg})
		}
		steps = s.Steps(ctx) // what is left after applying
	}
	if asJSON {
		jsonOut(d.Out, struct {
			Steps   []core.StepInfo `json:"steps"`
			Applied []setupResult   `json:"results,omitempty"`
		}{steps, results})
	} else {
		renderSetup(d.Out, steps, results, yes || dry, canAsk)
	}
	if failed {
		return core.ExitFail
	}
	return core.ExitOK
}

// interactive reports whether r can answer a prompt: a real terminal, not a pipe, a file or
// /dev/null (a plain io.Reader, as tests pass, is taken at its word).
func interactive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if dn, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, dn) {
		return false
	}
	return true
}

func orEmpty(r io.Reader) io.Reader {
	if r == nil {
		return strings.NewReader("")
	}
	return r
}

func renderSetup(w io.Writer, steps []core.StepInfo, results []setupResult, noPrompt, canAsk bool) {
	needed := 0
	for _, st := range steps {
		if st.State == "needed" || st.State == "blocked" {
			needed++
		}
	}
	for _, r := range results {
		if r.Applied {
			fmt.Fprintf(w, "done: %s\n", r.Message)
		} else if r.Error != "" {
			fmt.Fprintf(w, "%s: %s\n", r.Step, r.Error)
		}
	}
	if needed == 0 {
		fmt.Fprintln(w, "Nothing left to set up.")
	} else {
		fmt.Fprintf(w, "%d step(s) to set up:\n", needed)
	}
	for _, st := range steps {
		fmt.Fprintf(w, "  %-11s %s\n", st.State, st.Message)
		for _, l := range st.Lines {
			fmt.Fprintf(w, "              %s\n", l)
		}
		if st.Command != "" {
			fmt.Fprintf(w, "              command: %s\n", st.Command)
		}
	}
	if needed > 0 && !canAsk {
		fmt.Fprintln(w, "Run `abacus setup --yes` to apply the steps above, or press the key shown in the TUI.")
	}
}
