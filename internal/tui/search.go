package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

// Global search: ctrl+k opens an overlay over the active tab. It searches the reports each
// section already holds through core.SearchIndex (the CLI's `search` uses the same core), so
// opening it runs no tool beyond the one lazy first read of a domain that never loaded. All the
// matching is core's; this file holds the query line, the cursor and the drawing.

var (
	keySearch      = key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "search everything"))
	keySearchClose = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close"))
	keySearchJump  = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "jump"))
	keySearchMove  = key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "move"))
	keySearchGroup = key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "next group"))
	keySearchHist  = key.NewBinding(key.WithKeys("ctrl+p", "ctrl+n"), key.WithHelp("ctrl+p/n", "history"))
)

// searchBindings are the overlay's keys for the footer, most important first.
func searchBindings() []key.Binding {
	return []key.Binding{keySearchClose, keySearchJump, keySearchMove, keySearchGroup, keySearchHist}
}

// WithSearchHistory sets where search history is kept (tests inject a temp path; a demo session
// keeps none).
func WithSearchHistory(h core.SearchHistory) Option {
	return func(c *config) { c.history, c.historySet = h, true }
}

// target is one selectable row of the overlay: a result to jump to, or a recent query to reuse.
type target struct {
	res  core.SearchResult
	hist string
}

func (t target) key() string { return string(t.res.Domain) + "\x00" + t.res.ID + "\x00" + t.hist }

type searchOverlay struct {
	th     Theme
	text   []rune
	draft  []rune // what was typed before history recall began
	hist   core.SearchHistory
	recent []string // oldest first
	histAt int      // -1: not recalling

	stamps map[int]string
	feeds  map[int]core.SearchFeed
	ix     *core.SearchIndex
	dirty  bool
	reset  bool // the query changed: the cursor goes back to the top

	rep     core.SearchReport
	qerr    *query.Error
	targets []target
	groups  []int // index in targets where each group starts
	sel     int
	top     int
	plain   [][]string // the plain cells of each result row, for the highlighted line
	widths  []int
}

func (m *Model) openSearch() tea.Cmd {
	o := &searchOverlay{th: m.th, hist: m.searchHist, histAt: -1, stamps: map[int]string{}, feeds: map[int]core.SearchFeed{}, dirty: true, reset: true}
	o.recent = o.hist.Load(core.HistoryGlobal)
	m.search = o
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		if s, ok := t.sec.(searchable); ok {
			cmds = append(cmds, s.searchLoad())
		}
	}
	return tea.Batch(cmds...)
}

// searchKey handles a key while the overlay is open: nothing reaches the sections or the shell's
// global keys, so q types a q and a confirm hidden behind the overlay cannot be answered.
func (m *Model) searchKey(k tea.KeyMsg, hidden bool) (tea.Model, tea.Cmd) {
	o := m.search
	if k.Type == tea.KeyEsc || key.Matches(k, keySearch) {
		m.search = nil
		return m, nil
	}
	if hidden {
		return m, nil
	}
	switch {
	case k.Type == tea.KeyUp:
		o.move(-1)
	case k.Type == tea.KeyDown:
		o.move(1)
	case k.Type == tea.KeyTab:
		o.group(1)
	case k.Type == tea.KeyShiftTab:
		o.group(-1)
	case k.Type == tea.KeyEnter:
		return m, m.searchEnter()
	case k.Type == tea.KeyCtrlP:
		o.recall(-1)
	case k.Type == tea.KeyCtrlN:
		o.recall(1)
	case k.Type == tea.KeyCtrlU:
		o.edit(nil)
	case k.Type == tea.KeyBackspace:
		if n := len(o.text); n > 0 {
			o.edit(o.text[:n-1])
		}
	case k.Type == tea.KeyRunes || k.Type == tea.KeySpace:
		next := append([]rune(nil), o.text...)
		for _, r := range k.Runes {
			if !unicode.IsControl(r) && len(next) < query.MaxLen {
				next = append(next, r)
			}
		}
		o.edit(next)
	}
	return m, nil
}

