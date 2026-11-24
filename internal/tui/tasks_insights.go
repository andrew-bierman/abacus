package tui

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The Insights view: the graph engine's report as readable sections (next picks, blockers, bottlenecks,
// the critical path, influence, cycles, parallel tracks) with a cursor. enter opens the task under it in
// the List, g shows its graph, s and S sort a section's rows, and `?` explains every number.

// insightSort orders the rows of each ranked section.
type insightSort int

const (
	insRank  insightSort = iota // the engine's own order: the strongest first
	insID                       // by task id
	insTitle                    // by title
)

var insSortNames = [...]string{"rank", "id", "title"}

// insightsNav is the Insights cursor. The cursor is a key (section and task), not a row number, so a
// refresh that reorders the report keeps it on the same task.
type insightsNav struct {
	sel  string
	cur  int
	top  int
	sort insightSort
	desc bool
}

// insightEntry is one selectable row of a section.
type insightEntry struct {
	key      string // section and task, the cursor's identity
	id       string // the task the row opens
	title    string
	status   string
	value    float64
	hasValue bool
	unblocks int
	why      []string // a pick's reasons, shown under the row while it is selected
	text     string   // a row that is not one task (a cycle, a track)
}

// insightSection is a headed group of rows.
type insightSection struct {
	name    string
	entries []insightEntry
	note    string // why the section is empty, when the engine said
	ranked  bool   // the rows carry a metric
}

var (
	keyInsOpen  = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open task"))
	keyInsGraph = key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "graph"))
)

// insightsNormal is whether the report has rows to show (else the view says why it has none).
func insightsNormal(in *core.InsightsReport) bool {
	if in == nil || in.Empty || in.State == core.NotConnected {
		return false
	}
	return in.State != core.Degraded || in.Reason == "" || len(in.TopPicks)+len(in.Cycles)+len(in.Bottlenecks)+len(in.CriticalPath)+len(in.Blockers)+len(in.Influencers)+len(in.Tracks) > 0 || in.TotalActionable > 0
}

// insightSections builds the sections from the report, in the order they are drawn.
func (s *tasksSection) insightSections() []insightSection {
	in := s.insights
	if in == nil {
		return nil
	}
	ref := func(id string) (string, string) {
		if s.list.view != nil {
			if d, ok := s.list.view.Detail(id); ok {
				return d.Row.Title, d.Row.Status
			}
		}
		return "", ""
	}
	pick := func(sec string, p core.Pick) insightEntry {
		t, st := ref(p.ID)
		if t == "" {
			t = p.Title
		}
		return insightEntry{key: sec + "/" + p.ID, id: p.ID, title: t, status: st, unblocks: p.Unblocks, why: p.Reasons}
	}
	rank := func(sec string, r core.Ranked) insightEntry {
		t, st := ref(r.ID)
		return insightEntry{key: sec + "/" + r.ID, id: r.ID, title: t, status: st, value: r.Value, hasValue: true}
	}
	var out []insightSection
	add := func(sec insightSection) {
		if len(sec.entries) > 0 || sec.note != "" {
			out = append(out, sec)
		}
	}
	next := insightSection{name: "Next picks"}
	for _, p := range in.TopPicks {
		next.entries = append(next.entries, pick("next", p))
	}
	blk := insightSection{name: "Blockers to clear"}
	for _, p := range in.Blockers {
		blk.entries = append(blk.entries, pick("blocker", p))
	}
	bn := insightSection{name: "Bottlenecks · betweenness", ranked: true}
	for _, r := range in.Bottlenecks {
		bn.entries = append(bn.entries, rank("bottleneck", r))
	}
	cp := insightSection{name: "Critical path · impact depth", ranked: true}
	for _, r := range in.CriticalPath {
		cp.entries = append(cp.entries, rank("critical", r))
	}
	if len(cp.entries) == 0 {
		cp.note = in.CriticalPathNote
	}
	inf := insightSection{name: "Influence · centrality", ranked: true}
	for _, r := range in.Influencers {
		inf.entries = append(inf.entries, rank("influence", r))
	}
	cyc := insightSection{name: "Cycles"}
	for i, c := range in.Cycles {
		ids := make([]string, len(c))
		for j, id := range c {
			ids[j] = san(id)
		}
		if len(c) > 0 {
			cyc.entries = append(cyc.entries, insightEntry{key: fmt.Sprintf("cycle/%d/%s", i, c[0]), id: c[0], text: strings.Join(ids, " → ")})
		}
	}
	trk := insightSection{name: "Parallel tracks"}
	for _, t := range in.Tracks {
		if len(t.Items) == 0 {
			continue
		}
		e := insightEntry{key: "track/" + t.ID, id: t.Items[0], text: fmt.Sprintf("%s: %s", san(t.ID), san(strings.Join(t.Items, " ")))}
		trk.entries = append(trk.entries, e)
	}
	for _, sec := range []insightSection{next, blk, bn, cp, inf, cyc, trk} {
		add(sec)
	}
	return s.sortInsights(out)
}

