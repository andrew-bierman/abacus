package graph_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/graph"
)

func analyze(t *testing.T, issues []graph.Issue) core.InsightsReport {
	t.Helper()
	r, err := graph.NewBVAnalyzer().Analyze(context.Background(), issues)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func is(id string, st graph.Status, deps ...graph.Dependency) graph.Issue {
	return graph.Issue{ID: id, Title: "T " + id, Status: st, Priority: 2, Type: "task", DependsOn: deps}
}

func dep(id string) graph.Dependency { return graph.Dependency{ID: id, Type: graph.DepBlocks} }

// Statuses are mapped one by one: the counts below are what each mapping must produce, so a
// swapped mapping (closed read as open, in_progress as blocked, ...) changes a number here.
func TestStatusMapping(t *testing.T) {
	for _, c := range []struct {
		name                string
		issues              []graph.Issue
		actionable, blocked int
	}{
		{"a closed blocker is satisfied", []graph.Issue{is("a", graph.StatusClosed), is("b", graph.StatusOpen, dep("a"))}, 1, 0},
		{"an open blocker holds its dependent", []graph.Issue{is("a", graph.StatusOpen), is("b", graph.StatusOpen, dep("a"))}, 1, 1},
		{"an in-progress blocker is not finished", []graph.Issue{is("a", graph.StatusInProgress), is("b", graph.StatusOpen, dep("a"))}, 1, 1},
		{"in progress is actionable", []graph.Issue{is("a", graph.StatusInProgress)}, 1, 0},
		{"a blocked status is blocked", []graph.Issue{is("a", graph.StatusBlocked)}, 0, 1},
		{"a deferred issue is neither actionable nor blocked", []graph.Issue{is("a", graph.StatusDeferred)}, 0, 0},
		{"a custom status is parked: not actionable", []graph.Issue{is("a", "review")}, 0, 0},
		{"a custom-status blocker is not closed", []graph.Issue{is("a", "review"), is("b", graph.StatusOpen, dep("a"))}, 0, 1},
		{"a deferred blocker is not closed", []graph.Issue{is("a", graph.StatusDeferred), is("b", graph.StatusOpen, dep("a"))}, 0, 1},
		{"a tombstone (parsed) is closed", append(parse(t, `{"id":"a","status":"tombstone"}`), is("b", graph.StatusOpen, dep("a"))), 1, 0},
		{"a blocked-status blocker still holds", []graph.Issue{is("a", graph.StatusBlocked), is("b", graph.StatusOpen, dep("a"))}, 0, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := analyze(t, c.issues)
			if r.TotalActionable != c.actionable || r.TotalBlocked != c.blocked {
				t.Errorf("actionable %d blocked %d, want %d and %d", r.TotalActionable, r.TotalBlocked, c.actionable, c.blocked)
			}
		})
	}
}

// Only a blocking dependency holds an issue back. A link the tracker records for context, or a
// type abacus does not know, must never create a blocker.
func TestDependencyTypes(t *testing.T) {
	for typ, blocks := range map[graph.DepType]bool{
		graph.DepBlocks: true, graph.DepRelated: false, graph.DepDiscoveredFrom: false, "somethingnew": false,
	} {
		issues := []graph.Issue{is("a", graph.StatusOpen), is("b", graph.StatusOpen, graph.Dependency{ID: "a", Type: typ})}
		r := analyze(t, issues)
		wantActionable, wantBlocked := 2, 0
		if blocks {
			wantActionable, wantBlocked = 1, 1
		}
		if r.TotalActionable != wantActionable || r.TotalBlocked != wantBlocked {
			t.Errorf("%s: actionable %d blocked %d, want %d and %d", typ, r.TotalActionable, r.TotalBlocked, wantActionable, wantBlocked)
		}
	}
}

// "a blocks b" means b depends on a: a is the one to do first, whichever way it is written.
func TestDependencyDirection(t *testing.T) {
	for name, issues := range map[string][]graph.Issue{
		"depends on": {is("a", graph.StatusOpen), is("b", graph.StatusOpen, dep("a"))},
		"blocks":     {{ID: "a", Title: "T a", Status: graph.StatusOpen, Priority: 2, Blocks: []string{"b"}}, is("b", graph.StatusOpen)},
	} {
		t.Run(name, func(t *testing.T) {
			r := analyze(t, issues)
			if r.TotalActionable != 1 || r.TotalBlocked != 1 || len(r.Tracks) != 1 || r.Tracks[0].Items[0] != "a" {
				t.Fatalf("a must be the actionable one: %+v", r.Tracks)
			}
			if len(r.TopPicks) != 1 || r.TopPicks[0].ID != "a" || r.TopPicks[0].Unblocks != 1 {
				t.Fatalf("the pick is the blocker: %+v", r.TopPicks)
			}
			if len(r.CriticalPath) != 2 || r.CriticalPath[0].ID != "a" || r.CriticalPath[0].Value != 2 || r.CriticalPath[1].ID != "b" || r.CriticalPath[1].Value != 1 {
				t.Fatalf("the blocker sits at the head of the chain: %+v", r.CriticalPath)
			}
			if len(r.Blockers) != 1 || r.Blockers[0].ID != "a" {
				t.Fatalf("blockers %+v", r.Blockers)
			}
		})
	}
}

