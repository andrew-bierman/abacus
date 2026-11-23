package graph

// This is the ONLY file in abacus that imports bv's code (github.com/Dicklesworthstone/
// beads_viewer, pinned to an exact tag in go.mod). Removing bv later means deleting this file
// and the NewBVAnalyzer call in internal/app; nothing else names a bv type.
//
// Only pkg/analysis and pkg/model are used: bv's loaders, exporters and UI are not, and nothing
// here reads or writes a file or runs a process.
//
// Where bv's definitions differ from what you might expect (each is covered by a test in
// parity_test.go, and listed in docs/tool-contract.md under "Graph engine"):
//
//   - CriticalPath is bv's Keystones: a ranked list of per-issue chain heights (the number of
//     issues in the longest chain that ends at the issue's dependents), not an ordered path.
//     This adapter returns exactly that list, as the subprocess adapter does, so the two agree.
//   - A dependency cycle makes bv skip the critical-path computation (no scores at all). The
//     report then carries CriticalPathNote, filled the way internal/bv fills it.
//   - Closed issues stay in the input: a closed blocker is what makes its dependents actionable.
//     bv's insight lists (Keystones, Bottlenecks) leave closed issues out of the ranking, as
//     `bv --robot-insights` does, but a closed issue's chain is not erased from the graph.
//   - "Actionable" is an issue with an open or in-progress status and no unfinished blocking
//     dependency; "blocked" counts issues held back by an unfinished dependency or a blocked
//     status. A custom status counts as neither actionable nor closed.
//   - The next pick is bv's triage score (PageRank, unblocks, priority and more), not bd's ready
//     ordering; the two disagree by design.

import (
	"context"
	"fmt"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/debug"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Section sizes, matching internal/bv so the two engines fill the report the same way.
const (
	maxPicks    = 3
	maxBlockers = 5
	maxRanked   = 5
)

// BVAnalyzer is the Analyzer backed by bv's analysis package, run in this process.
type BVAnalyzer struct{}

func NewBVAnalyzer() BVAnalyzer {
	quietBV()
	return BVAnalyzer{}
}

// quietBV switches off bv's debug logger. bv's pkg/debug turns it on in an init() when BV_DEBUG
// is set, which runs before main, so the variable cannot be cleared in time; this flips the
// logger off instead. Its output goes to stderr in this process and would corrupt the TUI.
// bv's metrics (BV_METRICS) only collect in memory and print nothing, so they need no guard.
// The analysis path in v0.27.0 does not log, so this is defence for a later bv bump.
func quietBV() { debug.SetEnabled(false) }

// BVDebugEnabled reports whether bv's debug logger is on (it is off after NewBVAnalyzer).
func BVDebugEnabled() bool { return debug.Enabled() }

// bvStatus maps an abacus status to bv's, explicitly. A status abacus has no name for passes
// through unchanged: bv keeps a custom state as neither closed nor claimable.
func bvStatus(s Status) model.Status {
	switch s {
	case StatusOpen:
		return model.StatusOpen
	case StatusInProgress:
		return model.StatusInProgress
	case StatusBlocked:
		return model.StatusBlocked
	case StatusDeferred:
		return model.StatusDeferred
	case StatusClosed:
		return model.StatusClosed
	}
	return model.Status(s)
}

// bvDepType maps a dependency type to bv's, explicitly. An unknown type becomes "related",
// which never blocks anything: a link abacus does not understand cannot create a false blocker.
func bvDepType(t DepType) model.DependencyType {
	switch t {
	case DepBlocks:
		return model.DepBlocks
	case DepConditionalBlocks:
		return model.DepConditionalBlocks
	case DepWaitsFor:
		return model.DepWaitsFor
	case DepParentChild:
		return model.DepParentChild
	case DepRelated:
		return model.DepRelated
	case DepDiscoveredFrom:
		return model.DepDiscoveredFrom
	}
	return model.DepRelated
}

// toBV maps abacus issues to bv's model. Each edge hangs on the dependent issue with
// DependsOnID naming the issue it waits for, which is how bv reads "blocks": the edge from a
// blocker a to its dependent b is b.Dependencies = [{IssueID: b, DependsOnID: a}].
func toBV(issues []Issue) []model.Issue {
	out := make([]model.Issue, len(issues))
	at := make(map[string]int, len(issues))
	for n, i := range issues {
		out[n] = model.Issue{ID: i.ID, Title: i.Title, Status: bvStatus(i.Status), Priority: i.Priority, IssueType: model.IssueType(i.Type), UpdatedAt: i.UpdatedAt}
		at[i.ID] = n
	}
	for _, e := range Edges(issues) {
		n := at[e.Dependent]
		out[n].Dependencies = append(out[n].Dependencies, &model.Dependency{IssueID: e.Dependent, DependsOnID: e.DependsOn, Type: bvDepType(e.Type)})
	}
	return out
}

// Analyze runs bv's analysis over issues. No issues is core.EmptyInsights() with bv never
// called. The analysis runs off the caller's goroutine so ctx can end the wait, and a panic
// inside bv (a pre-1.0 dependency) is an error, not a crash.
func (BVAnalyzer) Analyze(ctx context.Context, issues []Issue) (core.InsightsReport, error) {
	if len(issues) == 0 {
		return core.EmptyInsights(), nil
	}
	type result struct {
		r   core.InsightsReport
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- result{err: fmt.Errorf("graph analysis failed: %v", p)}
			}
		}()
		r, err := analyze(ctx, issues)
		done <- result{r, err}
	}()
	select {
	case res := <-done:
		return res.r, res.err
	case <-ctx.Done():
		return core.InsightsReport{}, ctx.Err()
	}
}