// sortInsights applies the chosen order to the sections that are rankings; the rest keep the engine's.
func (s *tasksSection) sortInsights(secs []insightSection) []insightSection {
	n := s.insNav
	if n.sort == insRank && !n.desc {
		return secs
	}
	for i := range secs {
		e := secs[i].entries
		if len(e) < 2 || secs[i].name == "Cycles" || secs[i].name == "Parallel tracks" {
			continue
		}
		slices.SortStableFunc(e, func(a, b insightEntry) int {
			var c int
			switch n.sort {
			case insID:
				c = strings.Compare(a.id, b.id)
			case insTitle:
				c = strings.Compare(strings.ToLower(a.title), strings.ToLower(b.title))
			}
			if n.desc {
				c = -c
			}
			return c
		})
	}
	return secs
}

// flat is every selectable entry in drawing order.
func flatInsights(secs []insightSection) []insightEntry {
	var out []insightEntry
	for _, sec := range secs {
		out = append(out, sec.entries...)
	}
	return out
}

// insAnchor puts the cursor back on the entry it was on, by key, else clamps it.
func (n *insightsNav) anchor(entries []insightEntry) {
	for i, e := range entries {
		if n.sel != "" && e.key == n.sel {
			n.cur = i
			return
		}
	}
	n.cur = max(0, min(n.cur, len(entries)-1))
	if n.cur < len(entries) {
		n.sel = entries[n.cur].key
	}
}

// insKey handles the keys of the Insights view; it reports whether the key was one of them.
func (s *tasksSection) insKey(k tea.KeyMsg) (tea.Cmd, bool) {
	n := &s.insNav
	entries := flatInsights(s.insightSections())
	n.anchor(entries)
	move := func(d int) {
		if len(entries) > 0 {
			n.cur = max(0, min(n.cur+d, len(entries)-1))
			n.sel = entries[n.cur].key
		}
	}
	switch {
	case key.Matches(k, keyUp):
		move(-1)
	case key.Matches(k, keyDown):
		move(1)
	case key.Matches(k, keySort):
		if k.String() == "S" {
			n.desc = !n.desc
		} else {
			n.sort = (n.sort + 1) % insightSort(len(insSortNames))
		}
		entries = flatInsights(s.insightSections())
		n.anchor(entries)
	case key.Matches(k, keyInsOpen, keyInsGraph):
		if n.cur >= len(entries) {
			s.notice = "nothing is selected"
			return nil, true
		}
		id := entries[n.cur].id
		if key.Matches(k, keyInsGraph) {
			s.openGraphOn(id)
			return s.graphRefresh(), true
		}
		s.showTask(id)
		if cur, ok := s.current(); ok && cur.ID == id { // the detail, as enter does in the list
			if isStacked(s.width()) {
				s.enter(s.width(), len(s.rows()))
			} else {
				s.list.detailFocus = true
			}
		} else {
			s.notice = san(id) + " is not in the list"
		}
	default:
		return nil, false
	}
	return nil, true
}

// insBindings are the footer keys of the Insights view.
func (s *tasksSection) insBindings() []key.Binding {
	return []key.Binding{keyView, keyInsOpen, keyInsGraph, keySort, moveBindings()[0]}
}

