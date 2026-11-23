package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// The four list sections share listNav but keep two selection policies when the chosen row
// disappears from a refresh: Leases and Queues pin the selection (it returns with the row), Board
// and Tasks select the clamped row. These tests pin that difference at the section level.

func downKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyDown} }

func threeLeases() []core.Lease {
	return []core.Lease{{Resource: "L1"}, {Resource: "L2"}, {Resource: "L3"}}
}

func TestLeasesPinTheSelectionWhenItsRowVanishes(t *testing.T) {
	s := newLeasesSection(nil)
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	s.Update(leasesMsg{read: leasesReport(threeLeases()...)})
	s.Update(downKey())
	s.Update(downKey())
	s.Update(leasesMsg{read: leasesReport(threeLeases()[:2]...)})
	if s.cursor != 1 || s.selected != "L3" {
		t.Fatalf("the cursor clamps and the selection stays on L3: cursor=%d selected=%q", s.cursor, s.selected)
	}
	s.Update(leasesMsg{read: leasesReport(threeLeases()...)})
	if s.cursor != 2 {
		t.Fatalf("L3 is selected again when it returns: cursor=%d", s.cursor)
	}
}

func TestQueuesPinTheSelectionWhenItsRunVanishes(t *testing.T) {
	runs := []lambe.Run{{Issue: "A", Session: "1"}, {Issue: "B", Session: "1"}, {Issue: "C", Session: "1"}}
	view := func(r ...lambe.Run) lambeViewMsg {
		return lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: r}}}
	}
	s := newLambeSection(lambe.NewDemo(), nil)
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	s.Update(view(runs...))
	s.Update(downKey())
	s.Update(downKey())
	s.Update(view(runs[:2]...))
	if s.cursor != 1 || s.selected != runKey(runs[2]) {
		t.Fatalf("the selection stays on run C: cursor=%d selected=%q", s.cursor, s.selected)
	}
	s.Update(view(runs...))
	if s.cursor != 2 {
		t.Fatalf("run C is selected again when it returns: cursor=%d", s.cursor)
	}
}

func TestBoardFollowsTheClampedRowWhenItsRowVanishes(t *testing.T) {
	th := func(ids ...string) boardMsg {
		var r core.BoardReport
		for _, id := range ids {
			r.Threads = append(r.Threads, core.BoardThread{Root: core.BoardMessage{ID: id}})
		}
		return boardMsg{report: r}
	}
	s := newBoardSection(boardFake())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	s.Update(th("1", "2", "3"))
	s.Update(downKey())
	s.Update(downKey())
	s.Update(th("1", "2"))
	if s.cursor != 1 || s.selected != "t:2" {
		t.Fatalf("the clamped row becomes the selection: cursor=%d selected=%q", s.cursor, s.selected)
	}
	s.Update(th("1", "2", "3"))
	if s.cursor != 1 {
		t.Fatalf("thread 3 returning does not take the selection back: cursor=%d", s.cursor)
	}
}

func TestBoardCursorRunsOverThreadsThenTasks(t *testing.T) {
	s := newBoardSection(boardFake())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	s.Update(boardMsg{report: core.BoardReport{
		Threads: []core.BoardThread{{Root: core.BoardMessage{ID: "9"}}},
		Tasks:   []core.BoardTask{{ID: "7"}},
	}})
	s.Update(downKey())
	if s.cursor != 1 || s.selected != "k:7" {
		t.Fatalf("down from the last thread lands on the first task: cursor=%d selected=%q", s.cursor, s.selected)
	}
}

func TestTasksFollowTheClampedRowWhenItsRowVanishes(t *testing.T) {
	rep := func(ids ...string) tasksMsg {
		var r core.TasksReport
		r.Counts.Total = len(ids)
		for _, id := range ids {
			r.Recent = append(r.Recent, core.Task{ID: id})
		}
		return tasksMsg{report: r}
	}
	s := newTasksSection(core.Providers{}, &config{})
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	s.Update(rep("a", "b", "c"))
	s.Update(downKey())
	s.Update(downKey())
	s.Update(rep("a", "b"))
	if s.cursor != 1 || s.selected != "b" {
		t.Fatalf("the clamped row becomes the selection: cursor=%d selected=%q", s.cursor, s.selected)
	}
	s.Update(rep("a", "b", "c"))
	if s.cursor != 1 {
		t.Fatalf("task c returning does not take the selection back: cursor=%d", s.cursor)
	}
}

func TestEscPeelsTheDetailBeforeTheFilter(t *testing.T) {
	s := newLambeSection(lambe.NewDemo(), nil)
	s.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	s.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{{Issue: "A", Session: "1"}}}}})
	s.filter = "a"
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !s.drilled {
		t.Fatal("enter drills in the stacked layout")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.drilled || s.filter != "a" {
		t.Fatalf("the first esc closes the detail only: drilled=%v filter=%q", s.drilled, s.filter)
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.filter != "" {
		t.Fatalf("the second esc clears the filter: %q", s.filter)
	}
}

func TestTasksDrillIntoTheDetailBelowOneHundredAndBack(t *testing.T) {
	s := newTasksSection(core.Providers{}, &config{})
	s.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	s.Update(tasksMsg{report: core.TasksReport{Recent: []core.Task{{ID: "a", Title: "alpha"}}, Counts: core.TaskCounts{Total: 1}}})
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !s.drilled {
		t.Fatal("enter drills into the detail in the stacked layout")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.drilled {
		t.Fatal("esc leaves the detail")
	}
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.drilled || !s.list.detailFocus {
		t.Fatalf("side by side the detail is always shown; enter only focuses it for scrolling: drilled=%v focus=%v", s.drilled, s.list.detailFocus)
	}
}
