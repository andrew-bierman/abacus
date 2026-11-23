package taskview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/graph"
)

// t is a tiny issue builder: t("b", "open", "a") is task b, open, blocked by a.
func gt(id, status string, blockers ...string) core.TaskIssue {
	it := core.TaskIssue{ID: id, Title: "task " + id, Status: status, Priority: 2}
	for _, b := range blockers {
		it.Deps = append(it.Deps, core.TaskDep{DependsOn: b, Type: "blocks"})
	}
	return it
}

func gview(issues ...core.TaskIssue) *GraphIndex {
	return NewGraphIndex(Build(core.TaskSnapshot{Issues: issues, ReadAt: fixedNow}, fixedNow))
}

func layerIDs(v core.GraphView) [][]string {
	var out [][]string
	for _, l := range v.Layers {
		out = append(out, l.Nodes)
	}
	return out
}

func node(v core.GraphView, id string) core.GraphNode {
	for _, n := range v.Nodes {
		if n.ID == id {
			return n
		}
	}
	return core.GraphNode{}
}

func TestGraphWholeLayers(t *testing.T) {
	diamond := []core.TaskIssue{gt("a", "open"), gt("b", "open", "a"), gt("c", "open", "a"), gt("d", "open", "b", "c")}
	cases := []struct {
		name   string
		issues []core.TaskIssue
		layers [][]string
		chain  []string
		cycles [][]string
		isol   int
		comps  int
	}{
		{"chain", []core.TaskIssue{gt("a", "open"), gt("b", "open", "a"), gt("c", "open", "b")}, [][]string{{"a"}, {"b"}, {"c"}}, []string{"a", "b", "c"}, nil, 0, 1},
		{"diamond", diamond, [][]string{{"a"}, {"b", "c"}, {"d"}}, []string{"a", "b", "d"}, nil, 0, 1},
		{"fan-out", []core.TaskIssue{gt("h", "open"), gt("x1", "open", "h"), gt("x2", "open", "h"), gt("x3", "open", "h")}, [][]string{{"h"}, {"x1", "x2", "x3"}}, []string{"h", "x1"}, nil, 0, 1},
		{"cycle", []core.TaskIssue{gt("x", "open", "z"), gt("y", "open", "x"), gt("z", "open", "y"), gt("after", "open", "y")}, [][]string{{"x", "y", "z"}, {"after"}}, nil, [][]string{{"x", "y", "z"}}, 0, 1},
		{"self-dependency", []core.TaskIssue{gt("s", "open", "s"), gt("t", "open", "s")}, [][]string{{"s"}, {"t"}}, []string{"s", "t"}, nil, 0, 1},
		{"missing target", []core.TaskIssue{gt("m", "open", "ghost")}, [][]string{{"m"}}, nil, nil, 0, 1},
		{"disconnected", []core.TaskIssue{gt("a", "open"), gt("b", "open", "a"), gt("p", "open"), gt("q", "open", "p"), gt("lonely", "open")}, [][]string{{"a", "p"}, {"b", "q"}}, []string{"a", "b"}, nil, 1, 2},
		{"closed is muted but laid out; never on the chain", []core.TaskIssue{gt("a", "closed"), gt("b", "open", "a"), gt("c", "open", "b")}, [][]string{{"a"}, {"b"}, {"c"}}, []string{"b", "c"}, nil, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := gview(c.issues...).View(GraphOptions{Whole: true})
			if got := layerIDs(v); !reflect.DeepEqual(got, c.layers) {
				t.Errorf("layers %v, want %v", got, c.layers)
			}
			if !reflect.DeepEqual(v.Chain, c.chain) {
				t.Errorf("chain %v, want %v", v.Chain, c.chain)
			}
			if !reflect.DeepEqual(v.Cycles, c.cycles) {
				t.Errorf("cycles %v, want %v", v.Cycles, c.cycles)
			}
			if v.Isolated != c.isol || v.Components != c.comps {
				t.Errorf("isolated %d components %d, want %d and %d", v.Isolated, v.Components, c.isol, c.comps)
			}
		})
	}
}

