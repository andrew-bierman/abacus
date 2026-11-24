package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// jsonOut prints v with schema_version added (core.ReportJSON), so every standalone command is versioned
// the same way as the reports. The embedded report is flattened by encoding/json.
func jsonOut(w io.Writer, v any) {
	b, _ := core.ReportJSON(v)
	_, _ = w.Write(b)
}

// cliTimeout bounds a whole command, so a stuck provider cannot hang it.
const cliTimeout = 30 * time.Second

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
		line := fmt.Sprintf("    %s  %s", l.Resource, l.State)
		if d := l.Detail(); d != "" {
			line += " " + d
		}
		fmt.Fprintln(w, line)
	}
}

func renderQueues(w io.Writer, r core.QueuesReport) {
	head(w, r.Header)
	for _, x := range r.Runs {
		s := fmt.Sprintf("    run %s  %s  %s", x.Issue, orQ(string(x.State)), orQ(x.Phase))
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
	if r.Truncated != "" {
		fmt.Fprintf(w, "  note: %s\n", r.Truncated)
	}
	for _, c := range r.Channels {
		if c.Threads > 0 { // a board that gives no counts shows none, not (0)
			fmt.Fprintf(w, "    channel %s  (%d threads)\n", c.Path, c.Threads)
		} else {
			fmt.Fprintf(w, "    channel %s\n", c.Path)
		}
	}
	for _, t := range r.Threads {
		fmt.Fprintf(w, "    thread #%s  %s  %s  %s  (%d replies)\n", t.Root.ID, t.Root.Channel, t.Root.Author, oneLine(t.Root.Text), t.Replies)
		if t.Last != nil {
			fmt.Fprintf(w, "      latest #%s  %s  %s\n", t.Last.ID, t.Last.Author, oneLine(t.Last.Text))
		}
	}
	for _, t := range r.Tasks {
		s := fmt.Sprintf("    task #%s  %s", t.ID, t.State)
		if t.Claimant != "" {
			s += "  held by " + t.Claimant
		}
		fmt.Fprintf(w, "%s  %s\n", s, oneLine(t.Text))
	}
}

func oneLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

func renderDomains(w io.Writer, d core.DomainsReport) {
	renderBoard(w, d.Board)
	renderTasks(w, d.Tasks)
	renderLeases(w, d.Leases)
	renderQueues(w, d.Queues)
	head(w, d.Agents.Header) // the list is `abacus agents`: a status line is a summary
}

// runDomain handles `board|tasks|leases|queues [--json]` and `tasks create|update|close`.
func runDomain(cmd string, rest []string, d Deps) int {
	if cmd == "tasks" && len(rest) > 0 && (rest[0] == "list" || rest[0] == "show" || rest[0] == "board") {
		return runTaskView(rest[0], rest[1:], d)
	}
	if cmd == "tasks" && len(rest) > 0 && rest[0] == "history" {
		return runTaskHistory(rest[1:], d)
	}
	if cmd == "tasks" && len(rest) > 0 && rest[0] == "graph" {
		return runTaskGraph(rest[1:], d)
	}
	if cmd == "tasks" && firstOf(rest) == "bulk" {
		return runTaskBulk(rest[1:], d)
	}
	if _, edit := editVerbs[firstOf(rest)]; cmd == "tasks" && edit {
		return runTaskEdit(rest[0], rest[1:], d)
	}
	if cmd == "tasks" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		return runTaskWrite(rest[0], rest[1:], d)
	}
	if cmd == "board" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		return runBoardWrite(rest[0], rest[1:], d)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	asJSON := fs.Bool("json", false, "print JSON")
	fs.StringVar(&o.LambeConfig, "lambe-config", "", "config file selecting the queues daemon")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "%s: unexpected argument %q\n", cmd, fs.Arg(0))
		return core.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	dom := core.ReadDomain(ctx, d.Build(o).Detect(ctx), core.DomainID(cmd))
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

