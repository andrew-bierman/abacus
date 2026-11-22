package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// genericFor builds the status section over a fake module's scripted status.
func genericFor(f *modtest.Fake) *genericSection {
	return newGenericSection(f.IDStr, func(context.Context) core.Status { return f.Stat })
}

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
	s := genericFor(f)
	s.tick = func() tea.Cmd { return nil } // never sleep in a test
	var m tea.Model = s
	m = run(t, m, s.Init())
	if !strings.Contains(m.View(), "v1") {
		t.Fatalf("first load missing:\n%s", m.View())
	}
	f.Stat = core.Status{Summary: "v2"}
	m, cmd := m.Update(genericTickMsg{id: "g"})
	m = run(t, m, cmd)
	if !strings.Contains(m.View(), "v2") {
		t.Fatalf("a tick must re-fetch; still showing:\n%s", m.View())
	}
}

func TestGenericSectionRecoversFromAFailedFirstRead(t *testing.T) {
	f := &modtest.Fake{IDStr: "g", Det: core.Detection{State: core.Ready}, Stat: core.Status{Unavailable: "hub busy"}}
	s := genericFor(f)
	s.tick = func() tea.Cmd { return nil }
	var m tea.Model = s
	m = run(t, m, s.Init())
	if !strings.Contains(m.View(), "state unavailable: hub busy") {
		t.Fatalf("view:\n%s", m.View())
	}
	f.Stat = core.Status{Summary: "back"}
	m, cmd := m.Update(genericTickMsg{id: "g"})
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
	if s := genericFor(&modtest.Fake{IDStr: "g"}); s.tick == nil {
		t.Fatal("the production section must schedule its own refresh")
	}
}

// The shell broadcasts every non-key message to every section, so a tick must be scoped to its
// module: otherwise each extra generic section doubles the live timers (and hub polls).
func TestATickForOneGenericSectionIsIgnoredByAnother(t *testing.T) {
	a := genericFor(&modtest.Fake{IDStr: "a", Det: core.Detection{State: core.Ready}})
	b := genericFor(&modtest.Fake{IDStr: "b", Det: core.Detection{State: core.Ready}})
	ticks := 0
	a.tick = func() tea.Cmd { ticks++; return nil }
	b.tick = func() tea.Cmd { ticks++; return nil }

	if _, cmd := b.Update(genericTickMsg{id: "a"}); cmd != nil || ticks != 0 {
		t.Fatalf("section b reacted to a's tick (cmd=%v, re-armed timers=%d)", cmd != nil, ticks)
	}
	if _, cmd := a.Update(genericTickMsg{id: "a"}); cmd == nil || ticks != 1 {
		t.Fatalf("section a must handle its own tick and re-arm exactly once (cmd=%v, timers=%d)", cmd != nil, ticks)
	}
}
