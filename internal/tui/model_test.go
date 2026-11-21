package tui_test

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/beaddeck/internal/lambe"
	"github.com/andrew-bierman/beaddeck/internal/module"
	"github.com/andrew-bierman/beaddeck/internal/module/modtest"
	"github.com/andrew-bierman/beaddeck/internal/tui"
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

func entries(mods ...*modtest.Fake) []module.Entry {
	var out []module.Entry
	for _, f := range mods {
		out = append(out, module.Entry{Module: f, Detection: f.Det})
	}
	return out
}

func TestOnlyDetectedModulesGetASection(t *testing.T) {
	a := &modtest.Fake{IDStr: "a", Det: module.Detection{State: module.Ready}}
	b := &modtest.Fake{IDStr: "b", Det: module.Detection{State: module.Absent}}
	m := tui.New(entries(a, b))
	v := m.View()
	if !strings.Contains(v, "Fake a") || strings.Contains(v, "Fake b") || !strings.Contains(v, "section:a") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestTabSwitchesSections(t *testing.T) {
	a := &modtest.Fake{IDStr: "a", Det: module.Detection{State: module.Ready}}
	b := &modtest.Fake{IDStr: "b", Det: module.Detection{State: module.Ready}}
	var m tea.Model = tui.New(entries(a, b))
	m, _ = m.Update(key("tab"))
	if !strings.Contains(m.View(), "section:b") {
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
	en := []module.Entry{{Module: demo, Detection: demo.Detect(context.Background())}}
	var m tea.Model = tui.New(en)
	m = drain(m, m.Init())
	v := m.View()
	for _, want := range []string{"Lambe", "T-1", "(stalled)", "Q-2", "blocked", "Needs attention", "gate_failed"} {
		if !strings.Contains(v, want) {
			t.Errorf("demo view missing %q:\n%s", want, v)
		}
	}
}
