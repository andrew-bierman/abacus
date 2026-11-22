// Package tui is the interactive shell: a tab per detected module, hosting its section, plus
// a doctor tab. It renders core's typed reports and holds no business logic.
package tui

import (
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

// tab is one header entry. Only ready modules (and doctor) have a section; the rest stay
// visible, dimmed with their state, so a missing section is explained, not silent.
type tab struct {
	name  string
	state core.State
	sec   section
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

// Option customises the shell.
type Option func(*Model)

// WithTheme replaces the default (terminal-detected) theme.
func WithTheme(t Theme) Option { return func(m *Model) { m.th = t } }

// New builds the shell from detection entries. Absent tools get no tab.
func New(entries []core.Entry, opts ...Option) *Model {
	return NewWithHandoff(entries, execHandoff, opts...)
}

// NewWithHandoff injects how a command is handed the terminal (default: run it in place).
func NewWithHandoff(entries []core.Entry, handoff func(argv []string) tea.Cmd, opts ...Option) *Model {
	m := &Model{th: DefaultTheme()}
	for _, o := range opts {
		o(m)
	}
	m.help = help.New()
	m.help.Styles = m.th.HelpStyles()
	m.help.ShortSeparator = " · "
	var present []core.Entry
	for _, e := range entries {
		if e.Detection.State == core.Absent {
			continue
		}
		present = append(present, e)
		t := tab{name: e.Module.Name(), state: e.Detection.State}
		if e.Detection.State == core.Ready {
			t.sec = sectionFor(e.Module, handoff)
		}
		m.tabs = append(m.tabs, t)
	}
	if len(present) > 0 {
		m.tabs = append(m.tabs, tab{name: "Doctor", state: core.Ready, sec: newDoctorSection(present)})
	}
	for _, t := range m.tabs {
		if t.sec != nil {
			t.sec.SetTheme(m.th)
		}
	}
	m.sel = m.step(-1, 1)
	return m
}

// sectionFor picks the dedicated section for a known module, else the generic status view.
func sectionFor(mod core.Module, handoff func([]string) tea.Cmd) section {
	if lm, ok := mod.(*lambe.Module); ok {
		return newLambeSection(lm, handoff)
	}
	return newGenericSection(mod)
}

// execHandoff runs argv with the terminal, returning to the TUI when it exits. It is only
// reached from an explicit keypress in a section.
func execHandoff(argv []string) tea.Cmd {
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { return handoffDoneMsg{err: err} })
}

// handoffDoneMsg reports how a terminal handoff ended; a non-nil err is shown to the user.
type handoffDoneMsg struct{ err error }

// Run starts the program on the alternate screen.
func Run(entries []core.Entry) error {
	_, err := tea.NewProgram(New(entries), tea.WithAltScreen()).Run()
	return err
}

// step is the next selectable tab from index from in direction dir (wrapping), or from itself.
func (m *Model) step(from, dir int) int {
	n := len(m.tabs)
	for i := 1; i <= n; i++ {
		j := ((from+dir*i)%n + n) % n
		if m.tabs[j].sec != nil {
			return j
		}
	}
	return 0
}

func (m *Model) active() section {
	if len(m.tabs) == 0 {
		return nil
	}
	return m.tabs[m.sel].sec
}

func (m *Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		if t.sec != nil {
			cmds = append(cmds, t.sec.Init())
		}
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
	if i < 0 || i >= len(m.tabs) || m.tabs[i].sec == nil {
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
	}
	// Async results and ticks go to every section; each ignores message types it does not own.
	return m, m.broadcast(msg)
}

func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		if t.sec != nil {
			_, c := t.sec.Update(msg)
			cmds = append(cmds, c)
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(k, keyQuit) && (k.String() == "ctrl+c" || m.active() == nil || !m.active().Capturing()) {
		return m, tea.Quit
	}
	sec := m.active()
	if sec == nil {
		return m, nil
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
	out := []key.Binding{keyHelp, keyQuit, keyNext}
	if s := m.active(); s != nil {
		out = append(out, s.Bindings()...)
	}
	return out
}

func (m *Model) FullHelp() [][]key.Binding {
	groups := [][]key.Binding{{keyNext, keyPrev, keyJump, keyHelp, keyQuit}}
	if s := m.active(); s != nil {
		groups = append(groups, s.Bindings())
	}
	return groups
}

func (m *Model) tabLabel(t tab) string {
	switch {
	case t.sec == nil:
		return m.th.TabDim.Render(t.name + " (" + string(t.state) + ")")
	}
	return t.name
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
		switch {
		case t.sec == nil:
			parts[i] = m.tabLabel(t)
		case i == m.sel:
			parts[i] = m.th.ActiveTab(t.name)
		default:
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
	if len(m.tabs) == 0 {
		body := lipgloss.NewStyle().Width(w).Render("No Beads tools detected. Install one (for example lambe), then run `" + brand.Name + " doctor`.")
		out := []string{trunc(m.th.Title.Render(brand.Name+" · Beads tool deck"), w), "", body, "", m.th.Text(KindMuted, "q quits")}
		return strings.Join(m.fit(out, w, m.h), "\n")
	}
	head := m.headerLines(w)
	var body []string
	if m.showHelp {
		body = m.overlayLines(w)
	} else {
		body = strings.Split(strings.TrimRight(m.active().View(), "\n"), "\n")
	}
	_, bh := m.bodySize()
	if bh > 0 && len(body) > bh {
		body = body[:bh]
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
