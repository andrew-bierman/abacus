package taskview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

// shape is "id@depth" per tree row, in order, with a trailing * for a context row and + for a
// folded parent: the whole structure of a tree in one comparable string.
func shape(rep core.TaskTreeReport) string {
	var out []string
	for _, r := range rep.Rows {
		s := fmt.Sprintf("%s@%d", r.ID, r.Depth)
		if r.Context {
			s += "*"
		}
		if r.Folded {
			s += "+"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func treeOf(v *View, o TreeOptions) core.TaskTreeReport { return v.Tree(o) }

func mustQuery(t *testing.T, s string) query.Query {
	t.Helper()
	q, err := query.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestTreeShape(t *testing.T) {
	cases := []struct {
		name   string
		issues []core.TaskIssue
		want   string
	}{
		{"children under their epic, by priority then id",
			[]core.TaskIssue{iss("e", "open", 1), iss("c2", "open", 2, parent("e")), iss("c1", "open", 1, parent("e")), iss("c0", "open", 1, parent("e"))},
			"e@0 c0@1 c1@1 c2@1"},
		{"two epics and a loner, roots sorted like the list",
			[]core.TaskIssue{iss("lone", "open", 3), iss("b", "open", 1), iss("a", "open", 2, parent("b")), iss("z", "open", 0)},
			"z@0 b@0 a@1 lone@0"},
		{"a missing parent makes the child a root",
			[]core.TaskIssue{iss("kid", "open", 1, parent("gone")), iss("e", "open", 2)},
			"kid@0 e@0"},
		{"a task that is its own parent is a root",
			[]core.TaskIssue{iss("me", "open", 1, parent("me"))},
			"me@0"},
		{"a cycle of parent links is cut at its lowest id and shown once",
			[]core.TaskIssue{iss("b", "open", 1, parent("a")), iss("a", "open", 1, parent("b")), iss("c", "open", 1, parent("b"))},
			"a@0 b@1 c@2"},
		{"a three-cycle with a branch hanging off it",
			[]core.TaskIssue{iss("x", "open", 1, parent("z")), iss("y", "open", 1, parent("x")), iss("z", "open", 1, parent("y")), iss("w", "open", 1, parent("y"))},
			"x@0 y@1 w@2 z@2"},
		{"a second parent link is ignored: the first wins",
			[]core.TaskIssue{iss("p1", "open", 1), iss("p2", "open", 1), iss("k", "open", 1, parent("p1"), parent("p2"))},
			"p1@0 k@1 p2@0"},
		{"blocking links do not make a tree",
			[]core.TaskIssue{iss("a", "open", 1), iss("b", "open", 1, blk("a"))},
			"a@0 b@0"},
		{"duplicate ids appear once",
			[]core.TaskIssue{iss("d", "open", 1), iss("d", "open", 1), iss("k", "open", 1, parent("d"))},
			"d@0 k@1"},
		{"a closed parent is shown with an open child when everything is shown",
			[]core.TaskIssue{iss("cp", "closed", 1), iss("k", "open", 1, parent("cp"))},
			"cp@0 k@1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := treeOf(view(c.issues...), TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})
			if got := shape(rep); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestTreeCountsOrphansAndCycles(t *testing.T) {
	v := view(iss("kid", "open", 1, parent("gone")), iss("a", "open", 1, parent("b")), iss("b", "open", 1, parent("a")), iss("s", "open", 1, parent("s")), iss("ok", "open", 1))
	rep := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})
	if rep.Orphans != 1 {
		t.Errorf("orphans = %d, want 1 (only the missing parent counts)", rep.Orphans)
	}
	if !slices.Equal(rep.ParentCycles, []string{"a", "s"}) {
		t.Errorf("cycles = %v, want one id per cycle, ascending: [a s]", rep.ParentCycles)
	}
	if rep.Total != 5 || rep.Matched != 5 || rep.Shown != 5 {
		t.Errorf("total/matched/shown = %d/%d/%d", rep.Total, rep.Matched, rep.Shown)
	}
}

func TestTreeDeepNesting(t *testing.T) {
	const n = 300
	issues := []core.TaskIssue{iss("n000", "open", 1)}
	for i := 1; i < n; i++ {
		issues = append(issues, iss(fmt.Sprintf("n%03d", i), "open", 1, parent(fmt.Sprintf("n%03d", i-1))))
	}
	rep := view(issues...).Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})
	if len(rep.Rows) != n || rep.Rows[n-1].Depth != n-1 {
		t.Fatalf("rows %d, last depth %d", len(rep.Rows), rep.Rows[n-1].Depth)
	}
	for _, r := range rep.Rows {
		if w := len([]rune(r.Guide)); w > MaxGuideWidth {
			t.Fatalf("%s: guide is %d cells, the cap is %d", r.ID, w, MaxGuideWidth)
		}
	}
}

func TestGuide(t *testing.T) {
	if g := Guide(0, nil, false, false, false); g != "  " {
		t.Errorf("root leaf %q", g)
	}
	if g := Guide(0, nil, false, true, false); g != "▾ " {
		t.Errorf("root expanded %q", g)
	}
	if g := Guide(0, nil, false, true, true); g != "▹ " {
		t.Errorf("root folded %q", g)
	}
	if g := Guide(1, nil, false, false, false); g != "├── " {
		t.Errorf("child leaf %q", g)
	}
	if g := Guide(1, nil, true, false, false); g != "└── " {
		t.Errorf("last child leaf %q", g)
	}
	if g := Guide(1, nil, true, true, false); g != "└─▾ " {
		t.Errorf("last child expanded %q", g)
	}
	if g := Guide(2, []bool{false}, true, true, true); g != "│ └─▹ " {
		t.Errorf("grandchild under a non-last parent %q", g)
	}
	if g := Guide(2, []bool{true}, false, false, false); g != "  ├── " {
		t.Errorf("grandchild under a last parent %q", g)
	}
}

func TestTreeFold(t *testing.T) {
	v := view(iss("e", "open", 1), iss("s", "open", 1, parent("e")), iss("a", "open", 1, parent("s")), iss("b", "open", 1, parent("s")), iss("c", "open", 2, parent("e")), iss("x", "open", 1))
	all := ListOptions{Preset: PresetAll}
	if got := shape(v.Tree(TreeOptions{ListOptions: all})); got != "e@0 s@1 a@2 b@2 c@1 x@0" {
		t.Fatalf("unfolded: %s", got)
	}
	rep := v.Tree(TreeOptions{ListOptions: all, Folded: map[string]bool{"s": true}})
	if got := shape(rep); got != "e@0 s@1+ c@1 x@0" {
		t.Errorf("fold s: %s", got)
	}
	if rep.Rows[1].Hidden != 2 || !rep.Rows[1].HasChildren || rep.Folded != 1 {
		t.Errorf("s: hidden %d, has children %v, folded total %d", rep.Rows[1].Hidden, rep.Rows[1].HasChildren, rep.Folded)
	}
	if got := shape(v.Tree(TreeOptions{ListOptions: all, Folded: map[string]bool{"e": true, "s": true}})); got != "e@0+ x@0" {
		t.Errorf("fold e hides s too, and s's own fold is not reported: %s", got)
	}
	if got := v.Tree(TreeOptions{ListOptions: all, Folded: map[string]bool{"e": true}}).Rows[0].Hidden; got != 4 {
		t.Errorf("e hides 4 descendants, got %d", got)
	}
	if got := shape(v.Tree(TreeOptions{ListOptions: all, Folded: map[string]bool{"a": true, "x": true, "nosuch": true}})); got != "e@0 s@1 a@2 b@2 c@1 x@0" {
		t.Errorf("folding a leaf or an unknown id changes nothing: %s", got)
	}
	if got := v.Foldable(); !slices.Equal(got, []string{"e", "s"}) {
		t.Errorf("foldable = %v", got)
	}
}

func TestTreeFilterKeepsAncestorsDimmed(t *testing.T) {
	v := view(
		iss("e", "open", 1), iss("s", "open", 1, parent("e")), iss("hit", "open", 1, parent("s")), iss("miss", "open", 1, parent("s")),
		iss("other", "open", 1, parent("e")), iss("e2", "open", 1), iss("k2", "open", 1, parent("e2")),
	)
	rep := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll, Query: mustQuery(t, "id:hit")}})
	if got := shape(rep); got != "e@0* s@1* hit@2" {
		t.Errorf("a hit keeps its ancestors, dimmed, and nothing else: %s", got)
	}
	if rep.Matched != 1 || rep.Shown != 3 {
		t.Errorf("matched %d shown %d", rep.Matched, rep.Shown)
	}
	// An epic that matches keeps its place, not dimmed, and does not drag its children in.
	rep = v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll, Query: mustQuery(t, "id:s")}})
	if got := shape(rep); got != "e@0* s@1" {
		t.Errorf("matching parent: %s", got)
	}
	if rep := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll, Query: mustQuery(t, "id:nomatch")}}); len(rep.Rows) != 0 || rep.Matched != 0 {
		t.Errorf("no match, no rows: %s", shape(rep))
	}
}

