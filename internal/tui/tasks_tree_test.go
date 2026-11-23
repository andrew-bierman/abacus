package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// realTreeSnapshot is the real `bd export` of the tracker with two epics, sub-epics and
// cross-branch dependencies (internal/taskview/testdata/README.md says how it was captured).
func realTreeSnapshot(t testing.TB) core.TaskSnapshot {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", "export-tree.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := taskview.Parse(bytes.NewReader(b), goldenNow, "fleet")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// hostileTreeSnapshot goes through the real parser: terminal escapes and bidi controls in titles,
// wide runes, 200-character titles, a twelve-level chain, a parent cycle and an orphan.
func hostileTreeSnapshot(t testing.TB) core.TaskSnapshot {
	t.Helper()
	var buf bytes.Buffer
	add := func(id, title, parent string) {
		rec := map[string]any{"id": id, "title": title, "status": "open", "priority": 1, "issue_type": "task", "created_at": "2026-11-01T00:00:00Z", "updated_at": "2026-11-02T00:00:00Z"}
		if parent != "" {
			rec["dependencies"] = []map[string]string{{"issue_id": id, "depends_on_id": parent, "type": "parent-child"}}
		}
		b, _ := json.Marshal(rec)
		buf.Write(append(b, '\n'))
	}
	wide := strings.Repeat("漢字", 100)
	add("h00", "\x1b]0;owned\x07epic \u202eevil\u2066", "")
	for i := 1; i < 12; i++ {
		title := fmt.Sprintf("level %d \x1b[2J%s", i, wide)
		if i%2 == 0 {
			title = "plain " + strings.Repeat("x", 200)
		}
		add(fmt.Sprintf("h%02d", i), title, fmt.Sprintf("h%02d", i-1))
	}
	add("c1", "cycle one", "c2")
	add("c2", "cycle two", "c1")
	add("or", "orphan \x07", "nowhere")
	snap, err := taskview.Parse(&buf, goldenNow, "host\x1bile")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// treeSection is a Tasks section on a snapshot, 100x32 unless sized again, ascii theme.
func treeSection(t testing.TB, snap core.TaskSnapshot, st *core.TreeState) *tasksSection {
	t.Helper()
	s := newTasksSection(core.Providers{}, &config{treeState: st})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: snap})
	return s
}

func ids(s *tasksSection) []string {
	var out []string
	for _, r := range s.rows() {
		out = append(out, r.ID)
	}
	return out
}

const openTree = "tr-09k tr-09k.1 tr-09k.2 tr-jbt tr-jbt.1 tr-jbt.1.1 tr-jbt.1.3 tr-jbt.1.2 tr-jbt.2 tr-jbt.2.1 tr-jbt.3 tr-u40"

func TestTheTreeIsTheDefaultWhenThereAreChildrenAndTheListWhenThereAreNone(t *testing.T) {
	s := treeSection(t, realTreeSnapshot(t), nil)
	if !s.treeOn() || strings.Join(ids(s), " ") != openTree {
		t.Fatalf("tree by default, closed work hidden by the open preset: %v", ids(s))
	}
	flat := core.TaskSnapshot{ReadAt: goldenNow, Issues: []core.TaskIssue{{ID: "a", Title: "a", Status: "open"}, {ID: "b", Title: "b", Status: "open"}}}
	s = treeSection(t, flat, nil)
	if s.treeOn() {
		t.Fatal("no parent-child links: the flat list")
	}
	send(s, "T")
	if !strings.Contains(s.notice, "no parent-child links") || s.treeOn() {
		t.Errorf("T with nothing to fold says so and changes nothing: %q", s.notice)
	}
	send(s, "h", "l", "+", "-") // dead keys in the flat list, never a panic
	if got := strings.Join(ids(s), " "); got != "a b" {
		t.Errorf("flat rows: %q", got)
	}
}

func TestTBringsTheTreeAndTheListBackAndForth(t *testing.T) {
	s := treeSection(t, realTreeSnapshot(t), nil)
	send(s, "T")
	if s.treeOn() || len(s.rows()) != 12 || s.rows()[0].ID == "tr-09k" && s.rows()[1].ID == "tr-09k.1" {
		t.Fatalf("flat list is sorted by priority, not grouped: %v", ids(s))
	}
	send(s, "T")
	if !s.treeOn() || strings.Join(ids(s), " ") != openTree {
		t.Errorf("T again: %v", ids(s))
	}
}

