package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// genericPollEvery is deliberately slow: the Leases read shells out to a shared production
// hub, so a 5s poll would be too heavy.
const genericPollEvery = 30 * time.Second

// genericTickMsg is scoped to one module: the shell broadcasts messages to every section.
type genericTickMsg struct{ id string }

type genericMsg struct {
	id     string
	status core.Status
}

// genericSection shows a domain read as a core.Status (summary, typed rows, source). The
// Leases domain uses it: the fetch func turns the typed LeasesReport into rows.
type genericSection struct {
	id     string
	title  string // optional heading line (the domain name)
	read   func(ctx context.Context) core.Status
	status core.Status
	loaded bool
	tick   func() tea.Cmd // injectable so tests never sleep
	linesSection
}

func newGenericSection(id string, read func(context.Context) core.Status) *genericSection {
	return &genericSection{id: id, read: read, linesSection: linesSection{th: DefaultTheme()}, tick: func() tea.Cmd {
		return tea.Tick(genericPollEvery, func(time.Time) tea.Msg { return genericTickMsg{id: id} })
	}}
}

func (s *genericSection) SetTheme(t Theme)        { s.th = t }
func (s *genericSection) Capturing() bool         { return false }
func (s *genericSection) Bindings() []key.Binding { return moveBindings() }

func (s *genericSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return genericMsg{id: s.id, status: s.read(ctx)}
	}
}

func (s *genericSection) refresh() tea.Cmd { return s.fetch() }

func (s *genericSection) Init() tea.Cmd { return tea.Batch(s.fetch(), s.tick()) }

func (s *genericSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case genericMsg:
		if m.id == s.id {
			s.status, s.loaded = m.status, true
		}
	case genericTickMsg:
		if m.id == s.id {
			return s, tea.Batch(s.fetch(), s.tick())
		}
	case tea.WindowSizeMsg:
		s.resize(m)
	case tea.KeyMsg:
		s.scrollKey(m, len(s.lines()))
	}
	return s, nil
}

func (s *genericSection) lines() []string {
	out := s.bodyLines()
	if s.title != "" {
		return append([]string{s.th.Header.Render(s.title)}, out...)
	}
	return out
}

func (s *genericSection) bodyLines() []string {
	th := s.th
	w := s.width()
	switch {
	case !s.loaded:
		return []string{th.Text(KindMuted, "loading…")}
	case s.status.Unavailable != "":
		return wrap(th.Text(KindWarn, "state unavailable: "+san(s.status.Unavailable)), w)
	}
	head := th.Header.Render(san(s.status.Summary))
	if s.status.Source != "" {
		head += " " + th.Badge(KindWarn, san(s.status.Source))
	}
	out := []string{head}
	if len(s.status.Rows) > 0 {
		for _, r := range s.status.Rows {
			out = append(out, "  "+th.Badge(toneKind(r.Tone), san(r.State))+" "+san(r.Resource)+" "+th.Text(KindMuted, san(r.Detail)))
		}
		return out
	}
	for _, l := range s.status.Lines {
		out = append(out, "  "+san(l))
	}
	return out
}

func (s *genericSection) View() string { return s.render(s.lines()) }
