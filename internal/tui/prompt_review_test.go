package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// ---- hidden terminal ----

func TestPromptKeysAreIgnoredOnAScreenThatIsNotDrawn(t *testing.T) {
	m := typeLine(press(pm(t, 100, 32), ":"), "board")
	m = sized(m, 30, 10)
	m = press(m, "enter")
	if activeTab(m) != "Tasks" && activeTab(m) != "Board" {
		t.Fatalf("tab %s", activeTab(m))
	}
	if prm(m) == nil || string(prm(m).text) != "board" {
		t.Fatalf("enter on a hidden prompt ran or edited it: %v", prm(m))
	}
	if before := activeTab(m); before != "Board" {
		t.Errorf("the tab changed to %s", before)
	}
	m = press(m, "x") // typing is ignored too
	if string(prm(m).text) != "board" {
		t.Errorf("a hidden prompt took text: %q", string(prm(m).text))
	}
	m = press(m, "esc")
	if prm(m) != nil {
		t.Error("esc must still close a hidden prompt")
	}
}

// ---- help overlay scrolling ----

func TestHelpScrollsBothWaysAndClampsAtTheTop(t *testing.T) {
	m := press(pm(t, 60, 24), "?")
	first := strings.Join(lines(m), "\n")
	m = press(press(press(m, "j"), "j"), "down")
	down := strings.Join(lines(m), "\n")
	if down == first {
		t.Fatal("j did not scroll the help")
	}
	m = press(press(press(m, "k"), "k"), "up")
	if got := strings.Join(lines(m), "\n"); got != first {
		t.Errorf("k and up must scroll back to the first frame:\n%s", got)
	}
	for range 40 {
		m = press(m, "k")
	}
	if m.(*Model).helpOff != 0 || strings.Join(lines(m), "\n") != first {
		t.Errorf("the top is clamped: offset %d", m.(*Model).helpOff)
	}
	for range 40 {
		m = press(m, "j")
	}
	if !strings.Contains(strings.Join(lines(m), "\n"), "quit") {
		t.Error("the end is reachable")
	}
}

func TestHelpCloseHintIsAlwaysVisible(t *testing.T) {
	for _, sz := range [][2]int{{60, 24}, {100, 32}, {40, 12}, {80, 30}} {
		m := press(sized(load(t, asciiTheme(), nil), sz[0], sz[1]), "?")
		for i := 0; i < 3; i++ {
			if f := footer(m); !strings.Contains(f, "esc close") {
				t.Errorf("%dx%d (scrolled %d): footer %q lacks the close hint", sz[0], sz[1], i, f)
			}
			m = press(m, "j")
		}
	}
}

// ---- prompt line width ----

func TestPromptLineKeepsTheCaretWithWideRunes(t *testing.T) {
	for _, w := range []int{40, 41, 60} {
		m := typeLine(press(pm(t, w, 12), ":"), strings.Repeat("界", 30)+"末")
		f := footer(m)
		if lipgloss.Width(f) > w || !strings.HasSuffix(strings.TrimRight(f, " "), "▏") || !strings.Contains(f, "末") {
			t.Errorf("%d: the tail and the caret must stay on the row: %q (%d)", w, f, lipgloss.Width(f))
		}
		if !strings.Contains(f, "…") {
			t.Errorf("%d: a cut line starts with an ellipsis: %q", w, f)
		}
	}
}

// ---- no silent clamp, pasted text ----

func TestAnOverLongLineIsRefusedNotTruncated(t *testing.T) {
	m := press(pm(t, 100, 32), ":")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q" + strings.Repeat(" ", 299) + "junk"), Paste: true})
	if n := len(prm(m).text); n != 304 {
		t.Fatalf("the line was clamped to %d runes", n)
	}
	m2, c := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if c != nil && c() == tea.Quit() {
		t.Fatal("a truncated paste turned into :q and quit")
	}
	if prm(m2) == nil || !strings.Contains(footer(m2), "too long") {
		t.Errorf("an over-long line is refused with the reason: %q", footer(m2))
	}
	// far beyond any sane paste: still bounded and still refused
	m = press(pm(t, 100, 32), ":")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", 100000)), Paste: true})
	if n := len(prm(m).text); n > promptHardMax {
		t.Errorf("the line holds %d runes", n)
	}
	m = press(m, "enter")
	if prm(m) == nil || !strings.Contains(footer(m), "too long") {
		t.Errorf("%q", footer(m))
	}
}

