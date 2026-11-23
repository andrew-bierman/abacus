package graph_test

// Parity: the library engine and the bv subprocess adapter (internal/bv) must agree behind
// core.InsightsProvider. For each scenario the library analyzer runs on a tracker export under
// testdata, and the subprocess adapter's own decoder runs on the matching `bv --robot-*` JSON,
// served by a fake runner (bv is never run).
//
//   - fx_cyclic and fx_acyclic: the robot JSON is the output recorded from bv v0.27.0 on the
//     seven invented issues described in internal/bv/testdata/README.md; the exports are those
//     seven issues written as tracker JSONL.
//   - chain, diamond, cycle, closed, big200: the robot JSON under testdata/robot is HAND-WRITTEN
//     in bv's documented shape, with values worked out from the graph (and, for the first four,
//     the probe table in docs/plans/2026-11-22-graph-core-assessment.md). It was not captured
//     from a bv run.
//
// What agrees: actionable and blocked counts, graph size, cycles, the critical-path scores, the
// critical-path note, the first top pick and the first blocker.
//
// Where bv's own definitions differ from what a reader may expect, and what this adapter does
// about each (also in docs/tool-contract.md, "Graph engine"):
//
//  1. Critical path is a per-issue height score (the length of the longest chain an issue starts),
//     not an ordered path. Both engines report that ranked list as CriticalPath. The adapter does
//     not derive an ordered path: a diamond shows both of its middle issues with the same score.
//  2. A dependency cycle makes bv skip the computation: no scores at all, even for issues far
//     from the cycle. Both engines then leave CriticalPath empty and fill CriticalPathNote.
//  3. A closed issue is a satisfied blocker (its dependents become actionable) but is left out of
//     the ranked lists, as `bv --robot-insights` leaves it out: it does not raise its dependents'
//     scores. bv's raw, unscoped critical-path scores do count closed issues (the assessment's
//     probe saw a closed issue score 3); the adapter uses the scoped insights the CLI uses, so
//     it matches the subprocess, not the raw probe.
//  4. The next pick is bv's triage score (PageRank, unblocks, priority, staleness and more), not
//     bd's ready ordering, so scores differ from a recorded run by the fields the export does
//     not carry (labels, estimates, created_at) and can differ from `bd ready`. Parity is on
//     which issue is picked, never on the score value.
//  5. "Blocked" counts issues held back by an unfinished blocker or a blocked status; deferred
//     and custom-status issues are in neither count.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/bv"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/graph"
)

type robotRun map[string]string

func (r robotRun) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	out, ok := r[strings.Join(args, " ")]
	if !ok {
		return nil, -1, fmt.Errorf("unexpected argv %v", args)
	}
	return []byte(out), 0, nil
}

func readFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// subprocessReport is what internal/bv reports for the given robot documents.
func subprocessReport(t *testing.T, triage, plan, insights, graphDoc string) core.InsightsReport {
	t.Helper()
	m := bv.New(bv.Options{Runner: robotRun{
		"--robot-triage --format json": triage, "--robot-plan --format json": plan,
		"--robot-insights --format json": insights, "--robot-graph --format json": graphDoc,
	}, LookPath: func(string) (string, error) { return "/fake/bv", nil }})
	r, err := m.Insights(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func recorded(t *testing.T, insights string) core.InsightsReport {
	d := filepath.Join("..", "bv", "testdata")
	return subprocessReport(t, readFile(t, d, "triage.json"), readFile(t, d, "plan.json"), readFile(t, d, insights+".json"), readFile(t, d, "graph.json"))
}

func handWritten(t *testing.T, name string) core.InsightsReport {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, "testdata", "robot", name+".json")), &doc); err != nil {
		t.Fatal(err)
	}
	return subprocessReport(t, string(doc["triage"]), string(doc["plan"]), string(doc["insights"]), string(doc["graph"]))
}

// canon rotates a cycle to start at its smallest id and drops the repeated closing id, so the
// same loop found from a different starting point compares equal.
func canon(c []string) string {
	if len(c) > 1 && c[0] == c[len(c)-1] {
		c = c[:len(c)-1]
	}
	best := ""
	for i := range c {
		rot := strings.Join(append(slices.Clone(c[i:]), c[:i]...), ">")
		if best == "" || rot < best {
			best = rot
		}
	}
	return best
}

func cycles(r core.InsightsReport) []string {
	var out []string
	for _, c := range r.Cycles {
		out = append(out, canon(c))
	}
	slices.Sort(out)
	return out
}

func values(rs []core.Ranked) []float64 {
	var out []float64
	for _, r := range rs {
		out = append(out, r.Value)
	}
	return out
}