func (o *searchOverlay) edit(t []rune) {
	o.text, o.dirty, o.reset, o.histAt = append([]rune(nil), t...), true, true, -1
}

func (o *searchOverlay) move(d int) {
	if n := len(o.targets); n > 0 {
		o.sel = min(max(o.sel+d, 0), n-1)
	}
}

// group moves the cursor to the first row of the next (d > 0) or previous group.
func (o *searchOverlay) group(d int) {
	if len(o.groups) == 0 {
		return
	}
	cur := 0
	for i, g := range o.groups {
		if o.sel >= g {
			cur = i
		}
	}
	o.sel = o.groups[(cur+d+len(o.groups))%len(o.groups)]
}

// recall steps through the recent queries (older with d < 0), keeping what was typed as a draft.
func (o *searchOverlay) recall(d int) {
	if len(o.recent) == 0 {
		return
	}
	if o.histAt < 0 {
		if d > 0 {
			return
		}
		o.draft = o.text
		o.histAt = len(o.recent)
	}
	o.histAt += d
	switch {
	case o.histAt >= len(o.recent):
		o.text, o.histAt = o.draft, -1
	default:
		o.histAt = max(o.histAt, 0)
		o.text = []rune(o.recent[o.histAt])
	}
	o.dirty, o.reset = true, true
}

// searchEnter jumps to the selected result, or loads the selected recent query.
func (m *Model) searchEnter() tea.Cmd {
	o := m.search
	if o.sel >= len(o.targets) {
		return nil
	}
	t := o.targets[o.sel]
	if t.hist != "" {
		o.edit([]rune(t.hist))
		return nil
	}
	text := strings.TrimSpace(string(o.text))
	m.search = nil
	_ = o.hist.Add(core.HistoryGlobal, text) // a history that cannot be kept never blocks the jump
	for i, tab := range m.tabs {
		if tab.name != t.res.Domain.Title() {
			continue
		}
		cmd := m.goTo(i)
		j, ok := tab.sec.(jumper)
		switch {
		case !ok:
			m.note = tab.name + " is not connected."
		default:
			switch j.jumpTo(t.res.Jump()) {
			case jumpGone:
				m.note = "That item is no longer in " + tab.name + ". Refresh with r, or search again."
			case jumpCleared:
				m.note = "Cleared the filter in " + tab.name + " so the item shows."
			}
		}
		return cmd
	}
	return nil
}

// refresh rebuilds the index when a section has new data and re-runs the search when anything
// changed, keeping the cursor on the same result unless the query itself changed.
func (o *searchOverlay) refresh(m *Model) {
	changed := false
	for i, t := range m.tabs {
		s, ok := t.sec.(searchable)
		if !ok {
			continue
		}
		if st := s.searchStamp(); st != o.stamps[i] || o.ix == nil {
			o.stamps[i], o.feeds[i], changed = st, s.searchFeed(), true
		}
	}
	if changed || o.ix == nil {
		var feeds []core.SearchFeed
		for i := range m.tabs {
			if f, ok := o.feeds[i]; ok {
				feeds = append(feeds, f)
			}
		}
		o.ix = core.NewSearchIndex(feeds...)
	}
	if !changed && !o.dirty {
		return
	}
	var kept string
	if !o.reset && o.sel < len(o.targets) {
		kept = o.targets[o.sel].key()
	}
	o.dirty = false
	text := strings.TrimSpace(string(o.text))
	o.qerr = nil
	if text == "" {
		o.rep, _ = o.ix.Search("", core.SearchOptions{Now: nowFn()}) // statuses only
		o.targets, o.groups = nil, nil
		for i := len(o.recent) - 1; i >= 0; i-- {
			o.targets = append(o.targets, target{hist: o.recent[i]})
		}
	} else {
		rep, err := o.ix.Search(text, core.SearchOptions{Now: nowFn()})
		var qe *query.Error
		if errors.As(err, &qe) {
			o.qerr = qe // keep the last good results on screen while the query is mid-edit
		} else {
			o.rep = rep
			o.targets, o.groups = nil, nil
			for _, g := range rep.Groups {
				o.groups = append(o.groups, len(o.targets))
				for _, r := range g.Results {
					o.targets = append(o.targets, target{res: r})
				}
			}
		}
	}
	o.sel = 0
	if kept != "" {
		for i, t := range o.targets {
			if t.key() == kept {
				o.sel = i
			}
		}
	}
	o.reset = false
}