// helpNotes is the explanation of the numbers, shown in the `?` overlay while the Insights view is up.
func (s *tasksSection) helpNotes() (string, []string) {
	if s.list.cur != viewInsights || s.mode != modeList {
		return "", nil
	}
	return "What the Insights numbers mean", []string{
		"Next picks: ready work ranked by how much it unblocks and how urgent it is. 'unblocks N' counts the tasks waiting on it; the reasons show under the selected row.",
		"Blockers to clear: open work other tasks are waiting on, the most-blocking first.",
		"Bottlenecks (betweenness): tasks that sit on the most dependency paths between other tasks. A high value means a delay here ripples through many chains.",
		"Critical path (impact depth): tasks ranked by how long a chain of work hangs off them. The top ones set the minimum time left.",
		"Influence (centrality): a PageRank-style score, higher when important tasks depend on a task. It finds well-connected keystones the other two can miss.",
		"Cycles: tasks that wait on each other in a loop. None of them can become ready until one link is removed; the critical path is skipped while a cycle exists.",
		"Parallel tracks: groups of ready work that do not depend on each other, so they can proceed side by side.",
		"s changes the row order (rank, id, title), S reverses it, enter opens the task in the List, g shows its graph.",
	}
}

// metric is a ranking value as text: whole numbers plain, small ones with two decimals.
func metric(v float64) string {
	switch {
	case v == math.Trunc(v) && math.Abs(v) < 1e6:
		return fmt.Sprintf("%d", int64(v))
	case math.Abs(v) < 10:
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// insBar is a five-cell bar of v against the section's largest value (at least one cell for a value above 0).
func insBar(v, top float64) string {
	const w = 5
	n := 0
	if top > 0 && v > 0 {
		n = max(1, min(w, int(math.Round(v/top*w))))
	}
	return strings.Repeat("█", n) + strings.Repeat("░", w-n)
}

// insightLines draws the sections: a heading per section, one row per entry (the cursor row on the selection
// style, its reasons under it), and one muted line for the sections that are empty. It returns the lines
// and the line of the cursor.
func (s *tasksSection) insightLines(secs []insightSection, iw int) ([]string, int) {
	th, n := s.th, s.insNav
	idW := 6
	for _, sec := range secs {
		for _, e := range sec.entries {
			idW = max(idW, min(lipgloss.Width(san(e.id)), 12))
		}
	}
	wide := iw >= 50
	var out []string
	cursorLine, at := 0, 0
	for si, sec := range secs {
		if si > 0 {
			out = append(out, "")
		}
		head := th.Section(sec.name)
		if len(sec.entries) > 0 {
			head += th.Text(KindMuted, fmt.Sprintf("  %d", len(sec.entries)))
		}
		out = append(out, head)
		if sec.note != "" && len(sec.entries) == 0 {
			out = append(out, hang("  ", th.Text(KindMuted, san(sec.note)), iw)...)
		}
		top := 0.0
		for _, e := range sec.entries {
			top = max(top, e.value)
		}
		for _, e := range sec.entries {
			sel := at == n.cur
			at++
			if e.text != "" { // a cycle or a track: wrapped whole, never cut
				lead := "  "
				if sel {
					lead, cursorLine = "▸ ", len(out)
				}
				for _, l := range hang(lead, e.text, iw) {
					if sel {
						l = th.Selected.Render(padTo(trunc(l, iw), iw))
					}
					out = append(out, l)
				}
				continue
			}
			plain, styled := s.insightRow(e, sec, idW, iw, wide, top)
			if sel {
				cursorLine = len(out)
				out = append(out, th.Selected.Render(padTo(trunc("▸ "+plain, iw), iw)))
				for i, w := range e.why {
					if i == 2 {
						break
					}
					out = append(out, hang("    ", th.Text(KindMuted, "why: "+san(w)), iw)...)
				}
				continue
			}
			out = append(out, trunc("  "+styled, iw))
		}
	}
	return out, cursorLine
}

// insightRow is one entry as plain and styled text, fitted to iw columns (the 2-cell cursor gutter is the caller's).
func (s *tasksSection) insightRow(e insightEntry, sec insightSection, idW, iw int, wide bool, top float64) (plain, styled string) {
	th := s.th
	right, rightStyled := "", ""
	switch {
	case e.hasValue:
		v := metric(e.value)
		if wide {
			b := insBar(e.value, top)
			right, rightStyled = b+" "+fmt.Sprintf("%5s", v), th.Text(KindAccent, b)+" "+th.Text(KindMuted, fmt.Sprintf("%5s", v))
		} else {
			right = fmt.Sprintf("%5s", v)
			rightStyled = th.Text(KindMuted, right)
		}
	case e.unblocks > 0:
		t := fmt.Sprintf("+%d", e.unblocks)
		if wide {
			t = fmt.Sprintf("unblocks %d", e.unblocks)
		}
		right, rightStyled = t, th.Text(KindMuted, t)
	}
	id := padTo(trunc(san(e.id), idW), idW)
	glyph := taskGlyph(e.status)
	gs := s.glyph(e.status)
	fixed := idW + 1 + 1 + 1 + 1 + lipgloss.Width(right) // id, gap, glyph, gap, ..title.., gap, right
	titleW := max(iw-2-fixed, 4)
	title := padTo(trunc(san(e.title), titleW), titleW)
	plain = id + " " + glyph + " " + title + " " + right
	styled = id + " " + gs + " " + title + " " + rightStyled
	return plain, styled
}

// insightsView is the Insights view: the counts, then the sections, scrolled around the cursor.
func (s *tasksSection) insightsView() string {
	th, w := s.th, s.width()
	h := s.bodyH(s.noticeText())
	iw := innerW(w)
	c := s.report.Counts
	if s.list.view != nil && !s.list.depsUnknown { // the snapshot's counts, so the strip, list and this agree
		c = s.list.view.Counts()
	}
	var head []string
	if bar := th.stackBar(min(iw, taskBarMax), taskBarSegments(c)); bar != "" {
		head = append(head, bar)
	}
	head = append(head, wrap(fmt.Sprintf("%d tasks: %d open · %d in progress · %d blocked · %d deferred · %d closed", c.Total, c.Open, c.InProgress, c.Blocked, c.Deferred, c.Closed), iw)...)
	sub, ins := s.insightParts(iw)
	title := "Insights"
	if sub != "" {
		title += " · " + sub
	}
	var body []string
	cursorLine := 0
	if in := s.insights; insightsNormal(in) {
		cyc := fmt.Sprintf("%d cycles", len(in.Cycles))
		if len(in.Cycles) == 1 {
			cyc = "1 cycle"
		}
		head = append(head, wrap(th.Text(KindMuted, fmt.Sprintf("%d actionable · %d blocked · %s · ready ", in.TotalActionable, in.TotalBlocked, cyc))+th.Text(KindOK, fmt.Sprint(c.Ready)), iw)...)
		secs := s.insightSections()
		entries := flatInsights(secs)
		s.insNav.anchor(entries)
		if len(entries) == 0 && !hasNote(secs) {
			body = wrap(th.Text(KindMuted, "nothing to rank yet: no open task depends on another"), iw)
		} else {
			body, cursorLine = s.insightLines(secs, iw)
		}
		if in.Reason != "" {
			body = append(body, wrap(th.Text(KindWarn, san(in.Reason)), iw)...)
		}
		if n := s.insNav; n.sort != insRank || n.desc {
			dir := "↑"
			if n.desc {
				dir = "↓"
			}
			title += " · sort: " + insSortNames[n.sort] + " " + dir
		}
	} else {
		head = append(head, "ready "+th.Text(KindOK, fmt.Sprint(c.Ready)), "")
		body = ins
	}
	panelH := h
	if h > 0 {
		panelH = max(h-1, 1)
	}
	rows := 0
	if panelH > 0 {
		rows = max(panelH-panelChromeH, 1)
	}
	room := rows - len(head)
	if rows == 0 {
		room = 0
	} else if room < 3 { // a very short body: the cursor's rows matter more than the counts
		head, room = head[:0], rows
	}
	body = windowLines(th, body, cursorLine, room, &s.insNav.top)
	lines := append(append([]string(nil), head...), body...)
	view := []string{s.strip(w)}
	view = append(view, panel(th, title, lines, w, panelH, true)...)
	if n := s.noticeText(); n != "" {
		view = append(view, noticeLine(th, w, n)...)
	}
	return joinLines(view)
}

func hasNote(secs []insightSection) bool {
	for _, s := range secs {
		if s.note != "" {
			return true
		}
	}
	return false
}
