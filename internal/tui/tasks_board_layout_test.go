package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
)

func boardAt(w, h int, r core.BoardReport) tea.Model {
	s := newBoardSection(boardFake())
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(boardMsg{report: r})
	return sized(m, w, h)
}

func colOf(t *testing.T, line, sub string) int {
	t.Helper()
	i := strings.Index(line, sub)
	if i < 0 {
		t.Fatalf("%q not in %q", sub, line)
	}
	return lipgloss.Width(line[:i])
}

// ---- Board ----

func TestBoardDetailPanelAppearsExactlyAtOneHundredColumns(t *testing.T) {
	if v := boardAt(99, 40, demo.BoardReport()).View(); strings.Contains(v, "─ Thread ") {
		t.Fatalf("99 columns shows the list only:\n%s", v)
	}
	v := boardAt(100, 40, demo.BoardReport()).View()
	if !strings.Contains(v, "─ Thread ") || !strings.Contains(v, "2 replies, latest:") {
		t.Fatalf("100 columns shows the selected thread beside the list:\n%s", v)
	}
}

func TestBoardListIsAFocusedPanelAndTheDetailIsNot(t *testing.T) {
	v := boardAt(120, 40, demo.BoardReport()).View()
	if !strings.Contains(v, "╭─ ▸ Board") || strings.Contains(v, "▸ Thread ") {
		t.Fatalf("the list is focused, the detail is read-only:\n%s", v)
	}
}

func TestBoardRowsAlignIntoColumns(t *testing.T) {
	v := boardAt(120, 40, demo.BoardReport()).View()
	var cols []int
	for _, l := range strings.Split(v, "\n") {
		parts := strings.Split(l, "│") // the list is the first framed panel
		if len(parts) < 3 {
			continue
		}
		list := parts[1]
		if (strings.Contains(list, "#15 ") || strings.Contains(list, "#2 ")) && strings.Contains(list, "ada ") {
			cols = append(cols, colOf(t, list, "ada "))
		}
	}
	if len(cols) != 2 || cols[0] != cols[1] {
		t.Fatalf("the author column must line up across thread rows: %v\n%s", cols, v)
	}
}

func TestBoardDrillInBelowOneHundredAndBack(t *testing.T) {
	m := boardAt(80, 40, demo.BoardReport())
	if v := m.View(); strings.Contains(v, "2 replies") {
		t.Fatalf("stacked shows the list first:\n%s", v)
	}
	m = press(m, "enter")
	if v := m.View(); !strings.Contains(v, "2 replies, latest:") || strings.Contains(v, "Channels") || !strings.Contains(v, "▸ Thread") {
		t.Fatalf("enter drills into the selected thread:\n%s", v)
	}
	if v := press(m, "esc").View(); !strings.Contains(v, "Channels") || strings.Contains(v, "2 replies") {
		t.Fatalf("esc returns to the list:\n%s", v)
	}
}

// The sections are pointers, so each case builds its own: a drill that outlives a widening, or an
// Enter pressed while the detail is already beside the list, must not reappear on narrowing.
func TestBoardDrillNeverOutlivesAWideLayout(t *testing.T) {
	drilled := press(boardAt(80, 40, demo.BoardReport()), "enter")
	if v := sized(sized(drilled, 120, 40), 80, 40).View(); !strings.Contains(v, "Channels") {
		t.Fatalf("widening ends the drill:\n%s", v)
	}
	wide := press(boardAt(120, 40, demo.BoardReport()), "enter")
	if v := sized(wide, 80, 40).View(); !strings.Contains(v, "Channels") || strings.Contains(v, "▸ Thread") {
		t.Fatalf("enter while wide does nothing:\n%s", v)
	}
}

func TestBoardEmptyStatesAreWordsInsideAPanel(t *testing.T) {
	v := boardAt(120, 40, core.BoardReport{Header: core.Header{Source: core.SourceLive}}).View()
	for _, want := range []string{"none yet: the first post makes one", "no threads yet", "no open tasks", "╭", "nothing selected"} {
		if !strings.Contains(v, want) {
			t.Errorf("empty board missing %q:\n%s", want, v)
		}
	}
}

