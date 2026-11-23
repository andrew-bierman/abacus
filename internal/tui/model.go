// Package tui is the interactive shell: a tab per detected module, hosting its section, plus
// a doctor tab. It renders core's typed reports and holds no business logic.
package tui

import (
	"context"
	"errors"
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
	th          Theme
	audit       core.Audit
	setup       *core.Setup
	fullView    func(dir string) tea.Cmd
	fullViewSet bool // WithFullView was given, so a demo session leaves it alone
}

// Option customises the shell.
type Option func(*config)

// WithTheme replaces the default (terminal-detected) theme.
func WithTheme(t Theme) Option { return func(c *config) { c.th = t } }

// WithAudit sets where Tasks writes are audited (default: the abacus state dir).
func WithAudit(a core.Audit) Option { return func(c *config) { c.audit = a } }

// WithSetup replaces the first-run setup (tests inject one over fakes; the default is built
// from the detected modules and the standard abacus paths).
func WithSetup(s *core.Setup) Option { return func(c *config) { c.setup = s } }

// WithFullView replaces the handoff to the full interactive view (tests inject a fake;
// the default probes for it and runs it only on an explicit keypress).
func WithFullView(f func(dir string) tea.Cmd) Option {
	return func(c *config) { c.fullView, c.fullViewSet = f, true }
}

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
	if isDemo(entries) && !cfg.fullViewSet {
		cfg.fullView = demoFullView
	}
	if cfg.audit == nil {
		cfg.audit = auditFor(entries)
	}
	if cfg.setup == nil {
		o := core.DefaultSetupOptions()
		o.Audit = cfg.audit
		cfg.setup = core.NewSetup(entries, o)
	}
	var present []core.Entry
	for _, e := range entries {
		if e.Detection.State != core.Absent {
			present = append(present, e)
		}
	}
	for _, d := range core.DomainOrder {
		sec := domainSection(d, prov, handoff, cfg)
		if prov.Guiders[d] != nil && d != core.DomainBoard {
			sec = newGuidedSection(d, sec, cfg.setup)
		}
		m.tabs = append(m.tabs, tab{name: d.Title(), sec: sec})
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
			return newBoardSection(p.Board)
		}
	case core.DomainTasks:
		if p.Tasks != nil {
			return newTasksSection(p, cfg)
		}
	case core.DomainLeases:
		if p.Leases != nil {
			return newLeasesSection(func(ctx context.Context) leasesRead { return readLeases(ctx, p.Leases) })
		}
	case core.DomainQueues:
		if lm, ok := p.Queues.(*lambe.Module); ok {
			return newLambeSection(lm, handoff)
		}
	}
	h, ok := p.Missing[d]
	if !ok { // a provider exists but this front-end has no renderer for it
		h = core.Header{Domain: d, Title: d.Title(), State: core.NotConnected, Reason: "no view for this provider", HowToConnect: core.HowToConnectFor(d)}
	}
	var fv func(string) tea.Cmd
	if d == core.DomainTasks {
		fv = cfg.fullView
	}
	return newNotConnectedSection(h, fv)
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
	if m.h == 0 || m.h >= hRule {
		return 2
	}
	return 1
}

