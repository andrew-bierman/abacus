package tui

import (
	"strconv"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// `d` describes the item under the cursor and `y` shows it as typed JSON: two read-only panels over
// the active tab. Both are snapshots of the report at the moment of the key (a refresh behind them
// does not change what is on screen) and neither can write anything: they hold lines to draw and a
// scroll offset, nothing else.

var (
	keyDescribe = key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "describe"))
	keyJSON     = key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "json"))

	keyViewerClose  = key.NewBinding(key.WithKeys("esc", "q", "d", "y"), key.WithHelp("esc", "close"))
	keyViewerScroll = key.NewBinding(key.WithKeys("up", "down", "k", "j", "pgup", "pgdown", "home", "end", "g", "G"), key.WithHelp("↑/↓", "scroll"))
)

// keyHelpClose is the help overlay's footer hint; the keys that close it are ? and esc.
var keyHelpClose = key.NewBinding(key.WithKeys("?", "esc"), key.WithHelp("?/esc", "close"))

func viewerBindings() []key.Binding { return []key.Binding{keyViewerClose, keyViewerScroll} }

// maxViewerLines bounds what a panel keeps: a huge item is cut with a line that says so.
const maxViewerLines = 2000

// describable is implemented by a section that has an item under its cursor to show. A non-empty
// reason means there is none.
type describable interface {
	describeSelected() (describeItem, string)
}

// describeItem is one selected item: its title, how to draw it at a width, and the typed report the
// JSON panel prints (the one `--json` prints for the same item).
type describeItem struct {
	title  string
	lines  func(w int) []string
	report any
}

// viewer is an open panel.
type viewer struct {
	title string
	lines func(w int) []string // the content at an inner width
	off   int
	rows  int // visible rows at the last draw, for page keys
	// cache: the lines for the last width asked
	cw    int
	cache []string
}

func (v *viewer) content(w int) []string {
	if v.cache == nil || v.cw != w {
		ls := v.lines(w)
		if len(ls) > maxViewerLines {
			cut := len(ls) - maxViewerLines
			ls = append(append([]string(nil), ls[:maxViewerLines]...), "… "+strconv.Itoa(cut)+" more lines not shown")
		}
		v.cache, v.cw = ls, w
	}
	return v.cache
}

// openViewer opens the describe (json false) or JSON (json true) panel for the active section's
// selected item, or says why there is none.
func (m *Model) openViewer(asJSON bool) {
	d, ok := inner(m.active()).(describable)
	if !ok {
		return
	}
	item, why := d.describeSelected()
	if why != "" {
		m.note = why
		return
	}
	if asJSON {
		m.over = &viewer{title: "JSON · " + item.title, lines: jsonLines(item.report)}
		return
	}
	m.over = &viewer{title: item.title, lines: item.lines}
}

// viewerKey scrolls or closes the open panel; no other key does anything.
func (m *Model) viewerKey(k tea.KeyMsg, hidden bool) (tea.Model, tea.Cmd) {
	v := m.over
	switch {
	case key.Matches(k, keyViewerClose):
		m.over = nil
	case hidden:
	case k.Type == tea.KeyUp || k.String() == "k":
		v.off = max(v.off-1, 0)
	case k.Type == tea.KeyDown || k.String() == "j":
		v.off++
	case k.Type == tea.KeyPgUp:
		v.off = max(v.off-max(v.rows-1, 1), 0)
	case k.Type == tea.KeyPgDown:
		v.off += max(v.rows-1, 1)
	case k.Type == tea.KeyHome || k.String() == "g":
		v.off = 0
	case k.Type == tea.KeyEnd || k.String() == "G":
		v.off = 1 << 30
	}
	return m, nil
}

// view draws the panel in a body w wide and h tall (h <= 0: as tall as the content).
func (v *viewer) view(th Theme, w, h int) []string {
	iw := innerW(w)
	all := v.content(max(iw, 1))
	rows := 0
	if h > 0 {
		rows = max(h-panelChromeH, 1)
	}
	v.rows = rows
	vis, off := scrollWindow(th, all, v.off, rows)
	v.off = off
	return panel(th, san(v.title), vis, w, h, true)
}