func TestBoardNeverClipsTheIdAndStaysInsideTheTerminal(t *testing.T) {
	for _, w := range []int{40, 60, 80, 100, 140} {
		v := boardAt(w, 40, demo.BoardReport()).View()
		if !strings.Contains(v, "#15") || !strings.Contains(v, "#16") {
			t.Errorf("w=%d: ids must stay whole:\n%s", w, v)
		}
		for _, l := range strings.Split(v, "\n") {
			if lipgloss.Width(l) > w {
				t.Fatalf("w=%d: %q is %d wide", w, l, lipgloss.Width(l))
			}
		}
	}
}

func TestBoardSelectionStillFollowsTheRowAcrossARefresh(t *testing.T) {
	m := boardAt(120, 40, demo.BoardReport())
	m = press(m, "j") // thread #2
	r := demo.BoardReport()
	r.Threads[0], r.Threads[1] = r.Threads[1], r.Threads[0]
	m, _ = m.Update(boardMsg{report: r})
	if v := m.View(); !strings.Contains(v, "deploys are green again") {
		t.Fatalf("the detail follows the selected thread, not the row number:\n%s", v)
	}
}

func TestHostileTextNeverReachesTheBoardPanels(t *testing.T) {
	bad := "x\x1b[2J\u202etxt\u200b\u200d\ufeff\x07y\x00日本語"
	r := core.BoardReport{
		Channels: []core.BoardChannel{{Path: bad, Threads: 2}},
		Threads:  []core.BoardThread{{Root: core.BoardMessage{ID: bad, Channel: bad, Author: bad, Text: bad + "\n" + bad}, Replies: 1, Last: &core.BoardMessage{ID: bad, Author: bad, Text: bad}}},
		Tasks:    []core.BoardTask{{ID: bad, Channel: bad, Text: bad, State: bad, Claimant: bad, Until: bad}},
	}
	for _, w := range []int{40, 120} {
		m := boardAt(w, 40, r)
		hostileOK(t, fmt.Sprintf("board %d", w), m.View(), w)
		hostileOK(t, fmt.Sprintf("board drilled %d", w), press(m, "enter").View(), w)
		hostileOK(t, fmt.Sprintf("board task %d", w), press(press(m, "j"), "enter").View(), w)
	}
}

// ---- Tasks ----

func TestTasksDetailSitsBesideTheListAtOneHundredAndIsOneStepAwayUnder(t *testing.T) {
	both := func(v string) bool {
		for _, l := range strings.Split(v, "\n") {
			if strings.Contains(l, "▸ Tasks") && strings.Contains(l, "─ ac-12") {
				return true
			}
		}
		return false
	}
	narrow := sized(newTasksRig(t).m, 99, 50).View()
	if both(narrow) || strings.Contains(narrow, "─ ac-12") || !strings.Contains(narrow, "▸ Tasks") {
		t.Fatalf("99 columns shows the list alone:\n%s", narrow)
	}
	drilled := press(sized(newTasksRig(t).m, 99, 50), "enter").View()
	if !strings.Contains(drilled, "╭─ ▸ ac-12") || strings.Contains(drilled, "▸ Tasks") || !strings.Contains(drilled, "Comments (2)") {
		t.Fatalf("enter opens the detail full width, esc returns:\n%s", drilled)
	}
	if back := press(press(sized(newTasksRig(t).m, 99, 50), "enter"), "esc").View(); !strings.Contains(back, "▸ Tasks") {
		t.Fatalf("esc returns to the list:\n%s", back)
	}
	wide := sized(newTasksRig(t).m, 100, 50).View()
	if !both(wide) || !strings.Contains(wide, "Comments (2)") {
		t.Fatalf("100 columns puts the detail beside the list:\n%s", wide)
	}
}

func TestTasksListIsTheFocusedPanel(t *testing.T) {
	if v := sized(newTasksRig(t).m, 120, 50).View(); !strings.Contains(v, "╭─ ▸ Tasks") || strings.Contains(v, "─ ▸ ac-12") {
		t.Fatalf("the task list is focused, the detail is read-only:\n%s", v)
	}
}

