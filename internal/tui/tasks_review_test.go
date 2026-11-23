package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

func lastFrameRow(v string) int {
	last := 0
	for i, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "╯") {
			last = i
		}
	}
	return last
}

// ---- 1: e, x and w never act on a task the Insights view does not show ----

// writableAt is listAt with a task writer, so n, e and x have somewhere to go.
func writableAt(w, h int) *tasksSection {
	p := tasksFake()
	s := newTasksSection(core.Providers{Writer: p, Reader: p}, &config{audit: &memAudit{}})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	return s
}

func TestInsightsViewNeverActsOnTheHiddenTask(t *testing.T) {
	s := writableAt(100, 32)
	send(s, "V")
	for _, k := range []string{"e", "x", "w"} {
		send(s, k)
		if s.mode != modeList || !strings.Contains(s.notice, "List view") {
			t.Fatalf("%s from Insights: mode %v notice %q; want no change and a pointer to the List view", k, s.mode, s.notice)
		}
		if v := s.View(); !strings.Contains(v, "switch to the List view") {
			t.Fatalf("%s: the notice is on screen:\n%s", k, v)
		}
	}
	send(s, "n")
	if s.mode != modeForm || s.op != core.TaskCreate {
		t.Fatalf("n works from any view: mode %v op %v", s.mode, s.op)
	}
	send(s, "esc")
	send(s, "v") // back to the list
	send(s, "e")
	if s.mode != modeForm || s.op != core.TaskUpdate {
		t.Fatalf("e on the List edits the shown task: mode %v", s.mode)
	}
	s2 := writableAt(100, 32)
	send(s2, "V", "x")
	if s2.mode != modeList {
		t.Fatalf("x from Insights must not start a close: %v", s2.mode)
	}
}

// ---- 2: a write that lands while a read is in flight is never shown as pre-write state ----

func TestAWriteDuringAnInFlightReadIssuesAFreshRead(t *testing.T) {
	s := listAt(100, 32)
	s.src = failingSource{}
	if cmd := s.fetchSnapshot(); cmd == nil || !s.snapBusy { // a tick starts a read
		t.Fatal("no read started")
	}
	_, cmd := s.Update(taskAppliedMsg{res: core.TaskResult{ID: "ac-12", Title: "t"}, op: core.TaskClose})
	if cmd == nil || !s.snapDirty {
		t.Fatalf("the write asked for a read while one was in flight: it is remembered (dirty=%v)", s.snapDirty)
	}
	_, cmd = s.Update(taskSnapshotMsg{snap: fixtureSnapshot()}) // the stale (pre-write) answer
	if cmd == nil || !s.snapBusy || s.snapDirty {
		t.Fatalf("the stale answer is followed by a fresh read: cmd=%v busy=%v dirty=%v", cmd != nil, s.snapBusy, s.snapDirty)
	}
	if v := s.View(); !strings.Contains(v, "may not show your last change yet") {
		t.Fatalf("while the fresh read is out, the list says it may be behind:\n%s", v)
	}
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	if s.snapBusy || strings.Contains(s.View(), "may not show your last change") {
		t.Fatal("the fresh read clears the note and no further read is issued")
	}
}

// ---- 3: a resize never leaves the list frozen ----

func TestResizeFromWideToStackedReleasesTheDetailFocus(t *testing.T) {
	s := listAt(120, 30)
	send(s, "enter")
	if !s.list.detailFocus {
		t.Fatal("wide: enter focuses the detail")
	}
	s.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	if s.list.detailFocus || s.drilled {
		t.Fatalf("stacked after a resize: focus=%v drilled=%v", s.list.detailFocus, s.drilled)
	}
	send(s, "j")
	if s.cursor != 1 {
		t.Fatalf("j moves the cursor again: %d", s.cursor)
	}
	send(s, "enter")
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if s.drilled {
		t.Fatal("wide: the detail is beside the list, nothing is drilled")
	}
	send(s, "j")
	if s.cursor != 2 {
		t.Fatalf("j moves the cursor after drilled then widened: %d", s.cursor)
	}
}

// ---- 4: a recent-changes list does not know what is ready ----

func TestRecentOnlyNeverClaimsReadyOrBlocked(t *testing.T) {
	s := newTasksSection(core.Providers{}, &config{})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	v := s.View()
	if !strings.Contains(v, "recent only") || strings.Contains(v, "ready") || strings.Contains(v, "⊘") {
		t.Fatalf("no ready count or blocked marks without dependencies:\n%s", v)
	}
	if !strings.Contains(v, "3 tasks") {
		t.Fatalf("the strip still counts the tasks:\n%s", v)
	}
	var seen []taskview.Preset
	for i := 0; i < 5; i++ {
		send(s, "f")
		seen = append(seen, s.list.preset)
	}
	for _, p := range seen {
		if p == taskview.PresetReady || p == taskview.PresetBlocked {
			t.Fatalf("f skips ready and blocked in a recent-only list: %v", seen)
		}
	}
	if !strings.Contains(s.notice, "whole tracker") {
		t.Fatalf("pressing f says why: %q", s.notice)
	}
	for _, r := range s.rows() {
		if r.Ready || r.Blocked {
			t.Fatalf("row %+v", r)
		}
	}
}

// ---- 6: a refresh keeps the quick filter the user chose ----