// In-progress work is actionable (counted) but already claimed, so it is never the next pick; an
// open issue is.
func TestAnInProgressIssueIsCountedButNeverThePick(t *testing.T) {
	r := analyze(t, []graph.Issue{is("a", graph.StatusInProgress), is("b", graph.StatusOpen)})
	if r.TotalActionable != 2 || len(r.TopPicks) != 1 || r.TopPicks[0].ID != "b" {
		t.Fatalf("%d actionable, picks %+v", r.TotalActionable, r.TopPicks)
	}
}

// The first sanitizing test is cyclic, so it never reaches the critical path or the bottlenecks;
// this chain does, and so reaches every section that has an id.
func TestIdsInTheRankedSectionsAreSanitized(t *testing.T) {
	a, b, c := "a\x1b[2J", "b\u202e", "c\x07"
	r := analyze(t, []graph.Issue{
		{ID: a, Title: "T", Status: graph.StatusOpen, Priority: 1, Type: "task"},
		{ID: b, Title: "T", Status: graph.StatusOpen, Priority: 2, Type: "task", DependsOn: []graph.Dependency{{ID: a, Type: graph.DepBlocks}}},
		{ID: c, Title: "T", Status: graph.StatusOpen, Priority: 2, Type: "task", DependsOn: []graph.Dependency{{ID: b, Type: graph.DepBlocks}}},
	})
	if len(r.CriticalPath) != 3 || len(r.Bottlenecks) == 0 || len(r.Tracks) != 1 || len(r.Tracks[0].Items) != 1 {
		t.Fatalf("the probe must reach the ranked sections: %+v", r)
	}
	for _, id := range []string{r.CriticalPath[0].ID, r.CriticalPath[1].ID, r.CriticalPath[2].ID, r.Bottlenecks[0].ID, r.Tracks[0].ID, r.Tracks[0].Items[0]} {
		if strings.ContainsAny(id, "\x1b\x07\u202e") {
			t.Errorf("unsanitized id %q", id)
		}
	}
	if r.CriticalPath[0].ID != "a[2J" {
		t.Errorf("the readable part is kept: %q", r.CriticalPath[0].ID)
	}
}

// The influence ranking (eigenvector centrality) is carried too, with sanitized ids.
func TestInfluencersAreCarriedAndSanitized(t *testing.T) {
	a, b, c := "a\x1b[2J", "b\u202e", "c\x07"
	r := analyze(t, []graph.Issue{
		{ID: a, Title: "T", Status: graph.StatusOpen, Priority: 1, Type: "task"},
		{ID: b, Title: "T", Status: graph.StatusOpen, Priority: 2, Type: "task", DependsOn: []graph.Dependency{{ID: a, Type: graph.DepBlocks}}},
		{ID: c, Title: "T", Status: graph.StatusOpen, Priority: 2, Type: "task", DependsOn: []graph.Dependency{{ID: b, Type: graph.DepBlocks}}},
	})
	if len(r.Influencers) == 0 {
		t.Fatalf("a chain has an influence ranking: %+v", r)
	}
	for _, k := range r.Influencers {
		if strings.ContainsAny(k.ID, "\x1b\x07\u202e") || k.ID == "" {
			t.Errorf("unsanitized or empty id %q", k.ID)
		}
	}
}

func TestCyclesAreFoundAndSkipTheCriticalPath(t *testing.T) {
	r := analyze(t, load(t, "cycle"))
	if len(r.Cycles) != 1 || len(r.Cycles[0]) != 3 || r.Cycles[0][0] != r.Cycles[0][2] {
		t.Fatalf("cycles %+v", r.Cycles)
	}
	if len(r.CriticalPath) != 0 || !strings.HasPrefix(r.CriticalPathNote, "critical path skipped: ") || !strings.Contains(r.CriticalPathNote, "cycle") {
		t.Fatalf("critical path %+v note %q", r.CriticalPath, r.CriticalPathNote)
	}
	if r.TotalActionable != 1 || r.TotalBlocked != 2 || r.TopPicks[0].ID != "cy-3" {
		t.Fatalf("%+v", r)
	}
	// The same issues without the back edge have no cycle and a critical path.
	r = analyze(t, []graph.Issue{is("p", graph.StatusOpen, dep("q")), is("q", graph.StatusOpen)})
	if len(r.Cycles) != 0 || len(r.CriticalPath) == 0 || r.CriticalPathNote != "" {
		t.Fatalf("%+v", r)
	}
}

