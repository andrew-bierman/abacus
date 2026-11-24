package cli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

const bulkUsage = "tasks bulk --ids ID,ID,... --op close|status|priority|label-add|label-remove|assign [--value V | --clear] [--yes] [--dry-run] [--json]"

// bulkOps maps the --op words to the task verbs a batch may use.
var bulkOps = map[string]core.TaskOp{
	"close": core.TaskClose, "status": core.TaskUpdate, "priority": core.TaskPriority,
	"label-add": core.TaskLabelAdd, "label-remove": core.TaskLabelRemove, "assign": core.TaskAssign,
}

// bulkItemJSON is one task of a planned batch in --dry-run --json.
type bulkItemJSON struct {
	ID     string   `json:"id"`
	Title  string   `json:"title,omitempty"`
	Change string   `json:"change"`
	Argv   []string `json:"argv"`
}

// runTaskBulk handles `tasks bulk`: one verb on up to 25 tasks. Every task is read first, the plan lists
// every task with its exact command, one confirm covers the batch, and each task is read again and
// guarded right before its own write, so a task that changed is skipped and reported, never written.
func runTaskBulk(rest []string, d Deps) int {
	fs := flag.NewFlagSet("tasks bulk", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var yes, dry, asJSON, clear bool
	var ids, op, value string
	fs.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&dry, "dry-run", false, "print the plan and every command; run nothing")
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.BoolVar(&clear, "clear", false, "with --op assign: unassign")
	fs.StringVar(&ids, "ids", "", fmt.Sprintf("comma-separated task ids (at most %d)", core.MaxBulk))
	fs.StringVar(&op, "op", "", "close, status, priority, label-add, label-remove or assign")
	fs.StringVar(&value, "value", "", "the status, priority, label or assignee")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	bad := func(format string, a ...any) int {
		fmt.Fprintf(d.Err, "tasks bulk: %s\nusage: %s\n", fmt.Sprintf(format, a...), bulkUsage)
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		return bad("unexpected argument %q (quote values that contain spaces)", fs.Arg(0))
	}
	if asJSON && !yes && !dry {
		return bad("--json needs --yes or --dry-run (it cannot prompt)")
	}
	taskOp, ok := bulkOps[op]
	if !ok {
		return bad("--op must be one of close, status, priority, label-add, label-remove, assign")
	}
	spec := core.BulkSpec{Op: taskOp}
	switch taskOp {
	case core.TaskUpdate:
		spec.Status = value
	case core.TaskPriority:
		n, err := strconv.Atoi(value)
		if err != nil {
			return bad("--value must be a priority 0-4")
		}
		spec.Priority = &n
	case core.TaskLabelAdd, core.TaskLabelRemove:
		spec.Label = value
	case core.TaskAssign:
		if clear == (value != "") {
			return bad("--op assign needs exactly one of --value NAME and --clear")
		}
		spec.Assignee = &value
	}
	if taskOp == core.TaskClose && value != "" {
		return bad("--op close takes no --value")
	}
	if clear && taskOp != core.TaskAssign {
		return bad("--clear is for --op assign")
	}
	if err := spec.Validate(); err != nil {
		return bad("%v", err)
	}
	var list []string
	for _, id := range strings.Split(ids, ",") {
		if id = strings.TrimSpace(id); id != "" {
			list = append(list, id)
		}
	}
	if err := core.CheckBulkSize(len(list)); err != nil {
		return bad("%v", err)
	}
	for _, id := range list {
		if !core.TaskIDRE.MatchString(id) {
			return bad("invalid issue id %q", id)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout) // planning only: the batch gets its own time after the confirm
	defer cancel()
	pr := core.ResolveProviders(d.Build(o).Detect(ctx))
	w, r := pr.Writer, pr.Reader
	if w == nil {
		fmt.Fprintf(d.Err, "tasks: not connected. %s\n", core.HowToConnectFor(core.DomainTasks))
		return core.ExitFail
	}
	if r == nil {
		fmt.Fprintln(d.Err, "tasks bulk: cannot read the tasks' current state, so they cannot be changed safely")
		return core.ExitFail
	}
	targets := make([]core.Task, 0, len(list))
	for _, id := range list {
		t, err := r.ReadTask(ctx, id)
		if err != nil {
			fmt.Fprintf(d.Err, "tasks bulk: could not read %s first: %s\n", core.Sanitize(id), core.Sanitize(err.Error()))
			return core.ExitFail
		}
		if t.ID != id { // the tracker resolved a prefix or alias: only whole ids go in a batch
			fmt.Fprintf(d.Err, "tasks bulk: %s is not a whole task id (it resolves to %s): use the full id\n", core.Sanitize(id), core.Sanitize(t.ID))
			return core.ExitUsage
		}
		targets = append(targets, t)
	}
	plan, err := core.PlanBulk(w, spec, targets)
	if err != nil {
		fmt.Fprintf(d.Err, "tasks bulk: %v\n", err)
		return core.ExitUsage
	}
	if dry && asJSON {
		items := make([]bulkItemJSON, len(plan.Items))
		for i, it := range plan.Items {
			items[i] = bulkItemJSON{ID: it.Change.ID, Title: it.Change.Before.Title, Change: core.BulkItemText(it.Change), Argv: it.Argv}
		}
		jsonOut(d.Out, struct {
			SchemaVersion int             `json:"schema_version"`
			DryRun        bool            `json:"dry_run"`
			Op            string          `json:"op"`
			Items         []bulkItemJSON  `json:"items"`
			Skipped       []core.BulkSkip `json:"skipped,omitempty"`
		}{1, true, op, items, plan.Skipped})
		return core.ExitOK
	}
	if !asJSON {
		fmt.Fprintf(d.Out, "%s %s\n", plan.Spec.Summary(), core.Plural(len(plan.Items), "task"))
		for _, it := range plan.Items {
			fmt.Fprintf(d.Out, "  %s\n    command: %s\n", core.Sanitize(core.BulkItemText(it.Change)), core.DisplayArgv(it.Argv))
		}
		for _, s := range plan.Skipped {
			fmt.Fprintf(d.Out, "  left out: %s (%s)\n", core.Sanitize(s.ID), core.Sanitize(s.Reason))
		}
	}
	if dry {
		return core.ExitOK
	}
	if !yes {
		fmt.Fprintf(d.Err, "Apply %s? [y/N] ", core.Plural(len(plan.Items), "change"))
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
	actx, acancel := context.WithTimeout(context.Background(), core.BulkTotalTimeout)
	defer acancel()
	rep, err := core.NewApplier(w, au).ApplyBulk(actx, plan.Confirm(), r)
	if err != nil {
		fmt.Fprintf(d.Err, "tasks bulk: %s\n", core.DescribeWriteError(err))
		return core.ExitFail
	}
	if asJSON {
		jsonOut(d.Out, rep)
	} else {
		for _, it := range rep.Items {
			line := fmt.Sprintf("  %s: %s", core.Sanitize(it.ID), it.Outcome)
			if it.Detail != "" {
				line += " (" + it.Detail + ")"
			}
			fmt.Fprintln(d.Out, line)
		}
		fmt.Fprintf(d.Out, "%s (batch %s)\n", rep.Summary(), rep.BatchID)
	}
	if !rep.AllApplied() {
		return core.ExitFail
	}
	return core.ExitOK
}
