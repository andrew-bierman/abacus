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

	search     *searchOverlay // global search (ctrl+k) while it is open
	searchHist core.SearchHistory
	note       string // one footer line after a jump that needed saying something

	prompt      *prompt // the `:` command prompt while it is open (prompt.go)
	promptHist  []string
	over        *viewer // the describe or JSON panel while it is open (describe.go)
	helpOff     int     // scroll of the help overlay
	boardTarget string  // where the Board writes to, for the header context (never a credential)
}

// config is what Options set before the sections are built.
type config struct {
	th          Theme
	audit       core.Audit
	setup       *core.Setup
	fullView    func(dir string) tea.Cmd
	fullViewSet bool // WithFullView was given, so a demo session leaves it alone
	history     core.SearchHistory
	historySet  bool
	treeState   *core.TreeState // where the Tasks tree keeps its folds; nil keeps them for the session only
	filters     core.SavedFilters
	filtersSet  bool
	recents     core.RecentTrackers
	recentsSet  bool
	switcher    *core.Switcher // tests inject one; the default is built from the Tasks provider
}

// Option customises the shell.
type Option func(*config)

// WithTheme replaces the default (terminal-detected) theme.
func WithTheme(t Theme) Option { return func(c *config) { c.th = t } }

// WithAudit sets where Tasks writes are audited (default: the abacus state dir).
func WithAudit(a core.Audit) Option { return func(c *config) { c.audit = a } }