func assertParity(t *testing.T, lib, sub core.InsightsReport) {
	t.Helper()
	if lib.TotalActionable != sub.TotalActionable || lib.TotalBlocked != sub.TotalBlocked {
		t.Errorf("actionable/blocked: library %d/%d, subprocess %d/%d", lib.TotalActionable, lib.TotalBlocked, sub.TotalActionable, sub.TotalBlocked)
	}
	if lib.GraphNodes != sub.GraphNodes || lib.GraphEdges != sub.GraphEdges {
		t.Errorf("graph size: library %d/%d, subprocess %d/%d", lib.GraphNodes, lib.GraphEdges, sub.GraphNodes, sub.GraphEdges)
	}
	if !slices.Equal(cycles(lib), cycles(sub)) {
		t.Errorf("cycles: library %v, subprocess %v", cycles(lib), cycles(sub))
	}
	if !slices.Equal(values(lib.CriticalPath), values(sub.CriticalPath)) {
		t.Errorf("critical path scores: library %v, subprocess %v", values(lib.CriticalPath), values(sub.CriticalPath))
	}
	if lib.CriticalPathNote != sub.CriticalPathNote {
		t.Errorf("critical path note: library %q, subprocess %q", lib.CriticalPathNote, sub.CriticalPathNote)
	}
	if len(lib.TopPicks) == 0 || len(sub.TopPicks) == 0 || lib.TopPicks[0].ID != sub.TopPicks[0].ID || lib.TopPicks[0].Unblocks != sub.TopPicks[0].Unblocks {
		t.Errorf("top pick: library %+v, subprocess %+v", lib.TopPicks, sub.TopPicks)
	}
	if len(lib.Blockers) == 0 || len(sub.Blockers) == 0 || lib.Blockers[0].ID != sub.Blockers[0].ID {
		t.Errorf("first blocker: library %+v, subprocess %+v", lib.Blockers, sub.Blockers)
	}
	if len(lib.Tracks) != len(sub.Tracks) {
		t.Errorf("tracks: library %d, subprocess %d", len(lib.Tracks), len(sub.Tracks))
	}
	if lib.Summarize() != sub.Summarize() {
		t.Errorf("summary: library %q, subprocess %q", lib.Summarize(), sub.Summarize())
	}
	if lib.Source != sub.Source {
		t.Errorf("source: library %q, subprocess %q", lib.Source, sub.Source)
	}
}

func TestParityWithTheSubprocessAdapter(t *testing.T) {
	for _, c := range []struct {
		name string
		sub  func(*testing.T) core.InsightsReport
		// facts that are true of the scenario itself, so a wrong pair of fixtures cannot pass
		actionable, blocked, cycles int
		pick                        string
	}{
		{"fx_cyclic", func(t *testing.T) core.InsightsReport { return recorded(t, "insights") }, 2, 4, 1, "fx-1"},
		{"chain", func(t *testing.T) core.InsightsReport { return handWritten(t, "chain") }, 1, 2, 0, "ch-1"},
		{"diamond", func(t *testing.T) core.InsightsReport { return handWritten(t, "diamond") }, 1, 3, 0, "dm-1"},
		{"cycle", func(t *testing.T) core.InsightsReport { return handWritten(t, "cycle") }, 1, 2, 1, "cy-3"},
		{"closed", func(t *testing.T) core.InsightsReport { return handWritten(t, "closed") }, 1, 1, 0, "cl-2"},
		{"big200", func(t *testing.T) core.InsightsReport { return handWritten(t, "big200") }, 20, 177, 0, "bg-00-0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			issues := load(t, c.name)
			start := time.Now()
			lib := analyze(t, issues)
			t.Logf("%d issues analysed in %s", len(issues), time.Since(start).Round(time.Millisecond))
			sub := c.sub(t)
			assertParity(t, lib, sub)
			if sub.TotalActionable != c.actionable || sub.TotalBlocked != c.blocked || len(sub.Cycles) != c.cycles || sub.TopPicks[0].ID != c.pick {
				t.Fatalf("the subprocess fixture does not say what the scenario says: %+v", sub)
			}
			if lib.TotalActionable != c.actionable || lib.TotalBlocked != c.blocked || len(lib.Cycles) != c.cycles || lib.TopPicks[0].ID != c.pick {
				t.Fatalf("the library does not say what the scenario says: %+v", lib)
			}
		})
	}
}