func TestFoldKeysMoveFoldAndKeepTheSelectionOnATask(t *testing.T) {
	s := treeSection(t, realTreeSnapshot(t), nil)
	s.selectID("tr-jbt.1.1")
	send(s, "h") // a leaf: h goes up to the parent
	if s.selected != "tr-jbt.1" {
		t.Fatalf("h on a leaf selects its parent: %q", s.selected)
	}
	send(s, "h") // an expanded parent: h folds it
	if got := strings.Join(ids(s), " "); got != "tr-09k tr-09k.1 tr-09k.2 tr-jbt tr-jbt.1 tr-jbt.2 tr-jbt.2.1 tr-jbt.3 tr-u40" || s.selected != "tr-jbt.1" {
		t.Fatalf("folded tr-jbt.1: %q selected %q", got, s.selected)
	}
	send(s, "l") // a folded parent: l opens it
	if got := strings.Join(ids(s), " "); got != openTree || s.selected != "tr-jbt.1" {
		t.Fatalf("unfolded: %q", got)
	}
	send(s, "l") // an open parent: l goes to its first child
	if s.selected != "tr-jbt.1.1" {
		t.Fatalf("l on an open parent selects its first child: %q", s.selected)
	}
	send(s, "-") // collapse all: the selection moves to the nearest task still listed
	if got := strings.Join(ids(s), " "); got != "tr-09k tr-jbt tr-u40" || s.selected != "tr-jbt" {
		t.Fatalf("collapse all: %q selected %q", got, s.selected)
	}
	send(s, "+")
	if got := strings.Join(ids(s), " "); got != openTree || s.selected != "tr-jbt" {
		t.Fatalf("expand all: %q selected %q", got, s.selected)
	}
	s.selectID("tr-u40")
	send(s, "h", "l") // a root with no children: nothing to do
	if s.selected != "tr-u40" {
		t.Errorf("h and l on a lone root: %q", s.selected)
	}
}

func TestTheSelectionFollowsTheTaskAcrossRefreshFilterAndResize(t *testing.T) {
	s := treeSection(t, realTreeSnapshot(t), nil)
	s.selectID("tr-jbt.2.1")
	snap := realTreeSnapshot(t)
	slices.Reverse(snap.Issues) // the export's order is not stable: a refresh may shuffle it
	s.Update(taskSnapshotMsg{snap: snap})
	if s.selected != "tr-jbt.2.1" || ids(s)[s.cursor] != "tr-jbt.2.1" {
		t.Fatalf("refresh moved the selection: %q at %d", s.selected, s.cursor)
	}
	s.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	if ids(s)[s.cursor] != "tr-jbt.2.1" {
		t.Fatalf("resize moved the selection")
	}
	send(s, "/")
	typeIn(s, "queue")
	send(s, "enter")
	if got := strings.Join(ids(s), " "); got != "tr-jbt tr-jbt.2 tr-jbt.2.1" || ids(s)[s.cursor] != "tr-jbt.2.1" {
		t.Fatalf("a filter keeps the selected match and the branch above it: %q (cursor %d)", got, s.cursor)
	}
	send(s, "esc")
	if ids(s)[s.cursor] != "tr-jbt.2.1" || len(ids(s)) != 12 {
		t.Fatalf("clearing the filter keeps the selection: %v", ids(s))
	}
}

func TestAFilterKeepsAncestorsAndPausesFolds(t *testing.T) {
	s := treeSection(t, realTreeSnapshot(t), nil)
	s.selectID("tr-jbt")
	send(s, "h")
	if strings.Contains(strings.Join(ids(s), " "), "tr-jbt.1") {
		t.Fatalf("tr-jbt is folded: %v", ids(s))
	}
	send(s, "/")
	typeIn(s, "heartbeat")
	send(s, "enter")
	if got := strings.Join(ids(s), " "); got != "tr-jbt tr-jbt.1 tr-jbt.1.1" {
		t.Fatalf("a match is never hidden in a fold: %q", got)
	}
	s.selectID("tr-jbt")
	send(s, "h")
	if !strings.Contains(s.notice, "paused") || len(ids(s)) != 3 {
		t.Errorf("h while filtering says folds are paused: %q", s.notice)
	}
	v := s.View()
	if !strings.Contains(v, "folds paused") || !strings.Contains(v, "dim: context") {
		t.Errorf("the legend explains the paused folds and the dim rows:\n%s", v)
	}
	send(s, "esc")
	if got := strings.Join(ids(s), " "); !strings.Contains(got, "tr-jbt ") && !strings.HasSuffix(got, "tr-jbt") || strings.Contains(got, "tr-jbt.1") {
		t.Errorf("the fold is back once the query is cleared: %q", got)
	}
}