func analyze(ctx context.Context, issues []Issue) (core.InsightsReport, error) {
	quietBV()
	titles := make(map[string]string, len(issues))
	for _, i := range issues {
		titles[i.ID] = i.Title
	}
	mi := toBV(issues)

	an := analysis.NewAnalyzer(mi)
	stats := an.AnalyzeAsync(ctx)
	if err := stats.WaitForPhase2Context(ctx); err != nil {
		return core.InsightsReport{}, err
	}
	ins := an.GenerateScopedInsights(stats, maxRanked, false)
	plan := an.GetExecutionPlan()
	tri := analysis.ComputeTriageWithOptions(mi, analysis.TriageOptions{WaitForPhase2: true})

	r := core.InsightsReport{Header: core.Header{Source: core.SourceGraph}}
	r.TotalActionable, r.TotalBlocked = plan.TotalActionable, plan.TotalBlocked
	r.GraphNodes, r.GraphEdges = stats.NodeCount, stats.EdgeCount

	for _, t := range plan.Tracks {
		tr := core.Track{ID: core.Sanitize(t.TrackID)}
		for _, it := range t.Items {
			tr.Items = append(tr.Items, core.Sanitize(it.ID))
		}
		r.Tracks = append(r.Tracks, tr)
	}

	title := func(id, fromBV string) string {
		if t, ok := titles[id]; ok {
			return core.Sanitize(t)
		}
		return core.Sanitize(fromBV)
	}
	add := func(id, fromBV string, score float64, reasons []string, unblocks int) {
		cp := core.Pick{ID: core.Sanitize(id), Title: title(id, fromBV), Score: score, Unblocks: unblocks}
		for _, why := range reasons {
			cp.Reasons = append(cp.Reasons, core.Sanitize(why))
		}
		r.TopPicks = append(r.TopPicks, cp)
	}
	if tp := tri.QuickRef.TopPicks; len(tp) > 0 {
		for _, p := range tp[:min(len(tp), maxPicks)] {
			add(p.ID, p.Title, p.Score, p.Reasons, p.Unblocks)
		}
	} else { // as internal/bv does: no top picks, fall back to the ranked recommendations
		rec := tri.Recommendations
		for _, p := range rec[:min(len(rec), maxPicks)] {
			add(p.ID, p.Title, p.Score, p.Reasons, len(p.UnblocksIDs))
		}
	}
	for _, b := range tri.BlockersToClear {
		if len(r.Blockers) == maxBlockers {
			break
		}
		r.Blockers = append(r.Blockers, core.Pick{ID: core.Sanitize(b.ID), Title: title(b.ID, b.Title), Unblocks: b.UnblocksCount})
	}

	for _, k := range ins.Keystones {
		r.CriticalPath = append(r.CriticalPath, core.Ranked{ID: core.Sanitize(k.ID), Value: k.Value})
	}
	for _, b := range ins.Bottlenecks {
		r.Bottlenecks = append(r.Bottlenecks, core.Ranked{ID: core.Sanitize(b.ID), Value: b.Value})
	}
	for _, c := range ins.Cycles {
		cyc := make([]string, 0, len(c))
		for _, id := range c {
			cyc = append(cyc, core.Sanitize(id))
		}
		r.Cycles = append(r.Cycles, cyc)
	}

	if c := stats.Status().Critical; c.State == "skipped" {
		r.CriticalPathNote = core.Sanitize("critical path skipped: " + c.Reason)
	}
	return r, nil
}
