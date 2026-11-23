package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// listAt is a Tasks section over the demo tracker, w x h, in the ascii theme.
func listAt(w, h int) *tasksSection {
	s := newTasksSection(core.Providers{}, &config{})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	return s
}

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func send(s *tasksSection, keys ...string) {
	for _, k := range keys {
		switch k {
		case "enter":
			s.Update(tea.KeyMsg{Type: tea.KeyEnter})
		case "esc":
			s.Update(tea.KeyMsg{Type: tea.KeyEsc})
		case "up":
			s.Update(tea.KeyMsg{Type: tea.KeyUp})
		case "down":
			s.Update(tea.KeyMsg{Type: tea.KeyDown})
		case "backspace":
			s.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		case "space":
			s.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		default:
			s.Update(keyRunes(k))
		}
	}
}

func typeIn(s *tasksSection, text string) {
	for _, r := range text {
		if r == ' ' {
			send(s, "space")
		} else {
			send(s, string(r))
		}
	}
}

func rowIDs(s *tasksSection) string {
	var ids []string
	for _, r := range s.rows() {
		ids = append(ids, r.ID)
	}
	return strings.Join(ids, " ")
}

// ---- keys ----

func keyStrings(bs []key.Binding) []string {
	var out []string
	for _, b := range bs {
		if b.Enabled() {
			out = append(out, b.Keys()...)
		}
	}
	return out
}

// Every Tasks mode gives each key one meaning: no duplicate within a mode, no clash with a global
// key, nothing on `w` but the workspace jump, and `:` and `ctrl+k` stay unbound (later units own them).
func TestTasksKeysDoNotCollide(t *testing.T) {
	global := map[string]string{}
	for name, b := range map[string]key.Binding{"quit": keyQuit, "next": keyNext, "prev": keyPrev, "jump": keyJump, "help": keyHelp} {
		for _, k := range b.Keys() {
			global[k] = name
		}
	}
	modes := map[string]func(s *tasksSection){
		"list":      func(s *tasksSection) {},
		"typing":    func(s *tasksSection) { send(s, "/") },
		"drilled":   func(s *tasksSection) { send(s, "enter") },
		"insights":  func(s *tasksSection) { send(s, "V") },
		"confirm":   func(s *tasksSection) { s.mode = modeConfirm },
		"form":      func(s *tasksSection) { s.mode = modeForm },
		"filtered":  func(s *tasksSection) { send(s, "/", "a", "enter") },
		"emptylist": func(s *tasksSection) { send(s, "/", "z", "z", "z", "z", "z", "z", "enter") },
	}
	for name, setup := range modes {
		for _, w := range []int{60, 120} {
			s := listAt(w, 30)
			setup(s)
			seen := map[string]bool{}
			for _, k := range keyStrings(s.Bindings()) {
				if seen[k] {
					t.Errorf("%s %d: key %q is bound twice", name, w, k)
				}
				seen[k] = true
				if g, clash := global[k]; clash && name != "confirm" && name != "form" && k != "esc" {
					t.Errorf("%s %d: key %q clashes with the global %s key", name, w, k, g)
				}
				if k == ":" || k == "ctrl+k" {
					t.Errorf("%s: %q is reserved for a later unit", name, k)
				}
				if k == "w" && name != "list" {
					t.Errorf("%s: w is the workspace jump only", name)
				}
			}
		}
	}
}

// ---- query and filter ----

func TestFilterTypingFiltersLiveEnterKeepsEscClears(t *testing.T) {
	s := listAt(100, 32)
	all := rowIDs(s)
	send(s, "/")
	if !s.Capturing() {
		t.Fatal("typing the filter captures keys (q, digits and ? are text now)")
	}
	typeIn(s, "label:ui")
	if got := rowIDs(s); got != "ac-15 ac-30 ac-33" {
		t.Fatalf("live filter: %q", got)
	}
	send(s, "enter")
	if s.Capturing() || s.list.text != "label:ui" || rowIDs(s) != "ac-15 ac-30 ac-33" {
		t.Fatalf("enter keeps the query and leaves typing: capturing=%v text=%q rows=%q", s.Capturing(), s.list.text, rowIDs(s))
	}
	send(s, "esc")
	if s.list.text != "" || rowIDs(s) != all {
		t.Fatalf("esc clears a kept query: %q rows %q", s.list.text, rowIDs(s))
	}
	send(s, "/")
	typeIn(s, "label:ui")
	send(s, "esc")
	if s.Capturing() || s.list.text != "" || rowIDs(s) != all {
		t.Fatalf("esc while typing clears and leaves typing: %q", s.list.text)
	}
}

