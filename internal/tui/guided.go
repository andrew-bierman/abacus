package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// refresher is implemented by sections that can reload right now (after a setup step).
type refresher interface{ refresh() tea.Cmd }

const (
	guideEvery   = 5 * time.Second
	setupTimeout = 90 * time.Second // bd init creates a repository and a database
)

type guideMsg struct {
	domain core.DomainID
	g      *core.Guide
}
type guideTickMsg struct{ domain core.DomainID }
type setupAppliedMsg struct {
	domain core.DomainID
	msg    string
	err    error
}

type guidedMode int

const (
	guidedList guidedMode = iota
	guidedConfirm
	guidedBusy
)

// guidedSection fronts a domain section. While the domain needs setup it shows the one guided
// line instead of the section, and the setup key opens a confirm screen with the exact command.
// Nothing runs until y on that screen; the step then goes through core.Setup (confirmed,
// single use, audited). Once nothing is needed it is transparent.
type guidedSection struct {
	domain core.DomainID
	inner  section
	setup  *core.Setup

	guide  *core.Guide
	active bool // this tab is the visible one: only then is the (possibly slow) probe asked
	mode   guidedMode
	plan   core.SetupPlan
	notice string
	tickFn func() tea.Cmd // injectable so tests never sleep
	th     Theme
	geom
}

// stepKey is the key that starts a setup step; keys are a front-end choice, not data.
func stepKey(step core.StepID) string {
	if step == core.StepWorkspace {
		return "i"
	}
	return "s"
}

// guideLine is the one guided sentence: "<message> — press <key> to <action>".
func guideLine(g core.Guide) string {
	return g.Message + " — press " + stepKey(g.Step) + " to " + g.Action
}

func newGuidedSection(d core.DomainID, inner section, setup *core.Setup) *guidedSection {
	s := &guidedSection{domain: d, inner: inner, setup: setup, th: DefaultTheme()}
	s.tickFn = s.tick
	return s
}

func (s *guidedSection) SetTheme(t Theme) { s.th = t; s.inner.SetTheme(t) }
func (s *guidedSection) Capturing() bool {
	return s.mode != guidedList || (s.guide == nil && s.inner.Capturing())
}

func (s *guidedSection) setupKey() key.Binding {
	k := "s"
	if s.guide != nil {
		k = stepKey(s.guide.Step)
	}
	return key.NewBinding(key.WithKeys(k), key.WithHelp(k, "set up"))
}

func (s *guidedSection) Bindings() []key.Binding {
	switch {
	case s.mode == guidedConfirm:
		return []key.Binding{keyYes, keyNo}
	case s.mode == guidedBusy:
		return nil
	case s.guide != nil:
		return []key.Binding{s.setupKey()}
	}
	return s.inner.Bindings()
}

func (s *guidedSection) probe() tea.Cmd {
	d := s.domain
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return guideMsg{domain: d, g: s.setup.Guide(ctx, d)}
	}
}

func (s *guidedSection) tick() tea.Cmd {
	d := s.domain
	return tea.Tick(guideEvery, func(time.Time) tea.Msg { return guideTickMsg{domain: d} })
}

// Init loads the inner section but does not probe: a hidden tab never runs a probe (the Queues
// one shells out to lambe doctor). The probe runs when the tab is first shown.
func (s *guidedSection) Init() tea.Cmd { return tea.Batch(s.inner.Init(), s.tickFn()) }

// activate runs when the tab becomes the visible one: probe now, and forward lazy loading.
func (s *guidedSection) activate() tea.Cmd {
	s.active = true
	cmds := []tea.Cmd{s.probe()}
	if a, ok := s.inner.(activator); ok {
		cmds = append(cmds, a.activate())
	}
	return tea.Batch(cmds...)
}

// deactivate runs when another tab is shown.
func (s *guidedSection) deactivate() { s.active = false }

func (s *guidedSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case guideMsg:
		if m.domain == s.domain {
			s.guide = m.g
		}
		return s, nil
	case guideTickMsg:
		if m.domain == s.domain {
			if !s.active {
				return s, s.tickFn()
			}
			return s, tea.Batch(s.probe(), s.tickFn())
		}
		return s, nil
	case setupAppliedMsg:
		if m.domain != s.domain {
			return s, nil
		}
		s.mode = guidedList
		if m.err != nil {
			s.notice = core.DescribeWriteError(m.err)
			return s, s.probe()
		}
		s.notice, s.guide = m.msg, nil
		cmds := []tea.Cmd{s.probe()}
		if r, ok := s.inner.(refresher); ok {
			cmds = append(cmds, r.refresh())
		}
		return s, tea.Batch(cmds...)
	case tea.WindowSizeMsg:
		s.resize(m)
	case tea.KeyMsg:
		return s.key(m)
	}
	_, c := s.inner.Update(msg)
	return s, c
}

func (s *guidedSection) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s.mode {
	case guidedConfirm:
		switch {
		case key.Matches(k, keyYes):
			s.mode = guidedBusy
			return s, s.apply(s.plan.Confirm())
		case key.Matches(k, keyNo):
			s.mode, s.notice = guidedList, "cancelled: nothing was changed"
		}
		return s, nil
	case guidedBusy:
		return s, nil
	}
	if s.guide != nil {
		s.notice = ""
		if key.Matches(k, s.setupKey()) {
			p, err := s.setup.Plan(s.guide.Step)
			if err != nil {
				s.notice = core.Sanitize(err.Error())
				return s, nil
			}
			s.plan, s.mode = p, guidedConfirm
		}
		return s, nil // the section behind the guide is not interactive until it is set up
	}
	s.notice = ""
	_, c := s.inner.Update(k)
	return s, c
}

func (s *guidedSection) apply(p core.SetupPlan) tea.Cmd {
	d := s.domain
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
		defer cancel()
		msg, err := s.setup.Apply(ctx, p)
		return setupAppliedMsg{domain: d, msg: msg, err: err}
	}
}

func (s *guidedSection) lines() []string {
	th, w := s.th, s.width()
	switch s.mode {
	case guidedConfirm, guidedBusy:
		out := []string{th.Header.Render("Confirm this setup step"), ""}
		for _, l := range s.plan.Lines {
			out = append(out, wrap("  "+san(l), w)...)
		}
		out = append(out, "", th.Text(KindMuted, "exact command:"))
		out = append(out, wrap("  "+san(core.DisplayArgv(s.plan.Argv)), w)...)
		if s.mode == guidedBusy {
			out = append(out, "", th.Text(KindMuted, "applying…"))
		}
		return out
	}
	g := s.guide
	out := []string{th.Header.Render(san(g.Domain.Title())) + " " + th.Badge(KindWarn, "needs setup"), ""}
	out = append(out, wrap(th.Text(KindAccent, san(guideLine(*g))), w)...)
	if g.Detail != "" {
		out = append(out, "")
		out = append(out, wrap(th.Text(KindMuted, san(g.Detail)), w)...)
	}
	if s.notice != "" {
		out = append(out, "", th.Text(KindWarn, san(s.notice)))
	}
	return out
}

func (s *guidedSection) View() string {
	if s.mode != guidedList || s.guide != nil {
		lines := s.lines()
		for i := range lines {
			lines[i] = trunc(lines[i], s.width())
		}
		return strings.Join(lines, "\n") + "\n"
	}
	v := s.inner.View()
	if s.notice != "" {
		return trunc(s.th.Text(KindWarn, san(s.notice)), s.width()) + "\n" + v
	}
	return v
}
