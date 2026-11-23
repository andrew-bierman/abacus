package tui

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/graph"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// graphSnap reads a fixture export from internal/taskview/testdata (see its README: graph-export.jsonl
// is a real bd export, graph-export-edges.jsonl is that export plus a hand-added cycle, self-dependency
// and missing target).
func graphSnap(t testing.TB, name string) core.TaskSnapshot {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := taskview.Parse(bytes.NewReader(b), goldenNow, "gr")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// graphAt is a Tasks section over a snapshot, w x h in the ascii theme, on the Graph view.
func graphAt(t testing.TB, snap core.TaskSnapshot, w, h int, focus string) *tasksSection {
	t.Helper()
	s := newTasksSection(core.Providers{}, &config{})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: snap})
	if focus != "" {
		s.selected = focus
		s.reanchor(s.ids(), true)
	}
	s.list.preset, s.list.presetSet = taskview.PresetAll, true
	s.recompute()
	if focus != "" {
		s.selected = focus
		s.reanchor(s.ids(), true)
	}
	send(s, "g")
	return s
}

// engineInsights runs the in-process graph engine over a snapshot, as the Tasks tab does.
func engineInsights(t testing.TB, snap core.TaskSnapshot) core.InsightsReport {
	t.Helper()
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
	return rep
}

// graphShell is the whole shell on the Tasks tab with a snapshot and the engine's report for it.
func graphShell(t *testing.T, th Theme, snap core.TaskSnapshot) tea.Model {
	m := load(t, th, nil)
	m, _ = m.Update(taskSnapshotMsg{snap: snap})
	m, _ = m.Update(insightsMsg{report: engineInsights(t, snap)})
	return m
}

// ---- keys ----

// The Graph view gives each key one meaning in every layout: no duplicate, no clash with a global
// key, nothing on `w`, and `:` and `ctrl+k` stay unbound.
func TestGraphKeysDoNotCollide(t *testing.T) {
	global := map[string]string{}
	for name, b := range map[string]key.Binding{"quit": keyQuit, "next": keyNext, "prev": keyPrev, "jump": keyJump, "help": keyHelp} {
		for _, k := range b.Keys() {
			global[k] = name
		}
	}
	// keys the other Tasks views own: the Graph view must not take them over (g opens the graph from the list)
	listOnly := map[string]bool{"/": true, "s": true, "S": true, "f": true, "e": true, "x": true, "n": true, "o": true, "r": true}
	for name, setup := range map[string]func(*tasksSection){
		"neighborhood": func(*tasksSection) {},
		"whole":        func(s *tasksSection) { send(s, "g") },
	} {
		for _, w := range []int{60, 100, 120} {
			s := graphAt(t, graphSnap(t, "graph-export-edges.jsonl"), w, 30, "gr-ko8")
			setup(s)
			seen := map[string]bool{}
			for _, k := range keyStrings(s.Bindings()) {
				if seen[k] {
					t.Errorf("%s %d: key %q is bound twice", name, w, k)
				}
				seen[k] = true
				if g, clash := global[k]; clash && k != "esc" {
					t.Errorf("%s %d: key %q clashes with the global %s key", name, w, k, g)
				}
				if k == ":" || k == "ctrl+k" || k == "w" {
					t.Errorf("%s %d: %q is reserved", name, w, k)
				}
			}
			// the keys the Graph view handles itself
			own := []string{"up", "k", "down", "j", "enter", "g", "[", "]", "h", "l", "esc", "v", "V"}
			for _, k := range own {
				if listOnly[k] {
					t.Errorf("%q is a list key", k)
				}
			}
			for _, k := range []string{"enter", "g", "esc"} {
				if !seen[k] {
					t.Errorf("%s %d: footer lacks %q: %v", name, w, k, keyStrings(s.Bindings()))
				}
			}
		}
	}
	// g means "open the graph" on the list and "toggle whole" inside it: the list view still owns every list key
	s := listAt(100, 30)
	n := 0
	for _, k := range keyStrings(s.Bindings()) {
		if k == "g" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the list view binds g %d times", n)
	}
}

// ---- behavior ----

func sel(s *tasksSection) string { return s.graph.sel }