func TestTasksRowsAlignIntoColumns(t *testing.T) {
	v := sized(newTasksRig(t).m, 120, 50).View()
	var cols []int
	for _, l := range strings.Split(v, "\n") {
		for _, id := range []string{"ac-12 ", "ac-09 ", "ac-03 "} {
			if strings.Contains(l, id) && strings.Contains(l, " P") {
				cols = append(cols, colOf(t, l, " P"))
			}
		}
	}
	if len(cols) < 3 {
		t.Fatalf("no task rows found: %v\n%s", cols, v)
	}
	for _, c := range cols {
		if c != cols[0] {
			t.Fatalf("the priority column must line up across task rows: %v\n%s", cols, v)
		}
	}
}

func TestTasksKeepTheCountsAndRowsVisible(t *testing.T) {
	for _, w := range []int{60, 80, 120} {
		v := sized(newTasksRig(t).m, w, 50).View()
		if !strings.Contains(v, "14 tasks · 3 ready") || !strings.Contains(v, "of 14") || !strings.Contains(v, "ac-12") {
			t.Errorf("w=%d: the counts and rows stay visible:\n%s", w, v)
		}
		for _, l := range strings.Split(v, "\n") {
			if lipgloss.Width(l) > w {
				t.Fatalf("w=%d: %q is %d wide", w, l, lipgloss.Width(l))
			}
		}
	}
}

func TestTasksEmptyStateIsAWordsLineInsideThePanel(t *testing.T) {
	r := newTasksRig(t)
	m := withTasks(r.m, core.TasksReport{Header: core.Header{Source: core.SourceLive}})
	v := m.View()
	if !strings.Contains(v, "no tasks yet — press n to add the first one") || !strings.Contains(v, "╭") {
		t.Fatalf("an empty tracker says what to do, inside the panel:\n%s", v)
	}
}

func TestTasksSelectionAndWriteKeysStillWork(t *testing.T) {
	r := newTasksRig(t)
	m := press(sized(r.m, 120, 50), "j")
	rowWith(t, m.View(), "▸", "ac-01") // down moves the cursor (priority order: ac-12, then ac-01)
	m = pressRun(t, m, "x")            // close the selected task: the confirm screen, never straight to a write
	if v := m.View(); !strings.Contains(v, "Confirm this change") || !strings.Contains(v, "ac-01") {
		t.Fatalf("x on the selected task opens the confirm screen:\n%s", v)
	}
	if len(*r.prov.ran) != 0 {
		t.Fatal("a key must never write before the confirm")
	}
}

func TestHostileTextNeverReachesTheTasksPanels(t *testing.T) {
	bad := "x\x1b[2J\u202etxt\u200b\u200d\ufeff\x07y\x00日本語"
	r := newTasksRig(t)
	rep := core.TasksReport{Header: core.Header{Source: bad}, Counts: core.TaskCounts{Total: 1},
		Recent: []core.Task{{ID: bad, Title: bad, Status: bad, Priority: 1}}}
	ins := core.InsightsReport{TotalActionable: 1,
		TopPicks: []core.Pick{{ID: bad, Title: bad}}, Blockers: []core.Pick{{ID: bad, Title: bad}},
		CriticalPath: []core.Ranked{{ID: bad, Value: 1}}, Cycles: [][]string{{bad, bad}}, Header: core.Header{Source: bad, State: core.Connected, Reason: bad}}
	for _, w := range []int{40, 120} {
		m := withTasks(sized(r.m, w, 50), rep)
		m, _ = m.Update(insightsMsg{report: ins})
		hostileOK(t, fmt.Sprintf("tasks %d", w), m.View(), w)
		hostileOK(t, fmt.Sprintf("tasks detail %d", w), press(m, "enter").View(), w)
		hostileOK(t, fmt.Sprintf("tasks insights %d", w), press(m, "v").View(), w)
	}
}

func TestTasksAndBoardPanelsAreCompactInATallTerminal(t *testing.T) {
	for name, v := range map[string]string{
		"tasks": withTasks(sized(newTasksRig(t).m, 120, 60), demo.TasksReport()).View(),
		"board": boardAt(120, 60, demo.BoardReport()).View(),
	} {
		last := 0 // the row of the last panel border; the shell pads below it down to the footer
		for i, l := range strings.Split(v, "\n") {
			if strings.Contains(l, "╯") {
				last = i
			}
		}
		if last > 24 {
			t.Errorf("%s: a short list in a 60-row body is a compact box, but its frame ends at row %d", name, last)
		}
	}
}

