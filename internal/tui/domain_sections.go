package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// notConnectedSection is what a domain with no usable provider shows: the state, why, and how
// to connect. Board and Tasks also offer the full-view handoff on `o`.
type notConnectedSection struct {
	h        core.Header
	fullView func(dir string) tea.Cmd
	notice   string
	linesSection
}

// bodyRows is how many rows of text the panel holds (0: the height is not known yet).
func (s *notConnectedSection) bodyRows() int {
	h := s.bodyH(s.notice)
	if h <= 0 {
		return 0
	}
	return max(h-panelChromeH, 1)
}

func newNotConnectedSection(h core.Header, fullView func(string) tea.Cmd) *notConnectedSection {
	return &notConnectedSection{h: h, fullView: fullView, linesSection: linesSection{th: DefaultTheme()}}
}

func (s *notConnectedSection) Init() tea.Cmd { return nil }

func (s *notConnectedSection) Bindings() []key.Binding {
	out := moveBindings()
	if s.fullView != nil {
		out = append(out, keyFull)
	}
	return out
}

func (s *notConnectedSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.resize(m)
	case fullViewDoneMsg:
		if m.err != nil {
			s.notice = core.Sanitize(m.err.Error())
		}
	case tea.KeyMsg:
		s.notice = ""
		switch {
		case key.Matches(m, keyUp):
			s.scroll.move(-1, len(s.lines()), s.bodyRows())
			return s, nil
		case key.Matches(m, keyDown):
			s.scroll.move(1, len(s.lines()), s.bodyRows())
			return s, nil
		}
		if key.Matches(m, keyFull) && s.fullView != nil {
			return s, s.fullView(core.FullViewDir(""))
		}
	}
	return s, nil
}

// lines is the panel's text: the state, why, and how to connect.
func (s *notConnectedSection) lines() []string {
	return notConnectedBody(s.th, s.h.Reason, s.h.HowToConnect, innerW(s.width()))
}

func (s *notConnectedSection) View() string {
	th, w := s.th, s.width()
	vis, hidden := s.scroll.view(s.lines(), s.bodyRows())
	if hidden > 0 && s.bodyRows() >= 2 {
		vis = append(append([]string(nil), vis...), th.moreMarker(hidden))
	}
	height := len(vis) + panelChromeH
	if h := s.bodyH(s.notice); h > 0 {
		height = min(height, h)
	}
	out := panel(th, san(s.h.Title), vis, w, height, true)
	if s.notice != "" {
		out = append(out, noticeLine(th, w, s.notice)...)
	}
	return joinLines(out)
}
