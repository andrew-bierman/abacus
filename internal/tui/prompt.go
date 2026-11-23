package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The `:` command prompt (k9s style). It is a capturing state of the shell, drawn in the footer row,
// and it only navigates (it may keep UI layout state, such as the Tasks tree or list mode, but never
// changes a tracker, board, lease or queue): every line goes through core.ParseCommand into a typed
// core.Action of a navigation kind (go to a tab or a Tasks view, open search, apply a list filter, show the context,
// help, quit). Nothing here plans, confirms, applies, audits or runs anything: this file and its
// siblings name no writer, and a test reads their source to keep it so. Parsing, completion and
// the closest-match suggestion are core's pure functions; this file holds the line, the history and
// the drawing.
//
// Key ownership: `:` opens it (unbound everywhere else, and only where no section is capturing, so
// a form, the confirm screen or a filter always keeps its own colon). ctrl+k belongs to search; in
// the prompt it hands over to search. ctrl+p and ctrl+n recall history in whichever of the two is
// open (commands here, searches there): the modals never stack, so one key never has two meanings at
// once.

var keyPrompt = key.NewBinding(key.WithKeys(":"), key.WithHelp(":", "command"))

// promptHistMax is how many commands the session remembers; promptHardMax bounds what one line can
// hold (a paste of a megabyte must not fill memory). A line over core.MaxCommandLen is kept as typed
// and refused at enter with its reason, never cut: a truncated line could turn into another command.
const (
	promptHistMax = 100
	promptHardMax = 2000
)

type prompt struct {
	text   []rune
	draft  []rune // what was typed before history recall began
	histAt int    // -1: not recalling
	err    string // why the last line was refused; shown in place of the line until the next key

	// a completion in progress: the candidates for the text at the time of the first tab
	cands []string
	cycle int
}

func (m *Model) openPrompt() { m.prompt = &prompt{histAt: -1} }

func (p *prompt) edit(t []rune) {
	p.text, p.err, p.cands, p.cycle, p.histAt = t, "", nil, 0, -1
	if len(t) > promptHardMax {
		p.text, p.err = t[:promptHardMax], tooLong
	}
}

const tooLong = "command too long (more than 300 characters)"

// promptKey handles a key while the prompt is open: nothing reaches a section or the shell's global
// keys, so q types a q and a hidden confirm cannot be answered through it.
func (m *Model) promptKey(k tea.KeyMsg, hidden bool) (tea.Model, tea.Cmd) {
	p := m.prompt
	if k.Type == tea.KeyEsc {
		m.prompt = nil
		return m, nil
	}
	if hidden {
		return m, nil
	}
	switch {
	case k.Type == tea.KeyCtrlK:
		m.prompt = nil
		return m, m.openSearch()
	case k.Type == tea.KeyEnter:
		return m, m.promptEnter()
	case k.Type == tea.KeyTab:
		p.complete(1)
	case k.Type == tea.KeyShiftTab:
		p.complete(-1)
	case k.Type == tea.KeyUp || k.Type == tea.KeyCtrlP:
		p.recall(m.promptHist, -1)
	case k.Type == tea.KeyDown || k.Type == tea.KeyCtrlN:
		p.recall(m.promptHist, 1)
	case k.Type == tea.KeyCtrlU:
		p.edit(nil)
	case k.Type == tea.KeyBackspace:
		if n := len(p.text); n > 0 {
			p.edit(append([]rune(nil), p.text[:n-1]...))
		} else {
			m.prompt = nil // backspace on an empty line leaves, as in k9s
		}
	case k.Type == tea.KeyRunes || k.Type == tea.KeySpace:
		next := append([]rune(nil), p.text...)
		for _, r := range k.Runes {
			if !unicode.IsControl(r) {
				next = append(next, r)
			}
		}
		p.edit(next)
	}
	return m, nil
}

// promptEnter runs the line. An empty line just closes. A refused line keeps the prompt open with
// the reason, so a typo is fixed rather than retyped; it is not kept in the history.
func (m *Model) promptEnter() tea.Cmd {
	p := m.prompt
	line := strings.TrimSpace(string(p.text))
	if line == "" {
		m.prompt = nil
		return nil
	}
	a, err := core.ParseCommand(string(p.text)) // the raw line: a length limit is on what was typed
	if err != nil {
		p.err = err.Error()
		return nil
	}
	m.prompt = nil
	if n := len(m.promptHist); n == 0 || m.promptHist[n-1] != line {
		m.promptHist = append(m.promptHist, line)
		if len(m.promptHist) > promptHistMax {
			m.promptHist = m.promptHist[len(m.promptHist)-promptHistMax:]
		}
	}
	return m.runAction(a)
}

// complete fills the line from core's candidates: one candidate completes it; several first extend it
// to their common prefix, then each tab steps to the next (shift+tab back). Any edit ends the cycle.
func (p *prompt) complete(d int) {
	if p.cands == nil {
		cs := core.CompleteCommand(string(p.text))
		switch {
		case len(cs) == 0:
			return
		case len(cs) == 1:
			p.text, p.err = []rune(cs[0]), ""
			return
		}
		p.cands, p.cycle = cs, -1
		if cp := core.CommonPrefix(cs); len([]rune(cp)) > len(p.text) {
			p.text, p.err = []rune(cp), ""
			return
		}
	}
	n := len(p.cands)
	p.cycle = ((p.cycle+d)%n + n) % n
	p.text, p.err = []rune(p.cands[p.cycle]), ""
}

