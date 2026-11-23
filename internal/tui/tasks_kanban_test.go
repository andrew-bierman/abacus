package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// kbAt is the Kanban view over the demo tracker with a writer that records what it is asked to run.
func kbAt(w, h int) (*tasksSection, taskProv) {
	p := tasksFake()
	s := newTasksSection(core.Providers{Writer: p, Reader: p}, &config{audit: &memAudit{}})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	send(s, "v")
	return s, p
}

func selID(s *tasksSection) string {
	_, _, c, ok := s.place(shownColumns(s.board()))
	if !ok {
		return ""
	}
	return c.ID
}

func TestKanbanOpensOnTheFirstCardAndHAndLChangeColumnJAndKMoveCards(t *testing.T) {
	s, _ := kbAt(100, 32)
	if s.list.cur != viewKanban || selID(s) != "ac-01" {
		t.Fatalf("view %v, selected %q", s.list.cur, selID(s))
	}
	send(s, "j", "j")
	if selID(s) != "ac-09" {
		t.Fatalf("j j: %q", selID(s))
	}
	send(s, "k", "k", "k")
	if selID(s) != "ac-01" {
		t.Fatalf("k stops at the top: %q", selID(s))
	}
	send(s, "j", "j", "l")
	if selID(s) != "ac-33" { // the row index carries over to the next column, clamped to its last card
		t.Fatalf("l: %q", selID(s))
	}
	send(s, "h", "h")
	if selID(s) != "ac-09" {
		t.Fatalf("h stops at the first column: %q", selID(s))
	}
	send(s, "l", "l", "l", "l", "l")
	if _, ci, _ := cols(s); ci != 3 {
		t.Fatalf("l stops at the last column: %d", ci)
	}
}

func cols(s *tasksSection) ([]core.KanbanColumn, int, core.KanbanCard) {
	c := shownColumns(s.board())
	ci, _, card, _ := s.place(c)
	return c, ci, card
}

// Left and right stay tab navigation: the board never takes them.
func TestKanbanLeavesTheArrowKeysToTabNavigation(t *testing.T) {
	s, _ := kbAt(100, 32)
	for _, k := range []tea.KeyType{tea.KeyLeft, tea.KeyRight, tea.KeyTab} {
		if _, ok := s.listKey(tea.KeyMsg{Type: k}); ok {
			t.Errorf("key %v handled by the board", k)
		}
	}
}

// The selection is a task id: a refresh that moves the card to another column keeps the cursor on it,
// and a card that disappears lands the cursor on the card now at its place, never off the board.
func TestKanbanSelectionIsAnchoredOnTheTaskIDAcrossRefreshes(t *testing.T) {
	s, _ := kbAt(100, 32)
	send(s, "l") // ac-12 in progress, P0
	if selID(s) != "ac-12" {
		t.Fatal(selID(s))
	}
	snap := fixtureSnapshot()
	for i := range snap.Issues {
		if snap.Issues[i].ID == "ac-12" {
			snap.Issues[i].Status = "blocked"
		}
	}
	s.Update(taskSnapshotMsg{snap: snap})
	if _, ci, c := cols(s); c.ID != "ac-12" || ci != 2 {
		t.Fatalf("the cursor follows ac-12 to Blocked: %s in column %d", c.ID, ci)
	}
	var keep []core.TaskIssue
	for _, it := range snap.Issues {
		if it.ID != "ac-12" {
			keep = append(keep, it)
		}
	}
	snap.Issues = keep
	s.Update(taskSnapshotMsg{snap: snap})
	if id := selID(s); id == "" || id == "ac-12" {
		t.Fatalf("a vanished card hands the cursor to a neighbor in its column: %q", id)
	}
	if v := s.View(); !strings.Contains(v, "▸ ac-") {
		t.Fatalf("the cursor is drawn:\n%s", v)
	}
}