func TestGraphFlagsSelfMissingClosedCycle(t *testing.T) {
	g := gview(gt("s", "open", "s"), gt("m", "open", "ghost"), gt("a", "closed"), gt("x", "open", "y"), gt("y", "open", "x"))
	v := g.View(GraphOptions{Whole: true})
	if n := node(v, "s"); !n.SelfDep || n.InCycle {
		t.Errorf("a self-dependency is flagged, not a cycle: %+v", n)
	}
	if !reflect.DeepEqual(v.SelfDeps, []string{"s"}) {
		t.Errorf("self deps %v", v.SelfDeps)
	}
	if n := node(v, "m"); !reflect.DeepEqual(n.Missing, []string{"ghost"}) {
		t.Errorf("missing target kept as a flag: %+v", n)
	}
	if n := node(v, "x"); !n.InCycle {
		t.Errorf("cycle member not flagged: %+v", n)
	}
}

// Both the tie between two equally long chains and the order inside a layer fall to the id.
func TestGraphTieBreaksAreStable(t *testing.T) {
	v := gview(gt("a", "open"), gt("c", "open", "a"), gt("b", "open", "a"), gt("d", "open", "c", "b")).View(GraphOptions{Whole: true})
	if !reflect.DeepEqual(v.Chain, []string{"a", "b", "d"}) {
		t.Errorf("tie goes to the smaller id: %v", v.Chain)
	}
	if got := layerIDs(v)[1]; !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("layer order %v", got)
	}
	// two chains of different length: the longer wins whatever the ids
	v = gview(gt("a", "open"), gt("b", "open", "a"), gt("z1", "open"), gt("z2", "open", "z1"), gt("z3", "open", "z2")).View(GraphOptions{Whole: true})
	if !reflect.DeepEqual(v.Chain, []string{"z1", "z2", "z3"}) {
		t.Errorf("longest chain: %v", v.Chain)
	}
}

func TestGraphNeighborhood(t *testing.T) {
	g := gview(gt("a", "closed"), gt("b", "open", "a"), gt("c", "in_progress", "a"), gt("d", "open", "b", "c"), gt("e", "open", "d"), gt("f", "open", "d"), gt("g", "open", "e"))
	v := g.View(GraphOptions{Focus: "d", Depth: 2})
	want := [][]string{{"a"}, {"b", "c"}, {"d"}, {"e", "f"}, {"g"}}
	if got := layerIDs(v); !reflect.DeepEqual(got, want) {
		t.Fatalf("layers %v, want %v", got, want)
	}
	if node(v, "a").Layer != -2 || node(v, "d").Layer != 0 || node(v, "g").Layer != 2 || !node(v, "d").Focus {
		t.Errorf("signed layers: %+v", v.Nodes)
	}
	if v.OpenBlockers != 2 || v.Unblocks != 2 {
		t.Errorf("focus info: blockers %d unblocks %d", v.OpenBlockers, v.Unblocks)
	}
	if !reflect.DeepEqual(v.Chain, []string{"b", "d", "e", "g"}) {
		t.Errorf("chain through the focus: %v", v.Chain)
	}
	drawn := 0
	for _, e := range v.Edges {
		if e.Drawn {
			drawn++
		}
	}
	if drawn != len(v.Edges) || len(v.Edges) != 7 {
		t.Errorf("every edge joins neighboring layers here: %d of %d", drawn, len(v.Edges))
	}
	// depth 1 stops at the direct neighbors
	v = g.View(GraphOptions{Focus: "d", Depth: 1})
	if got := layerIDs(v); !reflect.DeepEqual(got, [][]string{{"b", "c"}, {"d"}, {"e", "f"}}) {
		t.Errorf("depth 1: %v", got)
	}
	// the outline lists blockers nested above, then the focus, then dependents
	var rows []string
	for _, r := range g.View(GraphOptions{Focus: "d", Depth: 2}).Outline {
		rows = append(rows, fmt.Sprintf("%s%d:%s", r.Side[:1], r.Depth, r.ID))
	}
	if got := strings.Join(rows, " "); got != "u1:b u2:a u1:c u2:a f0:d d1:e d2:g d1:f" {
		t.Errorf("outline %s", got)
	}
	if !g.View(GraphOptions{Focus: "d", Depth: 2}).Outline[3].Repeat {
		t.Error("a task listed twice is marked, not expanded twice")
	}
}