// A summary that does not fit wraps; it is never cut off mid-word.
func TestBoardSummaryWrapsInsteadOfBeingCut(t *testing.T) {
	v := boardAt(40, 40, demo.BoardReport()).View()
	for _, word := range []string{"channels,", "threads,", "open", "tasks"} {
		if !strings.Contains(v, word) {
			t.Errorf("summary word %q was cut at 40 columns:\n%s", word, v)
		}
	}
}

// Every Tasks view fits the body at small heights: nothing is taller than the body and every
// frame that opens closes.
func TestTasksViewsFitTheBodyAtSmallHeights(t *testing.T) {
	for _, w := range []int{60, 80, 120} {
		for _, h := range []int{12, 14, 20, 30} {
			m := sized(newTasksRig(t).m, w, h)
			for name, v := range map[string]string{"list": m.View(), "detail": press(m, "enter").View(), "insights": press(m, "v").View()} {
				out := strings.Split(strings.TrimRight(v, "\n"), "\n")
				if len(out) > h {
					t.Fatalf("%dx%d %s: %d rows", w, h, name, len(out))
				}
				if strings.Count(v, "╭") != strings.Count(v, "╰") && !strings.Contains(v, "more") {
					t.Errorf("%dx%d %s: frames must close:\n%s", w, h, name, v)
				}
				for _, l := range out {
					if lipgloss.Width(l) > w {
						t.Fatalf("%dx%d %s: %q is %d wide", w, h, name, l, lipgloss.Width(l))
					}
				}
			}
		}
	}
}

func TestBoardDetailTitleFollowsTheSelectedRowKind(t *testing.T) {
	m := boardAt(120, 40, demo.BoardReport())
	if v := m.View(); !strings.Contains(v, "─ Thread ") {
		t.Fatalf("a thread row shows a Thread detail:\n%s", v)
	}
	m = press(press(m, "j"), "j") // the first task
	v := m.View()
	if !strings.Contains(v, "─ Task ") || !strings.Contains(v, "held by codex via ada") {
		t.Fatalf("a task row shows a Task detail:\n%s", v)
	}
	rowWith(t, v, "▸", "#16")
}

// The write outcome is never lost: a timed-out write says "result unknown" even when the read that
// follows it also fails, and so does any other notice shown while the tasks cannot be read.
func TestTasksNoticeSurvivesAFailedRead(t *testing.T) {
	for name, msg := range map[string]tea.Msg{
		"unknown outcome": taskAppliedMsg{err: fmt.Errorf("bd timed out: %w", core.ErrOutcomeUnknown), op: core.TaskClose},
		"applied":         taskAppliedMsg{res: core.TaskResult{ID: "ac-12", Title: "done"}, op: core.TaskClose},
	} {
		r := newTasksRig(t)
		m, _ := r.m.Update(msg)
		m, _ = m.Update(tasksMsg{err: fmt.Errorf("tracker down")})
		v := m.View()
		if !strings.Contains(v, "state unavailable: tracker down") {
			t.Fatalf("%s: the read failure shows:\n%s", name, v)
		}
		want := map[string]string{"unknown outcome": "result unknown", "applied": "closed"}[name]
		if !strings.Contains(v, want) {
			t.Errorf("%s: the write outcome %q must still be on screen:\n%s", name, want, v)
		}
	}
}

func TestBoardDrillEndsWhenARefreshEmptiesTheRows(t *testing.T) {
	m := press(boardAt(80, 40, demo.BoardReport()), "enter")
	m, _ = m.Update(boardMsg{report: core.BoardReport{Header: core.Header{Source: core.SourceLive}}})
	if v := m.View(); strings.Contains(v, "▸ Message") || strings.Contains(v, "nothing selected") {
		t.Fatalf("an empty board shows its list, not an empty detail:\n%s", v)
	}
}

// ---- scrolling, clipping, the helpers and the strings the first pass did not reach ----

func longBoard(n int) core.BoardReport {
	r := core.BoardReport{Header: core.Header{Source: core.SourceLive}}
	for i := 0; i < n; i++ {
		r.Threads = append(r.Threads, core.BoardThread{Root: core.BoardMessage{ID: fmt.Sprint(100 + i), Channel: "dev", Author: "ada", Text: fmt.Sprintf("thread %02d", i)}})
	}
	for i := 0; i < n; i++ {
		r.Tasks = append(r.Tasks, core.BoardTask{ID: fmt.Sprint(500 + i), Channel: "work", State: "ready", Text: fmt.Sprintf("task %02d", i)})
	}
	return r
}