type countingAnalyzer struct{ calls int }

func (c *countingAnalyzer) Analyze(context.Context, []graph.Issue) (core.InsightsReport, error) {
	c.calls++
	return core.InsightsReport{TotalActionable: 7}, nil
}

type failingSource struct{}

func (failingSource) Issues(context.Context) ([]graph.Issue, error) {
	return nil, context.DeadlineExceeded
}

func TestAnEmptyTrackerNeverReachesTheAnalyzer(t *testing.T) {
	// The module short-circuits before calling any analyzer...
	c := &countingAnalyzer{}
	m := graph.NewModule(graph.Options{Source: graph.StaticSource(nil), Analyzer: c})
	r, err := m.Insights(context.Background())
	if err != nil || !r.Empty || r.Summary != "no tasks yet" || c.calls != 0 {
		t.Fatalf("%+v %v calls=%d", r, err, c.calls)
	}
	// ...and the bv analyzer is safe on its own: no issues is the empty report, not a bv result.
	got, err := graph.NewBVAnalyzer().Analyze(context.Background(), nil)
	if err != nil || !got.Empty || got.Summary != "no tasks yet" || got.TotalActionable != 0 || got.GraphNodes != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// With one issue the analyzer does run.
	m = graph.NewModule(graph.Options{Source: graph.StaticSource{is("a", graph.StatusOpen)}, Analyzer: c})
	if r, err := m.Insights(context.Background()); err != nil || r.TotalActionable != 7 || c.calls != 1 || r.Source != core.SourceGraph {
		t.Fatalf("%+v %v calls=%d", r, err, c.calls)
	}
}

func TestEveryStringInTheReportIsSanitized(t *testing.T) {
	hostile := "\x1b[31mred\x1b]0;pwned\x07‮ evil​\nline\x00"
	issues := []graph.Issue{
		{ID: "a-1\x1b[2J", Title: hostile, Status: graph.StatusOpen, Priority: 1, Type: "task"},
		{ID: "b‮2", Title: hostile, Status: graph.StatusOpen, Priority: 2, Type: "task", DependsOn: []graph.Dependency{{ID: "a-1\x1b[2J", Type: graph.DepBlocks}}},
		{ID: "c\x07", Title: hostile, Status: graph.StatusOpen, Priority: 2, Type: "task", DependsOn: []graph.Dependency{{ID: "d\x1b", Type: graph.DepBlocks}}},
		{ID: "d\x1b", Title: hostile, Status: graph.StatusOpen, Priority: 2, Type: "task", DependsOn: []graph.Dependency{{ID: "c\x07", Type: graph.DepBlocks}}},
	}
	r := analyze(t, issues)
	if len(r.TopPicks) == 0 || len(r.Blockers) == 0 || len(r.Cycles) == 0 || len(r.Tracks) == 0 {
		t.Fatalf("the probe must reach every section: %+v", r)
	}
	b, _ := json.Marshal(r)
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, c := range x {
				if unicode.IsControl(c) || (c >= 0x200b && c <= 0x200f) || (c >= 0x202a && c <= 0x202e) {
					t.Errorf("unsanitized rune %U in %q", c, x)
				}
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				walk(k)
				walk(e)
			}
		}
	}
	var doc any
	_ = json.Unmarshal(b, &doc)
	walk(doc)
	if !strings.Contains(string(b), "red") {
		t.Fatalf("sanitizing keeps the readable text: %s", b)
	}
	for _, p := range r.TopPicks {
		if strings.ContainsAny(p.Title, "\x1b\x07") {
			t.Errorf("title %q", p.Title)
		}
	}
}

func TestPanicOrCancelInTheAnalysisIsAnErrorNotACrash(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := graph.NewBVAnalyzer().Analyze(ctx, load(t, "big200")); err == nil {
		t.Fatal("a cancelled context must end the analysis with an error")
	}
}

