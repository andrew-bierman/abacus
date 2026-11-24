package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// runAgents handles `agents [--json] [--harness H] [--state S] [--repo R] [WORDS...]`: every
// running coding agent, from one read of the Agents domain. Read-only. Words after the flags are
// matched against the task, repository, branch and harness.
func runAgents(rest []string, d Deps) int {
	fs := flag.NewFlagSet("agents", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var asJSON bool
	var f core.AgentFilter
	var state string
	jsonVar(d, fs, &asJSON, "print JSON")
	fs.StringVar(&f.Harness, "harness", "", "only this harness (claude-code, codex, ...)")
	fs.StringVar(&state, "state", "", "only this state: "+statesList())
	fs.StringVar(&f.Repo, "repo", "", "only this repository")
	fs.BoolVar(&o.Demo, "demo", false, "render from fixtures")
	fs.BoolVar(&o.AllUsers, "all", false, "include agents run by other users (default: only yours)")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if state != "" {
		f.State = core.AgentState(strings.ToLower(state))
		if !f.State.Valid() {
			fmt.Fprintf(d.Err, "agents: unknown state %q (%s)\n", state, statesList())
			return core.ExitUsage
		}
	}
	f.Query = strings.Join(fs.Args(), " ")
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	r := core.ReadDomain(ctx, d.Build(o).Detect(ctx), core.DomainAgents).Agents
	r.Agents = core.FilterAgents(r.Agents, f)
	if asJSON {
		jsonOut(d.Out, r)
		return core.ExitOK
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	renderAgents(d.Out, r, now)
	return core.ExitOK
}

func statesList() string {
	s := make([]string, len(core.AgentStates))
	for i, st := range core.AgentStates {
		s[i] = string(st)
	}
	return strings.Join(s, ", ")
}

// renderAgents prints the report as a table. The last column says how the state was learned, and
// a state that is an inference rather than a report is marked with "~".
func renderAgents(w io.Writer, r core.AgentsReport, now time.Time) {
	head(w, r.Header)
	for _, n := range r.Notes {
		fmt.Fprintf(w, "  note: %s\n", n)
	}
	for _, a := range r.Agents {
		mark := " "
		if a.Guess() {
			mark = "~"
		}
		where := orDashStr(a.Repo)
		if a.Branch != "" {
			where += "@" + a.Branch
		}
		ctxPct := ""
		if a.ContextPct != nil {
			ctxPct = fmt.Sprintf("  ctx %.0f%%", *a.ContextPct)
		}
		fmt.Fprintf(w, "  %-8s%s %-11s %-30s %-5s %s%s  (%s)\n", a.State, mark, a.Title(), where, orDashStr(a.Age(now)), a.Task, ctxPct, a.Source)
	}
	for _, l := range r.RateLimits {
		s := "  limits " + core.Sanitize(core.HarnessTitle(l.Harness))
		if l.FiveHourPct != nil {
			s += fmt.Sprintf("  5h %.0f%%", *l.FiveHourPct)
		}
		if l.SevenDayPct != nil {
			s += fmt.Sprintf("  7d %.0f%%", *l.SevenDayPct)
		}
		fmt.Fprintln(w, s)
	}
	for _, p := range r.OrphanPorts {
		fmt.Fprintf(w, "  orphan port %d  %s\n", p.Port, core.Sanitize(p.Command))
	}
}

func orDashStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