func TestKanbanZShowsClosedAndSCyclesTheGrouping(t *testing.T) {
	s, _ := kbAt(100, 32)
	if got := s.board(); got.ClosedHidden != 2 {
		t.Fatalf("closed is folded: %+v", got.ClosedHidden)
	}
	send(s, "Z")
	if r := s.board(); r.ClosedHidden != 0 || len(shownColumns(r)) != 5 {
		t.Fatalf("Z shows the closed column: %d hidden, %d columns", r.ClosedHidden, len(shownColumns(r)))
	}
	send(s, "Z", "s")
	if s.board().Group != "priority" {
		t.Fatalf("s: %q", s.board().Group)
	}
	send(s, "s")
	if s.board().Group != "type" {
		t.Fatalf("s s: %q", s.board().Group)
	}
	send(s, "Z")
	if !strings.Contains(s.notice, "status grouping") {
		t.Errorf("Z outside the status grouping explains itself: %q", s.notice)
	}
	send(s, "s")
	if s.board().Group != "status" {
		t.Fatalf("s wraps back to status: %q", s.board().Group)
	}
}

func TestKanbanUsesTheListQueryAndEscClearsIt(t *testing.T) {
	s, _ := kbAt(100, 32)
	send(s, "/")
	typeIn(s, "label:ui")
	send(s, "enter")
	r := s.board()
	if r.Shown != 3 || r.Query != "label:ui" { // ac-15, ac-30, ac-33
		t.Fatalf("shown %d query %q", r.Shown, r.Query)
	}
	if v := s.View(); !strings.Contains(v, "/label:ui") || !strings.Contains(v, "3 of 14") {
		t.Fatalf("the query line:\n%s", v)
	}
	send(s, "esc")
	if s.list.text != "" || s.board().Shown != 12 {
		t.Fatalf("esc clears the query: %q %d", s.list.text, s.board().Shown)
	}
}

func TestKanbanEnterShowsTheCardInFullAndEscReturns(t *testing.T) {
	s, _ := kbAt(60, 24)
	send(s, "enter")
	v := s.View()
	if !s.kb.detail || !strings.Contains(v, "ac-01") || !strings.Contains(v, "Fleet workspace") || strings.Contains(v, "╮╭─") {
		t.Fatalf("detail:\n%s", v)
	}
	send(s, "esc")
	if s.kb.detail || !strings.Contains(s.View(), "[Open 6]") {
		t.Fatal("esc returns to the columns")
	}
}

func TestKanbanLayoutSwitchesAtOneHundredColumns(t *testing.T) {
	side, _ := kbAt(100, 32)
	if v := side.View(); !strings.Contains(v, "╮╭─ In progress") || strings.Contains(v, "‹") {
		t.Fatalf("100 columns: side by side\n%s", v)
	}
	narrow, _ := kbAt(99, 32)
	v := narrow.View()
	if strings.Contains(v, "╮╭─") || !strings.Contains(v, "[Open 6]") || !strings.Contains(v, "In progress 3") {
		t.Fatalf("99 columns: a tab strip with one column\n%s", v)
	}
	send(narrow, "l")
	if v := narrow.View(); !strings.Contains(v, "[In progress 3]") || !strings.Contains(v, "▸ ac-12") {
		t.Fatalf("l changes the column in the strip:\n%s", v)
	}
}

// ---- card moves ----

func ranMoves(p taskProv) string {
	var out []string
	for _, a := range *p.ran {
		out = append(out, strings.Join(a, " "))
	}
	return strings.Join(out, "; ")
}

func settleVerify(t *testing.T, s *tasksSection, task core.Task) {
	t.Helper()
	if s.mode != modeVerify {
		t.Fatalf("mode %v, want the re-read of the task first", s.mode)
	}
	s.Update(taskVerifiedMsg{id: task.ID, task: task})
}