func TestGraphNavigationAndJump(t *testing.T) {
	snap := graphSnap(t, "graph-export-edges.jsonl")
	s := graphAt(t, snap, 100, 32, "gr-ko8")
	if s.list.cur != viewGraph || s.graph.focus != "gr-ko8" || sel(s) != "gr-ko8" {
		t.Fatalf("g opens the graph on the selected task: view %v focus %q sel %q", s.list.cur, s.graph.focus, sel(s))
	}
	send(s, "k") // the box above the focus
	if got := sel(s); got != "gr-nqy" && got != "gr-bac" {
		t.Fatalf("k moves up to a blocker: %q", got)
	}
	blocker := sel(s)
	send(s, "enter") // another node: refocus
	if s.graph.focus != blocker || s.list.cur != viewGraph {
		t.Fatalf("enter on another node refocuses: focus %q", s.graph.focus)
	}
	send(s, "enter") // the focus itself: open it in the list
	if s.list.cur != viewList || s.selected != blocker {
		t.Fatalf("enter on the focus jumps to the list: view %v selected %q", s.list.cur, s.selected)
	}
	// depth keys
	send(s, "g")
	send(s, "]", "]", "]", "]", "]", "]", "]")
	if s.graph.depth != 6 {
		t.Errorf("depth stops at 6: %d", s.graph.depth)
	}
	send(s, "[", "[", "[", "[", "[", "[", "[", "[")
	if s.graph.depth != 1 {
		t.Errorf("depth stops at 1: %d", s.graph.depth)
	}
	// esc goes back to the list, g again reopens on the list's task
	send(s, "esc")
	if s.list.cur != viewList {
		t.Errorf("esc leaves the graph")
	}
}

func TestGraphWholeEnterJumpsAndClearsAHidingFilter(t *testing.T) {
	s := graphAt(t, graphSnap(t, "graph-export-edges.jsonl"), 80, 24, "gr-ko8")
	send(s, "g") // whole graph
	if !s.graph.whole {
		t.Fatal("g toggles the whole graph")
	}
	// walk to a closed task, which the list's default quick filter hides
	s.graph.sel = "gr-9ik"
	s.list.preset, s.list.presetSet = taskview.PresetOpen, true
	s.recompute()
	send(s, "enter")
	if s.list.cur != viewList || s.selected != "gr-9ik" || s.rows()[s.cursor].ID != "gr-9ik" {
		t.Fatalf("enter jumps to the task: view %v selected %q", s.list.cur, s.selected)
	}
	if !strings.Contains(s.notice, "filter cleared") {
		t.Errorf("a filter that hid the task is cleared and says so: %q", s.notice)
	}
}

func TestGraphSelectionSurvivesARefresh(t *testing.T) {
	snap := graphSnap(t, "graph-export-edges.jsonl")
	s := graphAt(t, snap, 100, 32, "gr-ko8")
	send(s, "j")
	want := sel(s)
	rev := snap
	rev.Issues = append([]core.TaskIssue(nil), snap.Issues...)
	for i, j := 0, len(rev.Issues)-1; i < j; i, j = i+1, j-1 {
		rev.Issues[i], rev.Issues[j] = rev.Issues[j], rev.Issues[i]
	}
	s.Update(taskSnapshotMsg{snap: rev})
	s.View()
	if sel(s) != want || s.graph.focus != "gr-ko8" {
		t.Errorf("selection is anchored on the task id: %q, want %q (focus %q)", sel(s), want, s.graph.focus)
	}
}

func TestGraphWithoutDependenciesSaysSo(t *testing.T) {
	s := listAt(100, 30)
	s.list.depsUnknown = true
	s.list.cur = viewGraph
	if out := s.View(); !strings.Contains(out, "needs the whole tracker") {
		t.Errorf("recent-changes fallback: %s", out)
	}
}

// ---- goldens ----

type graphScenario struct {
	name string
	keys []string
	snap func(testing.TB) core.TaskSnapshot // nil: the edited real export
}

