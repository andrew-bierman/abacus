package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
)

// toTop puts the cursor on the first row.
func toTop(s *tasksSection) {
	for i := 0; i < 60; i++ {
		send(s, "k")
	}
}

// markTasks marks the given tasks by moving to each and pressing space.
func markTasks(t *testing.T, s *tasksSection, ids ...string) {
	t.Helper()
	for _, id := range ids {
		toTop(s)
		onTask(t, s, id)
		send(s, "space")
	}
	toTop(s)
}

func markedIDs(s *tasksSection) string {
	var ids []string
	for _, t := range s.markedRows() {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ",")
}

func TestSpaceMarksAndAdvancesAndShowsTheCountInTheTitle(t *testing.T) {
	s, _, _ := editAt(100, 32)
	toTop(s)
	first, _ := s.current()
	send(s, "space")
	if cur, _ := s.current(); cur.ID == first.ID {
		t.Error("marking moves the cursor down so a run of tasks is quick to mark")
	}
	if !s.mk.set[first.ID] || !strings.Contains(s.View(), "1 marked") || !strings.Contains(s.View(), "+ ") {
		t.Errorf("the mark is drawn and counted:\n%s", s.View())
	}
	send(s, "k", "space") // unmark
	if s.markOn() || strings.Contains(s.View(), "marked") {
		t.Errorf("space on a marked task unmarks it: %v", s.mk.set)
	}
}

func TestRangeInvertAndClearMarks(t *testing.T) {
	s, _, _ := editAt(100, 32)
	ids := s.ids()
	toTop(s)
	send(s, "space", "j", "j", "R") // anchor is row 0 (the first mark), cursor row 3
	if got := markedIDs(s); got != strings.Join(ids[:4], ",") {
		t.Errorf("R marks from the last marked task to the cursor: %s", got)
	}
	send(s, "I")
	if got := markedIDs(s); got != strings.Join(ids[4:], ",") {
		t.Errorf("I flips every listed task: %s", got)
	}
	send(s, "U")
	if s.markOn() || s.notice != "" && strings.Contains(s.View(), "marked") {
		t.Errorf("U clears the marks: %v", s.mk.set)
	}
}

func TestMarksSurviveARefreshByIdAndDropWhenTheTaskIsGone(t *testing.T) {
	s, _, _ := editAt(100, 32)
	markTasks(t, s, "ac-12", "ac-20")
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	send(s, "s") // another order
	if got := markedIDs(s); !strings.Contains(got, "ac-12") || !strings.Contains(got, "ac-20") || len(s.mk.set) != 2 {
		t.Errorf("marks follow the task, not the row: %s", got)
	}
	snap := fixtureSnapshot()
	var kept []core.TaskIssue
	for _, i := range snap.Issues {
		if i.ID != "ac-20" {
			kept = append(kept, i)
		}
	}
	snap.Issues = kept
	s.Update(taskSnapshotMsg{snap: snap})
	if len(s.mk.set) != 1 || !s.mk.set["ac-12"] {
		t.Errorf("a task the tracker no longer has is unmarked: %v", s.mk.set)
	}
}

func TestMarksNotListedAreCountedAndNeverPartOfABatch(t *testing.T) {
	s, p, _ := editAt(100, 32)
	markTasks(t, s, "ac-12", "ac-15")
	send(s, "/")
	typeIn(s, "heartbeat")
	send(s, "enter")
	if !strings.Contains(s.View(), "1 marked (+1 not listed)") {
		t.Errorf("hidden marks are said:\n%s", s.View())
	}
	send(s, "x")
	if s.mode != modeBulk || len(s.mk.plan.Items) != 1 || s.mk.plan.Items[0].Change.ID != "ac-12" || ran(p) != "" {
		t.Fatalf("only the listed mark is in the batch: mode=%v %+v", s.mode, s.mk.plan.Items)
	}
}

