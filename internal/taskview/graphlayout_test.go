package taskview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"sort"
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
	ins := &core.InsightsReport{Bottlenecks: []core.Ranked{{ID: "a", Value: 3}, {ID: "c", Value: 2}}}
	v := gview(gt("a", "open"), gt("b", "open", "a"), gt("c", "closed", "b")).View(GraphOptions{Whole: true, Insights: ins})
	if !node(v, "a").Bottleneck || node(v, "b").Bottleneck || v.MarksSource != "engine" {
		t.Errorf("engine highlights: %+v", v.Nodes)
	}
	if node(v, "c").Bottleneck {
		t.Error("a closed task is never marked a bottleneck")
	}
	if v := gview(gt("a", "open"), gt("b", "open", "a")).View(GraphOptions{Whole: true}); v.MarksSource != "" || node(v, "a").Bottleneck {
		t.Error("no report, no marks")
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
		if n.Bottleneck {
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

func outlineIDs(v core.GraphView, side string) []string {
	var out []string
	for _, r := range v.Outline {
		if r.Side == side && r.ID != "" && !r.Repeat {
			out = append(out, r.ID)
		}
	}
	return out
}

// Every outline row carries its node: a task the boxes collapsed (a focus with seven dependents) is
// still drawn with its title and status by anything that reads the view, the CLI included.
func TestGraphOutlineRowsCarryTheirNodes(t *testing.T) {
	issues := []core.TaskIssue{gt("f", "open")}
	for i := 1; i <= 7; i++ {
		issues = append(issues, gt(fmt.Sprintf("d%d", i), "open", "f"))
	}
	v := gview(issues...).View(GraphOptions{Focus: "f", Depth: 1})
	if got := outlineIDs(v, core.OutlineDown); len(got) != 7 {
		t.Fatalf("seven dependents in the outline: %v", got)
	}
	inNodes := map[string]bool{}
	for _, n := range v.Nodes {
		inNodes[n.ID] = true
	}
	missing := 0
	for _, r := range v.Outline {
		if r.ID == "" {
			continue
		}
		if r.Node == nil || r.Node.ID != r.ID || r.Node.Title == "" || r.Node.Status == "" {
			t.Errorf("outline row %q has no node data: %+v", r.ID, r.Node)
		}
		if !inNodes[r.ID] {
			missing++
		}
	}
	if missing != 2 { // d6 and d7 are collapsed out of the boxes
		t.Errorf("expected the outline to reach past the boxes by 2 tasks, got %d", missing)
	}
}

// A shortcut edge must not hide a task that is within the depth: the outline lists each task at its
// shortest distance, the boxes' layers, whatever order the walk meets the edges in.
func TestGraphOutlineAndBoxesAgreeWithAShortcut(t *testing.T) {
	// f <- a <- b <- c, and the shortcut b -> f: c is two steps above f
	g := gview(gt("c", "open"), gt("b", "open", "c"), gt("a", "open", "b"), gt("f", "open", "a", "b"))
	v := g.View(GraphOptions{Focus: "f", Depth: 2, MaxPerLayer: 20, MaxFan: 20})
	up := outlineIDs(v, core.OutlineUp)
	var boxes []string
	for _, n := range v.Nodes {
		if n.Layer < 0 {
			boxes = append(boxes, n.ID)
		}
	}
	sort.Strings(up)
	sort.Strings(boxes)
	if !reflect.DeepEqual(up, boxes) || !reflect.DeepEqual(up, []string{"a", "b", "c"}) {
		t.Errorf("outline %v, boxes %v", up, boxes)
	}
	if r := v.Outline; r[0].ID != "a" || r[0].Depth != 1 {
		t.Errorf("a is one step above: %+v", r[0])
	}
}

// uncluttered is an independent check that no drawn group of links has several blockers and several
// dependents, which a shared bus would misread.
func uncluttered(t *testing.T, v core.GraphView) {
	t.Helper()
	layer := map[string]int{}
	for _, n := range v.Nodes {
		layer[n.ID] = n.Layer
	}
	type end struct {
		id   string
		pair int
	}
	adj := map[end][]end{}
	for _, e := range v.Edges {
		if !e.Drawn {
			continue
		}
		a, b := end{e.From, layer[e.From]}, end{e.To, layer[e.From]}
		adj[a], adj[b] = append(adj[a], b), append(adj[b], a)
	}
	seen := map[end]bool{}
	for start := range adj {
		if seen[start] {
			continue
		}
		parents, kids := map[string]bool{}, map[string]bool{}
		stack := []end{start}
		seen[start] = true
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if layer[x.id] == x.pair {
				parents[x.id] = true
			} else {
				kids[x.id] = true
			}
			for _, y := range adj[x] {
				if !seen[y] {
					seen[y] = true
					stack = append(stack, y)
				}
			}
		}
		if len(parents) > 1 && len(kids) > 1 {
			t.Fatalf("a drawn group has %d blockers and %d dependents: %v %v", len(parents), len(kids), parents, kids)
		}
	}
}

// A->C, B->C, B->D drawn on one bus would read as A->D too, so those links are not drawn.
func TestGraphDoesNotDrawAnAmbiguousBus(t *testing.T) {
	g := gview(gt("a", "open"), gt("b", "open"), gt("c", "open", "a", "b"), gt("d", "open", "b"), gt("f", "open", "c", "d"))
	v := g.View(GraphOptions{Focus: "f", Depth: 2})
	for _, e := range v.Edges {
		if e.From != "c" && e.From != "d" && e.Drawn {
			t.Errorf("%s->%s must not be drawn on a shared bus", e.From, e.To)
		}
	}
	if v.CrossLinks != 3 {
		t.Errorf("three links counted as not drawn: %d", v.CrossLinks)
	}
	uncluttered(t, v)
	// a blocker with several dependents, or a dependent with several blockers, stays drawn
	v = gview(gt("a", "open"), gt("b", "open", "a"), gt("c", "open", "a"), gt("f", "open", "b", "c")).View(GraphOptions{Focus: "f", Depth: 2})
	if v.CrossLinks != 0 {
		t.Errorf("a diamond is unambiguous: %d links hidden", v.CrossLinks)
	}
}

func TestGraphNeverDrawsAnAmbiguousBusOnRandomGraphs(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for k := 0; k < 300; k++ {
		n := 6 + r.Intn(14)
		var issues []core.TaskIssue
		for i := 0; i < n; i++ {
			it := gt(fmt.Sprintf("t%02d", i), "open")
			for j := 0; j < i; j++ {
				if r.Intn(4) == 0 {
					it.Deps = append(it.Deps, core.TaskDep{DependsOn: fmt.Sprintf("t%02d", j), Type: "blocks"})
				}
			}
			issues = append(issues, it)
		}
		g := gview(issues...)
		for _, f := range []string{"t00", fmt.Sprintf("t%02d", n-1), fmt.Sprintf("t%02d", n/2)} {
			uncluttered(t, g.View(GraphOptions{Focus: f, Depth: 4, MaxPerLayer: 6}))
		}
	}
}

// Five thousand tasks in the shapes that stress a layout: a deep chain, one long cycle with many small
// ones, and a hub several layers deep. Each is timed.
func TestGraphFiveThousandTasksStayFast(t *testing.T) {
	id := func(i int) string { return fmt.Sprintf("t%05d", i) }
	shapes := map[string]func() ([]core.TaskIssue, func(core.GraphView)){
		"deep chain": func() ([]core.TaskIssue, func(core.GraphView)) {
			var is []core.TaskIssue
			for i := 0; i < 5000; i++ {
				if i == 0 {
					is = append(is, gt(id(i), "open"))
				} else {
					is = append(is, gt(id(i), "open", id(i-1)))
				}
			}
			return is, func(v core.GraphView) {
				if len(v.Chain) != 5000 || len(v.Layers) != 5000 {
					t.Errorf("deep chain: chain %d, layers %d", len(v.Chain), len(v.Layers))
				}
			}
		},
		"long and many small cycles": func() ([]core.TaskIssue, func(core.GraphView)) {
			var is []core.TaskIssue
			for i := 0; i < 2000; i++ { // one cycle of 2000
				is = append(is, gt(id(i), "open", id((i+1999)%2000)))
			}
			for k := 0; k < 1000; k++ { // a thousand 3-cycles
				b := 2000 + 3*k
				is = append(is, gt(id(b), "open", id(b+2)), gt(id(b+1), "open", id(b)), gt(id(b+2), "open", id(b+1)))
			}
			return is, func(v core.GraphView) {
				if len(v.Cycles) != 1001 {
					t.Errorf("cycles %d", len(v.Cycles))
				}
			}
		},
		"multi-layer hub": func() ([]core.TaskIssue, func(core.GraphView)) {
			is := []core.TaskIssue{gt(id(0), "open")}
			n := 1
			prev := []int{0}
			for layer := 0; layer < 4 && n < 5000; layer++ {
				var next []int
				for _, p := range prev {
					for k := 0; k < 12 && n < 5000; k++ {
						is = append(is, gt(id(n), "open", id(p)))
						next = append(next, n)
						n++
					}
				}
				prev = next
			}
			for n < 5000 {
				is = append(is, gt(id(n), "open", id(prev[n%len(prev)])))
				n++
			}
			return is, func(v core.GraphView) {
				if len(v.Layers) < 5 || v.Hidden == 0 {
					t.Errorf("hub: %d layers, %d hidden", len(v.Layers), v.Hidden)
				}
			}
		},
	}
	for name, mk := range shapes {
		t.Run(name, func(t *testing.T) {
			issues, check := mk()
			start := time.Now()
			g := gview(issues...)
			whole := g.View(GraphOptions{Whole: true})
			near := g.View(GraphOptions{Focus: id(0), Depth: 6})
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("took %v", d)
			}
			if whole.Total != 5000 || len(near.Outline) > 400 {
				t.Errorf("total %d, outline %d rows", whole.Total, len(near.Outline))
			}
			check(whole)
		})
	}
}