func TestFoldKeysAreIgnoredWhileScrollingTheDetail(t *testing.T) {
	s := treeSection(t, realTreeSnapshot(t), nil)
	s.selectID("tr-jbt")
	send(s, "enter") // 100 wide: the detail pane takes the keys
	send(s, "h", "-", "T")
	if !s.treeOn() || len(s.rows()) != 12 {
		t.Errorf("fold keys act on the list, not the detail pane: %v", ids(s))
	}
}

func TestFoldStateIsKeptPerTrackerInAbacusState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "tasks-tree.json")
	st := core.NewTreeState(path)
	a := realTreeSnapshot(t)
	s := treeSection(t, a, st)
	s.selectID("tr-jbt")
	send(s, "h")
	s.selectID("tr-09k")
	send(s, "h", "T") // fold, then choose the flat list
	if got := st.Load("name:fleet"); !slices.Equal(got.Folded, []string{"tr-09k", "tr-jbt"}) || got.View != "flat" {
		t.Fatalf("saved: %+v", got)
	}
	// The next run: same tracker, same choices.
	s2 := treeSection(t, a, st)
	if s2.treeOn() {
		t.Fatal("the flat choice survives a restart")
	}
	send(s2, "T")
	if got := strings.Join(ids(s2), " "); got != "tr-09k tr-jbt tr-u40" {
		t.Errorf("the folds survive a restart: %q", got)
	}
	send(s2, "T") // flat again, which is what is kept
	// Another tracker starts clean, and the first one's folds are untouched.
	b := realTreeSnapshot(t)
	b.Tracker = "other"
	s3 := treeSection(t, b, st)
	if !s3.treeOn() || strings.Join(ids(s3), " ") != openTree {
		t.Errorf("another tracker starts expanded as a tree: %v", ids(s3))
	}
	send(s3, "+") // nothing folded: no write that could clobber
	if got := st.Load("name:fleet"); len(got.Folded) != 2 {
		t.Errorf("tracker one lost its folds: %+v", got)
	}
	// A tracker that is re-pointed while running loads that tracker's choices.
	s3.Update(taskSnapshotMsg{snap: a})
	if s3.treeOn() {
		t.Error("back on tracker one the flat choice applies")
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("closed")) || !bytes.Contains(raw, []byte(`"schema_version": 1`)) {
		t.Errorf("the state file holds ids and choices only: %s", raw)
	}
}

func TestFoldsOfTasksThatNoLongerExistAreNotKept(t *testing.T) {
	st := core.NewTreeState(filepath.Join(t.TempDir(), "s.json"))
	_ = st.Save("name:fleet", core.TreeLayout{Folded: []string{"tr-jbt", "gone-1"}})
	s := treeSection(t, realTreeSnapshot(t), st)
	send(s, "+") // clears every fold, including the one for a task that is gone
	if got := st.Load("name:fleet"); len(got.Folded) != 0 {
		t.Errorf("expand all cleared the saved folds: %+v", got)
	}
	_ = st.Save("name:fleet", core.TreeLayout{Folded: []string{"tr-jbt", "gone-1"}})
	s = treeSection(t, realTreeSnapshot(t), st)
	s.selectID("tr-09k")
	send(s, "h")
	if got := st.Load("name:fleet"); !slices.Equal(got.Folded, []string{"tr-09k", "tr-jbt"}) {
		t.Errorf("a fold of a task that is gone is dropped when the state is next written: %+v", got.Folded)
	}
}

func TestAFailingStateWriteIsReportedAndTheFoldStillWorks(t *testing.T) {
	st := core.NewTreeState("relative/tasks-tree.json")
	s := treeSection(t, realTreeSnapshot(t), st)
	s.selectID("tr-jbt")
	send(s, "h")
	if !strings.Contains(s.notice, "could not keep the fold state") || strings.Contains(strings.Join(ids(s), " "), "tr-jbt.1") {
		t.Errorf("notice %q rows %v", s.notice, ids(s))
	}
}

