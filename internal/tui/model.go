// Package tui is the interactive shell: a tab per detected module, hosting its section, plus
// a doctor tab. It renders core's typed reports and holds no business logic.
package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// tab is one header entry: a fleet domain (or Doctor). Every domain always has a section: one
// with no provider shows "not connected" and how to connect instead of disappearing.
type tab struct {
	name string
	sec  section
}

// Model is the shell.
type Model struct {
	tabs     []tab
	sel      int
	w, h     int
	th       Theme
	help     help.Model
	showHelp bool
}

// config is what Options set before the sections are built.
type config struct {
	th       Theme
	audit    core.Audit
	fullView func(dir string) tea.Cmd
}

// Option customises the shell.
type Option func(*config)

// WithTheme replaces the default (terminal-detected) theme.
func WithTheme(t Theme) Option { return func(c *config) { c.th = t } }

// WithAudit sets where Tasks writes are audited (default: the abacus state dir).
func WithAudit(a core.Audit) Option { return func(c *config) { c.audit = a } }

// WithFullView replaces the handoff to the full interactive view (tests inject a fake;
// the default probes for it and runs it only on an explicit keypress).
func WithFullView(f func(dir string) tea.Cmd) Option { return func(c *config) { c.fullView = f } }

// New builds the cockpit from detection entries.
func New(entries []core.Entry, opts ...Option) *Model {
	return NewWithHandoff(entries, execHandoff, opts...)
}

// NewWithHandoff injects how a command is handed the terminal (default: run it in place).
func NewWithHandoff(entries []core.Entry, handoff func(argv []string) tea.Cmd, opts ...Option) *Model {
	cfg := &config{th: DefaultTheme(), fullView: defaultFullView}
	for _, o := range opts {
		o(cfg)
	}
	m := &Model{th: cfg.th}
	m.help = help.New()
	m.help.Styles = m.th.HelpStyles()
	m.help.ShortSeparator = " · "
	prov := core.ResolveProviders(entries)
	if cfg.audit == nil {
		cfg.audit = core.DefaultAudit()
	}
	var present []core.Entry
	for _, e := range entries {
		if e.Detection.State != core.Absent {
			present = append(present, e)
		}
	}
	for _, d := range core.DomainOrder {
		m.tabs = append(m.tabs, tab{name: d.Title(), sec: domainSection(d, prov, handoff, cfg)})
	}
	m.tabs = append(m.tabs, tab{name: "Doctor", sec: newDoctorSection(present)})
	for _, t := range m.tabs {
		t.sec.SetTheme(m.th)
	}
	return m
}

// domainSection picks the section for a domain: the real one when a provider is usable,
// otherwise the not-connected one.
func domainSection(d core.DomainID, p core.Providers, handoff func([]string) tea.Cmd, cfg *config) section {
	switch d {
	case core.DomainBoard:
		if p.Board != nil {
			return newBoardSection(p.Board, cfg.fullView)
		}
	case core.DomainTasks:
		if p.Tasks != nil {
			return newTasksSection(p, cfg)
		}
	case core.DomainLeases:
		if p.Leases != nil {
			g := newGenericSection("leases", func(ctx context.Context) core.Status { return leasesStatus(p.Leases, ctx) })
			g.title = "Leases"
			return g
		}
	case core.DomainQueues:
		if lm, ok := p.Queues.(*lambe.Module); ok {
			return newLambeSection(lm, handoff)
		}
	}
	h, ok := p.Missing[d]
	if !ok { // a provider exists but this front-end has no renderer for it
		h = core.Header{Domain: d, Title: d.Title(), State: core.NotConnected, Reason: "no view for this provider", HowToConnect: core.HowToConnect[d]}
	}
	var fv func(string) tea.Cmd
	if d == core.DomainBoard || d == core.DomainTasks {
		fv = cfg.fullView
	}
	return newNotConnectedSection(h, fv)
}