// snippetMinW is the panel width from which a body match shows its excerpt beside the title.
const snippetMinW = 76

var searchCols = table{cols: []column{{min: 6, max: 14, prio: 0}, {min: 10, prio: 0}, {min: 12, max: 32, prio: 1}}, gap: 2}

// words is "A", "A and B" or "A, B and C".
func words(l []string) string {
	switch len(l) {
	case 0:
		return ""
	case 1:
		return l[0]
	}
	return strings.Join(l[:len(l)-1], ", ") + " and " + l[len(l)-1]
}

func (o *searchOverlay) noteLines() []string {
	th := o.th
	var gone, loading, bad []string
	var out []string
	for _, d := range o.rep.Domains {
		switch d.State {
		case core.SearchNotConnected:
			gone = append(gone, d.Title)
		case core.SearchLoading:
			loading = append(loading, d.Title)
		case core.SearchDegraded:
			bad = append(bad, d.Title+" could not be read just now"+map[bool]string{true: ": " + d.Reason}[d.Reason != ""])
		}
	}
	if len(gone) > 0 {
		out = append(out, th.Text(KindMuted, words(gone)+map[bool]string{true: " is", false: " are"}[len(gone) == 1]+" not connected."))
	}
	if len(loading) > 0 {
		out = append(out, th.Text(KindMuted, words(loading)+map[bool]string{true: " is", false: " are"}[len(loading) == 1]+" still loading…"))
	}
	for _, b := range bad {
		out = append(out, th.Text(KindWarn, b+"."))
	}
	return out
}

// region is the scrolling part: recent searches, results by domain, or the no-match text, then
// one muted line per kind of domain that was not searched. It returns the line of each target.
func (o *searchOverlay) region(iw int) (lines []string, at []int) {
	th := o.th
	text := strings.TrimSpace(string(o.text))
	switch {
	case text == "" && len(o.recent) == 0:
		lines = append(lines, wrap(th.Text(KindMuted, "Search tasks, board threads, leases and runs. Words match titles and text; field:value narrows, for example status:open, who:ada or in:board."), iw)...)
	case text == "":
		lines = append(lines, th.Section("Recent searches"))
		for _, t := range o.targets {
			at = append(at, len(lines))
			lines = append(lines, "  "+san(t.hist))
		}
	case len(o.targets) == 0 && o.qerr != nil: // the error line above says what to fix
	case len(o.targets) == 0:
		lines = append(lines, wrap(th.Text(KindMuted, "No matches for “"+san(text)+"”."), iw)...)
		lines = append(lines, wrap(th.Text(KindMuted, "Try fewer words, or narrow with field:value, for example status:open."), iw)...)
	default:
		var styled, plain [][]string
		for _, g := range o.rep.Groups {
			for _, r := range g.Results {
				title := san(r.Title)
				ptitle, stitle := title, title
				if r.Snippet != "" && iw >= snippetMinW {
					ptitle += " · " + san(r.Snippet)
					stitle += th.Text(KindMuted, " · "+san(r.Snippet))
				}
				meta := san(r.Meta)
				if r.Title == r.Label { // a lease is its resource name: the label already says it, so the detail takes the title's room
					ptitle, stitle, meta = meta, meta, ""
				}
				plain = append(plain, []string{san(r.Label), ptitle, meta})
				styled = append(styled, []string{san(r.Label), stitle, th.Text(KindMuted, meta)})
			}
		}
		widths := searchCols.fitRows(max(iw-2, 1), styled)
		n := 0
		for gi, g := range o.rep.Groups {
			head := th.Section(g.Title) + th.Text(KindMuted, " ("+strconv.Itoa(g.Total)+")")
			if g.Truncated > 0 {
				head = th.Section(g.Title) + th.Text(KindMuted, fmt.Sprintf(" (%d of %d)", len(g.Results), g.Total))
			}
			if age := staleAge(g); age != "" {
				head += th.Text(KindWarn, " · read "+age+" ago, may be out of date")
			}
			if gi > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, head)
			for range g.Results {
				at = append(at, len(lines))
				lines = append(lines, "  "+searchCols.row(widths, styled[n]))
				n++
			}
			if g.Truncated > 0 {
				lines = append(lines, th.Text(KindMuted, fmt.Sprintf("  +%d more: narrow the search", g.Truncated)))
			}
		}
		o.plain, o.widths = plain, widths
	}
	if notes := o.noteLines(); len(notes) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		for _, n := range notes {
			lines = append(lines, wrap(n, iw)...)
		}
	}
	return lines, at
}