func TestTreePresetKeepsClosedAncestorAsContext(t *testing.T) {
	v := view(iss("done", "closed", 1), iss("kid", "open", 1, parent("done")), iss("shut", "closed", 1, parent("done")))
	rep := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetOpen}})
	if got := shape(rep); got != "done@0* kid@1" {
		t.Errorf("open preset: a closed epic stays as context for its open child: %s", got)
	}
}

func TestTreeFoldsPausedWhileQuerying(t *testing.T) {
	v := view(iss("e", "open", 1), iss("hit", "open", 2, parent("e")), iss("s", "open", 1, parent("e")), iss("deep", "open", 1, parent("s")))
	folded := map[string]bool{"e": true, "s": true}
	q := ListOptions{Preset: PresetAll, Query: mustQuery(t, "id:deep")}
	rep := v.Tree(TreeOptions{ListOptions: q, Folded: folded})
	if got := shape(rep); got != "e@0* s@1* deep@2" || !rep.FoldsPaused {
		t.Errorf("a match is never hidden inside a fold: %s (paused %v)", got, rep.FoldsPaused)
	}
	rep = v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}, Folded: folded})
	if rep.FoldsPaused || shape(rep) != "e@0+" {
		t.Errorf("no query: folds apply: %s", shape(rep))
	}
}