// leasesStatus reads the Leases domain and shapes the typed report as rows for the section.
func leasesStatus(p core.LeaseProvider, ctx context.Context) core.Status {
	r, err := p.Leases(ctx)
	if err != nil {
		return core.Status{Unavailable: core.Sanitize(err.Error())}
	}
	held, stale, free := 0, 0, 0
	st := core.Status{}
	if r.Source != "" && r.Source != core.SourceLive {
		st.Source = r.Source
	}
	for _, l := range r.Leases {
		row := core.Row{Resource: l.Resource, State: string(l.State), Tone: core.ToneMuted}
		switch l.State {
		case core.LeaseHeld:
			held++
			exp := l.ExpiresAt
			if exp == "" {
				exp = "?"
			}
			row.Tone, row.Detail = core.ToneInfo, "by "+l.Holder+" (expires "+exp+")"
		case core.LeaseStale:
			stale++
			row.Tone, row.Detail = core.ToneWarn, "(last holder "+l.Holder+")"
		case core.LeaseFree:
			free++
			row.Tone = core.ToneOK
		}
		line := row.Resource + "  " + row.State
		if row.Detail != "" {
			line += " " + row.Detail
		}
		st.Lines, st.Rows = append(st.Lines, line), append(st.Rows, row)
	}
	st.Summary = fmt.Sprintf("%d held, %d stale, %d free", held, stale, free)
	return st
}

// defaultFullView probes for the full-view tool and, only if it answers, hands it the
// terminal in dir. It runs only from an explicit keypress, never at startup or in tests.
func defaultFullView(dir string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		bin, err := core.ProbeFullView(ctx, exec.LookPath, core.ExecRunner{})
		cancel()
		if err != nil {
			return fullViewDoneMsg{err: err}
		}
		c := exec.Command(bin)
		c.Dir = dir
		return tea.ExecProcess(c, func(err error) tea.Msg { return fullViewDoneMsg{err: err} })()
	}
}

// fullViewDoneMsg reports how the full-view handoff ended; a non-nil err is shown to the user.
type fullViewDoneMsg struct{ err error }

// execHandoff runs argv with the terminal, returning to the TUI when it exits. It is only
// reached from an explicit keypress in a section.
func execHandoff(argv []string) tea.Cmd {
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { return handoffDoneMsg{err: err} })
}

// handoffDoneMsg reports how a terminal handoff ended; a non-nil err is shown to the user.
type handoffDoneMsg struct{ err error }

// Seams for tests: Run must probe the terminal background before Bubble Tea takes the input.
var (
	probeBackground = lipgloss.HasDarkBackground
	startProgram    = func(m tea.Model, opts ...tea.ProgramOption) error {
		_, err := tea.NewProgram(m, opts...).Run()
		return err
	}
)

// Run starts the program on the alternate screen. Adaptive colors query the terminal for its
// background the first time they render; doing that once here, before Bubble Tea owns stdin,
// keeps the reply from being read as keystrokes.
func Run(entries []core.Entry) error {
	probeBackground()
	return startProgram(New(entries), tea.WithAltScreen())
}

// step is the next tab from index from in direction dir (wrapping).
func (m *Model) step(from, dir int) int {
	n := len(m.tabs)
	return ((from+dir)%n + n) % n
}

func (m *Model) active() section { return m.tabs[m.sel].sec }

func (m *Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		cmds = append(cmds, t.sec.Init())
	}
	if a, ok := m.active().(activator); ok {
		cmds = append(cmds, a.activate())
	}
	return tea.Batch(cmds...)
}

// bodySize is the room left for a section between header and footer.
func (m *Model) bodySize() (int, int) {
	h := 0
	if m.h > 0 {
		h = max(0, m.h-m.headerHeight()-1)
	}
	return m.width(), h
}

func (m *Model) width() int {
	if m.w <= 0 {
		return defaultWidth
	}
	return m.w
}

// headerHeight is the tab line, plus a rule when there is height to spare.
func (m *Model) headerHeight() int {
	if m.h == 0 || m.h >= 16 {
		return 2
	}
	return 1
}

// goTo selects tab i if it is selectable, returning the section's lazy-load command.
func (m *Model) goTo(i int) tea.Cmd {
	if i < 0 || i >= len(m.tabs) {
		return nil
	}
	m.sel = i
	if a, ok := m.tabs[i].sec.(activator); ok {
		return a.activate()
	}
	return nil
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		bw, bh := m.bodySize()
		return m, m.broadcast(tea.WindowSizeMsg{Width: bw, Height: bh})
	case tea.KeyMsg:
		return m.key(msg)
	case fullViewDoneMsg:
		// Only the visible tab shows the notice: the other tab with a handoff would repeat it.
		_, c := m.active().Update(msg)
		return m, c
	}
	// Async results and ticks go to every section; each ignores message types it does not own.
	return m, m.broadcast(msg)
}

