package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/beaddeck/internal/core"
)

type genericMsg struct {
	id     string
	status core.Status
}

// genericSection shows any module's core status summary, so a new module gets a working
// section before it has a dedicated one.
type genericSection struct {
	m      core.Module
	status core.Status
	loaded bool
}

func (s *genericSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return genericMsg{id: s.m.ID(), status: s.m.Status(ctx)}
	}
}

func (s *genericSection) Init() tea.Cmd { return s.fetch() }

func (s *genericSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(genericMsg); ok && m.id == s.m.ID() {
		s.status, s.loaded = m.status, true
	}
	return s, nil
}

func (s *genericSection) View() string {
	switch {
	case !s.loaded:
		return "loading…\n"
	case s.status.Unavailable != "":
		return "state unavailable: " + s.status.Unavailable + "\n"
	}
	return s.status.Summary + "\n" + strings.Join(s.status.Lines, "\n") + "\n"
}
