package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// jsonOut prints v with schema_version first, so every standalone command is versioned
// the same way as the reports. The embedded report is flattened by encoding/json.
func jsonOut(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	m["schema_version"] = json.RawMessage(strconv.Itoa(core.SchemaVersion))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}

func head(w io.Writer, h core.Header) {
	line := h.Title + "  " + strings.ReplaceAll(string(h.State), "-", " ")
	if h.Source != "" {
		line += "  (" + h.Source + ")"
	}
	fmt.Fprintln(w, line)
	if h.Summary != "" {
		fmt.Fprintf(w, "  %s\n", h.Summary)
	}
	if h.Reason != "" {
		fmt.Fprintf(w, "  %s\n", h.Reason)
	}
	if h.HowToConnect != "" {
		fmt.Fprintf(w, "  how to connect: %s\n", h.HowToConnect)
	}
}

func renderInsights(w io.Writer, in *core.InsightsReport) {
	if in == nil {
		return
	}
	head(w, in.Header)
	for _, p := range in.TopPicks {
		fmt.Fprintf(w, "    next %s  %s  (unblocks %d)\n", p.ID, p.Title, p.Unblocks)
	}
	for _, p := range in.Blockers {
		fmt.Fprintf(w, "    blocker %s  %s  (unblocks %d)\n", p.ID, p.Title, p.Unblocks)
	}
	for _, c := range in.CriticalPath {
		fmt.Fprintf(w, "    critical path %s  %g\n", c.ID, c.Value)
	}
	if in.CriticalPathNote != "" {
		fmt.Fprintf(w, "    %s\n", in.CriticalPathNote)
	}
	for _, c := range in.Cycles {
		fmt.Fprintf(w, "    cycle %s\n", strings.Join(c, " -> "))
	}
}

func renderTasks(w io.Writer, r core.TasksReport) {
	head(w, r.Header)
	if len(r.Recent) > 0 {
		fmt.Fprintln(w, "  recent changes")
	}
	for _, t := range r.Recent {
		fmt.Fprintf(w, "    %-10s %-11s P%d  %s\n", t.ID, t.Status, t.Priority, t.Title)
	}
	renderInsights(w, r.Insights)
}

func renderLeases(w io.Writer, r core.LeasesReport) {
	head(w, r.Header)
	for _, l := range r.Leases {
		extra := ""
		switch l.State {
		case core.LeaseHeld:
			extra = " by " + l.Holder + " (expires " + l.ExpiresAt + ")"
		case core.LeaseStale:
			extra = " (last holder " + l.Holder + ")"
		}
		fmt.Fprintf(w, "    %s  %s%s\n", l.Resource, l.State, extra)
	}
}

