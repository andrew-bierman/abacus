package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/graph"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// graphSnap is a real bd export (see internal/taskview/testdata/README.md).
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
}

// graphScenarios are the Graph view goldens: each focuses a task of the real export (with the
// hand-added cycle) from the list with a filter, then presses g.
func graphScenarios() []graphScenario {
	focus := func(id string) []string { return append(append([]string{"2", "/"}, typed(id)...), "enter", "g") }
	return []graphScenario{
		{"tasks-graph-diamond", focus("gr-ko8")},
		{"tasks-graph-chain", focus("gr-hsy")},
		{"tasks-graph-fanout", focus("gr-6yh")},
		{"tasks-graph-cycle", focus("gr-syt")},
		{"tasks-graph-hostile", focus("gr-67z")},
		{"tasks-graph-whole", append(focus("gr-ko8"), "g")},
	}
}

func TestGraphGoldens(t *testing.T) {
	th := asciiTheme()
	edges := graphSnap(t, "graph-export-edges.jsonl")
	for _, c := range graphScenarios() {
		t.Run(c.name, func(t *testing.T) {
			var b strings.Builder
			valid := true
			sc := scenario{name: c.name, tab: "Tasks"}
			for _, sz := range [][2]int{{100, 32}, {80, 24}, {60, 24}} {
				m := sized(graphShell(t, th, edges), sz[0], sz[1])
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