// graphScenarios are the Graph view goldens: each focuses a task of the real export (with the
// hand-added cycle) from the list with a filter, then presses g.
func graphScenarios() []graphScenario {
	focus := func(id string) []string { return append(append([]string{"2", "/"}, typed("id:"+id)...), "enter", "g") }
	return []graphScenario{
		{"tasks-graph-diamond", focus("gr-ko8"), nil},
		{"tasks-graph-chain", focus("gr-hsy"), nil},
		{"tasks-graph-fanout", focus("gr-6yh"), nil},
		{"tasks-graph-cycle", focus("gr-syt"), nil},
		{"tasks-graph-hostile", focus("gr-67z"), nil},
		{"tasks-graph-whole", append(focus("gr-ko8"), "g"), nil},
		// constructed shapes (not bd captures)
		{"tasks-graph-shortcut", focus("sc-f"), shortcutSnap},
		{"tasks-graph-bus", focus("bs-f"), busSnap},
		{"tasks-graph-longids", focus("tr-jbt.1.2.1"), longIDSnap},
	}
}

func TestGraphGoldens(t *testing.T) {
	th := asciiTheme()
	for _, c := range graphScenarios() {
		t.Run(c.name, func(t *testing.T) {
			var b strings.Builder
			valid := true
			sc := scenario{name: c.name, tab: "Tasks"}
			for _, sz := range [][2]int{{100, 32}, {80, 24}, {60, 24}} {
				snap := graphSnap(t, "graph-export-edges.jsonl")
				if c.snap != nil {
					snap = c.snap(t)
				}
				m := sized(graphShell(t, th, snap), sz[0], sz[1])
				for _, k := range c.keys {
					m = press(m, k)
				}
				out := m.View()
				if errs := invariantErrs(sc, out, sz[0], sz[1]); len(errs) > 0 {
					valid = false
					t.Errorf("%dx%d: %s", sz[0], sz[1], strings.Join(errs, "; "))
				}
				var lines []string
				for _, l := range strings.Split(out, "\n") {
					lines = append(lines, strings.TrimRight(l, " "))
				}
				fmt.Fprintf(&b, "=== %dx%d ===\n%s\n", sz[0], sz[1], strings.Join(lines, "\n"))
			}
			checkGolden(t, c.name+".ascii.golden", b.String(), valid)
		})
	}
}

// ---- width and hostile text ----

func graphHostileSnapshot() core.TaskSnapshot {
	evil := "\x1b[31mred\x1b[0m\x1b]0;title\x07 \u202eevil\u202c\r\ttab 日本語のとても長いタイトル " + strings.Repeat("w", 200)
	mk := func(id, title string, deps ...string) core.TaskIssue {
		it := core.TaskIssue{ID: id, Title: title, Status: "open", Priority: 2, Labels: []string{evil}}
		for _, d := range deps {
			it.Deps = append(it.Deps, core.TaskDep{DependsOn: d, Type: "blocks"})
		}
		return it
	}
	iss := []core.TaskIssue{mk("h-root", evil), mk("h-mid", evil, "h-root"), mk("h-self", evil, "h-self"), mk("h-miss", evil, "h-gone")}
	for i := 0; i < 30; i++ {
		iss = append(iss, mk(fmt.Sprintf("h-leaf%02d", i), evil, "h-mid"))
	}
	iss = append(iss, mk("h-cy1", evil, "h-cy2"), mk("h-cy2", evil, "h-cy1"))
	return core.TaskSnapshot{Issues: iss, ReadAt: goldenNow, Tracker: evil}
}

func TestGraphNeverOverflowsWithHostileText(t *testing.T) {
	snaps := map[string]core.TaskSnapshot{"hostile": graphHostileSnapshot(), "real": graphSnap(t, "graph-export-edges.jsonl")}
	for name, snap := range snaps {
		for _, w := range []int{40, 45, 59, 60, 61, 80, 99, 100, 101, 120, 140, 200, 300} {
			for _, h := range []int{12, 16, 24, 32, 50, 100} {
				for _, focus := range []string{"", "h-mid", "gr-ko8", "gr-6yh", "gr-syt"} {
					for _, whole := range []bool{false, true} {
						s := graphAt(t, snap, w, h, focus)
						if whole {
							send(s, "g")
						}
						for i := 0; i < 3; i++ {
							out := s.View()
							ls := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
							if len(ls) > h {
								t.Fatalf("%s %dx%d focus %q whole %v: %d lines", name, w, h, focus, whole, len(ls))
							}
							for _, l := range ls {
								if lipgloss.Width(l) > w {
									t.Fatalf("%s %dx%d focus %q whole %v: line %d cells wide: %q", name, w, h, focus, whole, lipgloss.Width(l), l)
								}
								for _, r := range l {
									if r < 0x20 && r != '\n' || r == 0x7f || (r >= 0x202a && r <= 0x202e) {
										t.Fatalf("%s %dx%d: control or bidi character %q in %q", name, w, h, r, l)
									}
								}
							}
							send(s, "j", "j", "l")
						}
					}
				}
			}
		}
	}
}