func firstOf(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

func runTaskWrite(verb string, rest []string, d Deps) int {
	op := core.TaskOp(verb)
	if op != core.TaskCreate && op != core.TaskUpdate && op != core.TaskClose {
		fmt.Fprintf(d.Err, "unknown tasks command %q (list, show, board, graph, history, create, update, close, label, assign, priority, dep, comment, bulk)\n", verb)
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
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "tasks %s: unexpected argument %q (quote values that contain spaces)\n", verb, fs.Arg(0))
		return core.ExitUsage
	}
	if asJSON && !yes && !dry {
		fmt.Fprintf(d.Err, "tasks %s: --json needs --yes or --dry-run (it cannot prompt)\n", verb)
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
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	entries := d.Build(o).Detect(ctx)
	w := core.ResolveProviders(entries).Writer
	if w == nil {
		fmt.Fprintf(d.Err, "tasks: not connected. %s\n", core.HowToConnectFor(core.DomainTasks))
		return core.ExitFail
	}
	if ch.Status != "" && !slices.Contains(core.TaskStatuses, ch.Status) {
		fmt.Fprintf(d.Err, "tasks %s: status must be one of %s (use close to close)\n", verb, strings.Join(core.TaskStatuses, ", "))
		return core.ExitUsage
	}
	if ch.Status != "" { // a status change names the status the task has now, read here, so a change by someone else is refused
		r := core.ResolveProviders(entries).Reader
		if r == nil {
			fmt.Fprintf(d.Err, "tasks %s: cannot read the task's current status, so --status is not available\n", verb)
			return core.ExitFail
		}
		cur, err := r.ReadTask(ctx, ch.ID)
		if err != nil {
			fmt.Fprintf(d.Err, "tasks %s: could not read %s first: %s\n", verb, core.Sanitize(ch.ID), core.Sanitize(err.Error()))
			return core.ExitFail
		}
		ch.IfStatus, ch.Before = cur.Status, cur
		// naming the status it already has is not a change: drop it (and its guard) when other fields
		// are set; alone it stays the "already X" refusal
		if ch.Status == cur.Status && (ch.Title != "" || ch.Description != "" || ch.Priority != nil) {
			ch.Status, ch.IfStatus = "", ""
		}
	}
	plan, err := core.PlanTaskChange(w, ch)
	if err != nil {
		fmt.Fprintf(d.Err, "tasks %s: %v\n", verb, err)
		return core.ExitUsage
	}
	return runWrite(d, writeFlow{label: "tasks " + verb, lines: plan.Lines, argv: plan.Argv, yes: yes, dry: dry, asJSON: asJSON, prompt: "Apply this change? [y/N] ",
		apply: func(au core.Audit) (any, string, error) {
			res, err := core.NewApplier(w, au).Apply(ctx, plan.Confirm())
			return res, fmt.Sprintf("done: %s %s", res.ID, res.Title), err
		}})
}

// writeFlow is everything one confirmed write needs; runWrite is the one flow shared by the
// Tasks and Board writes: print the plan, honor --dry-run, ask (on stderr, so stdout stays the
// result) unless --yes, audit, apply, print the result.
type writeFlow struct {
	label      string
	lines, ctx []string // the plan, and read-only context for the confirm step
	argv       []string
	more       [][]string // later commands of a plan with several steps
	yes, dry   bool
	asJSON     bool
	prompt     string
	apply      func(au core.Audit) (result any, done string, err error)
}

func runWrite(d Deps, f writeFlow) int {
	if f.dry && f.asJSON {
		jsonOut(d.Out, struct {
			DryRun  bool       `json:"dry_run"`
			Lines   []string   `json:"change"`
			Context []string   `json:"context,omitempty"`
			Argv    []string   `json:"argv"`
			Steps   [][]string `json:"steps,omitempty"`
		}{true, f.lines, f.ctx, f.argv, f.more})
		return core.ExitOK
	}
	if !f.asJSON {
		for _, l := range append(append([]string{}, f.lines...), f.ctx...) {
			fmt.Fprintln(d.Out, l)
		}
		fmt.Fprintf(d.Out, "command: %s\n", core.DisplayArgv(f.argv))
		for _, a := range f.more {
			fmt.Fprintf(d.Out, "command: %s\n", core.DisplayArgv(a))
		}
	}
	if f.dry {
		return core.ExitOK
	}
	if !f.yes {
		fmt.Fprint(d.Err, f.prompt)
		if !confirmed(d.In) {
			fmt.Fprintln(d.Err)
			fmt.Fprintln(d.Err, "not applied: the change was not confirmed")
			return core.ExitFail
		}
	}
	au := d.Audit
	if au == nil {
		au = core.DefaultAudit()
	}
	res, done, err := f.apply(au)
	if err != nil {
		fmt.Fprintf(d.Err, "%s: %s\n", f.label, core.DescribeWriteError(err))
		return core.ExitFail
	}
	if f.asJSON {
		jsonOut(d.Out, struct {
			Applied bool `json:"applied"`
			Result  any  `json:"result"`
		}{true, res})
	} else {
		fmt.Fprintln(d.Out, done)
	}
	return core.ExitOK
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

// runBoardWrite handles `board post CHANNEL TEXT` and `board reply ID TEXT`, through the same
// plan, confirm, single-use and audit gate as the Tasks writes.
func runBoardWrite(verb string, rest []string, d Deps) int {
	op := core.BoardOp(verb)
	if op != core.BoardPost && op != core.BoardReply {
		fmt.Fprintf(d.Err, "unknown board command %q (post CHANNEL TEXT, reply ID TEXT)\n", verb)
		return core.ExitUsage
	}
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		what := "a channel"
		if op == core.BoardReply {
			what = "a message id"
		}
		fmt.Fprintf(d.Err, "board %s needs %s first\n", verb, what)
		return core.ExitUsage
	}
	target, rest := rest[0], rest[1:]
	text := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		text, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("board "+verb, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var yes, dry, asJSON bool
	fs.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&dry, "dry-run", false, "print the change and the command; run nothing")
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.StringVar(&text, "text", text, "the message text (use it when the text starts with -)")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "board %s: unexpected argument %q (quote the text as one argument)\n", verb, fs.Arg(0))
		return core.ExitUsage
	}
	if asJSON && !yes && !dry {
		fmt.Fprintf(d.Err, "board %s: --json needs --yes or --dry-run (it cannot prompt)\n", verb)
		return core.ExitUsage
	}
	ch := core.BoardChange{Op: op, Text: text}
	if op == core.BoardPost {
		ch.Channel = target
	} else {
		ch.ID = target
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	w := core.ResolveProviders(d.Build(o).Detect(ctx)).BoardWriter
	if w == nil {
		fmt.Fprintf(d.Err, "board: not connected. %s\n", core.HowToConnectFor(core.DomainBoard))
		return core.ExitFail
	}
	plan, err := core.PlanBoardChange(w, ch)
	if err != nil {
		fmt.Fprintf(d.Err, "board %s: %v\n", verb, err)
		return core.ExitUsage
	}
	dctx, dcancel := context.WithTimeout(ctx, 2*core.DefaultExecTimeout)
	defer dcancel()
	return runWrite(d, writeFlow{label: "board " + verb, lines: plan.Lines, ctx: w.Describe(dctx, ch), argv: plan.Argv, yes: yes, dry: dry, asJSON: asJSON, prompt: "Post this to the board? [y/N] ",
		apply: func(au core.Audit) (any, string, error) {
			res, err := core.NewBoardApplier(w, au).Apply(ctx, plan.Confirm())
			return res, "done: posted #" + res.ID, err
		}})
}
