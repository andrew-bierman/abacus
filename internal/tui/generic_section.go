package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
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
	th     Theme
	geom
	scroll scroller
}

func newGenericSection(m core.Module) *genericSection {
	return &genericSection{m: m, th: DefaultTheme(), tick: func() tea.Cmd {
		return tea.Tick(genericPollEvery, func(time.Time) tea.Msg { return genericTickMsg{id: m.ID()} })
	}}
}

func (s *genericSection) SetTheme(t Theme)        { s.th = t }
func (s *genericSection) Capturing() bool         { return false }
func (s *genericSection) Bindings() []key.Binding { return moveBindings() }

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
	case tea.WindowSizeMsg:
		s.resize(m)
	case tea.KeyMsg:
		switch {
		case key.Matches(m, keyUp):
			s.scroll.move(-1, len(s.lines()), s.h)
		case key.Matches(m, keyDown):
			s.scroll.move(1, len(s.lines()), s.h)
		}
	}
	return s, nil
}

// leaseKinds colors the lease state word beads-lease prints after the resource. This is
// presentation only: core.Status carries the line as text today, so the word is recognised
// here but never interpreted.
var leaseKinds = map[string]Kind{"held": KindAccent, "stale": KindWarn, "free": KindOK}

func (s *genericSection) lines() []string {
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
	for _, l := range s.status.Lines {
		out = append(out, s.line(san(l)))
	}
	return out
}

// line badges a "<resource>  <state> ..." row when the state word is a known lease state.
func (s *genericSection) line(l string) string {
	res, rest, ok := strings.Cut(l, "  ")
	if !ok {
		return "  " + l
	}
	word, tail, _ := strings.Cut(rest, " ")
	kind, known := leaseKinds[word]
	if !known {
		return "  " + l
	}
	return "  " + s.th.Badge(kind, word) + " " + res + " " + s.th.Text(KindMuted, tail)
}

func (s *genericSection) View() string {
	all := s.lines()
	out := make([]string, 0, len(all))
	for _, l := range s.scroll.view(all, s.h) {
		out = append(out, trunc(l, s.width()))
	}
	return strings.Join(out, "\n") + "\n"
}
