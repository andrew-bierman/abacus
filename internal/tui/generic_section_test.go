package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// run executes a command and feeds each resulting message to the section; a Batch is flattened.
func run(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = run(t, m, c)
		}
	case nil:
	default:
		m, _ = m.Update(msg)
	}
	return m
}

func TestGenericSectionRefetchesOnEveryTick(t *testing.T) {
	f := &modtest.Fake{IDStr: "g", Det: core.Detection{State: core.Ready}, Stat: core.Status{Summary: "v1"}}
	s := newGenericSection(f)
	s.tick = func() tea.Cmd { return nil } // never sleep in a test
	var m tea.Model = s
	m = run(t, m, s.Init())
	if !strings.Contains(m.View(), "v1") {
		t.Fatalf("first load missing:\n%s", m.View())
	}
	f.Stat = core.Status{Summary: "v2"}
	m, cmd := m.Update(genericTickMsg{})
	m = run(t, m, cmd)
	if !strings.Contains(m.View(), "v2") {
		t.Fatalf("a tick must re-fetch; still showing:\n%s", m.View())
	}
}

func TestGenericSectionRecoversFromAFailedFirstRead(t *testing.T) {
	f := &modtest.Fake{IDStr: "g", Det: core.Detection{State: core.Ready}, Stat: core.Status{Unavailable: "hub busy"}}
	s := newGenericSection(f)
	s.tick = func() tea.Cmd { return nil }
	var m tea.Model = s
	m = run(t, m, s.Init())
	if !strings.Contains(m.View(), "state unavailable: hub busy") {
		t.Fatalf("view:\n%s", m.View())
	}
	f.Stat = core.Status{Summary: "back"}
	m, cmd := m.Update(genericTickMsg{})
	m = run(t, m, cmd)
	if !strings.Contains(m.View(), "back") {
		t.Fatalf("a transient failure must not stick until restart:\n%s", m.View())
	}
}

func TestGenericPollIntervalIsGentleOnSharedBackends(t *testing.T) {
	// Modules without a dedicated section (beads-lease) shell out to a shared production hub.
	if genericPollEvery < 30*time.Second {
		t.Fatalf("genericPollEvery = %v, want at least 30s", genericPollEvery)
	}
	if s := newGenericSection(&modtest.Fake{IDStr: "g"}); s.tick == nil {
		t.Fatal("the production section must schedule its own refresh")
	}
}
