package cli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// editVerbs are the Tasks edits beyond create, update and close: label, assign, priority, dep, comment.
var editVerbs = map[string]string{
	"label":    "tasks label add|remove ID LABEL",
	"assign":   "tasks assign ID NAME (or --clear to unassign)",
	"priority": "tasks priority ID 0-4",
	"dep":      "tasks dep add|remove ID --blocked-by OTHER (or --blocks OTHER)",
	"comment":  "tasks comment ID TEXT (or --text TEXT)",
}

// runTaskEdit handles `tasks label|assign|priority|dep|comment ...`. Like create, update and close it
// reads the task first, plans the exact command, shows it, asks (unless --yes), re-reads the task right
// before the write and goes through the audited gate; --dry-run runs nothing.
func runTaskEdit(verb string, rest []string, d Deps) int {
	usage := editVerbs[verb]
	// positional words first (the ones that are not flags), then flags, like the other write verbs
	var pos []string
	for len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		pos, rest = append(pos, rest[0]), rest[1:]
	}
	fs := flag.NewFlagSet("tasks "+verb, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var yes, dry, asJSON, unassign bool
	var blocks, blockedBy, text string
	fs.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&dry, "dry-run", false, "print the change and the command; run nothing")
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	switch verb {
	case "assign":
		fs.BoolVar(&unassign, "clear", false, "unassign the task")
	case "dep":
		fs.StringVar(&blocks, "blocks", "", "the task that ID holds back (ID blocks OTHER)")
		fs.StringVar(&blockedBy, "blocked-by", "", "the task that holds ID back (ID waits for OTHER)")
	case "comment":
		fs.StringVar(&text, "text", "", "the comment (use it when the text starts with -)")
	}
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "tasks %s: unexpected argument %q (quote values that contain spaces)\n", verb, fs.Arg(0))
		return core.ExitUsage
	}
	bad := func(format string, a ...any) int {
		fmt.Fprintf(d.Err, "tasks %s: %s\nusage: %s\n", verb, fmt.Sprintf(format, a...), usage)
		return core.ExitUsage
	}
	if asJSON && !yes && !dry {
		fmt.Fprintf(d.Err, "tasks %s: --json needs --yes or --dry-run (it cannot prompt)\n", verb)
		return core.ExitUsage
	}
	ch := core.TaskChange{}
	add := true
	switch verb {
	case "label":
		if len(pos) != 3 || (pos[0] != "add" && pos[0] != "remove") {
			return bad("needs add or remove, a task id and a label")
		}
		ch.Op, ch.ID, ch.Label = core.TaskLabelAdd, pos[1], pos[2]
		if pos[0] == "remove" {
			ch.Op = core.TaskLabelRemove
		}
	case "assign":
		if len(pos) != 2 && !(len(pos) == 1 && unassign) {
			return bad("needs a task id and a name, or a task id and --clear")
		}
		if unassign && len(pos) == 2 {
			return bad("--clear takes no name")
		}
		ch.Op, ch.ID = core.TaskAssign, pos[0]
		who := ""
		if len(pos) == 2 {
			who = pos[1]
		}
		ch.Assignee = &who
		if len(pos) == 2 && strings.TrimSpace(who) == "" {
			return bad("the name is empty (use --clear to unassign)")
		}
	case "priority":
		if len(pos) != 2 {
			return bad("needs a task id and a priority 0-4")
		}
		n, err := strconv.Atoi(pos[1])
		if err != nil {
			return bad("invalid priority %q", pos[1])
		}
		ch.Op, ch.ID, ch.Priority = core.TaskPriority, pos[0], &n
	case "dep":
		if len(pos) != 2 || (pos[0] != "add" && pos[0] != "remove") {
			return bad("needs add or remove and a task id")
		}
		if (blocks == "") == (blockedBy == "") {
			return bad("needs exactly one of --blocks and --blocked-by")
		}
		add = pos[0] == "add"
		ch.ID = pos[1]
	case "comment":
		switch {
		case len(pos) == 2 && text == "":
			text = pos[1]
		case len(pos) == 1 && text != "":
		default:
			return bad("needs a task id and the text (quote it as one argument)")
		}
		ch.Op, ch.ID, ch.Text = core.TaskAddComment, pos[0], text
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	entries := d.Build(o).Detect(ctx)
	pr := core.ResolveProviders(entries)
	w, r := pr.Writer, pr.Reader
	if w == nil {
		fmt.Fprintf(d.Err, "tasks: not connected. %s\n", core.HowToConnectFor(core.DomainTasks))
		return core.ExitFail
	}
	if r == nil {
		fmt.Fprintf(d.Err, "tasks %s: cannot read the task's current state, so it cannot be changed safely\n", verb)
		return core.ExitFail
	}
	if !core.TaskIDRE.MatchString(ch.ID) {
		return bad("invalid issue id %q", ch.ID)
	}
	cur, err := r.ReadTask(ctx, ch.ID)
	if err != nil {
		fmt.Fprintf(d.Err, "tasks %s: could not read %s first: %s\n", verb, core.Sanitize(ch.ID), core.Sanitize(err.Error()))
		return core.ExitFail
	}
	ch.Before, ch.Expect = cur, cur.Status
	if ch.Op == core.TaskAssign {
		ch.IfAssignee = cur.Assignee
	}
	if verb == "dep" {
		src := pr.Source
		if src == nil {
			fmt.Fprintln(d.Err, "tasks dep: cannot read the whole tracker, so the dependency cannot be checked")
			return core.ExitFail
		}
		snap, err := src.Snapshot(ctx)
		if err != nil {
			fmt.Fprintf(d.Err, "tasks dep: could not read the tracker: %s\n", core.Sanitize(err.Error()))
			return core.ExitFail
		}
		now := time.Now()
		if d.Now != nil {
			now = d.Now()
		}
		held, blocker := ch.ID, blocks
		if blockedBy != "" {
			blocker = blockedBy
		} else { // ID blocks OTHER: OTHER is the held task
			held, blocker = blocks, ch.ID
		}
		if !core.TaskIDRE.MatchString(held) || !core.TaskIDRE.MatchString(blocker) {
			return bad("invalid issue id")
		}
		ch, err = taskview.Build(snap, now).PlanDep(add, held, blocker)
		if err != nil {
			fmt.Fprintf(d.Err, "tasks dep: %v\n", err)
			return core.ExitUsage
		}
	}
	plan, err := core.PlanTaskChange(w, ch)
	if err != nil {
		fmt.Fprintf(d.Err, "tasks %s: %v\n", verb, err)
		return core.ExitUsage
	}
	return runWrite(d, writeFlow{label: "tasks " + verb, lines: plan.Lines, argv: plan.Argv, yes: yes, dry: dry, asJSON: asJSON, prompt: "Apply this change? [y/N] ",
		apply: func(au core.Audit) (any, string, error) {
			// the task is read again after the confirm: a change by someone else in between is refused, and
			// nothing is written or audited (an aborted plan never reaches the gate)
			if err := core.Fresh(ctx, r, plan.Change); err != nil {
				return core.TaskResult{}, "", err
			}
			res, err := core.NewApplier(w, au).Apply(ctx, plan.Confirm())
			return res, doneLine(plan.Change, res), err
		}})
}

func doneLine(ch core.TaskChange, res core.TaskResult) string {
	switch ch.Op {
	case core.TaskDepAdd:
		return fmt.Sprintf("done: %s now waits for %s", res.ID, ch.Blocker)
	case core.TaskDepRemove:
		return fmt.Sprintf("done: %s no longer waits for %s", res.ID, ch.Blocker)
	case core.TaskAddComment:
		return "done: comment added to " + res.ID
	}
	return fmt.Sprintf("done: %s %s", res.ID, res.Title)
}