func TestGraphNeighborhoodCollapsesFanOut(t *testing.T) {
	issues := []core.TaskIssue{gt("h", "open")}
	for i := 0; i < 12; i++ {
		issues = append(issues, gt(fmt.Sprintf("x%02d", i), "open", "h"))
	}
	v := gview(issues...).View(GraphOptions{Focus: "h", Depth: 1, MaxPerLayer: 4, MaxFan: 3})
	if l := v.Layers[1]; len(l.Nodes) != 4 || l.More != 8 {
		t.Errorf("layer collapsed to 4 with +8 more: %+v", l)
	}
	if v.Hidden != 8 {
		t.Errorf("hidden %d", v.Hidden)
	}
	last := v.Outline[len(v.Outline)-1]
	if last.ID != "" || last.More != 9 {
		t.Errorf("outline fan-out of 3 then +9 more: %+v", last)
	}
}

func TestGraphNeighborhoodCycleAndMissingFocus(t *testing.T) {
	g := gview(gt("x", "open", "z"), gt("y", "open", "x"), gt("z", "open", "y"))
	v := g.View(GraphOptions{Focus: "x", Depth: 3})
	if len(v.Nodes) != 3 || len(v.Cycles) != 1 || v.Chain != nil {
		t.Errorf("a cycle terminates and is flagged: %d nodes, cycles %v, chain %v", len(v.Nodes), v.Cycles, v.Chain)
	}
	v = g.View(GraphOptions{Focus: "nope"})
	if len(v.Nodes) != 0 || v.Reason == "" {
		t.Errorf("unknown focus says why: %+v", v.Header)
	}
}

func TestGraphInsightsHighlights(t *testing.T) {
	ins := &core.InsightsReport{Bottlenecks: []core.Ranked{{ID: "a", Value: 3}}, CriticalPath: []core.Ranked{{ID: "b", Value: 2}}}
	v := gview(gt("a", "open"), gt("b", "open", "a")).View(GraphOptions{Whole: true, Insights: ins})
	if !node(v, "a").Bottleneck || !node(v, "b").Keystone || node(v, "b").Bottleneck {
		t.Errorf("engine highlights: %+v", v.Nodes)
	}
}

func shuffled(issues []core.TaskIssue, seed int64) []core.TaskIssue {
	out := append([]core.TaskIssue(nil), issues...)
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	for i := range out { // dependency order too
		d := append([]core.TaskDep(nil), out[i].Deps...)
		r.Shuffle(len(d), func(a, b int) { d[a], d[b] = d[b], d[a] })
		out[i].Deps = d
	}
	return out
}

func TestGraphDeterministicUnderShuffle(t *testing.T) {
	var issues []core.TaskIssue
	for _, name := range []string{"graph-export.jsonl", "graph-export-edges.jsonl"} {
		snap, err := Parse(bytes.NewReader(fixture(t, name)), fixedNow, "t")
		if err != nil {
			t.Fatal(err)
		}
		issues = snap.Issues
		render := func(is []core.TaskIssue) string {
			g := NewGraphIndex(Build(core.TaskSnapshot{Issues: is, ReadAt: fixedNow}, fixedNow))
			var sb strings.Builder
			for _, o := range []GraphOptions{{Whole: true}, {Focus: "gr-ko8", Depth: 3}, {Focus: "gr-q7z", Depth: 4, MaxPerLayer: 2}} {
				b, _ := json.Marshal(g.View(o))
				sb.Write(b)
			}
			return sb.String()
		}
		want := render(issues)
		for seed := int64(0); seed < 100; seed++ {
			if got := render(shuffled(issues, seed)); got != want {
				t.Fatalf("%s: seed %d changed the layout", name, seed)
			}
		}
	}
}