func TestTreeProgressAndMatchesFlat(t *testing.T) {
	v := view(iss("e", "open", 1), iss("a", "closed", 1, parent("e")), iss("b", "open", 1, parent("e")), iss("c", "closed", 1, parent("e")))
	rep := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetOpen}})
	e := rep.Rows[0]
	if e.ChildDone != 2 || e.ChildTotal != 3 {
		t.Errorf("progress counts every direct child, shown or not: %d/%d", e.ChildDone, e.ChildTotal)
	}
}

// The tree shows exactly the tasks the list shows (plus dimmed context), for every preset, sort and a query.
func TestTreeAndFlatAgree(t *testing.T) {
	v := realTreeView(t)
	for _, p := range Presets {
		for _, k := range SortKeys {
			for _, qs := range []string{"", "label:ui", "!assignee:none", "ready:true", "kanban"} {
				o := ListOptions{Preset: p, Sort: Sort{Key: k, Desc: k.DefaultDesc()}, Query: mustQuery(t, qs)}
				flat := v.List(o)
				tree := v.Tree(TreeOptions{ListOptions: o})
				var got []string
				for _, r := range tree.Rows {
					if !r.Context {
						got = append(got, r.ID)
					}
				}
				want := ids(flat.Rows)
				slices.Sort(got)
				slices.Sort(want)
				if !slices.Equal(got, want) || tree.Matched != flat.Matched {
					t.Errorf("%s/%s/%q: tree %v, list %v", p, k, qs, got, want)
				}
			}
		}
	}
}