func TestBulkCloseListsEveryTaskAndCommandThenOneYApplies(t *testing.T) {
	s, p, au := editAt(100, 40)
	markTasks(t, s, "ac-09", "ac-15", "ac-16")
	send(s, "x")
	if s.mode != modeBulk {
		t.Fatalf("mode %v notice %q", s.mode, s.notice)
	}
	v := s.View()
	for _, want := range []string{"close 3 tasks", "ac-09", "ac-15", "ac-16", "bd close ac-09", "bd close ac-15", "bd close ac-16", "y applies all 3"} {
		if !strings.Contains(v, want) {
			t.Errorf("the confirm screen lacks %q:\n%s", want, v)
		}
	}
	if ran(p) != "" || au.n != 0 {
		t.Fatal("nothing runs before y")
	}
	_, cmd := s.Update(keyRunes("y"))
	if s.mode != modeBulkBusy || cmd == nil {
		t.Fatalf("y starts the batch: mode %v", s.mode)
	}
	s.Update(cmd())
	if s.mode != modeBulkDone || len(*p.ran) != 3 || au.n != 6 {
		t.Fatalf("mode %v ran %q audit %d", s.mode, ran(p), au.n)
	}
	v = s.View()
	for _, want := range []string{"Batch result", "3 of 3 applied", "applied", "batch-"} {
		if !strings.Contains(v, want) {
			t.Errorf("the result lacks %q:\n%s", want, v)
		}
	}
	if s.markOn() {
		t.Errorf("applied tasks are unmarked: %v", s.mk.set)
	}
	send(s, "esc")
	if s.mode != modeList || !strings.Contains(s.notice, "3 of 3 applied") {
		t.Errorf("esc closes the result: %v %q", s.mode, s.notice)
	}
}

func TestBulkNeedsTheWholePlanOnScreenBeforeYApplies(t *testing.T) {
	s, p, _ := editAt(60, 24)
	markTasks(t, s, "ac-01", "ac-09", "ac-12", "ac-15", "ac-16", "ac-20", "ac-21", "ac-30", "ac-25", "ac-33")
	send(s, "x")
	if s.mode != modeBulk || s.bulkAtEnd() {
		t.Fatalf("ten tasks do not fit 60x24: mode %v", s.mode)
	}
	if strings.Contains(s.View(), "y applies") {
		t.Error("the end of the plan is not on screen yet")
	}
	for _, b := range s.Bindings() {
		if b.Help().Key == "y" {
			t.Error("y is not offered while the plan is clipped")
		}
	}
	_, cmd := s.Update(keyRunes("y"))
	if cmd != nil || s.mode != modeBulk || ran(p) != "" {
		t.Fatal("y does nothing until every line has been on screen")
	}
	seen := s.View()
	for i := 0; i < 80 && !s.bulkAtEnd(); i++ {
		send(s, "j")
		seen += s.View()
	}
	for _, id := range []string{"ac-01", "ac-09", "ac-12", "ac-15", "ac-16", "ac-20", "ac-21", "ac-30", "ac-25", "ac-33"} {
		if !strings.Contains(seen, "bd close "+id) {
			t.Errorf("scrolling showed every command: %s missing", id)
		}
	}
	if !strings.Contains(s.View(), "y applies all 10") {
		t.Fatalf("at the end the footer line shows:\n%s", s.View())
	}
	_, cmd = s.Update(keyRunes("y"))
	if cmd == nil || s.mode != modeBulkBusy {
		t.Fatal("y applies once the end is in view")
	}
}

func TestBulkSkipsATaskThatChangedAfterThePlan(t *testing.T) {
	s, p, au := editAt(100, 40)
	markTasks(t, s, "ac-09", "ac-15", "ac-16")
	send(s, "x")
	moved := baseTask("ac-15")
	moved.Status = "in_progress" // someone else picked it up after the plan
	(*p.fresh)["ac-15"] = moved
	yes(s)
	if s.mode != modeBulkDone || strings.Contains(ran(p), "ac-15") || len(*p.ran) != 2 || au.n != 4 {
		t.Fatalf("mode %v ran %q audit %d", s.mode, ran(p), au.n)
	}
	v := s.View()
	for _, want := range []string{"2 of 3 applied, 1 skipped", "skipped", "now in_progress"} {
		if !strings.Contains(v, want) {
			t.Errorf("the result lacks %q:\n%s", want, v)
		}
	}
	if !s.mk.set["ac-15"] || len(s.mk.set) != 1 {
		t.Errorf("only the skipped task stays marked: %v", s.mk.set)
	}
}

// failProv fails the write of one task.
type failProv struct {
	taskProv
	id  string
	err error
}

func (f failProv) RunTask(ctx context.Context, argv []string) (core.TaskResult, error) {
	if len(argv) > 2 && argv[2] == f.id {
		*f.ran = append(*f.ran, argv)
		return core.TaskResult{}, f.err
	}
	return f.taskProv.RunTask(ctx, argv)
}