func TestKanbanMoveGoesThroughReReadConfirmAndAppliesOnlyOnY(t *testing.T) {
	s, p := kbAt(100, 32)
	send(s, "L")
	if s.mode != modeVerify || ranMoves(p) != "" {
		t.Fatalf("L plans and re-reads, nothing is written: mode %v ran %q", s.mode, ranMoves(p))
	}
	settleVerify(t, s, core.Task{ID: "ac-01", Title: "Fleet workspace", Status: "open", Priority: 1})
	if s.mode != modeConfirm || ranMoves(p) != "" {
		t.Fatalf("mode %v ran %q", s.mode, ranMoves(p))
	}
	if got := core.DisplayArgv(s.plan.Argv); got != "bd update ac-01 --status=in_progress --if-status=open" {
		t.Fatalf("the exact command: %s", got)
	}
	if lines := strings.Join(s.plan.Lines, "|"); !strings.Contains(lines, `status: "open" -> "in_progress"`) {
		t.Fatalf("before and after: %s", lines)
	}
	send(s, "n")
	if s.mode != modeList || ranMoves(p) != "" || !strings.Contains(s.notice, "nothing was changed") {
		t.Fatalf("n cancels: mode %v ran %q notice %q", s.mode, ranMoves(p), s.notice)
	}
	send(s, "L")
	settleVerify(t, s, core.Task{ID: "ac-01", Title: "Fleet workspace", Status: "open", Priority: 1})
	(*p.fresh)["ac-01"] = core.Task{ID: "ac-01", Status: "open"} // the apply-time re-read agrees
	_, cmd := s.Update(keyRunes("y"))
	if cmd == nil || s.mode != modeBusy {
		t.Fatalf("y applies: mode %v", s.mode)
	}
	msg := cmd()
	if ranMoves(p) != "bd update ac-01 --status=in_progress --if-status=open" {
		t.Fatalf("ran %q", ranMoves(p))
	}
	s.Update(msg)
	if s.mode != modeList || !strings.Contains(s.notice, "updated ac-") {
		t.Fatalf("notice %q", s.notice)
	}
}

// A card changed by someone else between the board draw and the confirm screen is refused on the
// spot: the plan is dropped, nothing runs, and the board reads again.
func TestKanbanMoveIsRefusedWhenTheStatusChangedSinceTheBoardWasDrawn(t *testing.T) {
	s, p := kbAt(100, 32)
	send(s, "L")
	_, cmd := s.Update(taskVerifiedMsg{id: "ac-01", task: core.Task{ID: "ac-01", Status: "blocked"}})
	if s.mode != modeList || ranMoves(p) != "" || !strings.Contains(s.notice, "changed since you opened it (now blocked)") {
		t.Fatalf("mode %v ran %q notice %q", s.mode, ranMoves(p), s.notice)
	}
	if cmd == nil {
		t.Error("the board is read again")
	}
	if _, cmd := s.Update(keyRunes("y")); cmd != nil || ranMoves(p) != "" {
		t.Error("y after a refusal does nothing")
	}
}

func TestKanbanMoveStaleGuardFromTheTrackerIsReportedAsChanged(t *testing.T) {
	s, _ := kbAt(100, 32)
	s.Update(taskAppliedMsg{err: core.ErrChanged, op: core.TaskUpdate})
	if !strings.Contains(s.notice, "changed since you opened it") || !strings.Contains(s.notice, "nothing was written") || !strings.HasPrefix(s.notice, "not applied") {
		t.Fatalf("%q", s.notice)
	}
}

func TestKanbanMoveWarnsWhileABlockerIsOpen(t *testing.T) {
	s, _ := kbAt(100, 32)
	send(s, "j", "j", "L") // ac-09 is held by ac-12
	settleVerify(t, s, core.Task{ID: "ac-09", Title: "queue attention digest", Status: "open", Priority: 2})
	if !strings.Contains(strings.Join(s.plan.Lines, "|"), "warning: ac-09 is held by 1 unfinished") {
		t.Fatalf("%v", s.plan.Lines)
	}
}

func TestKanbanClosedPathsUseCloseAndReopenAndNeverUpdate(t *testing.T) {
	s, p := kbAt(100, 32)
	send(s, "Z", "l", "l", "l", "j") // Deferred is the last working column; ac-36 is its only card
	for i := 0; i < 3; i++ {
		send(s, "k")
	}
	if selID(s) != "ac-36" {
		t.Fatalf("%q", selID(s))
	}
	send(s, "L")
	settleVerify(t, s, core.Task{ID: "ac-36", Status: "deferred"})
	if got := core.DisplayArgv(s.plan.Argv); got != "bd close ac-36" || ranMoves(p) != "" {
		t.Fatalf("into closed is a close: %s", got)
	}
	send(s, "n", "l") // onto the first closed card
	if id := selID(s); id != "ac-04" {
		t.Fatalf("%q", id)
	}
	send(s, "H") // out of closed there is one place to go: open
	settleVerify(t, s, core.Task{ID: "ac-04", Status: "closed"})
	if got := core.DisplayArgv(s.plan.Argv); got != "bd reopen ac-04" {
		t.Fatalf("out of closed is a reopen: %s", got)
	}
	send(s, "n", "L")
	if s.mode != modeList || ranMoves(p) != "" {
		t.Fatalf("nothing is right of closed: %v %q", s.mode, s.notice)
	}
}