// The recorded insights_acyclic.json is the insights read only (the project without the cycle),
// so it is compared on what that read carries: the critical path, the bottlenecks and no cycle.
func TestParityRecordedAcyclicInsights(t *testing.T) {
	sub := recorded(t, "insights_acyclic")
	lib := analyze(t, load(t, "fx_acyclic"))
	if len(lib.Cycles) != 0 || lib.CriticalPathNote != "" || sub.CriticalPathNote != "" {
		t.Fatalf("library %+v, subprocess %+v", lib, sub)
	}
	if fmt.Sprint(lib.CriticalPath) != fmt.Sprint(sub.CriticalPath) || fmt.Sprint(lib.Bottlenecks) != fmt.Sprint(sub.Bottlenecks) {
		t.Fatalf("critical path: library %v, subprocess %v; bottlenecks: library %v, subprocess %v", lib.CriticalPath, sub.CriticalPath, lib.Bottlenecks, sub.Bottlenecks)
	}
	if len(lib.CriticalPath) != 4 || lib.CriticalPath[0].ID != "fx-1" || lib.CriticalPath[0].Value != 3 {
		t.Fatalf("%v", lib.CriticalPath)
	}
}

// An empty tracker is the same state either way: core's own empty report, and bv is never asked.
func TestParityEmptyTracker(t *testing.T) {
	lib, err := graph.NewModule(graph.Options{Source: graph.StaticSource(load(t, "empty")), Analyzer: graph.NewBVAnalyzer()}).Insights(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := core.EmptyInsights()
	if lib.Empty != want.Empty || lib.Summary != want.Summary || lib.State != want.State || lib.Title != want.Title || lib.TotalActionable != 0 || len(lib.CriticalPath) != 0 {
		t.Fatalf("library %+v, core's empty report %+v", lib, want)
	}
	// core.ReadDomain makes the same call before it ever reaches a provider (readInsightsFor).
	m := graph.NewModule(graph.Options{Source: graph.StaticSource(nil), Analyzer: graph.NewBVAnalyzer()})
	rep := core.ReadInsights(context.Background(), m, "")
	if rep.State != core.Connected || !rep.Empty {
		t.Fatalf("%+v", rep)
	}
}

// Each documented difference, pinned: if a bv upgrade changes one, this fails and the doc is
// reviewed with it.
func TestDocumentedDifferences(t *testing.T) {
	t.Run("critical path is a height score per issue, not an ordered path", func(t *testing.T) {
		r := analyze(t, load(t, "diamond"))
		got := map[string]float64{}
		for _, k := range r.CriticalPath {
			got[k.ID] = k.Value
		}
		want := map[string]float64{"dm-1": 3, "dm-2": 2, "dm-3": 2, "dm-4": 1}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%v, want %v (both middle issues score alike; a path would name one)", got, want)
		}
	})
	t.Run("a cycle skips the critical path for the whole graph", func(t *testing.T) {
		// cy-3 is far from the cycle and still has no score.
		r := analyze(t, load(t, "cycle"))
		if len(r.CriticalPath) != 0 || r.CriticalPathNote == "" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a closed issue satisfies its dependents but does not raise their scores", func(t *testing.T) {
		r := analyze(t, load(t, "closed"))
		if r.TotalActionable != 1 || r.TopPicks[0].ID != "cl-2" {
			t.Fatalf("the open issue above the closed one is the pick: %+v", r.TopPicks)
		}
		for _, k := range r.CriticalPath {
			if k.ID == "cl-1" {
				t.Fatalf("closed issues are not ranked: %+v", r.CriticalPath)
			}
		}
		if len(r.CriticalPath) != 2 || r.CriticalPath[0].Value != 2 || r.CriticalPath[1].Value != 1 {
			t.Fatalf("scores count only the live chain: %+v", r.CriticalPath)
		}
	})
	t.Run("the next pick is the triage score, not priority order", func(t *testing.T) {
		// z-1 is the higher priority (P0) and blocks nothing; y-1 is P3 and holds up three issues.
		// bd's ready ordering would put z-1 first; bv's score picks the issue that unblocks work.
		issues := []graph.Issue{
			{ID: "z-1", Title: "urgent but isolated", Status: graph.StatusOpen, Priority: 0, Type: "task"},
			{ID: "y-1", Title: "unblocks three", Status: graph.StatusOpen, Priority: 3, Type: "task"},
			is("y-2", graph.StatusOpen, dep("y-1")), is("y-3", graph.StatusOpen, dep("y-1")), is("y-4", graph.StatusOpen, dep("y-1")),
		}
		r := analyze(t, issues)
		if r.TopPicks[0].ID != "y-1" || r.TopPicks[0].Unblocks != 3 {
			t.Fatalf("%+v", r.TopPicks)
		}
	})
	t.Run("deferred and custom statuses are in neither count", func(t *testing.T) {
		r := analyze(t, []graph.Issue{is("a", graph.StatusDeferred), is("b", "review"), is("c", graph.StatusOpen)})
		if r.TotalActionable != 1 || r.TotalBlocked != 0 {
			t.Fatalf("%+v", r)
		}
	})
}
