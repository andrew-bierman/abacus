package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
)

func setupFor(d Deps, entries []core.Entry) *core.Setup {
	if d.Setup != nil {
		return d.Setup(entries)
	}
	o := core.DefaultSetupOptions()
	o.Audit = d.Audit
	if demoRun(d) {
		o.Audit = demoAudit{}
	}
	return core.NewSetup(entries, o)
}

type setupResult = core.SetupResult

// runSetup is `abacus setup [--json] [--yes] [--dry-run]`: report the first-run steps and,
// only when asked (--yes, or a y at the prompt), perform them through the same confirmed,
// single-use, audited gate the TUI uses. It starts nothing.
func runSetup(rest []string, d Deps) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var yes, dry, asJSON, repair bool
	expectRaw := expectFlag(fs)
	fs.BoolVar(&yes, "yes", false, "apply every step that is needed without asking")
	fs.BoolVar(&dry, "dry-run", false, "report only; change nothing")
	jsonVar(d, fs, &asJSON, "print JSON")
	fs.BoolVar(&repair, "repair", false, "repair a half-created workspace: show the plan, set the unfinished files aside (nothing is deleted) and create it again")
	fs.StringVar(&o.LambeConfig, "lambe-config", "", "Lambe config file selecting the queues daemon")
	o.bind(fs, false)
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "setup: unexpected argument %q\n", fs.Arg(0))
		return core.ExitUsage
	}
	if code, ok := o.resolve(d); !ok {
		return code
	}
	// in demo mode setup is only ever a report or a --dry-run plan
	if code, refused := refuseDemoWrite(d, "setup", dry || !(yes || repair)); refused {
		return code
	}
	expect, okDigest := parseExpect(d, "setup", expectRaw)
	if !okDigest {
		return core.ExitUsage
	}
	ctx, cancel := d.withTimeout(cliTimeout)
	defer cancel()
	entries := d.Build(o).Detect(ctx)
	s := setupFor(d, entries)
	if repair {
		return runRepair(ctx, s, d, yes, dry, asJSON, expect)
	}
	steps := s.Steps(ctx)
	reviewed := s.SetupStepPlans(ctx)
	digest := digestFor(d, core.SetupDigest(reviewed))
	if expect != "" && expect != digest {
		return planChanged(d, "setup", expect, digest)
	}
	var results []setupResult
	failed, changedStep, unknownStep := false, false, false
	// Applying needs an explicit yes: --yes, or a y at the prompt (which needs input and text mode).
	canAsk := !asJSON && !demoRun(d) && d.In != nil && interactive(d.In)
	if !dry && (yes || canAsk) {
		in := bufio.NewReader(orEmpty(d.In))
		for _, st := range steps {
			if interrupted(d) { // a signal arrived: no further step is planned, audited or run
				failed = true
				break
			}
			if st.State != "needed" && st.State != "blocked" {
				continue
			}
			// Re-plan now: an earlier step (the workspace) can unblock a later one (Queues).
			if s.Guide(ctx, st.Domain) == nil {
				continue
			}
			p, err := s.Plan(st.Step)
			if err != nil {
				// With --yes the caller asked for it to be done: a step that cannot be is a failure.
				failed = failed || yes
				results = append(results, setupResult{Step: st.Step, Error: err.Error()})
				continue
			}
			if expect != "" && !slices.Equal(p.Argv, reviewedArgv(reviewed, st.Step)) {
				// a step that was blocked when the plan was reviewed, or whose command differs now, was never reviewed
				failed = true
				results = append(results, setupResult{Step: st.Step, Error: "the plan changed since you reviewed it: this step was not run; review it again"})
				changedStep = true
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
			msg, err := trapped(d, func() applyOut { m, e := s.Apply(ctx, p.Confirm()); return applyOut{m, e} }).unpack()
			if err != nil {
				failed = true
				unknownStep = unknownStep || errors.Is(err, core.ErrOutcomeUnknown)
				results = append(results, setupResult{Step: st.Step, Error: core.DescribeWriteError(err)})
				continue
			}
			results = append(results, setupResult{Step: st.Step, Applied: true, Message: msg})
		}
		if !interrupted(d) {
			steps = s.Steps(ctx) // what is left after applying
		}
	}
	if asJSON {
		rep := core.SetupReport{Steps: steps, Results: results, PlanDigest: digest}
		if changedStep { // a step was not run because it was never reviewed: the same error object as any refusal
			b := core.NewErrorDoc(core.CodeChanged, "the plan changed since you reviewed it: a step was not run; review it again").Error
			rep.Error = &b
		} else if unknownStep || (failed && interrupted(d)) { // a cancelled step may or may not have run
			b := core.NewErrorDoc(core.CodeUnknownOutcome, "a step was interrupted or timed out: its result is unknown; refresh before running setup again").Error
			rep.Error = &b
		}
		jsonOut(d.Out, rep)
	} else {
		renderSetup(d.Out, steps, results, yes || dry, canAsk)
	}
	if failed {
		return core.ExitFail
	}
	return core.ExitOK
}