func renderQueues(w io.Writer, r core.QueuesReport) {
	head(w, r.Header)
	for _, x := range r.Runs {
		s := fmt.Sprintf("    run %s  %s  %s", x.Issue, orQ(x.State), orQ(x.Phase))
		if x.Stalled {
			s += "  (stalled)"
		}
		fmt.Fprintln(w, s)
	}
	for _, x := range r.Queue {
		state := "ready"
		if x.Blocked {
			state = "blocked"
		}
		fmt.Fprintf(w, "    queued %s  %s  %s\n", x.ID, state, x.Title)
	}
	for _, x := range r.Attention {
		fmt.Fprintf(w, "    attention %s  %s  %s\n", x.Issue, x.Reason, x.Detail)
	}
	for _, x := range r.History {
		fmt.Fprintf(w, "    history %s  %s  %s\n", x.Issue, x.Outcome, x.Title)
	}
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func renderBoard(w io.Writer, r core.BoardReport) {
	head(w, r.Header)
	for _, c := range r.Columns {
		fmt.Fprintf(w, "    %s (%d)\n", c.Name, len(c.Cards))
		for _, k := range c.Cards {
			fmt.Fprintf(w, "      %s  %s\n", k.ID, k.Title)
		}
	}
	renderInsights(w, r.Insights)
}

func renderDomains(w io.Writer, d core.DomainsReport) {
	renderBoard(w, d.Board)
	renderTasks(w, d.Tasks)
	renderLeases(w, d.Leases)
	renderQueues(w, d.Queues)
}

// runDomain handles `board|tasks|leases|queues [--json]` and `tasks create|update|close`.
func runDomain(cmd string, rest []string, d Deps) int {
	if cmd == "tasks" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		return runTaskWrite(rest[0], rest[1:], d)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	asJSON := fs.Bool("json", false, "print JSON")
	fs.StringVar(&o.LambeConfig, "lambe-config", "", "config file selecting the queues daemon")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	ctx := context.Background()
	dom := core.ReadDomains(ctx, d.Build(o).Detect(ctx))
	var v any
	switch cmd {
	case "tasks":
		v = dom.Tasks
		if !*asJSON {
			renderTasks(d.Out, dom.Tasks)
		}
	case "leases":
		v = dom.Leases
		if !*asJSON {
			renderLeases(d.Out, dom.Leases)
		}
	case "queues":
		v = dom.Queues
		if !*asJSON {
			renderQueues(d.Out, dom.Queues)
		}
	case "board":
		v = dom.Board
		if !*asJSON {
			renderBoard(d.Out, dom.Board)
		}
	}
	if *asJSON {
		jsonOut(d.Out, v)
	}
	return core.ExitOK
}

func runTaskWrite(verb string, rest []string, d Deps) int {
	op := core.TaskOp(verb)
	if op != core.TaskCreate && op != core.TaskUpdate && op != core.TaskClose {
		fmt.Fprintf(d.Err, "unknown tasks command %q (create, update, close)\n", verb)
		return core.ExitUsage
	}
	ch := core.TaskChange{Op: op}
	if op != core.TaskCreate {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			fmt.Fprintf(d.Err, "tasks %s needs an issue id first\n", verb)
			return core.ExitUsage
		}
		ch.ID, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("tasks "+verb, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var yes, dry, asJSON bool
	var title, desc, status, reason, typ, prio string
	fs.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&dry, "dry-run", false, "print the change and the command; run nothing")
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.StringVar(&title, "title", "", "title")
	fs.StringVar(&desc, "description", "", "description")
	fs.StringVar(&prio, "priority", "", "priority 0-4")
	if op == core.TaskUpdate {
		fs.StringVar(&status, "status", "", "status: "+strings.Join(core.TaskStatuses, ", "))
	}
	if op == core.TaskCreate {
		fs.StringVar(&typ, "type", "", "type: "+strings.Join(core.TaskTypes, ", "))
	}
	if op == core.TaskClose {
		fs.StringVar(&reason, "reason", "", "reason for closing")
	}
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	ch.Title, ch.Description, ch.Status, ch.Reason, ch.Type = title, desc, status, reason, typ
	if prio != "" {
		n, err := strconv.Atoi(prio)
		if err != nil {
			fmt.Fprintf(d.Err, "invalid priority %q\n", prio)
			return core.ExitUsage
		}
		ch.Priority = &n
	}
	ctx := context.Background()
	entries := d.Build(o).Detect(ctx)
	w := taskWriter(entries)
	if w == nil {
		fmt.Fprintf(d.Err, "tasks: not connected. %s\n", core.HowToConnect[core.DomainTasks])
		return core.ExitFail
	}
	plan, err := core.PlanTaskChange(w, ch)
	if err != nil {
		fmt.Fprintf(d.Err, "tasks %s: %v\n", verb, err)
		return core.ExitUsage
	}
	if !asJSON || dry {
		for _, l := range plan.Lines {
			fmt.Fprintln(d.Out, l)
		}
		fmt.Fprintf(d.Out, "command: %s\n", core.DisplayArgv(plan.Argv))
	}
	if dry {
		if asJSON {
			jsonOut(d.Out, struct {
				DryRun bool     `json:"dry_run"`
				Lines  []string `json:"change"`
				Argv   []string `json:"argv"`
			}{true, plan.Lines, plan.Argv})
		}
		return core.ExitOK
	}
	if !yes {
		fmt.Fprint(d.Out, "Apply this change? [y/N] ")
		if !confirmed(d.In) {
			fmt.Fprintln(d.Out)
			fmt.Fprintln(d.Err, "not applied: the change was not confirmed")
			return core.ExitFail
		}
	}
	au := d.Audit
	if au == nil {
		au = core.FileAudit(core.DefaultAuditPath())
	}
	res, err := core.NewApplier(w, au).Apply(ctx, plan.Confirm())
	if err != nil {
		fmt.Fprintf(d.Err, "tasks %s failed: %s\n", verb, core.Sanitize(err.Error()))
		return core.ExitFail
	}
	if asJSON {
		jsonOut(d.Out, struct {
			Applied bool            `json:"applied"`
			Result  core.TaskResult `json:"result"`
		}{true, res})
	} else {
		fmt.Fprintf(d.Out, "done: %s %s\n", res.ID, res.Title)
	}
	return core.ExitOK
}

// taskWriter finds the first ready module that can write tasks.
func taskWriter(entries []core.Entry) core.TaskWriter {
	for _, e := range core.ReadyOnly(entries) {
		if w, ok := e.Module.(core.TaskWriter); ok {
			return w
		}
	}
	return nil
}

func confirmed(in io.Reader) bool {
	if in == nil {
		return false
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