// Parallel blocking edges (blocks and conditional-blocks on the same pair, or a repeated record)
// are one link: one edge, counted once.
func TestGraphParallelBlockingEdgesAreOneLink(t *testing.T) {
	b := gt("b", "open", "a")
	b.Deps = append(b.Deps, core.TaskDep{DependsOn: "a", Type: "conditional-blocks"}, core.TaskDep{DependsOn: "a", Type: "blocks"})
	v := gview(gt("a", "open"), b).View(GraphOptions{Focus: "b", Depth: 2})
	if len(v.Edges) != 1 || len(outlineIDs(v, core.OutlineUp)) != 1 || node(v, "b").BlockedBy != 1 {
		t.Errorf("edges %d, outline %v, blocked_by %d", len(v.Edges), outlineIDs(v, core.OutlineUp), node(v, "b").BlockedBy)
	}
}

// The default cap is five boxes a layer: five dependents all show, a sixth collapses.
func TestGraphDefaultLayerCapBoundary(t *testing.T) {
	for _, c := range []struct{ n, more int }{{5, 0}, {6, 1}} {
		issues := []core.TaskIssue{gt("f", "open")}
		for i := 0; i < c.n; i++ {
			issues = append(issues, gt(fmt.Sprintf("d%d", i), "open", "f"))
		}
		v := gview(issues...).View(GraphOptions{Focus: "f"})
		if l := v.Layers[len(v.Layers)-1]; l.More != c.more || len(l.Nodes)+l.More != c.n || v.Hidden != c.more {
			t.Errorf("%d dependents: %+v hidden %d", c.n, l, v.Hidden)
		}
	}
}