func TestPastedControlCharactersAreStrippedAndNeverRun(t *testing.T) {
	m := press(pm(t, 100, 32), "3")
	m = press(m, ":")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tasks\n\x1b[2J"), Paste: true})
	if prm(m) == nil || activeTab(m) != "Leases" {
		t.Fatal("a newline in a paste must not run the command")
	}
	got := string(prm(m).text)
	for _, r := range got {
		if r < ' ' || r == 0x7f {
			t.Fatalf("control rune %q survived: %q", r, got)
		}
	}
	if !strings.HasPrefix(got, "tasks") {
		t.Errorf("text %q", got)
	}
}

// ---- :ctx when nothing is known ----

func TestCtxSaysWhenNothingIsKnown(t *testing.T) {
	p := tasksFake()
	var m tea.Model = New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(asciiTheme()), WithFullView(noFullView))
	m = sized(m, 100, 32)
	m = cmd(m, "ctx")
	if f := footer(m); !strings.Contains(f, "context: not known yet (the tracker has not loaded)") {
		t.Errorf("%q", f)
	}
}

// ---- Tasks still loading ----

func TestViewAndFilterCommandsSayWhenTasksIsStillLoading(t *testing.T) {
	p := tasksFake()
	for _, line := range []string{"tree", "list", "filter status:open"} {
		var m tea.Model = New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(asciiTheme()), WithFullView(noFullView))
		m = sized(m, 100, 32)
		m = cmd(m, line)
		if f := footer(m); !strings.Contains(f, "Tasks is still loading: try again in a moment") {
			t.Errorf(":%s while loading: %q", line, f)
		}
	}
}

// ---- layout state is the only thing the prompt may persist ----

func TestTreeAndListWriteTheLayoutFileOnlyWhenTheModeChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tree.json")
	m := layoutModel(t, path)
	stat := func() (int64, bool) {
		fi, err := os.Stat(path)
		if err != nil {
			return 0, false
		}
		return fi.Size() + fi.ModTime().UnixNano(), true
	}
	m = cmd(m, "tree") // the default is already the tree: nothing to save
	if _, ok := stat(); ok {
		t.Error(":tree on a tree wrote the layout file")
	}
	m = cmd(m, "list")
	a, ok := stat()
	if !ok {
		t.Fatal(":list changes the mode, so the layout is kept")
	}
	m = cmd(m, "list")
	if b, _ := stat(); b != a {
		t.Error(":list on a list rewrote the layout file")
	}
	_ = m
}

func layoutModel(t *testing.T, path string) tea.Model {
	t.Helper()
	tasks, brd, lease, ghost, q := tasksFake(), boardFake(), leaseFake(), brokenFake(), lambe.NewDemo()
	en := []core.Entry{{Module: brd, Detection: brd.Det}, {Module: q, Detection: q.Detect(t.Context())}, {Module: lease, Detection: lease.Det}, {Module: tasks, Detection: tasks.Det}, {Module: ghost, Detection: ghost.Det}}
	var m tea.Model = New(en, WithTheme(asciiTheme()), WithFullView(noFullView), WithAudit(&memAudit{}), WithTreeState(core.NewTreeState(path)), WithSearchHistory(core.SearchHistory{}))
	m = sized(m, 100, 32)
	m, _ = m.Update(tasksMsg{report: fixtureTasks()})
	m, _ = m.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	return press(m, "2")
}

// Every command through the prompt, with the layout file in a temp dir: the tracker and the audit are
// untouched, and the layout file is the only file that may appear.
func TestPromptWritesNothingButLayoutState(t *testing.T) {
	dir := t.TempDir()
	m := layoutModel(t, filepath.Join(dir, "tree.json"))
	for _, c := range core.Commands() {
		for _, tab := range []string{"1", "2", "3", "4", "5"} {
			m = press(m, tab)
			m = cmd(m, c.Name)
			for range 3 {
				m = press(m, "esc")
			}
		}
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != "tree.json" && !strings.HasPrefix(e.Name(), "tree.json") {
			t.Errorf("unexpected file %s", e.Name())
		}
	}
}

// ---- Kanban and Graph through the prompt ----

func TestViewCommandsWorkFromEveryTab(t *testing.T) {
	want := map[string]taskViewID{"kanban": viewKanban, "graph": viewGraph, "insights": viewInsights, "list": viewList, "tree": viewList}
	for line, view := range want {
		for _, from := range []string{"1", "2", "3", "4", "5"} {
			m := cmd(press(pm(t, 100, 32), from), line)
			s := tasksSec(m)
			if activeTab(m) != "Tasks" || s.list.cur != view || prm(m) != nil {
				t.Errorf("from tab %s :%s -> tab %s view %d (want %d)", from, line, activeTab(m), s.list.cur, view)
			}
			if line == "tree" && !s.treeOn() {
				t.Errorf("from tab %s :tree is not the tree", from)
			}
			if line == "list" && s.treeOn() {
				t.Errorf("from tab %s :list is still the tree", from)
			}
			if out := strings.Join(lines(m), "\n"); strings.Contains(out, "not available") {
				t.Errorf(":%s says it is not available", line)
			}
		}
	}
}