func TestGraphFiveThousandTasksRenderFast(t *testing.T) {
	var iss []core.TaskIssue
	for i := 0; i < 5000; i++ {
		it := core.TaskIssue{ID: fmt.Sprintf("t%04d", i), Title: fmt.Sprintf("task number %d", i), Status: "open", Priority: i % 5}
		if i > 0 {
			it.Deps = append(it.Deps, core.TaskDep{DependsOn: fmt.Sprintf("t%04d", (i*7)%i), Type: "blocks"})
		}
		if i%1000 == 999 { // a few hubs with a huge fan-out
			it.Deps = nil
		}
		iss = append(iss, it)
	}
	snap := core.TaskSnapshot{Issues: iss, ReadAt: goldenNow}
	start := time.Now()
	for _, w := range []int{60, 100} {
		s := graphAt(t, snap, w, 32, "t0000")
		s.View()
		send(s, "g")
		for i := 0; i < 5; i++ {
			send(s, "j", "j", "j")
			s.View()
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("5000 tasks: %v", d)
	}
}

// ---- constructed shapes ----

func tg(id, title, status string, blockers ...string) core.TaskIssue {
	it := core.TaskIssue{ID: id, Title: title, Status: status, Priority: 2, CreatedAt: goldenNow, UpdatedAt: goldenNow}
	for _, b := range blockers {
		it.Deps = append(it.Deps, core.TaskDep{DependsOn: b, Type: "blocks"})
	}
	return it
}

func snapOf(issues ...core.TaskIssue) core.TaskSnapshot {
	return core.TaskSnapshot{Issues: issues, ReadAt: goldenNow, Tracker: "shapes"}
}

// shortcutSnap: f <- a <- b <- c, and b blocks f directly. c is two steps above f by the shortest path.
func shortcutSnap(testing.TB) core.TaskSnapshot {
	return snapOf(tg("sc-c", "Root cause", "open"), tg("sc-b", "Middle step", "open", "sc-c"), tg("sc-a", "Side step", "open", "sc-b"), tg("sc-f", "Shortcut focus", "open", "sc-a", "sc-b"))
}

// busSnap: A->C, B->C, B->D, C->F, D->F: one bus would also seem to say A->D.
func busSnap(testing.TB) core.TaskSnapshot {
	return snapOf(tg("bs-a", "Blocker A", "open"), tg("bs-b", "Blocker B", "open"), tg("bs-c", "Dependent C", "open", "bs-a", "bs-b"), tg("bs-d", "Dependent D", "open", "bs-b"), tg("bs-f", "Bus focus", "open", "bs-c", "bs-d"))
}

// longIDSnap: real-length ids (tr-jbt.1.1.1) with the flags that must stay visible: a chain, a
// missing blocker and a cycle among three siblings above the focus.
func longIDSnap(testing.TB) core.TaskSnapshot {
	return snapOf(
		tg("tr-jbt.1.1.1", "Sibling on the chain", "open", "tr-jbt.1.0.1"),
		tg("tr-jbt.1.1.2", "Sibling with a missing blocker", "open", "tr-gone.9"),
		tg("tr-jbt.1.1.3", "Sibling in a cycle", "open", "tr-jbt.1.1.4"),
		tg("tr-jbt.1.1.4", "Cycle partner", "open", "tr-jbt.1.1.3"),
		tg("tr-jbt.1.0.1", "Chain root", "open"),
		tg("tr-jbt.1.2.1", "Focus with long id", "open", "tr-jbt.1.1.1", "tr-jbt.1.1.2", "tr-jbt.1.1.3"),
		tg("tr-jbt.1.3.1", "Dependent", "open", "tr-jbt.1.2.1"))
}

func boxText(s *tasksSection, gv core.GraphView, w int) []string {
	lines, _, _ := s.boxLines(gv, graphGeom(w, 0).listInner())
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// Flags are reserved before the title is cut: with real-length ids the box still shows * (chain),
// ? (missing blocker) and ~ (cycle).
func TestGraphBoxFlagsSurviveRealLengthIds(t *testing.T) {
	s := graphAt(t, longIDSnap(t), 140, 32, "tr-jbt.1.2.1")
	gv := s.graphModel()
	lines := boxText(s, gv, 140)
	want := map[string]string{"tr-jbt.1.1.1": "*", "tr-jbt.1.1.2": "?", "tr-jbt.1.1.3": "~"}
	for id, flag := range want {
		found := false
		for _, l := range lines {
			if strings.Contains(l, id) {
				found = true
				if !strings.Contains(l, flag) {
					t.Errorf("%s lost its %s flag: %q", id, flag, l)
				}
			}
		}
		if !found {
			t.Errorf("no box for %s:\n%s", id, strings.Join(lines, "\n"))
		}
	}
}

// At 100 columns three 12-character ids do not fit three boxes: two show whole and the third is
// counted ("+1 more"), never an id cut so that siblings look alike.
func TestGraphBoxesShowIdsWholeOrCollapse(t *testing.T) {
	s := graphAt(t, longIDSnap(t), 100, 32, "tr-jbt.1.2.1")
	gv := s.graphModel()
	text := strings.Join(boxText(s, gv, 100), "\n")
	for _, l := range gv.Layers {
		for _, id := range l.Nodes {
			if !strings.Contains(text, id) {
				t.Errorf("box for %s is cut:\n%s", id, text)
			}
		}
	}
	if gv.Hidden != 1 || !strings.Contains(text, "+1 more") {
		t.Errorf("hidden %d:\n%s", gv.Hidden, text)
	}
}

func TestGraphNodeTextNeverLosesFlagsOrOverflows(t *testing.T) {
	s := graphAt(t, longIDSnap(t), 100, 32, "tr-jbt.1.2.1")
	n := core.GraphNode{ID: "tr-jbt.1.1.1.1.1.1", Title: "a title", Status: "open", OnChain: true, InCycle: true, Bottleneck: true, Blocks: 2, BlockedBy: 1}
	for w := 1; w <= 40; w++ {
		styled, plain := s.nodeText(n, w, true)
		if lipgloss.Width(plain) > w || lipgloss.Width(styled) > w {
			t.Fatalf("w=%d overflows: %q", w, plain)
		}
		if w >= 12 && !strings.HasSuffix(strings.TrimSpace(plain), "*~^") {
			t.Errorf("w=%d dropped the flags: %q", w, plain)
		}
	}
}

// ---- connectors ----

// connectedPairs reads the connectors back off the canvas: which (blocker, dependent) pairs a line
// of box-drawing joins, found by following the cells from under each blocker.
func connectedPairs(bl boxLayout, gv core.GraphView) map[[2]string]bool {
	out := map[[2]string]bool{}
	layerOf := map[string]int{}
	for i, l := range gv.Layers {
		for _, id := range l.Nodes {
			layerOf[id] = i
		}
	}
	type cell struct{ x, y int }
	for _, e := range gv.Edges {
		_ = e
	}
	for id, p := range bl.pos {
		i := layerOf[id]
		if i+1 >= len(gv.Layers) || bl.c.mask[min(bl.bottom[i], len(bl.c.mask)-1)][p.cx]&dU == 0 {
			continue
		}
		seen := map[cell]bool{}
		stack := []cell{{p.cx, bl.bottom[i]}}
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[c] {
				continue
			}
			seen[c] = true
			m := bl.c.mask[c.y][c.x]
			if c.y == bl.layerY[i+1]-1 && m&dD != 0 {
				for cid, cp := range bl.pos {
					if layerOf[cid] == i+1 && cp.cx == c.x {
						out[[2]string{id, cid}] = true
					}
				}
			}
			step := func(bit, opp uint8, dx, dy int) {
				nx, ny := c.x+dx, c.y+dy
				if m&bit != 0 && ny >= 0 && ny < len(bl.c.mask) && nx >= 0 && nx < bl.c.w && bl.c.mask[ny][nx]&opp != 0 && ny < bl.layerY[i+1] {
					stack = append(stack, cell{nx, ny})
				}
			}
			step(dL, dR, -1, 0)
			step(dR, dL, 1, 0)
			step(dD, dU, 0, 1)
		}
	}
	return out
}

// No connector path joins a pair that is not a dependency, and every link the view says is drawn
// is joined unless the layout reported dropping it.
func TestGraphConnectorsJoinExactlyTheDrawnLinks(t *testing.T) {
	snaps := map[string]core.TaskSnapshot{
		"bus": busSnap(t), "shortcut": shortcutSnap(t), "longids": longIDSnap(t),
		"real": graphSnap(t, "graph-export-edges.jsonl"), "hostile": graphHostileSnapshot(),
	}
	r := rand.New(rand.NewSource(11))
	for k := 0; k < 60; k++ {
		n := 6 + r.Intn(12)
		var is []core.TaskIssue
		for i := 0; i < n; i++ {
			var bl []string
			for j := 0; j < i; j++ {
				if r.Intn(4) == 0 {
					bl = append(bl, fmt.Sprintf("r%02d", j))
				}
			}
			is = append(is, tg(fmt.Sprintf("r%02d", i), "random", "open", bl...))
		}
		snaps[fmt.Sprintf("random%d", k)] = snapOf(is...)
	}
	for name, snap := range snaps {
		for _, w := range []int{100, 120} {
			for _, it := range snap.Issues {
				if len(snap.Issues) > 40 && it.ID != "gr-ko8" && it.ID != "gr-6yh" && it.ID != "gr-syt" && it.ID != "gr-67z" {
					continue
				}
				s := graphAt(t, snap, w, 200, it.ID)
				gv := s.graphModel()
				if gv.Focus != it.ID {
					continue
				}
				bl := s.layoutBoxes(gv, graphGeom(w, 0).listInner())
				got := connectedPairs(bl, gv)
				drawn := map[[2]string]bool{}
				for _, e := range gv.Edges {
					if e.Drawn {
						drawn[[2]string{e.From, e.To}] = true
					}
				}
				for p := range got {
					if !drawn[p] {
						t.Fatalf("%s focus %s: connectors join %s and %s, which is not a drawn link", name, it.ID, p[0], p[1])
					}
				}
				missing := 0
				for p := range drawn {
					if !got[p] {
						missing++
					}
				}
				if missing != s.graph.undrawn {
					t.Fatalf("%s focus %s w%d: %d drawn links not joined, layout reported %d dropped", name, it.ID, w, missing, s.graph.undrawn)
				}
			}
		}
	}
}

// ---- invariants that do not depend on a golden ----

var moreRE = regexp.MustCompile(`\+(\d+) more`)

func TestGraphBoxInvariants(t *testing.T) {
	for name, snap := range map[string]core.TaskSnapshot{"real": graphSnap(t, "graph-export-edges.jsonl"), "bus": busSnap(t), "longids": longIDSnap(t), "shortcut": shortcutSnap(t)} {
		for _, it := range snap.Issues {
			if len(snap.Issues) > 40 && it.ID != "gr-6yh" && it.ID != "gr-ko8" && it.ID != "gr-67z" {
				continue
			}
			for _, w := range []int{100, 140} {
				s := graphAt(t, snap, w, 200, it.ID)
				gv := s.graphModel()
				lines := boxText(s, gv, w)
				text := strings.Join(lines, "\n")
				// the focus box is there: double border with its id
				focusRow := false
				for _, l := range lines {
					focusRow = focusRow || (strings.Contains(l, "║") && strings.Contains(l, gv.Focus))
				}
				if !focusRow {
					t.Errorf("%s %s w%d: no focus box\n%s", name, it.ID, w, text)
				}
				more := 0
				for _, m := range moreRE.FindAllStringSubmatch(text, -1) {
					n, _ := strconv.Atoi(m[1])
					more += n
				}
				if more != gv.Hidden {
					t.Errorf("%s %s w%d: +N more lines add up to %d, view says %d hidden\n%s", name, it.ID, w, more, gv.Hidden, text)
				}
			}
		}
	}
}

// ---- keys and behavior ----

func TestGraphWholeToNearbyFocusesTheSelectedNode(t *testing.T) {
	s := graphAt(t, graphSnap(t, "graph-export-edges.jsonl"), 100, 32, "gr-ko8")
	send(s, "g")
	if !s.graph.whole {
		t.Fatal("g opens the whole graph")
	}
	s.graph.sel = "gr-hsy"
	send(s, "g")
	if s.graph.whole || s.graph.focus != "gr-hsy" || s.graphModel().Focus != "gr-hsy" {
		t.Errorf("whole -> nearby: whole %v focus %q", s.graph.whole, s.graph.focus)
	}
}

func TestGraphStepTable(t *testing.T) {
	near := core.GraphView{Mode: core.GraphNeighborhood, Layers: []core.GraphLayer{{Layer: -1, Nodes: []string{"a", "b", "c"}}, {Layer: 0, Nodes: []string{"f"}}}}
	whole := core.GraphView{Mode: core.GraphWhole, Layers: []core.GraphLayer{{Nodes: []string{"a", "b"}}, {Layer: 1, Nodes: []string{"c"}}, {Layer: 2, Nodes: []string{"d", "e"}}}}
	for _, c := range []struct {
		name string
		gv   core.GraphView
		sel  string
		d    int
		want string
	}{
		{"near right", near, "a", 1, "b"}, {"near left", near, "c", -1, "b"},
		{"near left end", near, "a", -1, ""}, {"near right end", near, "c", 1, ""},
		{"near lone node", near, "f", 1, ""}, {"near not listed", near, "zz", 1, ""},
		{"whole next layer", whole, "a", 1, "c"}, {"whole previous layer", whole, "d", -1, "c"},
		{"whole first layer back", whole, "b", -1, ""}, {"whole last layer on", whole, "e", 1, ""},
	} {
		if got := graphStep(c.gv, c.sel, c.d); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// key level: l moves to the sibling box, h at the end of a layer stays put
	s := graphAt(t, graphSnap(t, "graph-export-edges.jsonl"), 100, 32, "gr-ko8")
	s.graph.sel = "gr-bac"
	send(s, "l")
	if s.graph.sel != "gr-nqy" {
		t.Errorf("l steps to the next sibling: %q", s.graph.sel)
	}
	send(s, "l")
	if s.graph.sel != "gr-nqy" {
		t.Errorf("l at the end of a layer stays: %q", s.graph.sel)
	}
	send(s, "h")
	if s.graph.sel != "gr-bac" {
		t.Errorf("h steps back: %q", s.graph.sel)
	}
}

func TestVIntoTheGraphFocusesTheSelectedTask(t *testing.T) {
	s := listAt(100, 32)
	send(s, "v")
	first := s.graph.focus
	send(s, "V") // back to the list
	send(s, "j", "j")
	want := s.rows()[s.cursor].ID
	send(s, "v")
	if s.list.cur != viewGraph || s.graph.focus != want || want == first {
		t.Errorf("v opens the graph on the selected task %q, got focus %q (first was %q)", want, s.graph.focus, first)
	}
}

func TestGraphLayerHeadersMakeNoClaims(t *testing.T) {
	s := graphAt(t, graphSnap(t, "graph-export-edges.jsonl"), 100, 40, "gr-ko8")
	send(s, "g")
	out := s.View()
	for _, bad := range []string{"nothing blocks", "waits for"} {
		if strings.Contains(out, bad) {
			t.Errorf("layer header claims %q", bad)
		}
	}
	if !strings.Contains(out, "layer 0") || !strings.Contains(out, "includes a cycle") {
		t.Errorf("headers:\n%s", out)
	}
}

func TestGraphPanelTitleShowsStaleReads(t *testing.T) {
	s := graphAt(t, graphSnap(t, "graph-export-edges.jsonl"), 100, 32, "gr-ko8")
	if strings.Contains(s.View(), "stale") {
		t.Fatal("a fresh read is not stale")
	}
	s.list.readAt = goldenNow.Add(-10 * time.Minute)
	if out := s.View(); !strings.Contains(out, "Graph · gr-ko8 · stale 10m") {
		t.Errorf("stale tag:\n%s", out)
	}
}

func TestGraphChainPanelShowsAClipMarker(t *testing.T) {
	var is []core.TaskIssue
	for i := 0; i < 30; i++ {
		var b []string
		if i > 0 {
			b = []string{fmt.Sprintf("ch-%02d", i-1)}
		}
		is = append(is, tg(fmt.Sprintf("ch-%02d", i), "step", "open", b...))
	}
	s := graphAt(t, snapOf(is...), 100, 16, "ch-15")
	out := s.View()
	chain := false
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, "│ │"); i >= 0 {
			chain = chain || strings.Contains(l[i:], "↕")
		}
	}
	if !chain {
		t.Errorf("a clipped Chain panel says so:\n%s", out)
	}
}

