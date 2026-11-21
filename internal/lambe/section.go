package lambe

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const pollEvery = 5 * time.Second

type viewMsg struct {
	view   View
	reason string
}
type tickMsg struct{}

// section is the Lambe TUI model: runs, queue and attention, read-only.
type section struct {
	m      *Module
	view   View
	reason string
	loaded bool
	cursor int
	notice string
	width  int
}

// Section returns the TUI model for this module.
func (m *Module) Section() tea.Model { return &section{m: m} }

func (s *section) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		v, reason := s.m.read(ctx)
		return viewMsg{view: v, reason: reason}
	}
}

func tick() tea.Cmd { return tea.Tick(pollEvery, func(time.Time) tea.Msg { return tickMsg{} }) }

func (s *section) Init() tea.Cmd {
	if s.m.o.Demo {
		return s.fetch()
	}
	return tea.Batch(s.fetch(), tick())
}

func (s *section) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case viewMsg:
		s.view, s.reason, s.loaded = msg.view, msg.reason, true
		if n := len(s.view.Snapshot.Runs); s.cursor >= n {
			s.cursor = max(0, n-1)
		}
	case tickMsg:
		return s, tea.Batch(s.fetch(), tick())
	case tea.WindowSizeMsg:
		s.width = msg.Width
	case tea.KeyMsg:
		s.notice = ""
		runs := s.view.Snapshot.Runs
		switch msg.String() {
		case "up", "k":
			s.cursor = max(0, s.cursor-1)
		case "down", "j":
			s.cursor = min(max(0, len(runs)-1), s.cursor+1)
		case "o":
			if s.cursor >= len(runs) {
				return s, nil
			}
			machine := runs[s.cursor].Machine
			if machine == "" {
				s.notice = "no machine recorded for this run"
				return s, nil
			}
			return s, s.m.o.RexHandoff(machine)
		}
	}
	return s, nil
}

func (s *section) clip(line string) string {
	if s.width > 0 && len([]rune(line)) > s.width {
		return string([]rune(line)[:max(0, s.width-1)]) + "…"
	}
	return line
}

func (s *section) View() string {
	bold := lipgloss.NewStyle().Bold(true)
	var b strings.Builder
	w := func(line string) { b.WriteString(s.clip(line) + "\n") }
	switch {
	case !s.loaded:
		w("loading…")
	case s.reason != "":
		w("state unavailable: " + s.reason)
	default:
		w(bold.Render("Runs"))
		for i, r := range s.view.Snapshot.Runs {
			mark := "  "
			if i == s.cursor {
				mark = "> "
			}
			w(mark + runLine(r))
		}
		w(bold.Render("Queue"))
		for _, q := range s.view.Queue.Rows {
			state := "ready"
			if q.Blocked {
				state = "blocked"
			}
			w(fmt.Sprintf("  %s  %s  %s", q.ID, state, q.Title))
		}
		if n := s.view.Snapshot.NeedsHuman; len(n) > 0 {
			w(bold.Render("Needs attention"))
			for _, h := range n {
				w(fmt.Sprintf("  %s  %s  %s", h.Issue, h.Reason, h.Detail))
			}
		}
		w("j/k move, o open in Rex")
	}
	if s.notice != "" {
		w(s.notice)
	}
	return b.String()
}