func longTasks(n int) core.TasksReport {
	r := core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: n, Open: n}}
	for i := 0; i < n; i++ {
		r.Recent = append(r.Recent, core.Task{ID: fmt.Sprintf("T-%02d", i), Title: fmt.Sprintf("title %02d", i), Status: "open", Priority: 2})
	}
	return r
}

func TestLongBoardKeepsTheCursorVisibleWithAMoreMarkerAtEverySize(t *testing.T) {
	for _, w := range []int{40, 100, 140} {
		for _, h := range []int{12, 16, 24, 40} {
			m := boardAt(w, h, longBoard(20))
			clipped := m.View()
			if !strings.Contains(clipped, "more") {
				t.Errorf("%dx%d: a long board is clipped with a marker:\n%s", w, h, clipped)
			}
			for i := 0; i < 39; i++ { // down through every thread and task to the last row
				m = press(m, "j")
			}
			v := m.View()
			rowWith(t, v, "▸", "#519") // the last task is on screen with the cursor
			rows := strings.Split(strings.TrimRight(v, "\n"), "\n")
			if len(rows) > h {
				t.Fatalf("%dx%d: %d rows", w, h, len(rows))
			}
			for _, l := range rows {
				if lipgloss.Width(l) > w {
					t.Fatalf("%dx%d: %q is %d wide", w, h, l, lipgloss.Width(l))
				}
			}
		}
	}
}

func TestLongTasksKeepTheCursorVisibleWideAndStacked(t *testing.T) {
	for _, w := range []int{60, 99, 100, 140} {
		for _, h := range []int{14, 20, 30, 50} {
			m := withTasks(sized(newTasksRig(t).m, w, h), longTasks(70))
			if v := m.View(); !strings.Contains(v, "more") {
				t.Errorf("%dx%d: a long task list is clipped with a marker:\n%s", w, h, v)
			}
			for i := 0; i < 69; i++ {
				m = press(m, "j")
			}
			v := m.View()
			rowWith(t, v, "▸", "T-69")
			if rows := strings.Split(strings.TrimRight(v, "\n"), "\n"); len(rows) > h {
				t.Fatalf("%dx%d: %d rows", w, h, len(rows))
			}
		}
	}
}

func TestBoardDetailIsClippedToTheBodyWithAMarker(t *testing.T) {
	var text []string
	for i := 0; i < 30; i++ {
		text = append(text, fmt.Sprintf("line %02d of a long message", i))
	}
	r := core.BoardReport{Header: core.Header{Source: core.SourceLive},
		Threads: []core.BoardThread{{Root: core.BoardMessage{ID: "9", Channel: "dev", Author: "ada", Text: strings.Join(text, "\n")}}}}
	for _, w := range []int{100, 120} {
		v := boardAt(w, 14, r).View()
		rows := strings.Split(strings.TrimRight(v, "\n"), "\n")
		if len(rows) > 14 || !strings.Contains(v, "more") || strings.Count(v, "╯") != 2 {
			t.Errorf("w=%d: the detail is clipped to the body, says so, and both frames close (%d rows):\n%s", w, len(rows), v)
		}
		if strings.Contains(v, "line 29") {
			t.Errorf("w=%d: the end of a clipped message is not shown:\n%s", w, v)
		}
	}
}

func TestHostileTextReachesNoBoardHeaderNoticeOrDetailTail(t *testing.T) {
	bad := "x\x1b[2J\u202etxt\u200b\u200d\ufeff\x07y\x00日本語"
	r := core.BoardReport{Header: core.Header{Source: bad, Reason: bad}, Truncated: bad,
		Threads: []core.BoardThread{{Root: core.BoardMessage{ID: "1", Text: "x"}, Replies: 2, Last: &core.BoardMessage{ID: bad, Author: bad, At: bad, Text: bad + "\n" + bad}}}}
	for _, w := range []int{40, 120} {
		m := boardAt(w, 40, r)
		hostileOK(t, fmt.Sprintf("board header %d", w), m.View(), w)
		hostileOK(t, fmt.Sprintf("board detail %d", w), press(m, "enter").View(), w)
	}
	s := newBoardSection(boardFake())
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(boardMsg{report: demo.BoardReport()})
	m, _ = m.Update(boardInspectDoneMsg{err: fmt.Errorf("%s", bad)})
	hostileOK(t, "board notice", sized(m, 100, 40).View(), 100)
}

