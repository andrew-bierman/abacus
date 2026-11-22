package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// doctorMsg carries core's typed doctor report; the section only draws it.
type doctorMsg struct{ report core.Report }

// doctorSection is the last tab: core.DoctorReport for every detected module. It loads on
// first selection and on `r`, never on a timer, because doctor shells out to each tool.
type doctorSection struct {
	entries []core.Entry
	report  core.Report
	loaded  bool
	asked   bool
	th      Theme
	geom
	scroll scroller
}

func newDoctorSection(entries []core.Entry) *doctorSection {
	return &doctorSection{entries: entries, th: DefaultTheme()}
}

func (s *doctorSection) SetTheme(t Theme) { s.th = t }
func (s *doctorSection) Capturing() bool  { return false }
func (s *doctorSection) Bindings() []key.Binding {
	return append(moveBindings(), keyReload)
}

func (s *doctorSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return doctorMsg{report: core.DoctorReport(ctx, "", s.entries)}
	}
}

func (s *doctorSection) Init() tea.Cmd { return nil }

func (s *doctorSection) activate() tea.Cmd {
	if s.asked {
		return nil
	}
	s.asked = true
	return s.fetch()
}

func (s *doctorSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case doctorMsg:
		s.report, s.loaded = m.report, true
	case tea.WindowSizeMsg:
		s.resize(m)
	case tea.KeyMsg:
		switch {
		case key.Matches(m, keyUp):
			s.scroll.move(-1, len(s.lines()), s.h)
		case key.Matches(m, keyDown):
			s.scroll.move(1, len(s.lines()), s.h)
		case key.Matches(m, keyReload):
			return s, s.fetch()
		}
	}
	return s, nil
}

func checkKind(c core.CheckStatus) Kind {
	switch c.Normalize() {
	case core.StatusOK:
		return KindOK
	case core.StatusFail:
		return KindFail
	case core.StatusSkip:
		return KindSkip
	}
	return KindWarn
}

func stateKindOf(st core.State) Kind {
	switch st {
	case core.Ready:
		return KindOK
	case core.Broken:
		return KindFail
	case core.Incompatible:
		return KindWarn
	}
	return KindSkip
}

func (s *doctorSection) lines() []string {
	th := s.th
	if !s.loaded {
		return []string{th.Text(KindMuted, "running doctor…")}
	}
	if len(s.report.Modules) == 0 {
		return []string{th.Text(KindMuted, "no tools to check")}
	}
	var out []string
	for i, m := range s.report.Modules {
		if i > 0 {
			out = append(out, "")
		}
		head := th.Header.Render(san(m.Name)) + " " + th.Badge(stateKindOf(m.Detection.State), string(m.Detection.State))
		if v := san(m.Detection.Version); v != "" {
			head += " " + th.Text(KindMuted, v)
		}
		out = append(out, head)
		for _, c := range m.Checks {
			st := c.Status.Normalize()
			out = append(out, "  "+th.Badge(checkKind(st), string(st))+" "+san(c.Name)+"  "+th.Text(KindMuted, san(c.Detail)))
			if c.Fix != "" {
				out = append(out, "      "+th.Text(KindAccent, "fix:")+" "+san(c.Fix))
			}
		}
	}
	return out
}

func (s *doctorSection) View() string {
	out := []string{}
	for _, l := range s.scroll.view(s.lines(), s.h) {
		out = append(out, trunc(l, s.width()))
	}
	return strings.Join(out, "\n") + "\n"
}