func TestARefreshKeepsTheChosenQuickFilter(t *testing.T) {
	s := listAt(100, 32)
	send(s, "f")
	if s.list.preset != taskview.PresetReady {
		t.Fatal(s.list.preset)
	}
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	s.Update(tasksMsg{report: demo.TasksReport()})
	if s.list.preset != taskview.PresetReady {
		t.Fatalf("a refresh reset the quick filter to %q", s.list.preset)
	}
	// Nothing chosen: all in the fallback, open once a real snapshot lands.
	f := newTasksSection(core.Providers{}, &config{})
	f.SetTheme(asciiTheme())
	f.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	f.Update(tasksMsg{report: demo.TasksReport()})
	if f.list.preset != taskview.PresetAll {
		t.Fatalf("fallback before any f: %q", f.list.preset)
	}
	f.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	if f.list.preset != taskview.PresetOpen {
		t.Fatalf("a real snapshot after it: %q", f.list.preset)
	}
}

// ---- the Insights counts and the strip come from the snapshot ----

func TestInsightsSummaryAgreesWithTheList(t *testing.T) {
	s := listAt(100, 32)
	s.Update(tasksMsg{report: core.TasksReport{Counts: core.TaskCounts{Total: 99, Open: 50, Ready: 77}}}) // a disagreeing summary read
	send(s, "V")
	v := s.View()
	if !strings.Contains(v, "14 tasks:") || !strings.Contains(v, "ready 3") || strings.Contains(v, "99") || strings.Contains(v, "ready 77") {
		t.Fatalf("with a snapshot the counts are the snapshot's:\n%s", v)
	}
}

// ---- a refresh that removes the selected task pins the clamp ----

func TestARefreshThatRemovesTheSelectedTaskClampsTheCursor(t *testing.T) {
	s := listAt(100, 32)
	last := len(s.rows()) - 1
	for i := 0; i < last; i++ {
		send(s, "j")
	}
	gone := s.selected
	snap := fixtureSnapshot()
	for i, it := range snap.Issues {
		if it.ID == gone {
			snap.Issues = append(snap.Issues[:i], snap.Issues[i+1:]...)
			break
		}
	}
	s.Update(taskSnapshotMsg{snap: snap})
	if s.cursor != last-1 || s.selected == gone || s.rows()[s.cursor].ID != s.selected {
		t.Fatalf("cursor %d selected %q (was %q): the clamped row is selected", s.cursor, s.selected, gone)
	}
	send(s, "/")
	typeIn(s, "label:ui")
	if s.rows()[s.cursor].ID != s.selected || s.cursor >= len(s.rows()) {
		t.Fatalf("a filter that hides the selection moves it to the clamped row: %d of %d", s.cursor, len(s.rows()))
	}
}

// ---- reads that time out back off ----

func TestATimedOutReadBacksOffAndSaysSo(t *testing.T) {
	s := newTasksSection(core.Providers{}, &config{})
	s.SetTheme(asciiTheme())
	s.src = failingSource{}
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.snapBusy = true
	s.Update(taskSnapshotMsg{err: fmt.Errorf("bd timed out exporting the tracker: %w", core.ErrTimedOut)})
	if cmd := s.fetchSnapshot(); cmd != nil {
		t.Fatal("a tick right after a timeout does not retry the long export")
	}
	if v := s.View(); !strings.Contains(v, "tracker too large to load quickly") || !strings.Contains(v, "every 5 minutes") {
		t.Fatalf("the notice says so plainly:\n%s", v)
	}
	old := nowFn
	defer func() { nowFn = old }()
	nowFn = func() time.Time { return goldenNow.Add(6 * time.Minute) }
	if cmd := s.fetchSnapshot(); cmd == nil {
		t.Fatal("after five minutes it tries again")
	}
	s.snapBusy = false
	nowFn = old
	s.Update(taskSnapshotMsg{err: errors.New("x wrapped: " + core.ErrTimedOut.Error())}) // not typed: no back-off
	if cmd := s.fetchSnapshot(); cmd == nil {
		t.Fatal("an untyped failure retries as before")
	}
	s.snapBusy = false
	s.Update(taskSnapshotMsg{err: core.ErrTimedOut})
	send(s, "r") // an explicit refresh always tries
	if !s.snapBusy {
		t.Fatal("r retries at once")
	}
}

// ---- layout: panes fill the body, sparse details are useful, marks have a legend ----

func TestTheListAndDetailFillTheBody(t *testing.T) {
	for _, sz := range [][2]int{{100, 32}, {60, 24}, {120, 50}, {100, 14}} {
		v := sized(newTasksRig(t).m, sz[0], sz[1]).View()
		if got, want := lastFrameRow(v), sz[1]-2; got != want {
			t.Errorf("%dx%d: the panes end at row %d, want %d (just above the footer):\n%s", sz[0], sz[1], got, want, v)
		}
	}
}

func TestADetailWithNoDescriptionStillShowsTheUsefulFields(t *testing.T) {
	s := listAt(120, 40)
	send(s, "/")
	typeIn(s, "id:ac-30")
	send(s, "enter")
	v := s.View()
	for _, want := range []string{"ac-30  feature", "○ open  P3", "labels  later, ui", "created 2d ago", "updated", "Blocked by (0)", "Blocks (0)", "none", "no description"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
}

func TestTheListExplainsItsMarks(t *testing.T) {
	v := listAt(100, 32).View()
	for _, want := range []string{"⊘N held by N", "→N holds N", "⊘↑ held by parent"} {
		if !strings.Contains(v, want) {
			t.Errorf("legend lacks %q:\n%s", want, v)
		}
	}
	d := listAt(100, 32)
	send(d, "/")
	typeIn(d, "id:ac-20")
	if v := d.View(); !strings.Contains(v, "held by 1 unfinished") {
		t.Errorf("the detail spells the marks out in words:\n%s", v)
	}
}
