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

func TestTasksInsightsSitBesideAtOneHundredAndBelowTheListUnder(t *testing.T) {
	both := func(v string) bool {
		for _, l := range strings.Split(v, "\n") {
			if strings.Contains(l, "▸ Tasks") && strings.Contains(l, "─ Insights") {
				return true
			}
		}
		return false
	}
	narrow := sized(newTasksRig(t).m, 99, 50).View()
	if both(narrow) || !strings.Contains(narrow, "─ Insights") || !strings.Contains(narrow, "critical path") {
		t.Fatalf("99 columns stacks the insights under the list:\n%s", narrow)
	}
	wide := sized(newTasksRig(t).m, 100, 50).View()
	if !both(wide) || !strings.Contains(wide, "critical path") {
		t.Fatalf("100 columns puts the insights beside the list:\n%s", wide)
	}
}

func TestTasksListIsTheFocusedPanel(t *testing.T) {
	if v := sized(newTasksRig(t).m, 120, 50).View(); !strings.Contains(v, "╭─ ▸ Tasks") || strings.Contains(v, "▸ Insights") {
		t.Fatalf("the task list is focused, the insights are read-only:\n%s", v)
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

func TestTasksKeepTheSummaryAndTheCountsInThePanel(t *testing.T) {
	for _, w := range []int{40, 80, 120} {
		v := sized(newTasksRig(t).m, w, 50).View()
		if !strings.Contains(v, "14 tasks") || !strings.Contains(v, "ready") || !strings.Contains(v, "ac-12") {
			t.Errorf("w=%d: the summary and rows stay visible:\n%s", w, v)
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
	m, _ := r.m.Update(tasksMsg{report: core.TasksReport{Header: core.Header{Source: core.SourceLive}}})
	v := m.View()
	if !strings.Contains(v, "no tasks yet — press n to add the first one") || !strings.Contains(v, "╭") {
		t.Fatalf("an empty tracker says what to do, inside the panel:\n%s", v)
	}
}

func TestTasksSelectionAndWriteKeysStillWork(t *testing.T) {
	r := newTasksRig(t)
	m := press(sized(r.m, 120, 50), "j")
	if v := m.View(); !strings.Contains(v, "▸ ac-09") && !strings.Contains(v, "ac-09") {
		t.Fatalf("down moves the cursor:\n%s", v)
	}
	m = press(m, "x") // close the selected task: goes to the confirm screen, never straight to a write
	m = run(t, m, nil)
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
		m := sized(r.m, w, 50)
		m, _ = m.Update(tasksMsg{report: rep})
		m, _ = m.Update(insightsMsg{report: ins})
		hostileOK(t, fmt.Sprintf("tasks %d", w), m.View(), w)
	}
}

func TestTasksAndBoardPanelsAreCompactInATallTerminal(t *testing.T) {
	for name, v := range map[string]string{
		"tasks": sized(newTasksRig(t).m, 120, 60).View(),
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

// Stacked, the Tasks list and the insights share the body: both frames stay whole, the insights
// take no more than a third of the rows, and nothing is taller than the body.
func TestTasksStackedPanelsBothFitTheBody(t *testing.T) {
	for _, h := range []int{14, 20, 30} {
		v := sized(newTasksRig(t).m, 80, h).View()
		out := strings.Split(strings.TrimRight(v, "\n"), "\n")
		if len(out) > h {
			t.Fatalf("h=%d: %d rows", h, len(out))
		}
		if strings.Count(v, "╭") < 2 || strings.Count(v, "╰") < 2 {
			t.Errorf("h=%d: both frames must close:\n%s", h, v)
		}
		ins := -1
		for i, l := range out {
			if strings.Contains(l, "─ Insights") {
				ins = i
			}
		}
		if ins < 0 {
			t.Fatalf("h=%d: the insights panel is missing:\n%s", h, v)
		}
		bodyRows := h - 3 // header, rule and footer
		panelRows := 0
		for _, l := range out[ins:] {
			if strings.HasPrefix(l, "│") {
				panelRows++
			}
		}
		if panelRows > max(2, (bodyRows-2*panelChromeH)/3) {
			t.Errorf("h=%d: the insights take %d rows, more than a third of the body:\n%s", h, panelRows, v)
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