func TestHostileTextReachesNoTasksInsightsBranch(t *testing.T) {
	bad := "x\x1b[2J\u202etxt\u200b\u200d\ufeff\x07y\x00日本語"
	for name, in := range map[string]core.InsightsReport{
		"note":     {Header: core.Header{Source: bad, State: core.Connected}, TotalActionable: 1, CriticalPathNote: bad},
		"connect":  {Header: core.Header{State: core.NotConnected, HowToConnect: bad}},
		"degraded": {Header: core.Header{State: core.Degraded, Reason: bad}},
		"reason":   {Header: core.Header{Source: bad, State: core.Connected, Reason: bad}, TotalActionable: 1, TopPicks: []core.Pick{{ID: "a", Title: "b", Unblocks: 1}}},
		"empty":    {Empty: true},
		"unblocks": {Header: core.Header{Source: "graph", State: core.Connected}, TotalActionable: 1, Blockers: []core.Pick{{ID: bad, Title: bad, Unblocks: 99}}},
	} {
		for _, w := range []int{40, 120} {
			m := sized(newTasksRig(t).m, w, 50)
			m, _ = m.Update(insightsMsg{report: in})
			hostileOK(t, fmt.Sprintf("insights %s %d", name, w), m.View(), w)
		}
	}
}

func TestWindowLinesAndClipLinesHandleEveryRowBudget(t *testing.T) {
	th := asciiTheme()
	lines := []string{"a", "b", "c", "d", "e", "f"}
	top := 0
	if got := windowLines(th, lines, 0, 0, &top); len(got) != 6 {
		t.Fatalf("rows <= 0 shows everything: %v", got)
	}
	if got := windowLines(th, lines, 0, 10, &top); len(got) != 6 || strings.Contains(strings.Join(got, ""), "more") {
		t.Fatalf("a body that fits has no marker: %v", got)
	}
	got := windowLines(th, lines, 5, 4, &top) // cursor on the last line, room for three plus the marker
	if len(got) != 4 || !strings.Contains(got[3], "more") || !strings.Contains(strings.Join(got[:3], ""), "f") {
		t.Fatalf("the window follows the cursor and ends in a marker: %v", got)
	}
	if top == 0 {
		t.Fatal("the scroll state is kept")
	}
	if got := clipLines(th, lines, 1); len(got) != 1 || got[0] != "a" {
		t.Fatalf("one row has no room for a marker: %v", got)
	}
	if got := clipLines(th, lines, 3); len(got) != 3 || !strings.Contains(got[2], "+4 more") {
		t.Fatalf("a cut ends in a marker counting what it hides: %v", got)
	}
	if got := clipLines(th, lines, 6); len(got) != 6 {
		t.Fatalf("content that fits is untouched: %v", got)
	}
	if got := clipLines(th, lines, 0); len(got) != 6 {
		t.Fatalf("rows <= 0 shows everything: %v", got)
	}
}

func TestStackRowsGivesTheBottomAThirdAndTheTopItsContentPlusAir(t *testing.T) {
	for _, tc := range []struct{ h, top, bottom, wantTop, wantBottom int }{
		{0, 7, 5, 7, 5},     // unknown height: both keep their content
		{40, 4, 30, 10, 12}, // bottom capped at a third of (40-4); top gets content plus air to ten rows
		{40, 30, 3, 30, 3},  // a short bottom keeps its content, the top uses the rest
		{14, 30, 30, 7, 3},  // tiny: the bottom keeps a third, the top the rest
	} {
		top, bottom := stackRows(tc.h, tc.top, tc.bottom)
		if top != tc.wantTop || bottom != tc.wantBottom {
			t.Errorf("stackRows(%d, %d, %d) = %d, %d; want %d, %d", tc.h, tc.top, tc.bottom, top, bottom, tc.wantTop, tc.wantBottom)
		}
	}
}