func TestTreeIsStableUnderInputOrder(t *testing.T) {
	snap := mustParseTree(t)
	want := shape(Build(snap, fixedNow).Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}}))
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 100; i++ {
		s := snap
		s.Issues = slices.Clone(snap.Issues)
		rng.Shuffle(len(s.Issues), func(a, b int) { s.Issues[a], s.Issues[b] = s.Issues[b], s.Issues[a] })
		if got := shape(Build(s, fixedNow).Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})); got != want {
			t.Fatalf("shuffle %d:\n got %s\nwant %s", i, got, want)
		}
	}
	// With a cycle in the parent links too: the cut is by id, never by input order.
	cyc := []core.TaskIssue{iss("a", "open", 1, parent("c")), iss("b", "open", 1, parent("a")), iss("c", "open", 1, parent("b")), iss("d", "open", 1, parent("c"))}
	base := shape(view(cyc...).Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}}))
	for i := 0; i < 50; i++ {
		c := slices.Clone(cyc)
		rng.Shuffle(len(c), func(a, b int) { c[a], c[b] = c[b], c[a] })
		if got := shape(view(c...).Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})); got != base {
			t.Fatalf("cycle shuffle: %s vs %s", got, base)
		}
	}
}

func mustParseTree(t *testing.T) core.TaskSnapshot {
	t.Helper()
	s, err := Parse(bytes.NewReader(fixture(t, "export-tree.jsonl")), fixedNow, "tracker")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func realTreeView(t *testing.T) *View { t.Helper(); return Build(mustParseTree(t), fixedNow) }

// The real bd export: two epics, one with two sub-epics, built with bd's own --parent.
func TestTreeRealExport(t *testing.T) {
	v := realTreeView(t)
	if !v.HasHierarchy() {
		t.Fatal("the tracker has parent-child links")
	}
	rep := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})
	if len(rep.Rows) != 14 || rep.Orphans != 0 || len(rep.ParentCycles) != 0 {
		t.Fatalf("rows %d orphans %d cycles %v", len(rep.Rows), rep.Orphans, rep.ParentCycles)
	}
	byID := map[string]core.TaskTreeRow{}
	for _, r := range rep.Rows {
		byID[r.ID] = r
	}
	for id, depth := range map[string]int{"tr-jbt": 0, "tr-jbt.1": 1, "tr-jbt.1.1": 2, "tr-jbt.2.1": 2, "tr-jbt.3": 1, "tr-09k": 0, "tr-09k.2": 1, "tr-u40": 0} {
		if byID[id].Depth != depth {
			t.Errorf("%s depth %d, want %d", id, byID[id].Depth, depth)
		}
	}
	if r := byID["tr-jbt"]; r.ChildTotal != 3 || r.ChildDone != 0 {
		t.Errorf("tr-jbt direct children %d/%d", r.ChildDone, r.ChildTotal)
	}
	if r := byID["tr-jbt.2"]; r.ChildTotal != 2 || r.ChildDone != 1 {
		t.Errorf("tr-jbt.2 progress %d/%d (one of two children is closed)", r.ChildDone, r.ChildTotal)
	}
	if r := byID["tr-09k"]; r.ChildTotal != 3 || r.ChildDone != 1 {
		t.Errorf("tr-09k progress %d/%d", r.ChildDone, r.ChildTotal)
	}
	// Siblings follow the list order, children never leave their parent, and a parent comes first.
	pos := map[string]int{}
	for i, r := range rep.Rows {
		pos[r.ID] = i
	}
	for _, r := range rep.Rows {
		if r.Parent != "" && pos[r.Parent] >= pos[r.ID] {
			t.Errorf("%s is drawn before its parent %s", r.ID, r.Parent)
		}
	}
	golden(t, "tree.golden.json", rep)
}

// What the tree calls ready is what the real `bd ready` listed for this tracker.
func TestTreeReadyMatchesBd(t *testing.T) {
	var ready []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(fixture(t, "ready-tree.json"), &ready); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, r := range ready {
		want[r.ID] = true
	}
	rep := realTreeView(t).Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})
	for _, r := range rep.Rows {
		if r.Ready != want[r.ID] {
			t.Errorf("%s: ready %v, bd ready %v", r.ID, r.Ready, want[r.ID])
		}
	}
}

