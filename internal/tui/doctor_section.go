package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		ctx, cancel := context.WithTimeout(context.Background(), doctorTimeout)
		defer cancel()
		return doctorMsg{report: core.DoctorReport(ctx, "", entries), seq: seq}
	}
}

func (s *doctorSection) Init() tea.Cmd { return nil }

func (s *doctorSection) activate() tea.Cmd {
	if s.pending { // a run is already in flight
		return nil
	}
	s.asked = true
	return s.fetch() // every visit re-runs doctor: what you set up elsewhere shows up here
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
	th, w := s.th, s.width()
	if !s.loaded {
		return []string{th.Text(KindMuted, "running doctor…")}
	}
	var out []string
	if s.pending {
		out = append(out, th.Text(KindMuted, "running doctor…"), "")
	}
	if ov := s.report.Overview; ov != nil {
		out = append(out, s.statusLine(*ov), "")
		out = append(out, s.overviewPanels(*ov, w)...)
	}
	if len(s.report.Modules) == 0 {
		return append(out, th.Text(KindMuted, "no tools to check"))
	}
	for _, m := range s.report.Modules {
		out = append(out, s.modulePanel(m, w)...)
	}
	return out
}

// modulePanel is one tool's checks in a panel titled with its name.
func (s *doctorSection) modulePanel(m core.ModuleReport, w int) []string {
	th := s.th
	iw := max(w-panelChromeW, 1)
	head := th.Badge(stateKindOf(m.Detection.State), string(m.Detection.State))
	if v := san(m.Detection.Version); v != "" {
		head += " " + th.Text(KindMuted, v)
	}
	body := []string{head}
	nameW := 0
	for _, c := range m.Checks {
		nameW = max(nameW, min(lipgloss.Width(san(c.Name)), 22))
	}
	for _, c := range m.Checks {
		st := c.Status.Normalize()
		prefix := th.Badge(checkKind(st), string(st)) + " " + padTo(trunc(san(c.Name), nameW), nameW) + "  "
		body = append(body, hang(prefix, th.Text(KindMuted, san(c.Detail)), iw)...)
		if c.Fix != "" {
			body = append(body, hang("    "+th.Text(KindAccent, "fix:")+" ", san(c.Fix), iw)...)
		}
	}
	return panel(th, san(m.Name), body, w, 0, false)
}

// statusLine leads Doctor: ready or needs attention in plain language.
func (s *doctorSection) statusLine(ov core.Overview) string {
	th := s.th
	todo := 0
	for _, l := range ov.Domains {
		if l.Domain != core.DomainBoard && l.State != core.Connected {
			todo++
		}
	}
	switch {
	case ov.Ready && todo == 0:
		return th.Badge(KindOK, "Ready")
	case ov.Ready:
		return th.Badge(KindOK, "Ready") + " " + th.Text(KindMuted, strings.TrimSpace(strings.TrimPrefix(ov.Headline, "Ready")))
	}
	return th.Badge(KindWarn, "Needs attention") + " " + th.Text(KindMuted, fmt.Sprintf("%d to set up, see below", todo))
}

// overviewPanels draws the areas and the tools, side by side from bpWide columns and stacked
// below it, each in its own panel.
func (s *doctorSection) overviewPanels(ov core.Overview, w int) []string {
	th := s.th
	lw, rw := w, w
	if w >= bpWide {
		lw = (w - 1) / 2
		rw = w - 1 - lw
	}
	areas := s.areaLines(ov, max(lw-panelChromeW, 1))
	tools := s.toolLines(ov, max(rw-panelChromeW, 1))
	if w < bpWide {
		return append(panel(th, "Areas", areas, lw, 0, false), panel(th, "Tools", tools, rw, 0, false)...)
	}
	rows := max(len(areas), len(tools))
	pad := func(l []string) []string {
		return append(append([]string(nil), l...), make([]string, rows-len(l))...)
	}
	left := panel(th, "Areas", pad(areas), lw, 0, false)
	right := panel(th, "Tools", pad(tools), rw, 0, false)
	out := make([]string, len(left))
	for i := range left {
		out[i] = left[i] + " " + right[i]
	}
	return out
}

// areaLines is each fleet area in one wrapped line under its label.
func (s *doctorSection) areaLines(ov core.Overview, iw int) []string {
	th := s.th
	var out []string
	for _, l := range ov.Domains {
		kind := KindMuted
		switch l.State {
		case core.Connected:
			kind = KindOK
		case core.NeedsSetup, core.Degraded:
			kind = KindWarn
		}
		out = append(out, hang(th.Label.Render(padTo(san(l.Title), 7))+" ", th.Text(kind, san(tuiLine(l))), iw)...)
	}
	return out
}

// toolLines is each tool with where it was found and its version; a note wraps under it.
func (s *doctorSection) toolLines(ov core.Overview, iw int) []string {
	th := s.th
	var out []string
	for _, t := range ov.Tools {
		where := th.Badge(KindSkip, "missing")
		switch t.Where {
		case "bundled":
			where = th.Badge(KindOK, "bundled")
		case "PATH", "override":
			where = th.Badge(KindMuted, san(t.Where))
		}
		out = append(out, trunc(th.Label.Render(padTo(san(t.ID), 12))+" "+where+" "+th.Text(KindMuted, san(t.Version)), iw))
		if t.Note != "" {
			out = append(out, hang("  ", th.Text(KindWarn, san(t.Note)), iw)...)
		}
	}
	return out
}

func (s *doctorSection) View() string { return s.render(s.lines()) }

// doctorTimeout is Doctor's own deadline: it runs every tool's doctor, which can take far longer
// than the 8s a single section read gets.
const doctorTimeout = 30 * time.Second

// tuiLine is a domain's one-liner in front-end words: the setup key, not the CLI command.
func tuiLine(l core.DomainLine) string {
	if l.Guide != nil {
		return l.Guide.Message + " — press " + stepKey(l.Guide.Step) + " on the " + l.Title + " tab"
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