func TestTypingTheFilterTakesGlobalKeysAsText(t *testing.T) {
	var m tea.Model = New([]core.Entry{{Module: tasksFake(), Detection: tasksFake().Det}}, WithTheme(asciiTheme()), WithFullView(noFullView), WithAudit(&memAudit{}))
	m = sized(m, 100, 30)
	m, _ = m.Update(tasksMsg{report: demo.TasksReport()})
	m, _ = m.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	m = press(press(m, "2"), "/")
	for _, k := range []string{"q", "3", "?"} {
		var cmd tea.Cmd
		m, cmd = m.Update(keyRunes(k))
		if cmd != nil {
			t.Fatalf("%q while typing must not quit", k)
		}
	}
	v := m.View()
	if !strings.Contains(v, "[Tasks]") || !strings.Contains(v, "/q3?") || strings.Contains(v, "Keys") {
		t.Fatalf("q, 3 and ? are text while typing:\n%s", v)
	}
}

func TestABadQueryKeepsTheLastGoodFilterAndSaysWhereItIsWrong(t *testing.T) {
	s := listAt(100, 32)
	send(s, "/")
	typeIn(s, "status:blocked")
	want := rowIDs(s)
	typeIn(s, " !status:")
	if s.list.qerr == nil || s.list.qerr.Col != 24 || !strings.Contains(s.list.qerr.Msg, "missing value") {
		t.Fatalf("a typed error with a column: %+v", s.list.qerr)
	}
	if got := rowIDs(s); got != want || want != "ac-25 ac-27" {
		t.Fatalf("the last good filter keeps filtering: %q (was %q)", got, want)
	}
	if v := s.View(); !strings.Contains(v, "missing value after status: (col 24)") {
		t.Fatalf("the error is shown on the query line:\n%s", v)
	}
	for i := 0; i < len(" !status:"); i++ {
		send(s, "backspace")
	}
	if s.list.qerr != nil || rowIDs(s) != want {
		t.Fatalf("fixing the query clears the error: %+v", s.list.qerr)
	}
}

func TestUpAndDownMoveTheCursorWhileTyping(t *testing.T) {
	s := listAt(100, 32)
	send(s, "/", "down", "down")
	if s.cursor != 2 || !s.list.typing {
		t.Fatalf("cursor %d typing %v", s.cursor, s.list.typing)
	}
}

func TestTheSelectionFollowsTheTaskNotTheRow(t *testing.T) {
	s := listAt(100, 32)
	send(s, "j", "j") // ac-15
	if s.selected != "ac-15" {
		t.Fatalf("selected %q", s.selected)
	}
	send(s, "s") // re-sort: the cursor stays on ac-15
	if s.selected != "ac-15" || s.rows()[s.cursor].ID != "ac-15" {
		t.Fatalf("a sort keeps the selected task: %q at %d", s.selected, s.cursor)
	}
	send(s, "/")
	typeIn(s, "label:leases")
	if s.rows()[s.cursor].ID != s.selected {
		t.Fatalf("a filter that keeps the task keeps the cursor on it")
	}
	// A new snapshot with the same task in another position.
	snap := fixtureSnapshot()
	for i, j := 0, len(snap.Issues)-1; i < j; i, j = i+1, j-1 {
		snap.Issues[i], snap.Issues[j] = snap.Issues[j], snap.Issues[i]
	}
	before := s.selected
	s.Update(taskSnapshotMsg{snap: snap})
	if s.selected != before || s.rows()[s.cursor].ID != before {
		t.Fatalf("a refresh keeps the selected task: %q", s.selected)
	}
}

// ---- sort and quick filters ----