func TestTreeRowsDrawBranchesBarsAndTheCursor(t *testing.T) {
	s := treeSection(t, realTreeSnapshot(t), nil)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	v := s.View()
	for _, want := range []string{"▾ Tasks workspace 1", "├── Tree view", "└── Doctor stale", "│ ├── Wire the", "▰▱▱▱"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	send(s, "-")
	if v := s.View(); !strings.Contains(v, "▹ Fleet workspace") || strings.Contains(v, "Leases surface") {
		t.Errorf("a folded epic shows ▹ and hides its children:\n%s", v)
	}
}

func TestTheProgressBarIsOnlyOnParentsAndDropsBeforeTheDependencyMarks(t *testing.T) {
	for _, w := range []int{40, 60, 100, 140} {
		s := treeSection(t, realTreeSnapshot(t), nil)
		s.Update(tea.WindowSizeMsg{Width: w, Height: 32})
		v := s.View()
		bars := strings.Count(v, "▰") + strings.Count(v, "▱")
		if w >= 60 && bars == 0 {
			t.Errorf("w=%d: epics show a bar:\n%s", w, v)
		}
		if !strings.Contains(v, "Tasks w") && !strings.Contains(v, "Tasks…") {
			t.Errorf("w=%d: the title column must never be the one dropped:\n%s", w, v)
		}
		if w == 40 && bars != 0 {
			t.Errorf("w=%d: narrow lists drop the bar and keep the marks:\n%s", w, v)
		}
		for _, l := range strings.Split(v, "\n") {
			if lipgloss.Width(l) > w {
				t.Errorf("w=%d: %q is %d wide", w, l, lipgloss.Width(l))
			}
		}
	}
}

func TestContextRowsAreDimmedAndMatchesAreNot(t *testing.T) {
	styled := regexp.MustCompile("\x1b\\[[0-9;]*m")
	titleStyled := func(v, title string) bool {
		for _, l := range strings.Split(v, "\n") {
			if i := strings.Index(l, title); i >= 0 {
				seq := styled.FindAllString(l[:i], -1) // the last style change before the title
				return len(seq) > 0 && seq[len(seq)-1] != "\x1b[0m"
			}
		}
		t.Fatalf("no row with %q:\n%s", title, v)
		return false
	}
	s := treeSection(t, realTreeSnapshot(t), nil)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	s.SetTheme(colorTheme())
	send(s, "down", "down", "down") // off the first row, so no row is drawn on the highlight
	if titleStyled(s.View(), "Tree view") {
		t.Fatal("an ordinary open task is not dimmed")
	}
	send(s, "/")
	typeIn(s, "heartbeat")
	send(s, "enter")
	s.selectID("tr-jbt.1.1") // the cursor row is drawn on the highlight, so look at the others
	if v := s.View(); !titleStyled(v, "Fleet workspace") || !titleStyled(v, "Leases surface") {
		t.Errorf("rows shown only as context are dimmed:\n%s", v)
	}
	s.selectID("tr-jbt")
	if v := s.View(); titleStyled(v, "Wire the") {
		t.Errorf("a match is not dimmed:\n%s", v)
	}
}

func TestTreeKeysHaveNoCollisions(t *testing.T) {
	for name, setup := range map[string]func(*tasksSection){
		"tree":   func(*tasksSection) {},
		"folded": func(s *tasksSection) { send(s, "-") },
		"flat":   func(s *tasksSection) { send(s, "T") },
	} {
		s := treeSection(t, realTreeSnapshot(t), nil)
		setup(s)
		seen := map[string]bool{}
		for _, k := range keyStrings(s.Bindings()) {
			if seen[k] {
				t.Errorf("%s: key %q is bound twice", name, k)
			}
			seen[k] = true
			for _, g := range []string{"q", "ctrl+c", "tab", "right", "shift+tab", "left", "1", "9", "?"} {
				if k == g {
					t.Errorf("%s: %q clashes with a global key", name, k)
				}
			}
		}
		if name != "flat" && (!seen["h"] || !seen["T"] || !seen["+"]) {
			t.Errorf("%s: the footer lists the tree keys: %v", name, keyStrings(s.Bindings()))
		}
		if name == "flat" && (seen["h"] || !seen["T"]) {
			t.Errorf("flat with a hierarchy offers T only: %v", keyStrings(s.Bindings()))
		}
	}
}

func TestHostileTreeNeverBreaksTheScreen(t *testing.T) {
	snap := hostileTreeSnapshot(t)
	for _, size := range [][2]int{{1, 1}, {10, 5}, {40, 12}, {60, 24}, {100, 32}, {140, 32}, {300, 100}} {
		s := treeSection(t, snap, nil)
		s.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		send(s, "j", "j", "j", "-", "+", "l", "h", "T", "T")
		for i, l := range strings.Split(s.View(), "\n") {
			if lipgloss.Width(l) > size[0] {
				t.Fatalf("%v line %d is %d wide: %q", size, i, lipgloss.Width(l), l)
			}
			if strings.ContainsAny(l, "\x1b\x07\u202e\u2066") {
				t.Fatalf("%v: control or bidi character reached the screen: %q", size, l)
			}
		}
	}
}

func TestBigTreeScrollsAndFoldsQuickly(t *testing.T) {
	const n = 5000
	snap := core.TaskSnapshot{ReadAt: goldenNow, Tracker: "big"}
	for i := 0; i < n; i++ {
		it := core.TaskIssue{ID: fmt.Sprintf("b%05d", i), Title: fmt.Sprintf("task %d", i), Status: "open", Priority: i % 5}
		if i >= 50 {
			it.Deps = []core.TaskDep{{DependsOn: fmt.Sprintf("b%05d", (i*7)%i), Type: "parent-child"}}
		}
		snap.Issues = append(snap.Issues, it)
	}
	s := treeSection(t, snap, nil)
	if len(s.rows()) != n {
		t.Fatalf("rows %d", len(s.rows()))
	}
	for i := 0; i < 20; i++ {
		send(s, "down")
	}
	send(s, "-", "+")
	if v := s.View(); !strings.Contains(v, "Tasks") {
		t.Fatal("renders")
	}
}

// treeScenarios are the golden renders of the tree: the real two-epic tracker (default, folded,
// filtered, flat) and a hostile, deeply nested one, each at the standard grid plus 60x24, 100x32
// and 140x32. They join scenarios(), so the width and chrome invariants run over them at every size.
func treeScenarios() []scenario {
	over := func(snap func(testing.TB) core.TaskSnapshot) func(*testing.T, Theme) tea.Model {
		return func(t *testing.T, th Theme) tea.Model {
			m := load(t, th, nil)
			m, _ = m.Update(taskSnapshotMsg{snap: snap(t)})
			return m
		}
	}
	real, hostile := over(realTreeSnapshot), over(hostileTreeSnapshot)
	sizes := append(append([][2]int(nil), framedSizes...), [2]int{140, 32})
	return []scenario{
		{name: "tasks-tree", build: real, keys: []string{"2"}, tab: "Tasks", extra: sizes},
		{name: "tasks-tree-folded", build: real, keys: []string{"2", "-"}, tab: "Tasks", extra: sizes},
		{name: "tasks-tree-filter", build: real, keys: append(append([]string{"2", "/"}, typed("lease")...), "enter"), tab: "Tasks", extra: sizes},
		{name: "tasks-tree-flat", build: real, keys: []string{"2", "T"}, tab: "Tasks", extra: sizes},
		{name: "tasks-tree-hostile", build: hostile, keys: []string{"2", "j", "j", "j", "j", "j"}, tab: "Tasks", extra: sizes},
	}
}

func TestFoldsSurviveARefreshWithoutAnyStoredState(t *testing.T) {
	s := treeSection(t, realTreeSnapshot(t), nil)
	s.selectID("tr-jbt")
	send(s, "h")
	for i := 0; i < 3; i++ { // the 30 s poll delivers a new snapshot of the same tracker
		s.Update(taskSnapshotMsg{snap: realTreeSnapshot(t)})
	}
	if got := strings.Join(ids(s), " "); got != "tr-09k tr-09k.1 tr-09k.2 tr-jbt tr-u40" || s.selected != "tr-jbt" {
		t.Errorf("a refresh must not unfold: %q selected %q", got, s.selected)
	}
}

// bd names the children of a child by appending to its id, so nested trackers have long ids. Asking
// the table for a whole id and the branch lines must never push the title out at a narrow width.
func TestLongDottedIdsNeverCostTheTitle(t *testing.T) {
	snap := core.TaskSnapshot{ReadAt: goldenNow, Tracker: "dots"}
	id := "tr-abcd"
	for i := 0; i < 6; i++ {
		it := core.TaskIssue{ID: id, Title: fmt.Sprintf("Title at level %d", i), Status: "open", Priority: 1}
		if i > 0 {
			it.Deps = []core.TaskDep{{DependsOn: id[:strings.LastIndex(id, ".")], Type: "parent-child"}}
		}
		snap.Issues = append(snap.Issues, it)
		id += ".1"
	}
	for _, w := range []int{40, 50, 60, 80, 100, 140} {
		s := treeSection(t, snap, nil)
		s.Update(tea.WindowSizeMsg{Width: w, Height: 32})
		v := s.View()
		if !strings.Contains(v, "Title") {
			t.Errorf("w=%d: no title in the list:\n%s", w, v)
		}
		for _, l := range strings.Split(v, "\n") {
			if lipgloss.Width(l) > w {
				t.Errorf("w=%d: %q is %d wide", w, l, lipgloss.Width(l))
			}
		}
	}
}