func reviewedArgv(r []core.SetupStepPlan, id core.StepID) []string {
	for _, sp := range r {
		if sp.Step == id {
			return sp.Argv
		}
	}
	return nil
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
		if st.State == "needed" || st.State == "blocked" || st.State == "repair" {
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
		fmt.Fprintln(w, "Run `"+brand.Name+" setup --yes` to apply the steps above, or press the key shown in the TUI.")
	}
}

// runRepair is `abacus setup --repair`: plan the repair, show exactly what it will do, and
// apply it only after --yes or a y at the prompt, through the same audited gate. --dry-run only
// shows the plan. A folder that holds anything else of the user's is refused.
func runRepair(ctx context.Context, s *core.Setup, d Deps, yes, dry, asJSON bool, expect string) int {
	res := setupResult{Step: core.StepRepair}
	digest := ""
	var finishWith func(code int, ec core.ErrorCode) int
	finish := func(code int) int { return finishWith(code, "") }
	finishWith = func(code int, ec core.ErrorCode) int {
		if asJSON {
			rep := core.SetupRepairReport{Results: []setupResult{res}, PlanDigest: digest}
			if code != core.ExitOK { // the same error object every failing command prints, inside this document
				if ec == "" {
					ec = core.CodeFailed
				}
				b := core.NewErrorDoc(ec, res.Error).Error
				rep.Error = &b
			}
			jsonOut(d.Out, rep)
		}
		return code
	}
	p, err := s.Plan(core.StepRepair)
	if err != nil {
		res.Error = err.Error()
		if !asJSON {
			fmt.Fprintf(d.Err, "repair: %s\n", res.Error)
		}
		return finishWith(core.ExitFail, core.CodeRefused)
	}
	digest = digestFor(d, p.Digest())
	if expect != "" && expect != digest {
		return planChanged(d, "repair", expect, digest) // before anything is printed: under --json this is the error document
	}
	if !asJSON {
		for _, l := range p.Lines {
			fmt.Fprintf(d.Out, "%s\n", l)
		}
	}
	if dry {
		res.Message = "dry run: nothing was changed"
		if !asJSON {
			fmt.Fprintln(d.Out, res.Message)
		}
		return finish(core.ExitOK)
	}
	if !yes {
		if asJSON || d.In == nil || !interactive(d.In) {
			res.Error = "not confirmed: run `" + brand.Name + " setup --repair --yes` to apply the plan above"
			if !asJSON {
				fmt.Fprintln(d.Err, res.Error)
			}
			return finishWith(core.ExitFail, core.CodeRefused)
		}
		fmt.Fprint(d.Out, "Apply this repair? [y/N] ")
		line, _ := bufio.NewReader(d.In).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			res.Error = "not confirmed: nothing was changed"
			if !asJSON {
				fmt.Fprintln(d.Out, res.Error)
			}
			return finishWith(core.ExitFail, core.CodeRefused)
		}
	}
	msg, err := trapped(d, func() applyOut { m, e := s.Apply(ctx, p.Confirm()); return applyOut{m, e} }).unpack()
	if err != nil {
		res.Error = core.DescribeWriteError(err)
		if !asJSON {
			fmt.Fprintf(d.Err, "repair: %s\n", res.Error)
		}
		return finishWith(core.ExitFail, core.CodeFor(err))
	}
	res.Applied, res.Message = true, msg
	if !asJSON {
		fmt.Fprintf(d.Out, "done: %s\n", msg)
	}
	return finish(core.ExitOK)
}

// applyOut is one setup step's answer, carried out of trapped.
type applyOut struct {
	msg string
	err error
}

func (a applyOut) unpack() (string, error) { return a.msg, a.err }