func TestSortKeyCyclesThroughEightKeysAndSReversesIt(t *testing.T) {
	s := listAt(100, 32)
	seen := map[string]bool{}
	var prio string
	for i := 0; i < 8; i++ {
		seen[string(s.list.sort.Key)] = true
		if i == 0 {
			prio = rowIDs(s)
		}
		send(s, "s")
	}
	if len(seen) != 8 || s.list.sort.Key != taskview.SortPriority || rowIDs(s) != prio {
		t.Fatalf("eight presses of s come back to priority: %v %v", seen, s.list.sort)
	}
	send(s, "S")
	if !s.list.sort.Desc || !strings.HasPrefix(rowIDs(s), "ac-27") && !strings.HasPrefix(rowIDs(s), "ac-30") && !strings.HasPrefix(rowIDs(s), "ac-33") && !strings.HasPrefix(rowIDs(s), "ac-36") {
		t.Fatalf("S reverses: least urgent first, got %q", rowIDs(s))
	}
	if v := s.View(); !strings.Contains(v, "sort: priority ↓") {
		t.Fatalf("the direction is shown:\n%s", v)
	}
}

func TestQuickFilterCyclesAndKeepsTheQuery(t *testing.T) {
	s := listAt(100, 32)
	counts := map[taskview.Preset]int{}
	for i := 0; i < len(taskview.Presets); i++ {
		counts[s.list.preset] = s.list.rep.Matched
		send(s, "f")
	}
	if s.list.preset != taskview.PresetOpen {
		t.Fatalf("five presses of f come back to open: %q", s.list.preset)
	}
	want := map[taskview.Preset]int{taskview.PresetOpen: 12, taskview.PresetReady: 3, taskview.PresetBlocked: 6, taskview.PresetClosed: 2, taskview.PresetAll: 14}
	for p, n := range want {
		if counts[p] != n {
			t.Errorf("%s: %d tasks, want %d", p, counts[p], n)
		}
	}
	send(s, "/")
	typeIn(s, "type:feature")
	send(s, "enter", "f")
	if rowIDs(s) != "ac-15 ac-30" { // ready features; ac-09 is held and ac-27 is stored as blocked
		t.Fatalf("the query narrows the preset: %q", rowIDs(s))
	}
}

// ---- detail ----

