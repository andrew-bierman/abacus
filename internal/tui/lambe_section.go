package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

const (
	pollEvery    = 5 * time.Second
	fetchTimeout = 8 * time.Second
)

func tick() tea.Cmd {
	return tea.Tick(pollEvery, func(time.Time) tea.Msg { return lambeTickMsg{} })
}

type lambeViewMsg struct {
	view lambe.View
	err  error
}
type lambeTickMsg struct{}

// lambeSection renders Lambe's runs, queue and attention, read-only. All data and the
// Rex command come from the lambe core module; this file only draws and routes keys.
type lambeSection struct {
	m       *lambe.Module
	handoff func(argv []string) tea.Cmd
	view    lambe.View
	err     error
	loaded  bool
	cursor  int
	notice  string
	width   int
}

func newLambeSection(m *lambe.Module, handoff func([]string) tea.Cmd) *lambeSection {
	return &lambeSection{m: m, handoff: handoff}
}

func (s *lambeSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		v, err := s.m.Read(ctx)
		return lambeViewMsg{view: v, err: err}
	}
}

func (s *lambeSection) Init() tea.Cmd {
	if s.m.IsDemo() {
		return s.fetch()
	}
	return tea.Batch(s.fetch(), tick())
}

func (s *lambeSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case lambeViewMsg:
		s.view, s.err, s.loaded = msg.view, msg.err, true
		if n := len(s.view.Snapshot.Runs); s.cursor >= n {
			s.cursor = max(0, n-1)
		}
	case lambeTickMsg:
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
			return s, s.handoff(s.m.RexArgv(machine))
		}
	}
	return s, nil
}

func (s *lambeSection) clip(line string) string {
	if s.width > 0 && utf8.RuneCountInString(line) > s.width {
		return string([]rune(line)[:max(0, s.width-1)]) + "…"
	}
	return line
}

func (s *lambeSection) View() string {
	bold := lipgloss.NewStyle().Bold(true)
	var b strings.Builder
	w := func(line string) { b.WriteString(s.clip(line) + "\n") }
	switch {
	case !s.loaded:
		w("loading…")
	case s.err != nil:
		w("state unavailable: " + core.Sanitize(s.err.Error()))
	default:
		w(bold.Render("Runs"))
		for i, r := range s.view.Snapshot.Runs {
			mark := "  "
			if i == s.cursor {
				mark = "> "
			}
			w(mark + r.Line())
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