func TestKanbanMovesOnlyInTheStatusGroupingAndNeverWithoutAWriter(t *testing.T) {
	s, p := kbAt(100, 32)
	send(s, "s", "L")
	if s.mode != modeList || !strings.Contains(s.notice, "status columns only") || ranMoves(p) != "" {
		t.Fatalf("grouped by priority: mode %v notice %q", s.mode, s.notice)
	}
	ro := listAt(100, 32)
	send(ro, "v", "L")
	if ro.mode != modeList || !strings.Contains(ro.notice, "writes are not available") {
		t.Fatalf("no writer: %q", ro.notice)
	}
	send(ro, "H")
	s2, _ := kbAt(100, 32)
	send(s2, "H") // nothing left of Open
	if s2.mode != modeList || !strings.Contains(s2.notice, "no column that way") {
		t.Fatalf("%q", s2.notice)
	}
}

func TestKanbanMoveToACustomStatusIsRefused(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Issues[0].Status = "pinned" // ac-01 sits in a custom column
	p := tasksFake()
	s := newTasksSection(core.Providers{Writer: p, Reader: p}, &config{audit: &memAudit{}})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: snap})
	send(s, "v", "Z")
	cs, _, _ := cols(s)
	var keys []string
	for _, c := range cs {
		keys = append(keys, c.Key)
	}
	if got := strings.Join(keys, " "); got != "open in_progress blocked deferred pinned closed" {
		t.Fatal(got)
	}
	send(s, "l", "l", "l") // deferred
	send(s, "L")           // deferred -> pinned
	if s.mode != modeList || !strings.Contains(s.notice, "cannot be moved to pinned") || ranMoves(p) != "" {
		t.Fatalf("mode %v notice %q", s.mode, s.notice)
	}
}

func TestKanbanStatusEditFromTheListIsGuardedToo(t *testing.T) {
	p := tasksFake()
	s := newTasksSection(core.Providers{Writer: p, Reader: p}, &config{audit: &memAudit{}})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	send(s, "e") // ac-12, in progress
	s.fields[3].value, s.fields[3].edited = "blocked", true
	s.fi = 3
	send(s, "enter")
	if s.mode != modeVerify || s.pending.IfStatus != "in_progress" || s.pending.Expect != "in_progress" {
		t.Fatalf("mode %v pending %+v", s.mode, s.pending)
	}
}

// ---- hostile text and sizes ----

func TestKanbanHostileTextReachesNeitherColumnsNorStrip(t *testing.T) {
	const hostile = "x\x1b[2J\x1b]0;pwn\a\u202etxt\u200b\u200d\ufeff\ay\x00\u65e5\u672c\u8a9e\u0085"
	snap := fixtureSnapshot()
	snap.Issues[0].Title, snap.Issues[0].Assignee, snap.Issues[0].Status = hostile, hostile, hostile
	snap.Issues[1].Title = hostile
	for _, w := range []int{40, 60, 99, 100, 140} {
		for _, keys := range [][]string{nil, {"Z"}, {"s"}, {"s", "s"}, {"enter"}} {
			s := newTasksSection(core.Providers{}, &config{})
			s.SetTheme(asciiTheme())
			s.Update(tea.WindowSizeMsg{Width: w, Height: 30})
			s.Update(tasksMsg{report: demo.TasksReport()})
			s.Update(taskSnapshotMsg{snap: snap})
			send(s, "v")
			send(s, keys...)
			for i, l := range strings.Split(s.View(), "\n") {
				if lipgloss.Width(l) > w {
					t.Fatalf("w=%d %v line %d is %d wide: %q", w, keys, i, lipgloss.Width(l), l)
				}
				for _, r := range l {
					if r == 0x1b || r == 0x7 || r == 0 || unicode.Is(unicode.Cf, r) || r == 0x85 {
						t.Fatalf("w=%d %v: forbidden rune U+%04X in %q", w, keys, r, l)
					}
				}
			}
		}
	}
}