func TestDetailShowsBlockersBothWaysChildrenAndComments(t *testing.T) {
	s := listAt(120, 50)
	send(s, "/")
	typeIn(s, "id:ac-20")
	send(s, "enter")
	v := s.View()
	for _, want := range []string{"Blocked by (1)", "ac-21 export reader", "Blocks (1)", "held by 1 unfinished"} {
		if !strings.Contains(v, want) {
			t.Errorf("ac-20: missing %q:\n%s", want, v)
		}
	}
	s2 := listAt(120, 50)
	send(s2, "j") // the epic
	v = s2.View()
	for _, want := range []string{"Children", "1/5", "ac-03 retire the old poller", "Goal", "• leases show who holds what", "60x24"} {
		if !strings.Contains(v, want) {
			t.Errorf("epic: missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "**") || strings.Contains(v, "```") || strings.Contains(v, "# Goal") {
		t.Errorf("light markdown is rendered, not shown raw:\n%s", v)
	}
	s3 := listAt(120, 50)
	if v := s3.View(); !strings.Contains(v, "Comments (2)") || !strings.Contains(v, "Reproduced. The tick interval") {
		t.Errorf("comments:\n%s", v)
	}
}

func TestDetailScrollsAndEscLeavesIt(t *testing.T) {
	s := listAt(60, 24)
	send(s, "enter")
	top := s.View()
	send(s, "j", "j", "j")
	if s.cursor != 0 || s.list.dOff != 3 {
		t.Fatalf("j scrolls the detail, not the cursor: cursor %d offset %d", s.cursor, s.list.dOff)
	}
	if s.View() == top {
		t.Fatal("the detail moved")
	}
	for i := 0; i < 80; i++ {
		send(s, "j")
	}
	end := s.View()
	if strings.Contains(end, "more") || !strings.Contains(end, "without a stale window") && !strings.Contains(end, "Reproduced") {
		t.Fatalf("at the end of the detail nothing is hidden:\n%s", end)
	}
	send(s, "esc")
	if s.drilled || s.cursor != 0 || s.list.dOff != 0 {
		t.Fatalf("esc returns to the list: drilled %v offset %d", s.drilled, s.list.dOff)
	}
	send(s, "j", "enter")
	if s.list.dOff != 0 {
		t.Fatalf("a new task starts at the top of its detail")
	}
}

func TestEscPeelsTheDetailBeforeTheQuery(t *testing.T) {
	s := listAt(60, 24)
	send(s, "/")
	typeIn(s, "label:ui")
	send(s, "enter", "enter")
	if !s.drilled {
		t.Fatal("enter drills in")
	}
	send(s, "esc")
	if s.drilled || s.list.text != "label:ui" {
		t.Fatalf("the first esc closes the detail only: drilled=%v text=%q", s.drilled, s.list.text)
	}
	send(s, "esc")
	if s.list.text != "" {
		t.Fatalf("the second esc clears the query: %q", s.list.text)
	}
}

func TestViewKeyCyclesViewsAndWritesStillWorkFromAnyView(t *testing.T) {
	s := listAt(100, 32)
	send(s, "v")
	if s.list.cur != viewGraph || !strings.Contains(s.View(), "List [Graph] Insights") {
		t.Fatal("v opens the next view, Graph")
	}
	send(s, "v")
	if s.list.cur != viewInsights || !strings.Contains(s.View(), "Graph [Insights]") {
		t.Fatal("v opens Insights")
	}
	send(s, "v")
	if s.list.cur != viewList {
		t.Fatal("v wraps to the list")
	}
	send(s, "V")
	if s.list.cur != viewInsights {
		t.Fatal("V goes back from the list to the last view")
	}
	send(s, "V", "V", "V")
	if s.list.cur != viewInsights {
		t.Fatal("V wraps")
	}
	send(s, "x") // close: still the gated flow, never a write
	if s.mode != modeVerify && s.mode != modeConfirm && s.mode != modeList {
		t.Fatalf("mode %v", s.mode)
	}
}

// ---- reading problems ----

func TestSnapshotFailureFallsBackToTheSummaryAndKeepsTheLastGoodRead(t *testing.T) {
	// A failure before any snapshot: the summary's recent changes, with the reason.
	s := newTasksSection(core.Providers{Source: nil}, &config{})
	s.SetTheme(asciiTheme())
	s.src = failingSource{}
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	s.snapBusy = true // a read is in flight
	s.Update(tasksMsg{report: demo.TasksReport()})
	if strings.Contains(s.View(), "ac-12") && !strings.Contains(s.View(), "loading") {
		t.Fatal("while a snapshot is on its way the list waits for it")
	}
	s.Update(taskSnapshotMsg{err: errors.New("export timed out")})
	if v := s.View(); !strings.Contains(v, "recent only") || !strings.Contains(v, "the full list could not be read: export timed out") {
		t.Fatalf("fallback with a reason:\n%s", v)
	}
	// A failure after a good read keeps the data and says so.
	g := listAt(100, 30)
	g.Update(taskSnapshotMsg{err: errors.New("export timed out")})
	if v := g.View(); !strings.Contains(v, "ac-33") || !strings.Contains(v, "refresh failed: export timed out (showing the last read)") {
		t.Fatalf("the last good read stays:\n%s", v)
	}
	g.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	if strings.Contains(g.View(), "refresh failed") {
		t.Fatal("a good read clears the failure")
	}
}

type failingSource struct{}

func (failingSource) Snapshot(context.Context) (core.TaskSnapshot, error) {
	return core.TaskSnapshot{}, errors.New("no")
}

func TestAStaleSnapshotIsMarkedAndAnIncompleteOneIsFlagged(t *testing.T) {
	s := listAt(100, 30)
	if strings.Contains(s.View(), "· stale") {
		t.Fatal("a fresh read is not stale")
	}
	old := nowFn
	defer func() { nowFn = old }()
	nowFn = func() time.Time { return goldenNow.Add(5 * time.Minute) }
	if v := s.View(); !strings.Contains(v, "· stale 5m") {
		t.Fatalf("a read older than two polls is marked stale:\n%s", v)
	}
	nowFn = old
	snap := fixtureSnapshot()
	snap.Warnings = []string{"ac-12: 2 blocking dependencies reported, 1 read"}
	s.Update(taskSnapshotMsg{snap: snap})
	if v := s.View(); !strings.Contains(v, "the read looks incomplete: ac-12: 2 blocking dependencies reported, 1 read") {
		t.Fatalf("a snapshot that may have lost records says so:\n%s", v)
	}
}

// ---- hostile text, width and chrome ----

func hostileSnapshot() core.TaskSnapshot {
	bad := "x\x1b[2J\x1b]0;pwn\x07\u202etxt\u200b\u200d\ufeff\x07y\x00日本語\u0085"
	return core.TaskSnapshot{ReadAt: goldenNow, Tracker: bad, Issues: []core.TaskIssue{
		{ID: bad, Title: bad, Status: bad, Type: bad, Assignee: bad, Labels: []string{bad, bad}, Priority: 1, Description: "# " + bad + "\n- " + bad + "\n```\n" + bad + "\n```\n**" + bad + "** `" + bad + "`",
			Design: bad, Notes: bad, Acceptance: bad, CloseReason: bad,
			Comments: []core.TaskComment{{Author: bad, Text: bad + "\n" + bad}},
			Deps:     []core.TaskDep{{DependsOn: bad, Type: "blocks"}, {DependsOn: "ok-1", Type: "parent-child"}}},
		{ID: "ok-1", Title: strings.Repeat("長い題名", 60), Status: "open", Priority: 0},
	}, Warnings: []string{bad}}
}

func TestHostileTextReachesNeitherTheListNorTheDetail(t *testing.T) {
	for _, w := range []int{40, 60, 100, 140} {
		for _, h := range []int{12, 24, 32} {
			s := newTasksSection(core.Providers{}, &config{})
			s.SetTheme(asciiTheme())
			s.Update(tea.WindowSizeMsg{Width: w, Height: h})
			s.Update(tasksMsg{report: core.TasksReport{Counts: core.TaskCounts{Total: 2}}})
			s.Update(taskSnapshotMsg{snap: hostileSnapshot()})
			for name, keys := range map[string][]string{"list": nil, "detail": {"enter"}, "second": {"j", "enter"}, "insights": {"V"}, "typing": {"/", "\x1b", "x"}} {
				c := listAtCopy(s)
				send(c, keys...)
				for i, l := range strings.Split(c.View(), "\n") {
					if lipgloss.Width(l) > w {
						t.Fatalf("%s %dx%d line %d is %d wide: %q", name, w, h, i, lipgloss.Width(l), l)
					}
					for _, r := range l {
						if unicode.IsControl(r) || r == '\u202e' || r == '\u200b' || r == '\u200d' || r == '\ufeff' {
							t.Fatalf("%s %dx%d: %U reaches the screen in %q", name, w, h, r, l)
						}
					}
				}
			}
		}
	}
}

// listAtCopy is a fresh section in the same state as s, so each key sequence starts from the same place.
func listAtCopy(s *tasksSection) *tasksSection {
	c := newTasksSection(core.Providers{}, &config{})
	c.SetTheme(asciiTheme())
	c.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
	c.Update(tasksMsg{report: core.TasksReport{Counts: core.TaskCounts{Total: 2}}})
	c.Update(taskSnapshotMsg{snap: hostileSnapshot()})
	return c
}

// A thousand tasks render and move in bounded time and rows.
func TestAThousandTasksRenderAndScrollWithinBounds(t *testing.T) {
	var issues []core.TaskIssue
	for i := 0; i < 1000; i++ {
		issues = append(issues, core.TaskIssue{ID: fmt.Sprintf("t-%04d", i), Title: fmt.Sprintf("task %d", i), Status: "open", Priority: i % 5, CreatedAt: goldenNow.Add(-time.Duration(i) * time.Hour)})
	}
	s := newTasksSection(core.Providers{}, &config{})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: core.TasksReport{Counts: core.TaskCounts{Total: 1000}}})
	start := time.Now()
	s.Update(taskSnapshotMsg{snap: core.TaskSnapshot{Issues: issues, ReadAt: goldenNow}})
	for i := 0; i < 200; i++ {
		send(s, "j")
		_ = s.View()
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("1000 tasks: build and 200 moves took %v", d)
	}
	if n := len(strings.Split(strings.TrimRight(s.View(), "\n"), "\n")); n > 32 {
		t.Fatalf("%d rows", n)
	}
}
