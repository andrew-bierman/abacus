package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

func colorTheme() Theme { return NewTheme(rendererFor(termenv.TrueColor, true)) }

func tasksModelWith(t *testing.T, th Theme, rep core.TasksReport, w, h int) tea.Model {
	t.Helper()
	p := tasksFake()
	m := New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(th), WithAudit(&memAudit{}), WithFullView(noFullView))
	var sm tea.Model = sized(m, w, h)
	sm, _ = sm.Update(tasksMsg{report: rep})
	sm, _ = sm.Update(insightsMsg{report: demo.InsightsReport()})
	return press(sm, "2")
}

// ---- Tasks: a bar of the states, and status and priority colored by meaning ----

func TestTasksPanelShowsAStateBarAsWideAsThePanel(t *testing.T) {
	for _, w := range []int{40, 80, 120} {
		v := sized(newTasksRig(t).m, w, 40).View()
		var bar string
		for _, l := range strings.Split(v, "\n") {
			if strings.Contains(l, "▒") && strings.Contains(l, "█") && strings.Contains(l, "▓") {
				bar = strings.Split(l, "│")[1]
				break
			}
		}
		if bar == "" {
			t.Fatalf("w=%d: no state bar in the Tasks panel:\n%s", w, v)
		}
		listInner := duoLayout(w, 40).listInner()
		if got := lipgloss.Width(strings.TrimSpace(bar)); got != listInner {
			t.Errorf("w=%d: the bar is %d cells, the panel's content is %d:\n%s", w, got, listInner, v)
		}
	}
}

func TestTasksWithoutAnyTasksHaveNoBar(t *testing.T) {
	r := newTasksRig(t)
	m, _ := r.m.Update(tasksMsg{report: core.TasksReport{Header: core.Header{Source: core.SourceLive}}})
	if strings.ContainsAny(m.View(), "▒▓░") {
		t.Fatalf("an empty tracker has no state bar:\n%s", m.View())
	}
}

func TestTaskStatusAndPriorityAreColoredByMeaning(t *testing.T) {
	th := colorTheme()
	rep := core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 6},
		Recent: []core.Task{
			{ID: "T-0", Title: "selected", Status: "open", Priority: 3}, // the cursor row is drawn plain on the highlight
			{ID: "T-1", Title: "a", Status: "blocked", Priority: 0},
			{ID: "T-2", Title: "b", Status: "in_progress", Priority: 1},
			{ID: "T-3", Title: "c", Status: "closed", Priority: 2},
			{ID: "T-4", Title: "d", Status: "deferred", Priority: 4},
			{ID: "T-5", Title: "e", Status: "open", Priority: 3}}}
	v := tasksModelWith(t, th, rep, 120, 40).View()
	for name, want := range map[string]string{
		"blocked is a failure color":      th.Text(KindFail, "blocked"),
		"P0 is a failure color":           th.Text(KindFail, "P0"),
		"in progress is the accent color": th.Text(KindAccent, "in_progress"),
		"P1 is a warning color":           th.Text(KindWarn, "P1"),
		"deferred is a warning color":     th.Text(KindWarn, "deferred"),
		"closed is quiet":                 th.Text(KindMuted, "closed"),
	} {
		if !strings.Contains(v, want) {
			t.Errorf("%s: %q missing:\n%s", name, want, v)
		}
	}
	if strings.Contains(v, th.Text(KindFail, "open")) || strings.Contains(v, th.Text(KindMuted, "open")) {
		t.Errorf("an open task is plain text, not dimmed or alarmed:\n%s", v)
	}
}

func TestStatusAndPriorityKindsAreExplicit(t *testing.T) {
	for status, want := range map[string]Kind{"blocked": KindFail, "in_progress": KindAccent, "deferred": KindWarn, "closed": KindMuted} {
		if got, colored := taskStatusKind(status); !colored || got != want {
			t.Errorf("taskStatusKind(%q) = %v, %v; want %v", status, got, colored, want)
		}
	}
	for _, status := range []string{"open", "", "something-new"} {
		if _, colored := taskStatusKind(status); colored {
			t.Errorf("%q is plain text", status)
		}
	}
	for p, want := range map[int]Kind{0: KindFail, 1: KindWarn, 4: KindMuted} {
		if got, colored := taskPriorityKind(p); !colored || got != want {
			t.Errorf("taskPriorityKind(%d) = %v, %v; want %v", p, got, colored, want)
		}
	}
	for _, p := range []int{2, 3} {
		if _, colored := taskPriorityKind(p); colored {
			t.Errorf("P%d is plain", p)
		}
	}
}