// The real export (bd 1.3.4, see testdata/README.md): a diamond whose root is closed, a long
// chain, a fan-out of twelve, a disconnected pair and hostile text.
func TestGraphRealExport(t *testing.T) {
	snap, err := Parse(bytes.NewReader(fixture(t, "graph-export.jsonl")), fixedNow, "gr")
	if err != nil {
		t.Fatal(err)
	}
	g := NewGraphIndex(Build(snap, fixedNow))
	v := g.View(GraphOptions{Whole: true})
	if !reflect.DeepEqual(v.Chain, []string{"gr-q7z", "gr-hsy", "gr-3k9", "gr-hoz", "gr-fdz", "gr-l5l"}) {
		t.Errorf("longest chain is the unfinished tail of the long chain: %v", v.Chain)
	}
	if len(v.Cycles) != 0 || v.Total != len(snap.Issues) || v.Components < 5 {
		t.Errorf("cycles %v total %d components %d", v.Cycles, v.Total, v.Components)
	}
	if n := node(v, "gr-9ik"); n.Status != "closed" || n.Layer != 0 {
		t.Errorf("closed root is laid out: %+v", n)
	}
	n := g.View(GraphOptions{Focus: "gr-6yh", Depth: 1, MaxPerLayer: 5})
	if n.Layers[1].More != 7 || len(n.Layers[1].Nodes) != 5 {
		t.Errorf("fan-out of 12 collapses: %+v", n.Layers[1])
	}
}

// The edited export adds what bd refuses to create: a cycle, a self-dependency, a missing target.
func TestGraphEditedExportEdges(t *testing.T) {
	snap, err := Parse(bytes.NewReader(fixture(t, "graph-export-edges.jsonl")), fixedNow, "gr")
	if err != nil {
		t.Fatal(err)
	}
	v := NewGraphIndex(Build(snap, fixedNow)).View(GraphOptions{Whole: true})
	if !reflect.DeepEqual(v.Cycles, [][]string{{"gr-syt", "gr-tse", "gr-vgy"}}) {
		t.Errorf("cycles %v", v.Cycles)
	}
	if !reflect.DeepEqual(v.SelfDeps, []string{"gr-1fm"}) {
		t.Errorf("self deps %v", v.SelfDeps)
	}
	if n := node(v, "gr-l2q"); !reflect.DeepEqual(n.Missing, []string{"gr-gone"}) {
		t.Errorf("missing %+v", n)
	}
}

func bigIssues(n int) []core.TaskIssue {
	r := rand.New(rand.NewSource(7))
	out := make([]core.TaskIssue, n)
	for i := range out {
		out[i] = gt(fmt.Sprintf("t%05d", i), []string{"open", "closed", "in_progress"}[i%3])
		for k := 0; k < 1+i%3 && i > 0; k++ {
			out[i].Deps = append(out[i].Deps, core.TaskDep{DependsOn: fmt.Sprintf("t%05d", r.Intn(i)), Type: "blocks"})
		}
	}
	return out
}