func TestTreeHostileText(t *testing.T) {
	var buf bytes.Buffer
	for _, rec := range []string{
		`{"id":"e\u001b[2J","title":"\u001b]0;pwn\u0007epic\u202e","status":"open"}`,
		`{"id":"k","title":"kid\u0000\u009b","status":"open","dependencies":[{"depends_on_id":"e\u001b[2J","type":"parent-child"}]}`,
	} {
		buf.WriteString(rec + "\n")
	}
	snap, err := Parse(&buf, fixedNow, "t")
	if err != nil {
		t.Fatal(err)
	}
	rep := Build(snap, fixedNow).Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})
	if len(rep.Rows) != 2 || rep.Rows[1].Depth != 1 {
		t.Fatalf("the parent link survives sanitizing on both ends: %+v", rep.Rows)
	}
	for _, r := range rep.Rows {
		for _, f := range []string{r.ID, r.Title, r.Guide} {
			if strings.ContainsAny(f, "\x1b\x07\x00\u009b\u202e") {
				t.Errorf("%s: control or bidi character in %q", r.ID, f)
			}
		}
	}
}

func TestTreeDuplicateIdsUseOneRecord(t *testing.T) {
	v := view(iss("p1", "open", 1), iss("p2", "open", 1), iss("d", "open", 1, parent("p1"), blk("p1")), func() core.TaskIssue {
		l := iss("d", "open", 1, parent("p2"))
		l.Title = "later"
		return l
	}())
	rep := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})
	if got := shape(rep); got != "p1@0 p2@0 d@1" {
		t.Fatalf("%s", got)
	}
	if r := rep.Rows[2]; r.Title != "later" || r.Parent != "p2" || r.BlockedBy != 0 {
		t.Errorf("the row and everything derived for it come from the later record: %+v", r)
	}
}

func TestTreeBigTracker(t *testing.T) {
	const n = 5000
	issues := make([]core.TaskIssue, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("t%05d", i)
		if i < 50 {
			issues = append(issues, iss(id, "open", i%5))
			continue
		}
		issues = append(issues, iss(id, "open", i%5, parent(fmt.Sprintf("t%05d", (i*7)%i))))
	}
	start := time.Now()
	v := view(issues...)
	rep := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll}})
	rep2 := v.Tree(TreeOptions{ListOptions: ListOptions{Preset: PresetAll, Query: mustQuery(t, "t0499")}})
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("5000 tasks: build, tree and a filtered tree took %v", el)
	}
	if len(rep.Rows) != n || len(rep2.Rows) == 0 {
		t.Fatalf("rows %d / %d", len(rep.Rows), len(rep2.Rows))
	}
}

func TestHasHierarchy(t *testing.T) {
	if view(iss("a", "open", 1), iss("b", "open", 1, blk("a"))).HasHierarchy() {
		t.Error("blocking links are not a hierarchy")
	}
	if view(iss("a", "open", 1, parent("gone"))).HasHierarchy() {
		t.Error("a link to a missing parent shows nothing to fold")
	}
	if view(iss("a", "open", 1, parent("a"))).HasHierarchy() {
		t.Error("a task is not its own child")
	}
	if !view(iss("a", "open", 1), iss("b", "open", 1, parent("a"))).HasHierarchy() {
		t.Error("a child of a known parent is a hierarchy")
	}
	snap := core.TaskSnapshot{Issues: []core.TaskIssue{iss("a", "open", 1), iss("b", "open", 1, parent("a"))}, DepsUnknown: true}
	if Build(snap, fixedNow).HasHierarchy() {
		t.Error("a summary without dependency data claims no hierarchy")
	}
}

func TestProgress(t *testing.T) {
	for _, c := range []struct{ done, total, width, want int }{
		{0, 0, 4, 0}, {0, 5, 4, 0}, {5, 5, 4, 4}, {1, 100, 4, 1}, {99, 100, 4, 3}, {1, 2, 4, 2}, {3, 8, 4, 2}, {7, 8, 4, 3},
		{2, 3, 4, 3}, {9, 5, 4, 4}, {-1, 5, 4, 0}, {1, 2, 0, 0}, {1, 2, 1, 0},
	} {
		if got := Progress(c.done, c.total, c.width); got != c.want {
			t.Errorf("Progress(%d,%d,%d) = %d, want %d", c.done, c.total, c.width, got, c.want)
		}
	}
}
