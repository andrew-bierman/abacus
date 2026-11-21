package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/lambe"
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
	s, _ = s.Update(lambeViewMsg{err: errors.New("no live daemon found")})
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

func TestHandoffUsesTheSelectedRunNotTheFirst(t *testing.T) {
	var calls [][]string
	var s tea.Model = newLambeSection(lambe.NewDemo(), func(argv []string) tea.Cmd { calls = append(calls, argv); return nil })
	runs := []lambe.Run{{Issue: "A-1", Machine: "mac-one"}, {Issue: "A-2", Machine: "mac-two"}}
	s, _ = s.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: runs}}})
	s, _ = s.Update(runeKey("j"))
	s, _ = s.Update(runeKey("o"))
	if len(calls) != 1 || strings.Join(calls[0], " ") != "rex -S mac-two" {
		t.Fatalf("handoff calls = %v, want one call for mac-two", calls)
	}
	// k moves back up and the cursor clamps at the first run.
	s, _ = s.Update(runeKey("k"))
	s, _ = s.Update(runeKey("k"))
	_, _ = s.Update(runeKey("o"))
	if len(calls) != 2 || calls[1][2] != "mac-one" {
		t.Fatalf("after k,k,o calls = %v, want second call for mac-one", calls)
	}
}

func viewOf(issues ...string) lambeViewMsg {
	runs := make([]lambe.Run, len(issues))
	for i, is := range issues {
		runs[i] = lambe.Run{Issue: is, Machine: "mac-" + is}
	}
	return lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: runs}}}
}

func recorder() (*[][]string, func([]string) tea.Cmd) {
	var calls [][]string
	return &calls, func(argv []string) tea.Cmd { calls = append(calls, argv); return nil }
}

func TestHandoffFollowsTheSelectedRunWhenARefreshShiftsRows(t *testing.T) {
	calls, h := recorder()
	var s tea.Model = newLambeSection(lambe.NewDemo(), h)
	s, _ = s.Update(viewOf("A", "B", "C"))
	s, _ = s.Update(runeKey("j"))     // select B
	s, _ = s.Update(viewOf("B", "C")) // A finished; B moved to index 0
	_, _ = s.Update(runeKey("o"))
	if len(*calls) != 1 || (*calls)[0][2] != "mac-B" {
		t.Fatalf("handoff calls = %v, want mac-B", *calls)
	}
}

func TestSelectionFallsBackToClampedIndexWhenTheRunDisappears(t *testing.T) {
	calls, h := recorder()
	var s tea.Model = newLambeSection(lambe.NewDemo(), h)
	s, _ = s.Update(viewOf("A", "B", "C"))
	s, _ = s.Update(runeKey("j"))
	s, _ = s.Update(runeKey("j"))     // select C (index 2)
	s, _ = s.Update(viewOf("A", "B")) // C is gone
	_, _ = s.Update(runeKey("o"))
	if len(*calls) != 1 || (*calls)[0][2] != "mac-B" {
		t.Fatalf("handoff calls = %v, want the clamped last run mac-B", *calls)
	}
}

func TestFailedHandoffShowsANoticeAndSuccessShowsNone(t *testing.T) {
	var s tea.Model = newLambeSection(lambe.NewDemo(), func([]string) tea.Cmd { return nil })
	s, _ = s.Update(viewOf("A"))
	s, _ = s.Update(handoffDoneMsg{err: errors.New("exec: \"rex\": not found\x1b[2J")})
	v := s.View()
	if !strings.Contains(v, "rex: exec") || strings.Contains(v, "\x1b") {
		t.Fatalf("want a sanitized rex error notice, got:\n%s", v)
	}
	s, _ = s.Update(runeKey("j")) // any key clears the notice
	s, _ = s.Update(handoffDoneMsg{})
	if strings.Contains(s.View(), "rex:") {
		t.Fatalf("a successful handoff must not show a notice:\n%s", s.View())
	}
}