func TestModuleReportsAFailedReadAndRecovers(t *testing.T) {
	m := graph.NewModule(graph.Options{Source: failingSource{}, Analyzer: &countingAnalyzer{}})
	if _, err := m.Insights(context.Background()); err == nil {
		t.Fatal("a failed read is an error")
	}
	ch := m.Doctor(context.Background())
	if len(ch) != 1 || ch[0].Status != core.StatusWarn || !strings.Contains(ch[0].Detail, "failed") || !strings.Contains(ch[0].Fix, "bd status") || strings.Contains(ch[0].Fix, "issues.jsonl") {
		t.Fatalf("%+v", ch)
	}
	if st := m.Status(context.Background()); st.Unavailable == "" {
		t.Fatalf("%+v", st)
	}
	ok := graph.NewModule(graph.Options{Source: graph.StaticSource{is("a", graph.StatusOpen)}, Analyzer: &countingAnalyzer{}})
	if _, err := ok.Insights(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ch := ok.Doctor(context.Background()); ch[0].Status != core.StatusOK {
		t.Fatalf("%+v", ch)
	}
	if d := ok.Detect(context.Background()); d.State != core.Ready {
		t.Fatalf("%+v", d)
	}
	// Through core's reader the library report is a connected Insights header like bv's.
	rep := core.ReadInsights(context.Background(), ok, "")
	if rep.State != core.Connected || rep.Title != "Insights" || rep.Source != core.SourceGraph {
		t.Fatalf("%+v", rep.Header)
	}
}

// A status abacus has no name for is parked: it is not actionable, and not closed either, so a
// dependent of it stays blocked. (bvStatus's fall-through case.)
func TestCustomStatusIsParkedNeitherActionableNorClosed(t *testing.T) {
	r, err := graph.NewBVAnalyzer().Analyze(context.Background(), []graph.Issue{
		is("review", "in_review"),
		is("waiter", graph.StatusOpen, graph.Dependency{ID: "review", Type: graph.DepBlocks}),
		is("free", graph.StatusOpen),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only "free" is actionable; "waiter" is held by the parked issue; "review" is in neither.
	if r.TotalActionable != 1 || r.TotalBlocked != 1 {
		t.Fatalf("actionable %d, blocked %d; want 1 and 1", r.TotalActionable, r.TotalBlocked)
	}
}

// A dependency type abacus has no name for is "related": it never blocks. (bvDepType's
// fall-through case.)
func TestUnknownDependencyTypeIsRelatedAndNeverBlocks(t *testing.T) {
	r, err := graph.NewBVAnalyzer().Analyze(context.Background(), []graph.Issue{
		is("a", graph.StatusOpen, graph.Dependency{ID: "b", Type: "mystery-link"}),
		is("b", graph.StatusOpen),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.TotalActionable != 2 || r.TotalBlocked != 0 {
		t.Fatalf("actionable %d, blocked %d; want 2 and 0", r.TotalActionable, r.TotalBlocked)
	}
	// The known hard type, for contrast, does block.
	r, _ = graph.NewBVAnalyzer().Analyze(context.Background(), []graph.Issue{
		is("a", graph.StatusOpen, graph.Dependency{ID: "b", Type: graph.DepBlocks}),
		is("b", graph.StatusOpen),
	})
	if r.TotalActionable != 1 || r.TotalBlocked != 1 {
		t.Fatalf("blocks: actionable %d, blocked %d; want 1 and 1", r.TotalActionable, r.TotalBlocked)
	}
}

// bv's pkg/debug reads BV_DEBUG in an init() that runs before main (so unsetting the variable in
// main is too late) and then logs to stderr, which would scribble over the TUI. The analyzer
// switches it off whatever the environment said. This runs the check in a child process that
// really starts with BV_DEBUG set.
func TestAnalyzerSilencesBvDebugLogging(t *testing.T) {
	switch os.Getenv("ABACUS_TEST_BV_DEBUG_CHILD") {
	case "new": // the constructor alone is enough
		before := graph.BVDebugEnabled()
		graph.NewBVAnalyzer()
		fmt.Printf("before=%v after=%v", before, graph.BVDebugEnabled())
		return
	case "zero": // the zero value never went through the constructor; analysis re-asserts the guard
		before := graph.BVDebugEnabled()
		var an graph.BVAnalyzer
		if _, err := an.Analyze(context.Background(), []graph.Issue{is("a", graph.StatusOpen)}); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("before=%v after=%v", before, graph.BVDebugEnabled())
		return
	}
	for _, mode := range []string{"new", "zero"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAnalyzerSilencesBvDebugLogging$")
		cmd.Env = append(os.Environ(), "ABACUS_TEST_BV_DEBUG_CHILD="+mode, "BV_DEBUG=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
		// "before=true" proves bv's init() ran with the variable set, ahead of any abacus code.
		if !strings.Contains(string(out), "before=true after=false") {
			t.Fatalf("%s: child output: %s", mode, out)
		}
	}
}

// With nothing set the guard is a no-op: the logger stays off.
func TestBvDebugIsOffByDefault(t *testing.T) {
	if os.Getenv("BV_DEBUG") != "" {
		t.Skip("BV_DEBUG is set")
	}
	if graph.BVDebugEnabled() {
		t.Fatal("bv debug logging is on without BV_DEBUG")
	}
}