// WithTreeState sets where the Tasks tree keeps its fold choices (default: the abacus state dir; a
// demo session keeps them for the session only).
func WithTreeState(t *core.TreeState) Option { return func(c *config) { c.treeState = t } }

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
	if !cfg.historySet { // a demo session keeps no history; any other uses the state dir
		cfg.history = core.DefaultSearchHistory()
		if c, err := (core.ConfigStore{Path: configPath()}).Load(); isDemo(entries) || (err == nil && c.SearchHistory == "off") {
			cfg.history = core.SearchHistory{}
		}
	}
	if !cfg.filtersSet && !isDemo(entries) {
		cfg.filters = core.DefaultSavedFilters()
	}
	if !cfg.recentsSet && !isDemo(entries) {
		cfg.recents = core.DefaultRecentTrackers()
	}
	m := &Model{th: cfg.th, searchHist: cfg.history}
	m.help = help.New()
	m.help.Styles = m.th.HelpStyles()
	m.help.ShortSeparator = " · "
	prov := core.ResolveProviders(entries)
	if prov.BoardWriter != nil {
		m.boardTarget = prov.BoardWriter.Target()
	}
	if isDemo(entries) && !cfg.fullViewSet {
		cfg.fullView = demoFullView
	}
	if cfg.audit == nil {
		cfg.audit = auditFor(entries)
	}
	if cfg.treeState == nil && !isDemo(entries) {
		cfg.treeState = core.DefaultTreeState()
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
		if h, ok := sec.(workspaceHost); ok { // the optional workspace layer; never runs without a key
			h.setWorkspace(newWsLayer(string(d), prov.Workspace, isDemo(entries), handoff))
		}
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
	case core.DomainSessions:
		if p.Sessions != nil {
			return newSessionsSection(p.Sessions.Sessions)
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
	for i, t := range m.tabs {
		cmds = append(cmds, t.sec.Init())
		if d, ok := t.sec.(deactivator); ok && i != m.sel { // a tab that starts hidden does not poll until shown
			d.deactivate()
		}
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
	cmd := m.broadcast(msg)
	if m.search != nil {
		m.search.refresh(m) // an async read may have changed what search sees; never rebuilt in View
	}
	return m, cmd
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
	if key.Matches(k, keyQuit) && (k.String() == "ctrl+c" || !(sec.Capturing() || m.search != nil || m.prompt != nil || m.over != nil)) {
		return m, tea.Quit
	}
	hidden := tooSmall(m.width(), m.h) // the section is not drawn, so it must not act on keys
	m.note = ""
	if m.search != nil {
		return m.searchKey(k, hidden)
	}
	if m.prompt != nil {
		return m.promptKey(k, hidden)
	}
	if m.over != nil {
		return m.viewerKey(k, hidden)
	}
	if sec.Capturing() {
		if hidden {
			return m, nil
		}
		_, cmd := sec.Update(k)
		return m, cmd
	}
	if m.showHelp {
		switch {
		case key.Matches(k, keyHelp) || key.Matches(k, keyBack):
			m.showHelp = false
		case k.Type == tea.KeyUp || k.String() == "k":
			m.helpOff = max(m.helpOff-1, 0)
		case k.Type == tea.KeyDown || k.String() == "j":
			m.helpOff++
		}
		return m, nil
	}
	switch {
	case key.Matches(k, keyHelp):
		m.showHelp, m.helpOff = true, 0
	case key.Matches(k, keySearch) && !hidden:
		return m, m.openSearch()
	case key.Matches(k, keyPrompt) && !hidden:
		m.openPrompt()
	case key.Matches(k, keyDescribe, keyJSON) && !hidden && m.describes():
		m.openViewer(key.Matches(k, keyJSON))
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
	if m.search != nil {
		return searchBindings()
	}
	if m.over != nil {
		return viewerBindings()
	}
	if m.showHelp && !tooSmall(m.width(), m.h) { // the way out is always on the footer, whatever the scroll
		return []key.Binding{keyHelpClose, keyViewerScroll}
	}
	if sec := m.active(); sec.Capturing() {
		return append(append([]key.Binding(nil), sec.Bindings()...), keyQuit, keyHelp, keyNext)
	}
	return append(append([]key.Binding{keyHelp, keyQuit, keyNext, keyPrompt}, m.active().Bindings()...), m.describeKeys()...)
}

// describes is whether the active section has an item to describe.
func (m *Model) describes() bool { _, ok := inner(m.active()).(describable); return ok }

// describeKeys are the keys the shell owns on a tab with something to describe: last in the footer,
// where the section's own keys come first, and in the help overlay.
func (m *Model) describeKeys() []key.Binding {
	if m.describes() {
		return []key.Binding{keyDescribe, keyJSON}
	}
	return nil
}

func (m *Model) FullHelp() [][]key.Binding {
	return [][]key.Binding{{keyNext, keyPrev, keyJump, keySearch, keyPrompt, keyHelp, keyQuit}, append(append([]key.Binding(nil), m.active().Bindings()...), m.describeKeys()...)}
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
	} else {
		line = m.withContext(line, w)
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
	rows = append(append(rows, ""), m.th.Section("Commands (after :, tab completes)"))
	rows = append(rows, m.commandHelpRows(widths)...)
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
	case m.search != nil:
		body = m.search.view(m, w, bh)
	case m.over != nil:
		body = m.over.view(m.th, w, bh)
	case m.showHelp:
		body, m.helpOff = scrollWindow(m.th, m.overlayLines(w, bh), m.helpOff, bh)
	default:
		body = strings.Split(strings.TrimRight(m.active().View(), "\n"), "\n")
	}
	if m.h > 0 { // known height: clip to what is left, even when that is nothing
		body = body[:min(len(body), bh)]
	}
	m.help.Width = w
	foot := m.help.ShortHelpView(m.ShortHelp())
	if m.note != "" {
		foot = m.th.Text(KindWarn, san(m.note))
	}
	if m.prompt != nil {
		foot = m.prompt.footer(m.th, w)
	}
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

// configPath is the abacus config file ("" when there is no home).
func configPath() string {
	p, _ := core.DefaultConfigPath()
	return p
}

// demoFullView is the full-view handoff of a demo session: there is no tracker, so there is
// nothing to open, and the real tool on PATH is never probed or run.
func demoFullView(string) tea.Cmd {
	return func() tea.Msg { return fullViewDoneMsg{err: errors.New("demo data: there is no tracker to open")} }
}