// ---- one agent, one color, on every tab ----

func TestAnAgentNameHasTheSameColorOnLeasesBoardAndQueues(t *testing.T) {
	th := colorTheme()
	tint := th.Tint("ada", "ada")

	lr := leasesReport(core.Lease{Resource: "ac-1", State: core.LeaseHeld, Holder: "ada", ExpiresAt: "5m"})
	ls := leasesWith(&lr)
	ls.SetTheme(th)
	var lm tea.Model = ls
	lm, _ = lm.Update(leasesMsg{read: lr})
	if v := sized(lm, 120, 30).View(); !strings.Contains(v, tint) {
		t.Errorf("a lease holder is tinted by name:\n%s", v)
	}

	bs := newBoardSection(boardFake())
	bs.SetTheme(th)
	var bm tea.Model = bs
	bm, _ = bm.Update(boardMsg{report: demo.BoardReport()})
	if v := sized(bm, 120, 40).View(); !strings.Contains(v, tint) {
		t.Errorf("a board author is tinted by name:\n%s", v)
	}

	qs := newLambeSection(lambe.NewDemo(), nil)
	qs.SetTheme(th)
	var qm tea.Model = qs
	qm, _ = qm.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{
		{Issue: "A-1", State: "running", Phase: "p", Machine: "other"},
		{Issue: "A-2", State: "running", Phase: "p", Machine: "ada"}}}}})
	if v := sized(qm, 140, 30).View(); !strings.Contains(v, tint) {
		t.Errorf("a run's machine is tinted by name:\n%s", v)
	}
}

func TestSelectedRowsAreNotTintedSoTheHighlightStaysWhole(t *testing.T) {
	th := colorTheme()
	bs := newBoardSection(boardFake())
	bs.SetTheme(th)
	var bm tea.Model = bs
	bm, _ = bm.Update(boardMsg{report: demo.BoardReport()})
	for _, l := range strings.Split(sized(bm, 120, 40).View(), "\n") {
		if strings.Contains(l, "▸ #15") && strings.Contains(l, th.Tint("ada", "ada")) {
			t.Fatalf("the selected row is drawn plain on the highlight: %q", l)
		}
	}
}

// ---- Queues: a dot before the state ----

func TestQueueStatesGetADotColoredByTheirTone(t *testing.T) {
	th := colorTheme()
	qs := newLambeSection(lambe.NewDemo(), nil)
	qs.SetTheme(th)
	var qm tea.Model = qs
	qm, _ = qm.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{
		{Issue: "A-0", State: "pending"}, {Issue: "A-1", State: "running"}, {Issue: "A-2", State: "failed"}, {Issue: "A-3", State: "deferred"}}}}})
	v := sized(qm, 120, 30).View()
	for state, kind := range map[string]Kind{"running": KindOK, "failed": KindFail, "deferred": KindWarn} {
		if !strings.Contains(v, th.Text(kind, "●")+" "+th.Text(kind, state)) {
			t.Errorf("%s has a %v dot:\n%s", state, kind, v)
		}
	}
	plain := lambeAt(120, 30, manyRuns(2)).View()
	if !strings.Contains(plain, "● running") {
		t.Errorf("the dot is a glyph, so it survives without color:\n%s", plain)
	}
}

// ---- section headings in the accent color ----

func TestPanelSectionHeadingsUseTheAccentSection(t *testing.T) {
	th := colorTheme()
	bs := newBoardSection(boardFake())
	bs.SetTheme(th)
	var bm tea.Model = bs
	bm, _ = bm.Update(boardMsg{report: demo.BoardReport()})
	v := sized(bm, 120, 40).View()
	for _, h := range []string{"Channels", "Recent threads", "Tasks"} {
		if !strings.Contains(v, th.Section(h)) {
			t.Errorf("Board heading %q is an accent section:\n%s", h, v)
		}
	}
	tv := tasksModelWith(t, th, demo.TasksReport(), 120, 40).View()
	if !strings.Contains(tv, th.Section("Recent changes")) {
		t.Errorf("Tasks heading is an accent section:\n%s", tv)
	}
}
