package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

func demoSection(t *testing.T, w, h int) tea.Model {
	t.Helper()
	var s tea.Model = newLambeSection(lambe.NewDemo(), nil)
	s.(*lambeSection).SetTheme(asciiTheme())
	v, err := lambe.NewDemo().Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, _ = s.Update(lambeViewMsg{view: v})
	return sized(s, w, h)
}

func TestWideLayoutShowsRunDetailBesideTheList(t *testing.T) {
	v := demoSection(t, 120, 30).View()
	for _, want := range []string{"Runs", "Queue", "phase", "machine", "│"} {
		if !strings.Contains(v, want) {
			t.Errorf("wide view missing %q:\n%s", want, v)
		}
	}
}

func TestNarrowLayoutDrillsInWithEnterAndBackWithEsc(t *testing.T) {
	var s tea.Model = demoSection(t, 80, 30)
	if v := s.View(); strings.Contains(v, "machine") {
		t.Fatalf("stacked list must not show the detail pane:\n%s", v)
	}
	s = press(s, "enter")
	if v := s.View(); !strings.Contains(v, "machine") || strings.Contains(v, "Queue") {
		t.Fatalf("enter must drill into the detail pane:\n%s", v)
	}
	s = press(s, "esc")
	if v := s.View(); strings.Contains(v, "machine") || !strings.Contains(v, "Queue") {
		t.Fatalf("esc must return to the list:\n%s", v)
	}
}

func TestFilterNarrowsRunsAndKeepsSelectionOnIdentity(t *testing.T) {
	var s tea.Model = newLambeSection(lambe.NewDemo(), nil)
	s, _ = s.Update(viewOf("A-1", "B-2", "B-3"))
	s = press(s, "/")
	for _, r := range "b-" {
		s = press(s, string(r))
	}
	s = press(s, "enter")
	v := s.View()
	if strings.Contains(v, "A-1") || !strings.Contains(v, "B-2") || !strings.Contains(v, "2/3") {
		t.Fatalf("filter view:\n%s", v)
	}
	s = press(s, "j")
	calls, h := recorder()
	s.(*lambeSection).handoff = h
	_ = press(s, "o")
	if len(*calls) != 1 || (*calls)[0][2] != "mac-B-3" {
		t.Fatalf("handoff after filtering = %v, want mac-B-3", *calls)
	}
	s = press(s, "esc") // clears the filter
	if !strings.Contains(s.View(), "A-1") {
		t.Fatalf("esc must clear the filter:\n%s", s.View())
	}
}

func TestHostileFieldsAreSanitizedInEveryPane(t *testing.T) {
	bad := "x\x1b[2J\x1b]0;pwn\x07y\x00"
	v := lambe.View{
		Snapshot: lambe.Snapshot{
			Runs:       []lambe.Run{{Issue: bad, State: bad, Phase: bad, Machine: bad, Actor: bad, Workflow: bad, Session: bad}},
			NeedsHuman: []lambe.NeedsHuman{{Issue: bad, Reason: bad, Detail: bad}},
		},
		Queue: lambe.Queue{Rows: []lambe.QueueRow{{ID: bad, Title: bad}}},
	}
	for _, w := range []int{40, 120} {
		var s tea.Model = newLambeSection(lambe.NewDemo(), nil)
		s.(*lambeSection).SetTheme(asciiTheme())
		s, _ = s.Update(lambeViewMsg{view: v})
		s = sized(s, w, 30)
		s = press(s, "enter")
		for _, view := range []string{s.View(), press(s, "esc").View()} {
			if strings.ContainsAny(view, "\x1b\x07\x00") {
				t.Fatalf("w=%d: control character reached the screen: %q", w, view)
			}
		}
	}
}

func TestLeaseStatesGetBadges(t *testing.T) {
	r := leasesReport(
		core.Lease{Resource: "ac-1", State: core.LeaseHeld, Holder: "alice", ExpiresAt: "5m"},
		core.Lease{Resource: "ac-2", State: core.LeaseStale, Holder: "bob"},
		core.Lease{Resource: "ac-3", State: core.LeaseFree})
	s := leasesWith(&r)
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(leasesMsg{read: r})
	v := m.View()
	for _, want := range []string{"1 held, 1 stale, 1 free", "(held) ac-1 by alice (expires 5m)", "(stale) ac-2 (last holder bob)", "(free) ac-3"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
}

func TestTrackerSubsetAndSourceLabelsAreBadges(t *testing.T) {
	r := leasesRead{report: core.LeasesReport{Header: core.Header{Source: "tracker subset"}}}
	s := leasesWith(&r)
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(leasesMsg{read: r})
	if !strings.Contains(m.View(), "(tracker subset)") {
		t.Fatalf("source label missing:\n%s", m.View())
	}
}