func TestKanbanNeverExceedsTheTerminalAtAnySize(t *testing.T) {
	for _, w := range gridWidths {
		for _, h := range gridHeights {
			if tooSmall(w, h) { // the shell draws its own message there; the property test over every tab covers those sizes
				continue
			}
			for _, keys := range [][]string{nil, {"Z"}, {"s"}, {"l", "l"}, {"enter"}} {
				s, _ := kbAt(w, h)
				send(s, keys...)
				out := s.View()
				if n := strings.Count(out, "\n"); h > 0 && n > h+1 {
					t.Fatalf("%dx%d %v: %d lines", w, h, keys, n)
				}
				for i, l := range strings.Split(out, "\n") {
					if lipgloss.Width(l) > w {
						t.Fatalf("%dx%d %v line %d is %d wide: %q", w, h, keys, i, lipgloss.Width(l), l)
					}
				}
			}
		}
	}
}

func TestKanbanAThousandTasksRenderAndScroll(t *testing.T) {
	var issues []core.TaskIssue
	for i := 0; i < 1000; i++ {
		issues = append(issues, core.TaskIssue{ID: fmt.Sprintf("t-%04d", i), Title: fmt.Sprintf("task %d", i), Status: []string{"open", "in_progress", "blocked", "deferred"}[i%4], Priority: i % 5})
	}
	p := tasksFake()
	s := newTasksSection(core.Providers{Writer: p, Reader: p}, &config{audit: &memAudit{}})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: core.TaskSnapshot{Issues: issues, ReadAt: goldenNow}})
	send(s, "v")
	for i := 0; i < 200; i++ {
		send(s, "j")
	}
	if !strings.Contains(s.View(), "▸ ") || !strings.Contains(s.View(), "more") {
		t.Fatalf("the cursor stays in view and the column says how much is hidden:\n%s", s.View())
	}
}

// keep the compiler honest about the helpers' imports when a test above is removed.
var _ = context.Background
var _ = errors.New
var _ = taskview.GroupStatus

// ---- review fixes: zero columns ----

func kbWith(w, h int, issues []core.TaskIssue) *tasksSection {
	s := newTasksSection(core.Providers{}, &config{})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: core.TaskSnapshot{Issues: issues, ReadAt: goldenNow}})
	send(s, "v")
	return s
}

func TestKanbanWithNoColumnsRendersAHintInsteadOfPanicking(t *testing.T) {
	for _, w := range []int{40, 60, 100, 140} {
		s, _ := kbAt(w, 30)
		send(s, "/")
		typeIn(s, "zzzzzz")
		send(s, "enter", "s")
		v := s.View()
		if !strings.Contains(v, "no cards in this grouping") || !strings.Contains(v, "press s") {
			t.Errorf("no-match query, priority grouping, %d wide:\n%s", w, v)
		}
		closed := fixtureSnapshot()
		for i := range closed.Issues {
			closed.Issues[i].Status = "closed"
		}
		s2 := kbWith(w, 30, closed.Issues)
		send(s2, "s")
		if v := s2.View(); !strings.Contains(v, "no cards in this grouping") {
			t.Errorf("all closed, priority grouping, %d wide:\n%s", w, v)
		}
		send(s2, "s", "Z", "enter", "j", "k", "H", "L", "l", "h")
		_ = s2.View()
	}
}

// Every grouping, every snapshot shape, every width: no panic and no line wider than the terminal.
func TestKanbanNeverPanicsOverGroupingsShapesAndWidths(t *testing.T) {
	mk := func(status string) core.TaskIssue { return core.TaskIssue{ID: "x-1", Title: "t", Status: status} }
	shapes := map[string][]core.TaskIssue{
		"empty": nil, "all closed": {mk("closed")}, "custom only": {mk("pinned")}, "one open": {mk("open")},
	}
	for name, issues := range shapes {
		for _, keys := range [][]string{nil, {"s"}, {"s", "s"}, {"Z"}, {"s", "Z"}, {"enter"}, {"l", "L", "H"}} {
			for w := 20; w <= 300; w += 7 {
				s := kbWith(w, 24, issues)
				send(s, keys...)
				for i, l := range strings.Split(s.View(), "\n") {
					if lipgloss.Width(l) > w {
						t.Fatalf("%s %v w=%d line %d is %d wide: %q", name, keys, w, i, lipgloss.Width(l), l)
					}
				}
			}
		}
	}
}