// ---- engine marks ----

type fakeInsights struct{ calls *int }

func (f fakeInsights) Insights(context.Context) (core.InsightsReport, error) {
	*f.calls++
	return core.InsightsReport{}, nil
}

func TestGraphDropsMarksFromAReportThatPredatesTheSnapshot(t *testing.T) {
	snap := graphSnap(t, "graph-export-edges.jsonl")
	s := graphAt(t, snap, 100, 32, "gr-ko8")
	s.insights = &core.InsightsReport{Bottlenecks: []core.Ranked{{ID: "gr-ko8", Value: 9}}}
	s.graph.insStart = goldenNow // as fresh as the snapshot
	if !strings.Contains(strings.Join(boxText(s, s.graphModel(), 100), "\n"), "^") {
		t.Fatal("a fresh report marks the bottleneck")
	}
	// a newer snapshot arrives: the report on hand describes the old one
	snap.ReadAt = goldenNow.Add(time.Minute)
	s.Update(taskSnapshotMsg{snap: snap})
	if gv := s.graphModel(); gv.MarksSource != "" || strings.Contains(strings.Join(boxText(s, gv, 100), "\n"), "^") {
		t.Errorf("a stale report must not mark the new snapshot: source %q", gv.MarksSource)
	}
	// and the section asks for a fresh read when the graph is on screen
	calls := 0
	s.ins = fakeInsights{&calls}
	s.insBusy = false
	_, cmd := s.Update(taskSnapshotMsg{snap: snap})
	if cmd == nil || !s.insBusy {
		t.Fatal("a stale report on the Graph view is read again")
	}
}