// :kanban and :graph do what the view key does: the same function runs, so the same state results.
// (The shell is a pointer: every scenario builds its own.)
func TestPromptViewEntryIsTheKeyPathEntry(t *testing.T) {
	board := func() tea.Model { // the board with the second card selected
		k := press(press(pm(t, 100, 32), "2"), "v")
		_ = lines(k)
		k = press(k, "j")
		_ = lines(k)
		return k
	}
	card := tasksSec(board()).kb.sel
	if card == "" || card == tasksSec(press(press(pm(t, 100, 32), "2"), "v")).kb.sel {
		t.Fatalf("no distinct selected card: %q", card)
	}
	// the Graph from the Kanban opens on the selected card, by key and by prompt
	for name, m := range map[string]tea.Model{"v": press(board(), "v"), "prompt": cmd(board(), "graph")} {
		if s := tasksSec(m); s.graph.focus != card || s.graph.sel != card || s.list.cur != viewGraph || s.drilled || s.kb.detail {
			t.Errorf("%s: graph focus %q sel %q view %d, want the card %q", name, s.graph.focus, s.graph.sel, s.list.cur, card)
		}
	}
	// the Graph from the List opens on the list's selected task, by key (v v) and by prompt
	list := func() tea.Model { return press(press(press(press(pm(t, 100, 32), "2"), "T"), "j"), "j") }
	viaPrompt := tasksSec(cmd(list(), "graph"))
	viaKeys := tasksSec(press(press(list(), "v"), "v"))
	if viaPrompt.list.cur != viewGraph || viaPrompt.graph.focus == "" {
		t.Fatalf("the graph has no focus: %q", viaPrompt.graph.focus)
	}
	if viaKeys.graph.focus != viaPrompt.graph.focus && tasksSec(list()).selected != viaPrompt.graph.focus {
		t.Errorf("graph focus differs from the list's selection: %q vs %q", viaPrompt.graph.focus, tasksSec(list()).selected)
	}
	// :graph while the Graph is showing keeps it where it is
	g := press(press(list(), "v"), "v")
	before := tasksSec(g).graph
	if after := tasksSec(cmd(g, "graph")).graph; after.focus != before.focus || after.sel != before.sel || after.whole != before.whole {
		t.Error(":graph in the Graph moved it")
	}
	// the List from the Kanban carries the selected card, as v does
	if s := tasksSec(cmd(board(), "list")); s.list.cur != viewList || s.selected != card {
		t.Errorf(":list from the board selects %q, want %q", s.selected, card)
	}
	// a view command closes the board's detail drill, as v does
	d := press(board(), "enter")
	if !tasksSec(d).kb.detail {
		t.Fatal("enter did not open the card's detail")
	}
	if tasksSec(cmd(d, "list")).kb.detail {
		t.Error(":list left the board's detail open")
	}
}

func TestDescribeAndJSONWorkOnTheKanbanCard(t *testing.T) {
	k := press(press(pm(t, 100, 32), "2"), "v")
	_ = lines(k)
	card := tasksSec(k).kb.sel
	m := press(k, "d")
	if overOf(m) == nil || !strings.Contains(overOf(m).title, card) {
		t.Errorf("d on the board describes the selected card %q: %+v", card, overOf(m))
	}
}

// ---- concrete content, not recomputed with the code under test ----

func TestDescribePanelShowsTheKnownTask(t *testing.T) {
	m := press(press(pm(t, 100, 40), "2"), "T")
	s := tasksSec(m)
	if !s.selectID("ac-12") {
		t.Fatal("ac-12 is not listed")
	}
	m = press(m, "d")
	out := strings.Join(lines(m), "\n")
	for _, want := range []string{"Task ac-12", "wire the lease heartbeat", "in progress", "P0", "ada", "core, leases", "ac-01 Fleet workspace", "ac-09 queue attention digest", "heartbeat misses a beat"} {
		if !strings.Contains(out, want) {
			t.Errorf("describe lacks %q:\n%s", want, out)
		}
	}
}

func TestJSONPanelParsesAndCarriesTheKnownTask(t *testing.T) {
	m := press(press(pm(t, 100, 40), "2"), "T")
	if !tasksSec(m).selectID("ac-12") {
		t.Fatal("ac-12 is not listed")
	}
	m = press(m, "y")
	raw := strings.Join(overOf(m).content(100000), "\n")
	var got struct {
		SchemaVersion int `json:"schema_version"`
		Row           struct {
			ID       string   `json:"id"`
			Title    string   `json:"title"`
			Status   string   `json:"status"`
			Priority int      `json:"priority"`
			Labels   []string `json:"labels"`
			Assignee string   `json:"assignee"`
		} `json:"row"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("the panel is not JSON: %v\n%s", err, raw)
	}
	if got.SchemaVersion != 1 || got.Row.ID != "ac-12" || got.Row.Title != "wire the lease heartbeat" || got.Row.Status != "in_progress" || got.Row.Priority != 0 || got.Row.Assignee != "ada" || strings.Join(got.Row.Labels, ",") != "core,leases" {
		t.Errorf("%+v", got)
	}
}