func staleAge(g core.SearchGroup) string {
	for _, r := range g.Results {
		if r.Stale {
			return r.Age
		}
	}
	return ""
}

// queryLine is the box's first row: the query with a caret and, on the right, the hit count.
func (o *searchOverlay) queryLine(iw int) string {
	th := o.th
	label := th.Text(KindMuted, "search ")
	typed := string(o.text)
	room := max(iw-7-1, 1)
	if r := []rune(typed); len(r) > room { // keep the end, where the person is typing
		typed = "…" + string(r[len(r)-room+1:])
	}
	left := label + san(typed) + "▏"
	right := ""
	if strings.TrimSpace(string(o.text)) != "" && (o.qerr == nil || len(o.targets) > 0) {
		right = th.Text(KindMuted, fmt.Sprintf("%d hit", o.rep.Total)+map[bool]string{true: "", false: "s"}[o.rep.Total == 1])
	}
	if right == "" || lipgloss.Width(left)+2+lipgloss.Width(right) > iw {
		return trunc(left, iw)
	}
	return left + strings.Repeat(" ", iw-lipgloss.Width(left)-lipgloss.Width(right)) + right
}

// lines draws the overlay as one focused panel w wide and h tall (h <= 0: as tall as needed).
func (o *searchOverlay) lines(m *Model, w, h int) []string {
	th := o.th
	o.refresh(m)
	iw := innerW(w)
	rows := 0
	if h > 0 {
		rows = max(h-panelChromeH, 1)
	}
	head := []string{o.queryLine(iw)}
	if o.qerr != nil {
		head = append(head, trunc(th.Text(KindWarn, san(o.qerr.Error())), iw))
	}
	region, at := o.region(iw)
	cursor := 0
	if o.sel < len(at) {
		cursor = at[o.sel]
		region[cursor] = o.selectedLine(iw, o.sel, region[cursor])
	}
	room := 0
	if rows > 0 {
		room = max(rows-len(head), 1)
	}
	body := append(head, windowLines(th, region, cursor, room, &o.top)...)
	for len(body) < rows { // a fixed-height box, so it does not jump as results come and go
		body = append(body, "")
	}
	return panel(th, "Search", body, w, h, true)
}

// selectedLine is the row under the cursor: its plain text on the highlight, with the marker.
func (o *searchOverlay) selectedLine(iw, sel int, styled string) string {
	th := o.th
	if o.plain != nil && sel < len(o.plain) && len(o.targets) > 0 && o.targets[sel].hist == "" {
		return th.Selected.Render(padTo(trunc("▸ "+searchCols.row(o.widths, o.plain[sel]), iw), iw))
	}
	if sel < len(o.targets) && o.targets[sel].hist != "" {
		return th.Selected.Render(padTo(trunc("▸ "+san(o.targets[sel].hist), iw), iw))
	}
	return styled
}

// view is the body shown while the overlay is open.
func (o *searchOverlay) view(m *Model, w, h int) []string { return o.lines(m, w, h) }
