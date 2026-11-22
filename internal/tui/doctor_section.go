package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	if ov := s.report.Overview; ov != nil {
		out = append(out, s.overviewLines(*ov)...)
		if len(s.report.Modules) > 0 {
			out = append(out, "", th.Header.Render("Details"))
		}
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

// tuiLine is a domain's one-liner in front-end words: the setup key, not the CLI command.
func tuiLine(l core.DomainLine) string {
	if l.Guide != nil {
		return l.Guide.Message + " — press " + l.Guide.Key + " on the " + l.Title + " tab"
	}
	return l.Line
}

// hang wraps text to w cells with a hanging indent, so a wrapped line stays under its label.
func hang(prefix, text string, w int) []string {
	pw := lipgloss.Width(prefix)
	parts := wrap(text, max(w-pw, 8))
	for i := range parts {
		if i == 0 {
			parts[i] = prefix + parts[i]
		} else {
			parts[i] = strings.Repeat(" ", pw) + parts[i]
		}
	}
	return parts
}

// overviewLines leads Doctor: ready or needs attention in plain language, each area in one
// line, and each tool with where it came from.
func (s *doctorSection) overviewLines(ov core.Overview) []string {
	th, w := s.th, s.width()
	todo := 0
	for _, l := range ov.Domains {
		if l.Domain != core.DomainBoard && l.State != core.Connected {
			todo++
		}
	}
	var out []string
	switch {
	case ov.Ready && todo == 0:
		out = append(out, th.Badge(KindOK, "Ready"))
	case ov.Ready:
		out = append(out, th.Badge(KindOK, "Ready")+" "+th.Text(KindMuted, strings.TrimPrefix(ov.Headline, "Ready")))
	default:
		out = append(out, th.Badge(KindWarn, "Needs attention")+" "+th.Text(KindMuted, fmt.Sprintf("%d to set up, see below", todo)))
	}
	out = append(out, "", th.Header.Render("Areas"))
	for _, l := range ov.Domains {
		kind := KindMuted
		switch l.State {
		case core.Connected:
			kind = KindOK
		case core.NeedsSetup, core.Degraded:
			kind = KindWarn
		}
		out = append(out, hang("  "+th.Label.Render(padTo(san(l.Title), 7))+" ", th.Text(kind, san(tuiLine(l))), w)...)
	}
	out = append(out, "", th.Header.Render("Tools"))
	for _, t := range ov.Tools {
		where := th.Badge(KindSkip, "missing")
		switch t.Where {
		case "bundled":
			where = th.Badge(KindOK, "bundled")
		case "PATH", "override":
			where = th.Badge(KindMuted, san(t.Where))
		}
		out = append(out, "  "+th.Label.Render(padTo(san(t.ID), 12))+" "+where+" "+th.Text(KindMuted, san(t.Version)))
	}
	return out
}
