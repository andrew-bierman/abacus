package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

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

// settle runs a command and returns its message, or nil when it does not finish promptly:
// a refresh timer would otherwise block (and re-arm) for the whole poll interval.
func settle(cmd tea.Cmd) tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

// drain executes a command tree and feeds every resulting message back to the model.
func drain(m tea.Model, cmd tea.Cmd) tea.Model {
	if cmd == nil {
		return m
	}
	switch msg := settle(cmd).(type) {
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

func TestDemoRendersFixtureData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")
	demo := lambe.NewDemo()
	en := []core.Entry{{Module: demo, Detection: demo.Detect(context.Background())}}
	var m tea.Model = tui.New(en)
	m = drain(m, m.Init())
	m, _ = m.Update(key("4")) // Queues
	v := m.View()
	for _, want := range []string{"Queues", "T-1", "(stalled)", "Q-2", "blocked", "Needs attention", "gate_failed"} {
		if !strings.Contains(v, want) {
			t.Errorf("demo view missing %q:\n%s", want, v)
		}
	}
}
