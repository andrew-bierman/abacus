package tui_test

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/lambe"
	"github.com/andrew-bierman/abacus/internal/tui"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// drain executes a command tree and feeds every resulting message back to the model.
func drain(m tea.Model, cmd tea.Cmd) tea.Model {
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = drain(m, c)
		}
	case nil:
	default:
		var next tea.Cmd
		m, next = m.Update(msg)
		m = drain(m, next)
	}
	return m
}

func entries(mods ...*modtest.Fake) []core.Entry {
	var out []core.Entry
	for _, f := range mods {
		out = append(out, core.Entry{Module: f, Detection: f.Det})
	}
	return out
}

func TestOnlyDetectedModulesGetASection(t *testing.T) {
	a := &modtest.Fake{IDStr: "a", Det: core.Detection{State: core.Ready}}
	b := &modtest.Fake{IDStr: "b", Det: core.Detection{State: core.Absent}}
	a.Stat = core.Status{Summary: "summary-a"}
	var m tea.Model = tui.New(entries(a, b))
	m = drain(m, m.Init())
	v := m.View()
	if !strings.Contains(v, "Fake a") || strings.Contains(v, "Fake b") || !strings.Contains(v, "summary-a") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestTabSwitchesSections(t *testing.T) {
	a := &modtest.Fake{IDStr: "a", Det: core.Detection{State: core.Ready}}
	b := &modtest.Fake{IDStr: "b", Det: core.Detection{State: core.Ready}}
	a.Stat, b.Stat = core.Status{Summary: "summary-a"}, core.Status{Summary: "summary-b"}
	var m tea.Model = tui.New(entries(a, b))
	m = drain(m, m.Init())
	m, _ = m.Update(key("tab"))
	if !strings.Contains(m.View(), "summary-b") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestNoModulesShowsGuidanceAndQuitKeyQuits(t *testing.T) {
	var m tea.Model = tui.New(nil)
	if !strings.Contains(m.View(), "No Beads tools detected") {
		t.Fatal("expected guidance")
	}
	_, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q should return QuitMsg")
	}
}

// Demo mode renders runs, queue and attention from fixtures with no daemon or binary.
func TestDemoRendersFixtureData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")
	demo := lambe.NewDemo()
	en := []core.Entry{{Module: demo, Detection: demo.Detect(context.Background())}}
	var m tea.Model = tui.New(en)
	m = drain(m, m.Init())
	v := m.View()
	for _, want := range []string{"Lambe", "T-1", "(stalled)", "Q-2", "blocked", "Needs attention", "gate_failed"} {
		if !strings.Contains(v, want) {
			t.Errorf("demo view missing %q:\n%s", want, v)
		}
	}
}
