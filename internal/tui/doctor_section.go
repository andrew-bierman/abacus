package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// doctorMsg carries core's typed doctor report; the section only draws it.
type doctorMsg struct {
	report core.Report
	seq    int // which fetch produced it; only the latest is applied
}

// doctorSection is the last tab: core.DoctorReport for every detected module. It loads on
// first selection and on `r`, never on a timer, because doctor shells out to each tool.
type doctorSection struct {
	entries []core.Entry
	report  core.Report
	loaded  bool
	asked   bool
	pending bool // a fetch is in flight: r is ignored until its report arrives
	seq     int
	linesSection
}

func newDoctorSection(entries []core.Entry) *doctorSection {
	return &doctorSection{entries: entries, linesSection: linesSection{th: DefaultTheme()}}
}

func (s *doctorSection) Bindings() []key.Binding {
	return append(moveBindings(), keyReload)
}

func (s *doctorSection) fetch() tea.Cmd {
	s.seq++
	s.pending = true
	seq, entries := s.seq, s.entries
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return doctorMsg{report: core.DoctorReport(ctx, "", entries), seq: seq}
	}
}

func (s *doctorSection) Init() tea.Cmd { return nil }

func (s *doctorSection) activate() tea.Cmd {
	if s.asked || s.loaded {
		return nil
	}
	s.asked = true
	return s.fetch()
}

func (s *doctorSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case doctorMsg:
		if m.seq == s.seq { // a report from an older fetch must not overwrite a newer one
			s.report, s.loaded, s.pending = m.report, true, false
		}
	case tea.WindowSizeMsg:
		s.resize(m)
	case tea.KeyMsg:
		if s.scrollKey(m, len(s.lines())) {
			return s, nil
		}
		if key.Matches(m, keyReload) && !s.pending {
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
	var out []string
	if s.pending {
		out = append(out, th.Text(KindMuted, "running doctor…"), "")
	}
	if len(s.report.Modules) == 0 {
		return append(out, th.Text(KindMuted, "no tools to check"))
	}
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

func (s *doctorSection) View() string { return s.render(s.lines()) }