func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		_, c := t.sec.Update(msg)
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	sec := m.active()
	if key.Matches(k, keyQuit) && (k.String() == "ctrl+c" || !sec.Capturing()) {
		return m, tea.Quit
	}
	if sec.Capturing() {
		_, cmd := sec.Update(k)
		return m, cmd
	}
	if m.showHelp {
		if key.Matches(k, keyHelp) || key.Matches(k, keyBack) {
			m.showHelp = false
		}
		return m, nil
	}
	switch {
	case key.Matches(k, keyHelp):
		m.showHelp = true
	case key.Matches(k, keyNext):
		return m, m.goTo(m.step(m.sel, 1))
	case key.Matches(k, keyPrev):
		return m, m.goTo(m.step(m.sel, -1))
	case key.Matches(k, keyJump):
		return m, m.goTo(int(k.Runes[0] - '1'))
	default:
		_, cmd := sec.Update(k)
		return m, cmd
	}
	return m, nil
}

// ShortHelp and FullHelp make the model a help.KeyMap: the footer and overlay are generated
// from the same bindings the keys use. Escape hatches come first so a narrow footer keeps them.
func (m *Model) ShortHelp() []key.Binding {
	return append([]key.Binding{keyHelp, keyQuit, keyNext}, m.active().Bindings()...)
}

func (m *Model) FullHelp() [][]key.Binding {
	return [][]key.Binding{{keyNext, keyPrev, keyJump, keyHelp, keyQuit}, m.active().Bindings()}
}

// headerLines is the tab bar (and a rule under it when tall enough). When the tabs do not
// fit it shrinks to the active tab and a position marker, so the active tab is never cut.
func (m *Model) headerLines(w int) []string {
	brandPrefix := ""
	if w >= 60 {
		brandPrefix = m.th.Title.Render(brand.Name) + "  "
	}
	parts := make([]string, len(m.tabs))
	for i, t := range m.tabs {
		if i == m.sel {
			parts[i] = m.th.ActiveTab(t.name)
		} else {
			parts[i] = m.th.TabIdle.Render(t.name)
		}
	}
	line := brandPrefix + strings.Join(parts, "")
	if lipgloss.Width(line) > w {
		pos := m.th.Text(KindMuted, " "+strconv.Itoa(m.sel+1)+"/"+strconv.Itoa(len(m.tabs)))
		line = m.th.ActiveTab(m.tabs[m.sel].name) + pos
	}
	out := []string{trunc(line, w)}
	if m.headerHeight() == 2 {
		out = append(out, m.th.Divider.Render(strings.Repeat("─", w)))
	}
	return out
}

func (m *Model) overlayLines(w int) []string {
	out := []string{m.th.Header.Render("Keys"), ""}
	groups := m.FullHelp()
	for gi, g := range groups {
		if gi > 0 {
			out = append(out, "")
		}
		for _, b := range g {
			if b.Enabled() {
				h := b.Help()
				out = append(out, "  "+m.th.Key(padTo(h.Key, 13))+" "+h.Desc)
			}
		}
	}
	return append(out, "", m.th.Text(KindMuted, "? or esc closes"))
}

func (m *Model) View() string {
	w := m.width()
	head := m.headerLines(w)
	var body []string
	if m.showHelp {
		body = m.overlayLines(w)
	} else {
		body = strings.Split(strings.TrimRight(m.active().View(), "\n"), "\n")
	}
	if m.h > 0 { // known height: clip to what is left, even when that is nothing
		_, bh := m.bodySize()
		body = body[:min(len(body), bh)]
	}
	m.help.Width = w
	foot := m.help.ShortHelpView(m.ShortHelp())
	all := append(append([]string{}, head...), body...)
	if m.h > 0 {
		// Pad so the footer sits on the last row.
		for len(all) < m.h-1 {
			all = append(all, "")
		}
	}
	all = append(all, foot)
	return strings.Join(m.fit(all, w, m.h), "\n")
}

// fit enforces the width on every line and the height on the whole screen, header first.
func (m *Model) fit(lines []string, w, h int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, trunc(l, w))
	}
	if h > 0 && len(out) > h {
		out = out[:h]
	}
	return out
}