func TestBulkReportsAFailureAndKeepsGoing(t *testing.T) {
	base, au := tasksFake(), &memAudit{}
	p := failProv{taskProv: base, id: "ac-15", err: errors.New("bd said no")}
	s := newTasksSection(core.Providers{Writer: p, Reader: base, Source: base}, &config{audit: au})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	for _, i := range fixtureSnapshot().Issues {
		(*base.fresh)[i.ID] = baseTask(i.ID)
	}
	markTasks(t, s, "ac-09", "ac-15", "ac-16")
	send(s, "x")
	yes(s)
	if len(*base.ran) != 3 || s.mode != modeBulkDone {
		t.Fatalf("a failure does not stop the rest: ran %q mode %v", ran(base), s.mode)
	}
	v := s.View()
	for _, want := range []string{"2 of 3 applied, 1 failed", "failed", "bd said no"} {
		if !strings.Contains(v, want) {
			t.Errorf("the failure is named: %q missing:\n%s", want, v)
		}
	}
	if len(s.mk.set) != 1 || !s.mk.set["ac-15"] {
		t.Errorf("the failed task stays marked for another try: %v", s.mk.set)
	}
}

func TestBulkAnUnknownResultIsNeverCalledNotApplied(t *testing.T) {
	base := tasksFake()
	p := failProv{taskProv: base, id: "ac-09", err: core.UnknownOutcome("timed out")}
	s := newTasksSection(core.Providers{Writer: p, Reader: base, Source: base}, &config{audit: &memAudit{}})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	for _, i := range fixtureSnapshot().Issues {
		(*base.fresh)[i.ID] = baseTask(i.ID)
	}
	markTasks(t, s, "ac-09", "ac-15", "ac-16")
	send(s, "x")
	yes(s)
	v := s.View()
	if len(*base.ran) != 2 || !strings.Contains(v, "1 of 3 applied, 2 result unknown") || strings.Contains(v, "not applied") || len(s.mk.set) != 2 {
		t.Fatalf("ran %q marks %v:\n%s", ran(base), s.mk.set, v)
	}
}

func TestBulkFormsForEveryEditKeyAndTheirPlans(t *testing.T) {
	cases := map[string]struct {
		input string
		want  []string
	}{
		"t": {"urgent", []string{"add the label urgent to 2 tasks", "--label=urgent", "add label urgent"}},
		"u": {"ui", []string{"remove the label ui from 2 tasks", "remove label ui"}},
		"a": {"ada", []string{"assign ada to 2 tasks", "--assignee=ada", "--if-assignee="}},
		"p": {"0", []string{"set the priority to 0 on 2 tasks", "--priority=0"}},
		"e": {"blocked", []string{"set the status to blocked on 2 tasks", "--status=blocked", "--if-status=open"}},
	}
	for k, c := range cases {
		s, p, _ := editAt(100, 40)
		markTasks(t, s, "ac-15", "ac-30") // open; ac-15 has labels leases and ui, ac-30 later and ui
		send(s, k)
		if s.mode != modeForm || !s.mk.form || !strings.Contains(s.View(), "2 marked tasks") {
			t.Fatalf("%s: mode %v form %v\n%s", k, s.mode, s.mk.form, s.View())
		}
		typeIn(s, c.input)
		send(s, "enter")
		if s.mode != modeBulk {
			t.Fatalf("%s: mode %v formErr %q", k, s.mode, s.formErr)
		}
		v := s.View()
		for _, w := range c.want {
			if !strings.Contains(v, w) {
				t.Errorf("%s: the plan lacks %q:\n%s", k, w, v)
			}
		}
		if ran(p) != "" {
			t.Errorf("%s: nothing ran", k)
		}
	}
}

func TestBulkFormRefusesBadInputInTheForm(t *testing.T) {
	for k, in := range map[string]string{"p": "9", "t": "a b", "e": "closed", "u": ""} {
		s, p, _ := editAt(100, 40)
		markTasks(t, s, "ac-15", "ac-30")
		send(s, k)
		typeIn(s, in)
		send(s, "enter")
		if s.mode != modeForm || s.formErr == "" || ran(p) != "" {
			t.Errorf("%s %q: mode %v err %q", k, in, s.mode, s.formErr)
		}
	}
}

