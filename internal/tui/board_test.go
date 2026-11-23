package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// inspectProv is a Board provider with an inspector; the command is only ever built here, never run.
type inspectProv struct {
	boardProv
	built *int
	reads *int
}

func (p inspectProv) InspectCmd() *exec.Cmd { *p.built++; return exec.Command("true") }
func (p inspectProv) Board(ctx context.Context) (core.BoardReport, error) {
	*p.reads++
	return p.boardProv.Board(ctx)
}

func newInspectSection() (*boardSection, *int, *int) {
	built, reads := new(int), new(int)
	s := newBoardSection(inspectProv{boardFake(), built, reads})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return s, built, reads
}

func keyO() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")} }

func hasBinding(s *boardSection, k string) bool {
	for _, b := range s.Bindings() {
		for _, kk := range b.Keys() {
			if kk == k {
				return true
			}
		}
	}
	return false
}

func TestOpenInBrowserOnlyOnceTheBoardHasLoaded(t *testing.T) {
	s, built, _ := newInspectSection()
	if hasBinding(s, "o") {
		t.Fatal("not loaded yet: no o binding")
	}
	if _, cmd := s.Update(keyO()); cmd != nil || *built != 0 {
		t.Fatal("o does nothing before the board has loaded")
	}
	s.Update(boardMsg{err: errors.New("could not reach the board")})
	if hasBinding(s, "o") {
		t.Fatal("a failed read: no o binding")
	}
	if _, cmd := s.Update(keyO()); cmd != nil || *built != 0 {
		t.Fatal("o does nothing while the read is failing")
	}
	s.Update(boardMsg{err: &core.NotConnectedError{Reason: "x", HowToConnect: "y"}})
	if hasBinding(s, "o") {
		t.Fatal("not connected: no o binding")
	}
	s.Update(boardMsg{report: fixtureBoard()})
	if !hasBinding(s, "o") {
		t.Fatal("connected: o is offered")
	}
	if _, cmd := s.Update(keyO()); cmd == nil || *built != 1 {
		t.Fatal("o hands off once connected")
	}
}

func TestBoardPollingNeverOverlapsAndStopsWhenHidden(t *testing.T) {
	s, _, reads := newInspectSection()
	s.activate()
	if s.fetches != 1 || !s.busy {
		t.Fatalf("activation starts one read: %d busy=%v", s.fetches, s.busy)
	}
	gen := s.gen
	s.Update(boardTickMsg{gen})
	s.Update(boardTickMsg{gen})
	if s.fetches != 1 {
		t.Fatalf("a stuck read cannot be overlapped by the poll: %d reads started", s.fetches)
	}
	s.Update(boardMsg{report: fixtureBoard()})
	s.Update(boardTickMsg{gen})
	if s.fetches != 2 {
		t.Fatalf("an idle visible tab polls: %d", s.fetches)
	}
	s.Update(boardMsg{report: fixtureBoard()})
	s.deactivate()
	if _, cmd := s.Update(boardTickMsg{gen}); cmd != nil || s.fetches != 2 {
		t.Fatalf("a hidden tab does not poll, and its old tick chain ends: %d", s.fetches)
	}
	s.activate()
	if _, cmd := s.Update(boardTickMsg{gen}); cmd != nil { // the tick of the earlier visit
		t.Fatal("a tick from an earlier visit is dropped")
	}
	if *reads != 0 {
		t.Fatal("no command was run by this test, so no provider was called")
	}
}

func TestBoardReadFailureIsOneLineWithABoardHintAndNoInstallAdvice(t *testing.T) {
	s, _, _ := newInspectSection()
	s.Update(boardMsg{err: errors.New("could not reach the board\x1b[2J")})
	v := s.View()
	if !strings.Contains(v, "state unavailable: could not reach the board") || !strings.Contains(v, "check board_url") || strings.Contains(v, "npm i") || strings.ContainsRune(v, 0x1b) {
		t.Fatalf("%s", v)
	}
	if !strings.Contains(strings.ToLower(v), "r") {
		t.Fatal()
	}
}

func TestBoardPartialReadAndTruncationAreShown(t *testing.T) {
	s, _, _ := newInspectSection()
	r := fixtureBoard()
	r.Reason, r.Truncated = "channel work could not be read", "only the first 50 tasks are shown"
	s.Update(boardMsg{report: r})
	v := s.View()
	if !strings.Contains(v, "channel work could not be read") || !strings.Contains(v, "only the first 50 tasks are shown") {
		t.Fatalf("%s", v)
	}
	if strings.Contains(v, "(0)") {
		t.Fatalf("a channel with no count shows none: %s", v)
	}
}

func TestBoardDetailKeepsALineBreakAndFlattensTheListRow(t *testing.T) {
	s, _, _ := newInspectSection()
	r := fixtureBoard()
	r.Threads[0].Root.Text = "first line\nsecond line\x1b[2J"
	s.Update(boardMsg{report: r})
	v := s.View()
	if strings.ContainsRune(v, 0x1b) || !strings.Contains(v, "first line") || !strings.Contains(v, "second line") {
		t.Fatalf("%s", v)
	}
	rows := strings.Split(v, "\n")
	for _, l := range rows {
		if strings.Contains(l, "#15") && strings.Contains(l, "second line") {
			t.Fatalf("the list row shows the first line only: %q", l)
		}
	}
	broken := false
	for _, l := range rows {
		if strings.Contains(l, "second line") && !strings.Contains(l, "first line") {
			broken = true // the detail panel puts each stored line on its own row
		}
	}
	if !broken {
		t.Fatalf("the detail pane breaks the lines: %s", v)
	}
}

func TestBoardDeletedMessageNeverShowsOldText(t *testing.T) {
	s, _, _ := newInspectSection()
	r := fixtureBoard()
	r.Threads[0].Last.Text = "(deleted)"
	s.Update(boardMsg{report: r})
	if v := s.View(); !strings.Contains(v, "(deleted)") {
		t.Fatalf("%s", v)
	}
}
