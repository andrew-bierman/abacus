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

// runTaskHistory handles `tasks history ID [--limit N] [--json]`: a read of one task's event log,
// newest first, the same typed report the TUI detail shows.
func runTaskHistory(rest []string, d Deps) int {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		fmt.Fprintln(d.Err, "tasks history needs a task id first")
		return core.ExitUsage
	}
	id, rest := rest[0], rest[1:]
	fs := flag.NewFlagSet("tasks history", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var asJSON bool
	limit := core.HistoryDefaultLimit
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.IntVar(&limit, "limit", core.HistoryDefaultLimit, fmt.Sprintf("most events to show (1-%d)", core.HistoryMaxLimit))
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.Err, "tasks history: unexpected argument %q\n", fs.Arg(0))
		return core.ExitUsage
	}
	if !core.TaskIDRE.MatchString(id) {
		fmt.Fprintf(d.Err, "tasks history: %q is not a task id\n", core.Sanitize(id))
		return core.ExitUsage
	}
	if limit < 1 || limit > core.HistoryMaxLimit {
		fmt.Fprintf(d.Err, "tasks history: --limit must be 1 to %d\n", core.HistoryMaxLimit)
		return core.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	src := core.ResolveProviders(d.Build(o).Detect(ctx)).History
	if src == nil {
		return failf(d, core.CodeNotConnected, "tasks: not connected. %s\n", core.HowToConnectFor(core.DomainTasks))
	}
	rep, err := src.History(ctx, id, limit)
	if err != nil {
		return failf(d, core.CodeFor(err), "tasks history: %s\n", describeReadError(err))
	}
	if asJSON {
		jsonOut(d.Out, rep)
	} else {
		renderTaskHistory(d.Out, rep)
	}
	return core.ExitOK
}

func renderTaskHistory(w io.Writer, r core.TaskHistoryReport) {
	if len(r.Events) == 0 {
		fmt.Fprintf(w, "%s: no history here\n", r.ID)
		return
	}
	fmt.Fprintf(w, "%s: %d events, newest first\n", r.ID, len(r.Events))
	for _, e := range r.Events {
		at := "-"
		if !e.At.IsZero() {
			at = e.At.Format(time.DateTime) + "Z"
		}
		line := fmt.Sprintf("%s  %-16s %s", at, e.Kind, dash(e.Actor))
		if e.Detail != "" {
			line += "  " + e.Detail
		}
		fmt.Fprintln(w, line)
	}
	if r.Truncated {
		fmt.Fprintln(w, "(more events exist: raise --limit)")
	}
}
