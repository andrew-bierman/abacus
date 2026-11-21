package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/beaddeck/internal/lambe"
)

func runeKey(r string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)} }

func demoLoaded(t *testing.T, handoff func([]string) tea.Cmd) tea.Model {
	t.Helper()
	var s tea.Model = newLambeSection(lambe.NewDemo(), handoff)
	msg := s.Init()()
	s, _ = s.Update(msg)
	return s
}

func TestHandoffKeyCallsRexOnceWithSelectedMachine(t *testing.T) {
	var calls [][]string
	s := demoLoaded(t, func(argv []string) tea.Cmd { calls = append(calls, argv); return nil })
	_ = s.View()
	if len(calls) != 0 {
		t.Fatalf("handoff ran without a keypress: %v", calls)
	}
	s, _ = s.Update(runeKey("o"))
	if len(calls) != 1 || strings.Join(calls[0], " ") != "rex -S operators-mac" {
		t.Fatalf("handoff calls = %v", calls)
	}
}

func TestHandoffOnRunWithoutMachineShowsNoticeAndDoesNotCallRunner(t *testing.T) {
	called := false
	var s tea.Model = newLambeSection(lambe.NewDemo(), func([]string) tea.Cmd { called = true; return nil })
	s, _ = s.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{{Issue: "X-1"}}}}})
	s, _ = s.Update(runeKey("o"))
	if called || !strings.Contains(s.View(), "no machine recorded") {
		t.Fatalf("called=%v view:\n%s", called, s.View())
	}
}

func TestUnavailableStateRendersNotACrash(t *testing.T) {
	var s tea.Model = newLambeSection(lambe.NewDemo(), nil)
	s, _ = s.Update(lambeViewMsg{reason: "no live daemon found"})
	if v := s.View(); !strings.Contains(v, "state unavailable: no live daemon found") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestNarrowWidthRendersWithoutPanic(t *testing.T) {
	s := demoLoaded(t, nil)
	s, _ = s.Update(tea.WindowSizeMsg{Width: 10, Height: 5})
	if s.View() == "" {
		t.Fatal("empty view")
	}
}