func TestEditKeysWithMarksNeverActOnTheCursorTaskAlone(t *testing.T) {
	s, p, _ := editAt(100, 40)
	markTasks(t, s, "ac-15", "ac-30")
	for _, k := range []string{"b", "B", "c"} {
		send(s, k)
		if s.mode != modeList || !strings.Contains(s.notice, "one task at a time") || ran(p) != "" {
			t.Errorf("%s with marks: mode %v notice %q", k, s.mode, s.notice)
		}
	}
	send(s, "U", "p") // without marks the single form opens again
	if s.mode != modeForm || s.mk.form || strings.Contains(s.View(), "marked") {
		t.Errorf("no marks: the one-task form: mode %v bulk=%v", s.mode, s.mk.form)
	}
	send(s, "p", "esc", "U", "p") // a batch form left by esc must not leak into the next single form
	if s.mk.form || s.mode != modeForm || strings.Contains(s.View(), "marked") {
		t.Error("after esc, U and p the form is the single-task one")
	}
}

func TestBulkCancelRunsNothing(t *testing.T) {
	s, p, au := editAt(100, 40)
	markTasks(t, s, "ac-15", "ac-30")
	send(s, "x", "n")
	if s.mode != modeList || ran(p) != "" || au.n != 0 || len(s.mk.set) != 2 || !strings.Contains(s.notice, "nothing was changed") {
		t.Errorf("mode %v notice %q marks %v", s.mode, s.notice, s.mk.set)
	}
}

func TestBulkIsBoundedAtTwentyFiveTasks(t *testing.T) {
	s, p, _ := editAt(100, 40)
	snap := core.TaskSnapshot{Tracker: "big", ReadAt: goldenNow}
	for i := 1; i <= core.MaxBulk+1; i++ {
		snap.Issues = append(snap.Issues, core.TaskIssue{ID: fmt.Sprintf("big-%02d", i), Title: fmt.Sprintf("task %d", i), Status: "open", Priority: 2, CreatedAt: goldenNow.Add(-time.Hour), UpdatedAt: goldenNow})
	}
	s.Update(taskSnapshotMsg{snap: snap})
	toTop(s)
	for range snap.Issues {
		send(s, "space")
	}
	send(s, "x")
	if s.mode != modeList || !strings.Contains(s.notice, "at most 25") || ran(p) != "" {
		t.Fatalf("26 marks are refused with the reason: mode %v notice %q", s.mode, s.notice)
	}
	send(s, "G") // not a key here; unmark one by hand
	toTop(s)
	send(s, "space")
	send(s, "x")
	if s.mode != modeBulk || len(s.mk.plan.Items) != core.MaxBulk {
		t.Fatalf("25 marks are allowed: mode %v %q", s.mode, s.notice)
	}
}

func TestMarksClearWhenTheTrackerIsSwitched(t *testing.T) {
	s, _, _ := editAt(100, 32)
	markTasks(t, s, "ac-12")
	s.resetForTracker()
	if s.markOn() {
		t.Error("marks describe one tracker")
	}
}

func TestMarkKeysAreInTheHelpAndHaveNoOtherOwner(t *testing.T) {
	s, _, _ := editAt(100, 32)
	got := map[string]string{}
	for _, b := range s.Bindings() {
		got[b.Help().Key] = b.Help().Desc
	}
	for k, d := range map[string]string{"space": "mark", "R": "mark range", "I": "invert marks"} {
		if got[k] != d {
			t.Errorf("binding %s = %q, want %q", k, got[k], d)
		}
	}
	markTasks(t, s, "ac-12")
	got = map[string]string{}
	for _, b := range s.Bindings() {
		got[b.Help().Key] = b.Help().Desc
	}
	if got["U"] != "clear marks" {
		t.Errorf("U is offered once something is marked: %v", got)
	}
}

// In the tree a folded branch hides its marked children from the batch, and flat or tree the batch is
// the same tasks.
func TestMarksInTheTreeAndTheFlatListAndAFoldedBranch(t *testing.T) {
	s, p, _ := editAt(100, 40)
	markTasks(t, s, "ac-12", "ac-15")
	onTask(t, s, "ac-01")
	send(s, "h") // fold the epic: its two marked children leave the list
	if _, hidden := s.markCount(); hidden != 2 || !strings.Contains(s.View(), "0 marked (+2 not listed)") {
		t.Fatalf("a folded branch hides its marks from the batch: %s", s.marksTitle())
	}
	send(s, "x")
	if s.mode != modeList || ran(p) != "" || !strings.Contains(s.notice, "none of the marked tasks is listed") {
		t.Errorf("nothing listed, nothing planned: mode %v notice %q", s.mode, s.notice)
	}
	send(s, "l")
	send(s, "T") // flat
	if s.treeOn() {
		t.Fatal("T leaves the tree")
	}
	send(s, "x")
	if s.mode != modeBulk || len(s.mk.plan.Items) != 2 {
		t.Errorf("flat, the same two marks are the batch: mode %v items %d", s.mode, len(s.mk.plan.Items))
	}
}