// recall steps through the commands run this session (older with d < 0), keeping what was typed as a
// draft; past the newest it returns to the draft.
func (p *prompt) recall(hist []string, d int) {
	if len(hist) == 0 {
		return
	}
	if p.histAt < 0 {
		if d > 0 {
			return
		}
		p.draft, p.histAt = p.text, len(hist)
	}
	at := p.histAt + d
	switch {
	case at >= len(hist):
		p.text, p.histAt = p.draft, -1
	default:
		p.histAt = max(at, 0)
		p.text = []rune(hist[p.histAt])
	}
	p.err, p.cands, p.cycle = "", nil, 0
}

// footer is the prompt's row: the colon and the line with a caret, then on the right the candidates
// (when there are several) and the keys that matter, as much as fits. A refused line shows its
// reason on the whole row instead, until the next key.
func (p *prompt) footer(th Theme, w int) string {
	if p.err != "" {
		return trunc(th.Text(KindWarn, san(p.err)), w)
	}
	typed := san(string(p.text))
	if lipgloss.Width(":"+typed+"▏") > w { // keep the end, where the person is typing: drop whole runes from the front by cells
		r := []rune(typed)
		for len(r) > 0 && lipgloss.Width(":…"+string(r)+"▏") > w {
			r = r[1:]
		}
		typed = "…" + string(r)
	}
	left := th.Key(":") + typed + "▏"
	var hints []string
	if cs := core.CompleteCommand(string(p.text)); len(cs) > 1 {
		var names []string
		for _, c := range cs {
			names = append(names, strings.TrimSpace(c))
		}
		hints = append(hints, strings.Join(names, " ")+" · esc close")
	}
	hints = append(hints, "tab complete · enter go · esc close", "esc close")
	for _, h := range hints {
		right := th.Text(KindMuted, h)
		if lipgloss.Width(left)+2+lipgloss.Width(right) <= w {
			return left + strings.Repeat(" ", w-lipgloss.Width(left)-lipgloss.Width(right)) + right
		}
	}
	return trunc(left, w)
}

// inner is the section behind a setup guard, once nothing is left to set up; a guarded tab keeps its
// guard, which offers none of the optional capabilities below.
func inner(s section) section {
	if g, ok := s.(*guidedSection); ok && g.guide == nil {
		return g.inner
	}
	return s
}

// The optional capabilities a section may have for the prompt and the describe keys. The shell
// asks; a section that does not have one is simply skipped.
type (
	// viewSwitcher selects one of the section's views by name; a non-empty note says why it could not.
	viewSwitcher interface {
		switchView(name string) (tea.Cmd, string)
	}
	// filterer applies (or, with "", clears) the section's list filter.
	filterer interface{ setFilter(text string) string }
	// trackerSwitcher opens the confirm screen for switching to the tracker named; a non-empty note says why not.
	trackerSwitcher interface{ switchTo(name string) string }
	// contexter says what the header may show about this section.
	contexter interface{ contextLine() core.ContextLine }
)

func (m *Model) tabIndex(title string) int {
	for i, t := range m.tabs {
		if t.name == title {
			return i
		}
	}
	return -1
}

// runAction carries out a parsed command. Every case is navigation: select a tab or view, open
// search, set a list filter, show a line of context, open help, quit.
func (m *Model) runAction(a core.Action) tea.Cmd {
	switch a.Kind {
	case core.ActHelp:
		m.showHelp, m.helpOff = true, 0
	case core.ActQuit:
		return tea.Quit
	case core.ActSearch:
		cmd := m.openSearch()
		if a.Arg != "" {
			m.search.edit(cleanQuery([]rune(a.Arg)))
			m.search.refresh(m)
		}
		return cmd
	case core.ActContext:
		if a.Arg != "" { // :ctx NAME opens the same confirm screen as P; nothing changes until y
			i := m.tabIndex(core.DomainTasks.Title())
			if i >= 0 {
				if sw, ok := inner(m.tabs[i].sec).(trackerSwitcher); ok {
					cmd := m.goTo(i)
					m.note = sw.switchTo(a.Arg)
					return cmd
				}
			}
			m.note = "switching trackers is not available here"
			return nil
		}
		if c := m.contextLine().String(); c != "" {
			m.note = "context: " + c
		} else {
			m.note = "context: not known yet (the tracker has not loaded)"
		}
	case core.ActGoTab, core.ActGoView, core.ActFilter:
		i := m.tabIndex(a.Tab)
		if i < 0 {
			m.note = a.Tab + " is not available"
			return nil
		}
		cmd := m.goTo(i)
		sec := inner(m.tabs[i].sec)
		switch a.Kind {
		case core.ActGoView:
			if v, ok := sec.(viewSwitcher); ok {
				c, note := v.switchView(a.View)
				m.note = note
				return tea.Batch(cmd, c)
			}
			m.note = a.Tab + " has no " + a.View + " view yet: finish its setup first"
		case core.ActFilter:
			if f, ok := sec.(filterer); ok {
				m.note = f.setFilter(a.Arg)
			} else {
				m.note = a.Tab + " cannot be filtered yet: finish its setup first"
			}
		}
		return cmd
	}
	return nil
}

// commandHelpRows are the prompt's commands for the help overlay: the tabs on one row, the views on
// one, then each command that takes an argument or has a meaning of its own.
func (m *Model) commandHelpRows(widths []int) []string {
	var rows []string
	for _, g := range core.CommandGroups() {
		if g.Title == "Other" {
			for _, c := range g.Commands {
				name := c.Name
				if c.Arg.Name != "" {
					name += " " + c.Arg.Name
				}
				rows = append(rows, helpKeys.row(widths, []string{m.th.Key(name), c.Desc}))
			}
			continue
		}
		var names []string
		for _, c := range g.Commands {
			names = append(names, c.Name)
		}
		rows = append(rows, helpKeys.row(widths, []string{m.th.Key(g.Title), strings.Join(names, " ")}))
	}
	return rows
}