func TestGraphFiveThousandTasksStayFast(t *testing.T) {
	issues := bigIssues(5000)
	// one long chain and one hub with a huge fan-out ride along
	for i := 1; i < 800; i++ {
		issues[i].Status = "open"
		issues[i].Deps = append(issues[i].Deps, core.TaskDep{DependsOn: fmt.Sprintf("t%05d", i-1), Type: "blocks"})
	}
	start := time.Now()
	g := gview(issues...)
	whole := g.View(GraphOptions{Whole: true})
	near := g.View(GraphOptions{Focus: "t00000", Depth: 6})
	took := time.Since(start)
	if took > 2*time.Second {
		t.Errorf("5000 tasks took %v", took)
	}
	if len(whole.Nodes)+whole.Hidden+whole.Isolated != whole.Total || len(near.Outline) > 400 {
		t.Errorf("whole %d nodes + %d hidden + %d isolated of %d, outline %d rows", len(whole.Nodes), whole.Hidden, whole.Isolated, whole.Total, len(near.Outline))
	}
	if len(whole.Chain) < 700 {
		t.Errorf("chain %d", len(whole.Chain))
	}
}

// The cycles and the bottleneck marks must agree with the in-process graph engine fed the same
// snapshot: one answer to "what is a cycle" across the views.
func TestGraphAgreesWithTheEngine(t *testing.T) {
	snap, err := Parse(bytes.NewReader(fixture(t, "graph-export-edges.jsonl")), fixedNow, "gr")
	if err != nil {
		t.Fatal(err)
	}
	var issues []graph.Issue
	for _, it := range snap.Issues {
		gi := graph.Issue{ID: it.ID, Title: it.Title, Status: graph.ParseStatus(it.Status), Priority: it.Priority, Type: it.Type}
		for _, d := range it.Deps {
			gi.DependsOn = append(gi.DependsOn, graph.Dependency{ID: d.DependsOn, Type: graph.ParseDepType(d.Type)})
		}
		issues = append(issues, gi)
	}
	rep, err := graph.NewBVAnalyzer().Analyze(context.Background(), issues)
	if err != nil {
		t.Fatal(err)
	}
	v := NewGraphIndex(Build(snap, fixedNow)).View(GraphOptions{Whole: true, Insights: &rep})
	// The engine closes each cycle path (first id repeated) and counts a self-dependency as a
	// one-task cycle; the view lists members once and keeps self-dependencies apart.
	var engine [][]string
	var self []string
	for _, c := range rep.Cycles {
		members := slices.Compact(slices.Sorted(slices.Values(c)))
		if len(members) == 1 {
			self = append(self, members[0])
			continue
		}
		engine = append(engine, members)
	}
	if !reflect.DeepEqual(engine, v.Cycles) || !reflect.DeepEqual(self, v.SelfDeps) {
		t.Errorf("engine cycles %v self %v, view cycles %v self %v", engine, self, v.Cycles, v.SelfDeps)
	}
	marked := 0
	for _, n := range v.Nodes {
		if n.Bottleneck || n.Keystone {
			marked++
		}
	}
	t.Logf("engine: %d bottlenecks, %d keystones; %d nodes marked", len(rep.Bottlenecks), len(rep.CriticalPath), marked)
}

// The nodes flagged OnChain are exactly the chain, in the whole graph and through a focus, and a tie
// between two equal branches below the focus goes to the smaller id.
func TestGraphChainIsFlaggedOnNodes(t *testing.T) {
	g := gview(gt("a", "open"), gt("b", "open", "a"), gt("c", "open", "a"), gt("d", "open", "b", "c"))
	for name, v := range map[string]core.GraphView{"whole": g.View(GraphOptions{Whole: true}), "near": g.View(GraphOptions{Focus: "a", Depth: 3})} {
		var flagged []string
		for _, n := range v.Nodes {
			if n.OnChain {
				flagged = append(flagged, n.ID)
			}
		}
		if !reflect.DeepEqual(flagged, []string{"a", "b", "d"}) || !reflect.DeepEqual(v.Chain, []string{"a", "b", "d"}) {
			t.Errorf("%s: flagged %v, chain %v", name, flagged, v.Chain)
		}
	}
}