// ---- review fixes: the close/reopen guard fails closed ----

type countAudit struct{ n int }

func (a *countAudit) Record(core.AuditEntry) error { a.n++; return nil }

func kbAudited(r core.TaskReader) (*tasksSection, taskProv, *countAudit) {
	p := tasksFake()
	au := &countAudit{}
	pr := core.Providers{Writer: p}
	if r != nil {
		pr.Reader = r
	} else {
		pr.Reader = p
	}
	s := newTasksSection(pr, &config{audit: au})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	send(s, "v")
	return s, p, au
}

func TestKanbanMoveIsRefusedWhenTheReReadFails(t *testing.T) {
	s, p, au := kbAudited(nil)
	send(s, "L")
	_, cmd := s.Update(taskVerifiedMsg{id: "ac-01", err: errors.New("bd show timed out")})
	if s.mode != modeList || !strings.Contains(s.notice, "could not verify the current status; nothing was written") || cmd != nil || ranMoves(p) != "" || au.n != 0 {
		t.Fatalf("mode %v notice %q ran %q audit %d", s.mode, s.notice, ranMoves(p), au.n)
	}
}

func TestKanbanMoveIsRefusedWithoutAReader(t *testing.T) {
	p := tasksFake()
	au := &countAudit{}
	s := newTasksSection(core.Providers{Writer: p}, &config{audit: au})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	send(s, "v", "L")
	if s.mode != modeList || !strings.Contains(s.notice, "could not verify") || ranMoves(p) != "" || au.n != 0 {
		t.Fatalf("mode %v notice %q", s.mode, s.notice)
	}
}

// The status changes between the confirm screen and y: the plan aborts, nothing runs, no audit line.
func TestKanbanMoveAbortsAtApplyTimeWhenTheStatusChangedAfterConfirm(t *testing.T) {
	s, p, au := kbAudited(nil)
	send(s, "L")
	settleVerify(t, s, core.Task{ID: "ac-01", Title: "Fleet workspace", Status: "open", Priority: 1})
	(*p.fresh)["ac-01"] = core.Task{ID: "ac-01", Status: "blocked"}
	_, cmd := s.Update(keyRunes("y"))
	s.Update(cmd())
	if ranMoves(p) != "" || au.n != 0 || s.mode != modeList || !strings.Contains(s.notice, "changed since you opened it") {
		t.Fatalf("ran %q audit %d mode %v notice %q", ranMoves(p), au.n, s.mode, s.notice)
	}
}

func TestKanbanMoveAbortsAtApplyTimeWhenTheRecheckFails(t *testing.T) {
	s, p, au := kbAudited(nil)
	send(s, "L")
	settleVerify(t, s, core.Task{ID: "ac-01", Status: "open", Priority: 1})
	*p.readErr = errors.New("down")
	_, cmd := s.Update(keyRunes("y"))
	s.Update(cmd())
	if ranMoves(p) != "" || au.n != 0 || !strings.Contains(s.notice, "could not verify") {
		t.Fatalf("ran %q audit %d notice %q", ranMoves(p), au.n, s.notice)
	}
}

func TestKanbanMoveStillAppliesWhenTheRecheckAgrees(t *testing.T) {
	s, p, _ := kbAudited(nil)
	send(s, "L")
	settleVerify(t, s, core.Task{ID: "ac-01", Status: "open", Priority: 1})
	(*p.fresh)["ac-01"] = core.Task{ID: "ac-01", Status: "open"}
	_, cmd := s.Update(keyRunes("y"))
	s.Update(cmd())
	if ranMoves(p) == "" {
		t.Fatalf("ran %q", ranMoves(p))
	}
}
