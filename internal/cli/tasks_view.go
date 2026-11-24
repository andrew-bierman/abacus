package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// runTaskView handles `tasks list` and `tasks show ID`: read-only, from the same tracker
// snapshot and the same typed reports the TUI renders.
func runTaskView(verb string, rest []string, d Deps) int {
	id := ""
	if verb == "show" {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			fmt.Fprintln(d.Err, "tasks show needs a task id first")
			return core.ExitUsage
		}
		id, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("tasks "+verb, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var asJSON, reverse, tree, closed bool
	var q, sortKey, preset, group string
	jsonVar(d, fs, &asJSON, "print JSON")
	if verb == "board" {
		fs.StringVar(&q, "query", "", "narrow the cards, for example: label:ui !assignee:none (also -q)")
		fs.StringVar(&q, "q", "", "short for --query")
		fs.StringVar(&group, "group", taskview.GroupStatus, "group the columns by "+strings.Join(taskview.Groups, ", "))
		fs.BoolVar(&closed, "closed", false, "show the closed cards instead of folding them to a count")
	}
	if verb == "list" {
		fs.StringVar(&q, "query", "", "filter, for example: status:open label:ui !assignee:none (also -q)")
		fs.StringVar(&q, "q", "", "short for --query")
		fs.StringVar(&sortKey, "sort", "", "sort key: "+joinKeys())
		fs.BoolVar(&reverse, "reverse", false, "reverse the sort direction")
		fs.StringVar(&preset, "preset", "", "open (default), ready, blocked, closed or all")
		fs.BoolVar(&tree, "tree", false, "print epics and their children as a tree (a match keeps the parents above it)")
	}
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "tasks %s: unexpected argument %q (quote values that contain spaces)\n", verb, fs.Arg(0))
		return core.ExitUsage
	}
	opts := taskview.ListOptions{}
	bo := taskview.BoardOptions{ShowClosed: closed, Group: strings.ToLower(group)}
	if verb == "board" {
		if !slices.Contains(taskview.Groups, bo.Group) {
			fmt.Fprintf(d.Err, "tasks board: unknown grouping %q (%s)\n", group, strings.Join(taskview.Groups, ", "))
			return core.ExitUsage
		}
		pq, err := query.Parse(q)
		if err != nil {
			fmt.Fprintf(d.Err, "tasks board: bad query: %v\n", err)
			return core.ExitUsage
		}
		bo.Query = pq
	}
	if verb == "list" {
		pq, err := query.Parse(q)
		if err != nil {
			fmt.Fprintf(d.Err, "tasks list: bad query: %v\n", err)
			return core.ExitUsage
		}
		opts.Query = pq
		if sortKey != "" {
			k := taskview.SortKey(strings.ToLower(sortKey))
			if !k.Valid() {
				fmt.Fprintf(d.Err, "tasks list: unknown sort %q (%s)\n", sortKey, joinKeys())
				return core.ExitUsage
			}
			opts.Sort = taskview.Sort{Key: k, Desc: k.DefaultDesc()}
		} else {
			opts.Sort = taskview.DefaultSort()
		}
		if reverse {
			opts.Sort = opts.Sort.Flip()
		}
		if preset != "" {
			opts.Preset = taskview.Preset(strings.ToLower(preset))
			if !opts.Preset.Valid() {
				fmt.Fprintf(d.Err, "tasks list: unknown preset %q (open, ready, blocked, closed, all)\n", preset)
				return core.ExitUsage
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	src := core.ResolveProviders(d.Build(o).Detect(ctx)).Source
	if src == nil {
		return failf(d, core.CodeNotConnected, "tasks: not connected. %s\n", core.HowToConnectFor(core.DomainTasks))
	}
	snap, err := src.Snapshot(ctx)
	if err != nil {
		return failf(d, core.CodeFor(err), "tasks %s: %s\n", verb, describeReadError(err))
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	v := taskview.Build(snap, now)
	if verb == "list" && tree {
		rep := v.Tree(taskview.TreeOptions{ListOptions: opts})
		if asJSON {
			jsonOut(d.Out, rep)
		} else {
			renderTaskTree(d.Out, rep)
		}
		return core.ExitOK
	}
	if verb == "board" {
		rep := v.Board(bo)
		if asJSON {
			jsonOut(d.Out, rep)
		} else {
			renderKanban(d.Out, rep)
		}
		return core.ExitOK
	}
	if verb == "list" {
		rep := v.List(opts)
		if asJSON {
			jsonOut(d.Out, rep)
		} else {
			renderTaskList(d.Out, rep)
		}
		return core.ExitOK
	}
	rep, ok := v.Detail(id)
	if !ok {
		return failf(d, core.CodeNotFound, "tasks show: no task %q\n", core.Sanitize(id))
	}
	if asJSON {
		jsonOut(d.Out, rep)
	} else {
		renderTaskDetail(d.Out, rep)
	}
	return core.ExitOK
}

// renderKanban prints the board as text: one block per column, the cards under it.
func renderKanban(w io.Writer, r core.KanbanReport) {
	fmt.Fprintf(w, "Board  %d of %d tasks  (by %s", r.Shown, r.Total, r.Group)
	if r.Query != "" {
		fmt.Fprintf(w, ", query: %s", r.Query)
	}
	if r.ClosedHidden > 0 {
		fmt.Fprintf(w, ", %d closed folded (--closed shows them)", r.ClosedHidden)
	}
	fmt.Fprintln(w, ")")
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	for _, c := range r.Columns {
		fmt.Fprintf(w, "\n%s  %d", c.Title, c.Count)
		if c.P0P1 > 0 {
			fmt.Fprintf(w, "  (%d urgent)", c.P0P1)
		}
		if c.Blocked > 0 {
			fmt.Fprintf(w, "  (%d held)", c.Blocked)
		}
		fmt.Fprintln(w)
		for _, k := range c.Cards {
			held := ""
			if k.BlockedBy > 0 {
				held = fmt.Sprintf("  blocked by %d", k.BlockedBy)
			}
			fmt.Fprintf(w, "  %-12s P%d  %-9s %s%s\n", k.ID, k.Priority, dash(k.Assignee), k.Title, held)
		}
	}
}

func joinKeys() string {
	var ks []string
	for _, k := range taskview.SortKeys {
		ks = append(ks, string(k))
	}
	return strings.Join(ks, ", ")
}

func describeReadError(err error) string {
	var nc *core.NotConnectedError
	if errors.As(err, &nc) {
		return core.Sanitize(nc.Reason + " " + nc.HowToConnect)
	}
	if g, ok := core.AsNeedsSetup(err); ok {
		return core.Sanitize(g.CLILine())
	}
	return core.Sanitize(err.Error())
}

func renderTaskList(w io.Writer, r core.TaskListReport) {
	dir := "↑"
	if r.Descending {
		dir = "↓"
	}
	fmt.Fprintf(w, "Task list  %d of %d  (%s, sort: %s %s", r.Matched, r.Total, r.Preset, r.Sort, dir)
	if r.Query != "" {
		fmt.Fprintf(w, ", query: %s", r.Query)
	}
	fmt.Fprintln(w, ")")
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range r.Rows {
		fmt.Fprintf(tw, "%s\tP%d\t%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Priority, t.Status, dash(t.Type), dash(t.Assignee), dash(t.Age), deps(t), t.Title)
	}
	_ = tw.Flush()
}

// renderTaskTree prints the tree with its branch lines; a task shown only as the parent of a match is
// marked "(context)".
func renderTaskTree(w io.Writer, r core.TaskTreeReport) {
	dir := "↑"
	if r.Descending {
		dir = "↓"
	}
	fmt.Fprintf(w, "Task tree  %d of %d  (%s, sort: %s %s", r.Matched, r.Total, r.Preset, r.Sort, dir)
	if r.Query != "" {
		fmt.Fprintf(w, ", query: %s", r.Query)
	}
	fmt.Fprintln(w, ")")
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range r.Rows {
		title := t.Guide + t.Title
		if t.Context {
			title += "  (context)"
		}
		fmt.Fprintf(tw, "%s\tP%d\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Priority, t.Status, dash(t.Type), dash(t.Assignee), deps(t.TaskRow), title)
	}
	_ = tw.Flush()
	if r.Orphans > 0 {
		fmt.Fprintf(w, "%d tasks name a parent that is not in this tracker and are shown at the top level\n", r.Orphans)
	}
	if len(r.ParentCycles) > 0 {
		fmt.Fprintf(w, "parent links form a cycle at %s: shown at the top level\n", strings.Join(r.ParentCycles, ", "))
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func deps(t core.TaskRow) string {
	var p []string
	if t.BlockedBy > 0 {
		p = append(p, fmt.Sprintf("blocked by %d", t.BlockedBy))
	}
	if t.Blocks > 0 {
		p = append(p, fmt.Sprintf("blocks %d", t.Blocks))
	}
	if t.ChildTotal > 0 {
		p = append(p, fmt.Sprintf("%d/%d done", t.ChildDone, t.ChildTotal))
	}
	if len(p) == 0 {
		return "-"
	}
	return strings.Join(p, ", ")
}

func renderTaskDetail(w io.Writer, d core.TaskDetailReport) {
	r := d.Row
	fmt.Fprintf(w, "%s  %s\n%s\n", r.ID, dash(r.Type), r.Title)
	fmt.Fprintf(w, "  %s  P%d  assignee %s  age %s", r.Status, r.Priority, dash(r.Assignee), dash(r.Age))
	if r.Ready {
		fmt.Fprint(w, "  ready")
	}
	fmt.Fprintln(w)
	if len(r.Labels) > 0 {
		fmt.Fprintf(w, "  labels %s\n", strings.Join(r.Labels, ", "))
	}
	if d.Parent != nil {
		fmt.Fprintf(w, "  parent %s %s\n", d.Parent.ID, d.Parent.Title)
	}
	refs := func(name string, rs []core.TaskRef) {
		if len(rs) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s (%d)\n", name, len(rs))
		for _, x := range rs {
			fmt.Fprintf(w, "  %-12s %-11s %s\n", x.ID, x.Status, x.Title)
		}
	}
	refs("Blocked by", d.BlockedBy)
	refs("Blocks", d.Blocks)
	if len(d.Children) > 0 {
		refs(fmt.Sprintf("Children, %d of %d done", r.ChildDone, r.ChildTotal), d.Children)
	}
	for _, s := range []struct{ name, text string }{{"Description", d.Description}, {"Design", d.Design}, {"Notes", d.Notes}, {"Acceptance", d.Acceptance}} {
		if s.text != "" {
			fmt.Fprintf(w, "\n%s\n%s\n", s.name, indent(s.text))
		}
	}
	if d.CloseReason != "" {
		fmt.Fprintf(w, "\nClosed: %s\n", d.CloseReason)
	}
	if len(d.Comments) > 0 {
		fmt.Fprintf(w, "\nComments (%d)\n", len(d.Comments))
		for _, c := range d.Comments {
			fmt.Fprintf(w, "  %s %s\n%s\n", dash(c.Author), c.Age, indent(c.Text))
		}
	}
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}
