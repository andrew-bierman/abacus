package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// genericPollEvery is deliberately slow: modules without a dedicated section (today
// beads-lease) shell out to a shared production hub, so a 5s poll would be too heavy.
const genericPollEvery = 30 * time.Second

// genericTickMsg is scoped to one module: the shell broadcasts messages to every section.
type genericTickMsg struct{ id string }

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
	tick   func() tea.Cmd // injectable so tests never sleep
}

func newGenericSection(m core.Module) *genericSection {
	return &genericSection{m: m, tick: func() tea.Cmd {
		return tea.Tick(genericPollEvery, func(time.Time) tea.Msg { return genericTickMsg{id: m.ID()} })
	}}
}

func (s *genericSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return genericMsg{id: s.m.ID(), status: s.m.Status(ctx)}
	}
}

func (s *genericSection) Init() tea.Cmd { return tea.Batch(s.fetch(), s.tick()) }

func (s *genericSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case genericMsg:
		if m.id == s.m.ID() {
			s.status, s.loaded = m.status, true
		}
	case genericTickMsg:
		if m.id == s.m.ID() {
			return s, tea.Batch(s.fetch(), s.tick())
		}
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
