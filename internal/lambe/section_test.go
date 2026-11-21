package lambe

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func runeKey(r string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)} }

func loadedSection(t *testing.T, o Options) (*Module, tea.Model) {
	t.Helper()
	m := New(o)
	var s tea.Model = m.Section()
	v, err := demoView()
	if err != nil {
		t.Fatal(err)
	}
	s, _ = s.Update(viewMsg{view: v})
	return m, s
}

func TestHandoffKeyCallsRexOnceWithSelectedMachine(t *testing.T) {
	var machines []string
	_, s := loadedSection(t, Options{RexHandoff: func(machine string) tea.Cmd {
		machines = append(machines, machine)
		return nil
	}})
	// Loading data and rendering never hand off.
	_ = s.View()
	if len(machines) != 0 {
		t.Fatalf("handoff ran without a keypress: %v", machines)
	}
	s, _ = s.Update(runeKey("o"))
	if len(machines) != 1 || machines[0] != "operators-mac" {
		t.Fatalf("handoff calls = %v", machines)
	}
}

func TestHandoffOnRunWithoutMachineShowsNoticeAndDoesNotCallRunner(t *testing.T) {
	called := false
	m := New(Options{RexHandoff: func(string) tea.Cmd { called = true; return nil }})
	var s tea.Model = m.Section()
	s, _ = s.Update(viewMsg{view: View{Snapshot: Snapshot{Runs: []Run{{Issue: "X-1"}}}}})
	s, _ = s.Update(runeKey("o"))
	if called || !strings.Contains(s.View(), "no machine recorded") {
		t.Fatalf("called=%v view:\n%s", called, s.View())
	}
}

func TestUnavailableStateRendersNotACrash(t *testing.T) {
	m := New(Options{})
	var s tea.Model = m.Section()
	s, _ = s.Update(viewMsg{reason: "no live daemon found"})
	if v := s.View(); !strings.Contains(v, "state unavailable: no live daemon found") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestNarrowWidthRendersWithoutPanic(t *testing.T) {
	_, s := loadedSection(t, Options{})
	s, _ = s.Update(tea.WindowSizeMsg{Width: 10, Height: 5})
	if s.View() == "" {
		t.Fatal("empty view")
	}
}