// ---- colors ----

// Without color the marks are text; with it, closed work is muted and the chain flag is accented.
func TestGraphColorsMuteClosedWorkAndAccentTheChain(t *testing.T) {
	th := NewTheme(rendererFor(termenv.TrueColor, true))
	for _, w := range []int{60, 100} {
		s := graphAt(t, graphSnap(t, "graph-export-edges.jsonl"), w, 40, "gr-ko8")
		s.SetTheme(th)
		out := s.View()
		var closed, chain string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "gr-9ik") && strings.Contains(l, "Snapshot") {
				closed = l
			}
			if strings.Contains(l, "gr-bac") && strings.Contains(l, "Sanitize") {
				chain = l
			}
		}
		if !regexp.MustCompile(`\x1b\[[0-9;]*m[^\x1b]*Snapshot`).MatchString(closed) {
			t.Errorf("w%d: the closed task's title is not styled: %q", w, closed)
		}
		if !regexp.MustCompile(`\x1b\[[0-9;]*m\*`).MatchString(chain) {
			t.Errorf("w%d: the chain flag is not accented: %q", w, chain)
		}
		plain := graphAt(t, graphSnap(t, "graph-export-edges.jsonl"), w, 40, "gr-ko8").View()
		if strings.Contains(plain, "\x1b") {
			t.Errorf("w%d: the ascii theme emitted an escape", w)
		}
	}
}