// bigBatch marks 25 tasks and opens the confirm at 60x24, where it is about 75 lines.
func bigBatch(t *testing.T) (*tasksSection, taskProv) {
	s, p, _ := editAt(60, 24)
	snap := core.TaskSnapshot{Tracker: "big", ReadAt: goldenNow}
	for i := 1; i <= core.MaxBulk; i++ {
		snap.Issues = append(snap.Issues, core.TaskIssue{ID: fmt.Sprintf("big-%02d", i), Title: fmt.Sprintf("task %d", i), Status: "open", Priority: 2, CreatedAt: goldenNow.Add(-time.Hour), UpdatedAt: goldenNow})
	}
	s.Update(taskSnapshotMsg{snap: snap})
	toTop(s)
	for range snap.Issues {
		send(s, " ")
	}
	send(s, "x")
	if s.mode != modeBulk || len(s.bulkLines()) < 50 {
		t.Fatalf("mode %v lines %d", s.mode, len(s.bulkLines()))
	}
	return s, p
}

// A jump to the end (G, end) or a page jump past unseen lines never opens the gate: every line from the
// top must have been on screen.
func TestBulkGateNeedsEveryLineSeenAndAJumpDoesNotCountSkippedLines(t *testing.T) {
	for _, jump := range []string{"G", "end"} {
		s, p := bigBatch(t)
		if jump == "end" {
			s.bulkScroll(tea.KeyMsg{Type: tea.KeyEnd})
		} else {
			send(s, jump)
		}
		if s.mk.off == 0 || s.bulkAtEnd() {
			t.Fatalf("%s: off %d atEnd %v", jump, s.mk.off, s.bulkAtEnd())
		}
		_, cmd := s.Update(keyRunes("y"))
		if cmd != nil || s.mode != modeBulk || ran(p) != "" {
			t.Errorf("%s then y applied after only the first and last screens", jump)
		}
	}
	s, _ := bigBatch(t)
	send(s, "j", "j", "j")
	send(s, "G") // a jump over lines no screen showed
	if s.bulkAtEnd() {
		t.Error("a few lines and then a jump is not every line")
	}
	send(s, "g") // back to the top: scrolling down from there counts again
	for i := 0; i < 200 && !s.bulkAtEnd(); i++ {
		send(s, "j")
	}
	if !s.bulkAtEnd() {
		t.Fatal("scrolling a line at a time reaches the end")
	}
	if _, cmd := s.Update(keyRunes("y")); cmd == nil || s.mode != modeBulkBusy {
		t.Error("y applies once every line was shown")
	}
	// pages alone (overlapping by two lines) cover everything too
	s, _ = bigBatch(t)
	for i := 0; i < 40 && !s.bulkAtEnd(); i++ {
		send(s, "pgdown")
	}
	if !s.bulkAtEnd() {
		t.Error("page by page reaches the end")
	}
}

func TestBulkGateStartsOverWhenTheTerminalIsResized(t *testing.T) {
	s, _ := bigBatch(t)
	for i := 0; i < 200 && !s.bulkAtEnd(); i++ {
		send(s, "j")
	}
	if !s.bulkAtEnd() {
		t.Fatal("setup")
	}
	s.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	if s.bulkAtEnd() {
		t.Error("the lines are wrapped again after a resize: what was seen no longer counts")
	}
}

// In the detail view the edit keys act on the task shown, not on the marked batch.
func TestEditKeysInTheDetailViewActOnTheShownTaskNotTheMarks(t *testing.T) {
	s, p, _ := editAt(100, 40)
	markTasks(t, s, "ac-15", "ac-30")
	onTask(t, s, "ac-12")
	send(s, "enter")
	if !s.inDetail() {
		t.Fatal("setup: the detail is focused")
	}
	send(s, "x")
	if s.mode != modeVerify || s.pending.ID != "ac-12" || len(s.mk.plan.Items) != 0 || ran(p) != "" {
		t.Errorf("x acts on ac-12 alone: mode %v pending %q batch %d", s.mode, s.pending.ID, len(s.mk.plan.Items))
	}
}