// goTo selects tab i if it is selectable, returning the section's lazy-load command.
func (m *Model) goTo(i int) tea.Cmd {
	if i < 0 || i >= len(m.tabs) {
		return nil
	}
	if d, ok := m.tabs[m.sel].sec.(deactivator); ok && m.sel != i {
		d.deactivate()
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
	hidden := tooSmall(m.width(), m.h) // the section is not drawn, so it must not act on keys
	if sec.Capturing() {
		if hidden {
			return m, nil
		}
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
		if hidden {
			return m, nil
		}
		_, cmd := sec.Update(k)
		return m, cmd
	}
	return m, nil
}

// ShortHelp and FullHelp make the model a help.KeyMap: the footer and overlay are generated
// from the same bindings the keys use. Escape hatches come first so a narrow footer keeps them.
// While the section captures keys (a form, a confirm screen) its own keys come first: the decision
// on the screen outranks the way around the app, so a narrow footer keeps "y apply" and "n cancel".
func (m *Model) ShortHelp() []key.Binding {
	if sec := m.active(); sec.Capturing() {
		return append(append([]key.Binding(nil), sec.Bindings()...), keyQuit, keyHelp, keyNext)
	}
	return append([]key.Binding{keyHelp, keyQuit, keyNext}, m.active().Bindings()...)
}

func (m *Model) FullHelp() [][]key.Binding {
	return [][]key.Binding{{keyNext, keyPrev, keyJump, keyHelp, keyQuit}, m.active().Bindings()}
}

// headerLines is the tab bar (and a rule under it when tall enough). When the tabs do not
// fit it shrinks to the active tab and a position marker, so the active tab is never cut.
func (m *Model) headerLines(w int) []string {
	brandPrefix := ""
	if w >= bpCompact {
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

var helpKeys = table{cols: []column{{min: 13, max: 13, prio: 0}, {min: 1, prio: 1}}, gap: 1}

// overlayLines is the help overlay for a body w wide and h tall (h <= 0: unknown). It is a
// focused panel when the whole text fits inside one, and a plain indented list when a frame
// would cost rows the text needs.
func (m *Model) overlayLines(w, h int) []string {
	var rows []string
	widths := helpKeys.fit(max(w-panelChromeW, 1))
	for gi, g := range m.FullHelp() {
		if gi > 0 {
			rows = append(rows, "")
		}
		for _, b := range g {
			if b.Enabled() {
				hp := b.Help()
				rows = append(rows, helpKeys.row(widths, []string{m.th.Key(hp.Key), hp.Desc}))
			}
		}
	}
	hint := m.th.Text(KindMuted, "? or esc closes")
	if w >= panelMinW && (h <= 0 || len(rows)+2+panelChromeH <= h) { // +2: the blank line and the hint
		return panel(m.th, "Keys", append(rows, "", hint), w, 0, true)
	}
	out := []string{m.th.Header.Render("Keys"), ""}
	for _, r := range rows {
		if r != "" {
			r = "  " + r
		}
		out = append(out, r)
	}
	return append(out, "", hint)
}

func (m *Model) View() string {
	w := m.width()
	head := m.headerLines(w)
	_, bh := m.bodySize()
	var body []string
	switch {
	case tooSmall(w, m.h):
		body = tooSmallLines(m.th, w, m.h)
	case m.showHelp:
		body = m.overlayLines(w, bh)
	default:
		body = strings.Split(strings.TrimRight(m.active().View(), "\n"), "\n")
	}
	if m.h > 0 { // known height: clip to what is left, even when that is nothing
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

// tooSmallLines is the body shown when the terminal cannot hold the layout. The tab bar and
// footer still frame it, so the user can see where they are and quit.
func tooSmallLines(th Theme, w, h int) []string {
	return []string{
		th.Text(KindWarn, "Terminal too small"),
		th.Text(KindMuted, fmt.Sprintf("needs %dx%d, has %dx%d", bpMin, hMin, w, max(h, 0))),
	}
}

// demoModule is implemented by fixture providers (the demo): a session made of them must not
// write to the real audit log.
type demoModule interface{ IsDemo() bool }

type discardAudit struct{}

func (discardAudit) Record(core.AuditEntry) error { return nil }

// auditFor is the audit a session writes to when none was injected: the abacus state dir, except
// for a demo session, which changes nothing and so records nothing.
func auditFor(entries []core.Entry) core.Audit {
	if isDemo(entries) {
		return discardAudit{}
	}
	return core.DefaultAudit()
}

// isDemo reports whether any module in the session is fixture data.
func isDemo(entries []core.Entry) bool {
	for _, e := range entries {
		if d, ok := e.Module.(demoModule); ok && d.IsDemo() {
			return true
		}
	}
	return false
}

// demoFullView is the full-view handoff of a demo session: there is no tracker, so there is
// nothing to open, and the real tool on PATH is never probed or run.
func demoFullView(string) tea.Cmd {
	return func() tea.Msg { return fullViewDoneMsg{err: errors.New("demo data: there is no tracker to open")} }
}
