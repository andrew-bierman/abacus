package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// runTaskGraph handles `tasks graph [ID] [--whole] [--depth N] [--json]`: the same typed GraphView
// the TUI draws, laid out by core from one tracker snapshot. Read-only.
func runTaskGraph(rest []string, d Deps) int {
	id := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		id, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("tasks graph", flag.ContinueOnError)
	fs.SetOutput(d.Err)
	var o Options
	var asJSON, whole bool
	var depth int
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.BoolVar(&whole, "whole", false, "every linked task in topological layers, not one task's neighborhood")
	fs.IntVar(&depth, "depth", 2, "neighborhood depth, 1 to 6")
	if fs.Parse(rest) != nil {
		return core.ExitUsage
	}
	if fs.NArg() > 0 || depth < 1 || depth > 6 || (whole && id != "") {
		fmt.Fprintln(d.Err, "usage: abacus tasks graph [ID | --whole] [--depth 1-6] [--json]")
		return core.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	prov := core.ResolveProviders(d.Build(o).Detect(ctx))
	src := prov.Source
	if src == nil {
		fmt.Fprintf(d.Err, "tasks: not connected. %s\n", core.HowToConnectFor(core.DomainTasks))
		return core.ExitFail
	}
	snap, err := src.Snapshot(ctx)
	if err != nil {
		fmt.Fprintf(d.Err, "tasks graph: %s\n", describeReadError(err))
		return core.ExitFail
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	// the same engine marks the TUI draws, when the engine is connected and read cleanly
	var ins *core.InsightsReport
	if prov.Insights != nil {
		if rep := core.ReadInsights(ctx, prov.Insights, prov.InsightsWhy); rep.State == core.Connected && !rep.Empty {
			ins = &rep
		}
	}
	gv := taskview.NewGraphIndex(taskview.Build(snap, now)).View(taskview.GraphOptions{Focus: id, Depth: depth, Whole: whole, Insights: ins})
	if id != "" && gv.Focus == "" {
		fmt.Fprintf(d.Err, "tasks graph: no task %q\n", core.Sanitize(id))
		return core.ExitFail
	}
	if asJSON {
		jsonOut(d.Out, gv)
	} else {
		renderTaskGraph(d.Out, gv)
	}
	return core.ExitOK
}

func renderTaskGraph(w io.Writer, g core.GraphView) {
	byID := map[string]core.GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	for _, r := range g.Outline { // outline rows carry their nodes: the boxes' Nodes list is shorter
		if r.Node != nil {
			byID[r.ID] = *r.Node
		}
	}
	line := func(n core.GraphNode) string {
		f := ""
		switch {
		case n.InCycle:
			f = " [cycle]"
		case n.OnChain:
			f = " [chain]"
		}
		if n.Bottleneck {
			f += " [bottleneck]"
		}
		return fmt.Sprintf("%-12s %-11s %s%s", n.ID, n.Status, n.Title, f)
	}
	if g.Mode == core.GraphWhole {
		fmt.Fprintf(w, "Dependency graph  %d linked, %d without links, %d groups\n", g.Total-g.Isolated, g.Isolated, g.Components)
		for _, l := range g.Layers {
			fmt.Fprintf(w, "\nlayer %d (%d)\n", l.Layer, len(l.Nodes)+l.More)
			for _, id := range l.Nodes {
				fmt.Fprintf(w, "  %s\n", line(byID[id]))
			}
			if l.More > 0 {
				fmt.Fprintf(w, "  +%d more\n", l.More)
			}
		}
	} else {
		if g.Focus == "" {
			fmt.Fprintf(w, "Dependency graph: %s\n", g.Reason)
			return
		}
		fmt.Fprintf(w, "Dependency graph around %s (depth %d)\n", g.Focus, g.Depth)
		for _, r := range g.Outline {
			ind := strings.Repeat("  ", max(r.Depth, 1))
			switch {
			case r.Side == core.OutlineFocus:
				fmt.Fprintf(w, "\n= %s\n\n", line(byID[r.ID]))
			case r.ID == "":
				fmt.Fprintf(w, "%s+%d more\n", ind, r.More)
			case r.Repeat:
				fmt.Fprintf(w, "%s%s (above)\n", ind, r.ID)
			default:
				arrow := "blocked by"
				if r.Side == core.OutlineDown {
					arrow = "blocks"
				}
				fmt.Fprintf(w, "%s%s %s\n", ind, arrow, line(byID[r.ID]))
			}
		}
	}
	if len(g.Chain) > 1 {
		fmt.Fprintf(w, "\nlongest chain (%d): %s\n", len(g.Chain), strings.Join(g.Chain, " -> "))
	}
	for _, c := range g.Cycles {
		fmt.Fprintf(w, "cycle: %s -> %s\n", strings.Join(c, " -> "), c[0])
	}
	for _, id := range g.SelfDeps {
		fmt.Fprintf(w, "depends on itself: %s\n", id)
	}
	if g.MarksSource == "" {
		fmt.Fprintln(w, "bottleneck marks need the graph engine: not shown here")
	}
}
