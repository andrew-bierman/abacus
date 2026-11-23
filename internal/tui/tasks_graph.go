package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// The Graph view of the Tasks tab (plan unit U5): the neighborhood of one task as boxes with
// connectors from bpWide columns and as an indented outline below it, and the whole tracker as
// topological layers. All layout is taskview.GraphIndex's; this file keeps the selection and draws.

var (
	keyGraph       = key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "graph"))
	keyGraphToggle = key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "whole"))
	keyDepth       = key.NewBinding(key.WithKeys("[", "]"), key.WithHelp("[ ]", "depth"))
	keySibling     = key.NewBinding(key.WithKeys("h", "l"), key.WithHelp("h/l", "sibling"))
	keyLayer       = key.NewBinding(key.WithKeys("h", "l"), key.WithHelp("h/l", "layer"))
)

// graphState is the Graph view's state: the index of the current snapshot, what is focused and
// selected, and the laid-out view it last drew.
type graphState struct {
	idx    *taskview.GraphIndex
	idxFor *taskview.View
	focus  string // the task whose neighborhood is shown
	sel    string // the selected node, by id: it survives a refresh and a re-layout
	depth  int    // 0 is the default
	whole  bool
	top    int // first visible canvas row of the boxes
	gv     core.GraphView
	have   bool
	at     graphKey
	// insStart is when the last insights read began and insAt when its report arrived: the report
	// describes the tracker as of insStart, so it can mark a snapshot only if it began after it.
	insStart, insAt time.Time
	maxID           int // widest id in the tracker, so boxes are sized to show an id whole
	undrawn         int // links the box layout left out, beyond those the view counted
}

type graphKey struct {
	view  *taskview.View
	ins   *core.InsightsReport
	focus string
	depth int
	whole bool
	cap   int
}

const (
	graphInfoW  = 36 // the Chain panel beside the boxes
	boxMinW     = 18
	boxMaxW     = 28
	boxGap      = 2
	boxRows     = 3
	graphMinCap = 2
	graphMaxCap = 5
)

// boxCap is how many boxes fit in a row of inner width iw.
func boxCap(iw, idW int) int {
	minW := max(boxMinW, min(idW, 24)+9) // border, padding, glyph and room for the flags: an id is never cut
	return min(max((iw+boxGap)/(minW+boxGap), graphMinCap), graphMaxCap)
}

// graphBoxes is whether the neighborhood is drawn as boxes at this width.
func graphBoxes(w int, whole bool) bool { return w >= bpWide && !whole }

// graphGeom splits a wide body into the graph panel and the Chain panel beside it.
func graphGeom(w, h int) duoGeom {
	g := duoLayout(w, h)
	g.fill = true
	if g.wide {
		g.detail = min(graphInfoW, w/3)
		g.list = w - g.detail - 1
	}
	return g
}

// graphFocus is the task the neighborhood is about: the chosen focus, else the list's selection.
func (s *tasksSection) graphFocus() string {
	gs := &s.graph
	if s.list.view == nil {
		return ""
	}
	if gs.focus != "" {
		if _, ok := s.list.view.Detail(gs.focus); ok {
			return gs.focus
		}
	}
	if s.selected != "" {
		if _, ok := s.list.view.Detail(s.selected); ok {
			return s.selected
		}
	}
	return ""
}

// graphInsightsStale is whether the insights report on hand predates the snapshot on screen.
func (s *tasksSection) graphInsightsStale() bool {
	if s.insights == nil {
		return false
	}
	at := s.graph.insStart
	if at.IsZero() {
		at = s.graph.insAt
	}
	return at.Before(s.list.readAt)
}

// graphRefresh reads the insights again when the one on hand predates the snapshot, so the marks
// describe what is on screen.
func (s *tasksSection) graphRefresh() tea.Cmd {
	if s.ins != nil && !s.insBusy && s.graphInsightsStale() {
		return s.fetchInsights()
	}
	return nil
}

// graphInsights is the report whose marks may be drawn: none when it is stale.
func (s *tasksSection) graphInsights() *core.InsightsReport {
	if s.graphInsightsStale() {
		return nil
	}
	return s.insights
}

// refetchSnapshotIf is the follow-up snapshot read a write that landed mid-read asks for.
func (s *tasksSection) refetchSnapshotIf(dirty bool) tea.Cmd {
	if dirty {
		return s.fetchSnapshot()
	}
	return nil
}

// graphModel lays the graph out for the current state and width, reusing the last layout while
// nothing it depends on changed.
func (s *tasksSection) graphModel() core.GraphView {
	gs := &s.graph
	v := s.list.view
	if v == nil || s.list.depsUnknown {
		return core.GraphView{}
	}
	if gs.idxFor != v {
		gs.idx, gs.idxFor, gs.have = taskview.NewGraphIndex(v), v, false
		gs.maxID = gs.idx.MaxIDWidth()
	}
	w := s.width()
	capN := 0
	if graphBoxes(w, gs.whole) {
		capN = boxCap(graphGeom(w, 0).listInner(), gs.maxID)
	}
	k := graphKey{v, s.graphInsights(), s.graphFocus(), gs.depth, gs.whole, capN}
	if !gs.have || gs.at != k {
		gs.gv = gs.idx.View(taskview.GraphOptions{Focus: k.focus, Depth: k.depth, Whole: k.whole, MaxPerLayer: k.cap, Insights: k.ins})
		gs.at, gs.have = k, true
	}
	if gs.focus == "" || gs.focus != gs.gv.Focus {
		gs.focus = gs.gv.Focus
	}
	seq := graphSeq(gs.gv, graphBoxes(w, gs.whole))
	if len(seq) > 0 && indexOf(seq, gs.sel) < 0 {
		gs.sel = seq[0]
		if indexOf(seq, gs.gv.Focus) >= 0 {
			gs.sel = gs.gv.Focus
		}
	}
	return gs.gv
}

func indexOf(s []string, id string) int {
	for i, x := range s {
		if x == id {
			return i
		}
	}
	return -1
}

// graphSeq is the selectable nodes in the order j and k walk them: the boxes top to bottom and left
// to right, the outline top to bottom, or the layers of the whole graph.
func graphSeq(gv core.GraphView, boxes bool) []string {
	var seq []string
	if gv.Mode == core.GraphNeighborhood && !boxes {
		for _, r := range gv.Outline {
			if r.ID != "" && !r.Repeat {
				seq = append(seq, r.ID)
			}
		}
		return seq
	}
	for _, n := range gv.Nodes {
		seq = append(seq, n.ID)
	}
	return seq
}

// openGraph shows the neighborhood of the list's selected task.
func (s *tasksSection) openGraph() {
	gs := &s.graph
	gs.focus, gs.sel, gs.whole, gs.top = "", "", false, 0
	if rows := s.rows(); s.cursor < len(rows) {
		gs.focus, gs.sel = rows[s.cursor].ID, rows[s.cursor].ID
	}
	s.list.cur, s.drilled, s.list.detailFocus = viewGraph, false, false
}

// showTask jumps to a task in the List view, clearing the query and quick filter when they hide it.
func (s *tasksSection) showTask(id string) {
	l := &s.list
	l.cur, s.drilled, l.detailFocus = viewList, false, false
	has := func() bool {
		for _, r := range s.rows() {
			if r.ID == id {
				return true
			}
		}
		return false
	}
	if !has() && l.view != nil {
		l.text, l.q, l.qerr, l.typing = "", query.Query{}, nil, false
		s.recompute()
		if !has() {
			l.preset, l.presetSet = taskview.PresetAll, true
			s.recompute()
		}
		if has() {
			s.notice = "filter cleared to show " + san(id)
		}
	}
	s.selected = id
	s.reanchor(s.ids(), true)
	s.moved()
}

// graphKeys handles the keys of the Graph view; it reports whether the key was one of them.
func (s *tasksSection) graphKeys(k tea.KeyMsg) (tea.Cmd, bool) {
	gs := &s.graph
	gv := s.graphModel()
	boxes := graphBoxes(s.width(), gs.whole)
	seq := graphSeq(gv, boxes)
	at := indexOf(seq, gs.sel)
	switch {
	case key.Matches(k, keyUp):
		if at > 0 {
			gs.sel = seq[at-1]
		}
	case key.Matches(k, keyDown):
		if at >= 0 && at+1 < len(seq) {
			gs.sel = seq[at+1]
		}
	case key.Matches(k, keyBack):
		s.list.cur = viewList
	case key.Matches(k, keyGraphToggle):
		if gs.whole { // back to the neighborhood of what was selected
			gs.focus = gs.sel
		}
		gs.whole, gs.top = !gs.whole, 0
	case k.String() == "[":
		gs.depth = max(1, graphDepth(gs.depth)-1)
	case k.String() == "]":
		gs.depth = min(6, graphDepth(gs.depth)+1)
	case k.String() == "h" || k.String() == "l":
		d := 1
		if k.String() == "h" {
			d = -1
		}
		if id := graphStep(gv, gs.sel, d); id != "" {
			gs.sel = id
		}
	case key.Matches(k, keyEnter):
		switch {
		case gs.sel == "":
		case gs.whole || gs.sel == gv.Focus:
			s.showTask(gs.sel)
		default:
			gs.focus, gs.top = gs.sel, 0
		}
	default:
		return nil, false
	}
	return nil, true
}

func graphDepth(d int) int {
	if d <= 0 {
		return 2
	}
	return d
}

// graphStep moves h and l: to the neighbor in the same layer of a neighborhood, to the first task of
// the previous or next layer of the whole graph.
func graphStep(gv core.GraphView, sel string, d int) string {
	for i, l := range gv.Layers {
		at := indexOf(l.Nodes, sel)
		if at < 0 {
			continue
		}
		if gv.Mode == core.GraphNeighborhood {
			if at+d >= 0 && at+d < len(l.Nodes) {
				return l.Nodes[at+d]
			}
			return ""
		}
		if i+d >= 0 && i+d < len(gv.Layers) && len(gv.Layers[i+d].Nodes) > 0 {
			return gv.Layers[i+d].Nodes[0]
		}
		return ""
	}
	return ""
}

func (s *tasksSection) graphBindings() []key.Binding {
	gs := &s.graph
	toggle := keyGraphToggle
	if gs.whole {
		toggle.SetHelp("g", "nearby")
	}
	enter := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "focus"))
	if gs.whole || gs.sel == gs.gv.Focus {
		enter.SetHelp("enter", "open")
	}
	bs := []key.Binding{toggle, enter}
	if gs.whole {
		bs = append(bs, moveBindings()[0], keyLayer)
	} else {
		bs = append(bs, keyDepth, moveBindings()[0], keySibling)
	}
	return append(bs, keyView, keyBack)
}

// ---- drawing ----

// graphFlags are the marks after a task: * longest chain, ~ cycle, ^ bottleneck, ! depends on
// itself, ? blocker not in this tracker.
func graphFlags(n core.GraphNode) string {
	var b strings.Builder
	if n.OnChain {
		b.WriteByte('*')
	}
	if n.InCycle {
		b.WriteByte('~')
	}
	if n.Bottleneck {
		b.WriteByte('^')
	}
	if n.SelfDep {
		b.WriteByte('!')
	}
	if len(n.Missing) > 0 {
		b.WriteByte('?')
	}
	return b.String()
}

func graphDeps(n core.GraphNode) string {
	var p string
	if n.BlockedBy > 0 {
		p = fmt.Sprintf("⊘%d", n.BlockedBy)
	}
	if n.Blocks > 0 {
		p += fmt.Sprintf("→%d", n.Blocks)
	}
	return p
}

// graphLine is the row under the view strip: what the graph is about.
func (s *tasksSection) graphLine(w int, gv core.GraphView) string {
	th := s.th
	var l string
	switch {
	case s.list.depsUnknown || gv.Mode == "":
		l = "dependency graph"
	case gv.Mode == core.GraphWhole:
		l = fmt.Sprintf("whole graph · %d linked · %d without links · %d groups", gv.Total-gv.Isolated, gv.Isolated, gv.Components)
	default:
		l = fmt.Sprintf("focus %s · depth %d · blocking links", san(gv.Focus), gv.Depth)
		if gv.Focus == "" {
			l = "dependency graph"
		}
	}
	return trunc(th.Text(KindMuted, l), w)
}

// graphView draws the view: the strip, one line of context, then the panel or panels.
func (s *tasksSection) graphView() string {
	th, w := s.th, s.width()
	gs := &s.graph
	gv := s.graphModel()
	h := s.bodyH(s.noticeText())
	panelH := h
	if h > 0 {
		panelH = max(h-2, 1)
	}
	rows := 0
	if panelH > 0 {
		rows = max(panelH-panelChromeH, 1)
	}
	body := []string{s.strip(w), s.graphLine(w, gv)}
	g := graphGeom(w, panelH)
	g.fill = true
	iw := g.listInner()
	title := "Graph"
	if gv.Focus != "" && gv.Mode == core.GraphNeighborhood {
		title += " · " + san(gv.Focus)
	}
	if seen := s.seenAt(); !seen.IsZero() && !taskview.Fresh(seen, nowFn(), s.pollEvery()) {
		title += " · " + th.Text(KindWarn, "stale "+taskview.Age(seen, nowFn()))
	}
	var list, info []string
	switch {
	case s.list.depsUnknown:
		list = wrap(th.Text(KindMuted, "the dependency graph needs the whole tracker: not available in recent changes"), iw)
	case len(gv.Nodes) == 0:
		list = wrap(th.Text(KindMuted, s.graphEmpty(gv)), iw)
	case gv.Mode == core.GraphWhole:
		var cur int
		var head []string
		if !g.wide { // the cycles and the chain stay pinned above the layers when there is no Chain panel
			head = clipLines(th, append(s.cycleLines(gv, iw), s.chainSummary(gv, iw)), max(rows/2, 1))
		}
		list, cur = s.wholeLines(gv, iw)
		list = append(append([]string(nil), head...), windowLines(th, list, cur, max(rows-len(head), 1), &gs.top)...)
	case graphBoxes(w, false):
		var y0, y1 int
		list, y0, y1 = s.boxLines(gv, iw)
		list = s.boxWindow(list, y0, y1, rows)
	default:
		var cur int
		list, cur = s.outlineLines(gv, iw)
		list = windowLines(th, list, cur, rows, &gs.top)
	}
	if g.wide {
		info = clipLines(th, s.graphInfo(gv, g.detail-panelChromeW), rows)
		body = append(body, g.draw(th, title, list, "Chain", info, false)...)
	} else {
		body = append(body, panel(th, title, padRows(list, rows), g.list, max(panelH, 0), true)...)
	}
	if n := s.noticeText(); n != "" {
		body = append(body, noticeLine(th, w, n)...)
	}
	return joinLines(body)
}

func (s *tasksSection) graphEmpty(gv core.GraphView) string {
	switch {
	case gv.Reason != "":
		return gv.Reason + ": press esc to go back to the list"
	case gv.Mode == core.GraphWhole:
		return "no task has a blocking link yet: nothing to draw"
	}
	return "no task selected"
}

// wrapTokens wraps words at the spaces only, so an id is never split at its hyphen, and cuts a word
// that is still too wide.
func wrapTokens(words []string, w int) []string {
	var lines []string
	cur := ""
	for _, word := range words {
		word = trunc(word, w)
		switch {
		case cur == "":
			cur = word
		case lipgloss.Width(cur)+1+lipgloss.Width(word) <= w:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// chainLines is the longest chain, one wrapped line.
func (s *tasksSection) chainLines(gv core.GraphView, w int) []string {
	if len(gv.Chain) < 2 {
		return []string{s.th.Text(KindMuted, "longest chain: none")}
	}
	head := fmt.Sprintf("longest chain (%d):", len(gv.Chain))
	var words []string
	for i, id := range gv.Chain {
		if i > 0 {
			words = append(words, "→")
		}
		words = append(words, san(id))
	}
	return append([]string{s.th.Section(head)}, wrapTokens(words, w)...)
}

// chainSummary is the longest chain in one line: its length, first and last task.
func (s *tasksSection) chainSummary(gv core.GraphView, w int) string {
	n := len(gv.Chain)
	if n < 2 {
		return s.th.Text(KindMuted, "longest chain: none")
	}
	return trunc(fmt.Sprintf("longest chain (%d): %s → … → %s", n, san(gv.Chain[0]), san(gv.Chain[n-1])), w)
}

// cycleLines lists each cycle explicitly, at most three, with the rest counted.
func (s *tasksSection) cycleLines(gv core.GraphView, w int) []string {
	th := s.th
	if len(gv.Cycles) == 0 && len(gv.SelfDeps) == 0 {
		return []string{th.Text(KindMuted, "cycles: none")}
	}
	var out []string
	for i, c := range gv.Cycles {
		if i == 3 {
			out = append(out, th.Text(KindMuted, fmt.Sprintf("+%d more cycles", len(gv.Cycles)-3)))
			break
		}
		words := []string{"cycle"}
		for _, id := range c {
			words = append(words, san(id), "→")
		}
		words = append(words, san(c[0]))
		for _, l := range wrapTokens(words, w) {
			out = append(out, th.Text(KindFail, l))
		}
	}
	for _, id := range gv.SelfDeps[:min(len(gv.SelfDeps), 3)] {
		out = append(out, th.Text(KindFail, "depends on itself ")+san(id))
	}
	return out
}

// graphInfo is the Chain panel beside the boxes and the whole graph.
func (s *tasksSection) graphInfo(gv core.GraphView, w int) []string {
	th := s.th
	lines := s.chainLines(gv, w)
	lines = append(lines, "")
	lines = append(lines, s.cycleLines(gv, w)...)
	if gv.Mode == core.GraphNeighborhood && gv.Focus != "" {
		lines = append(lines, "", fmt.Sprintf("blockers open: %d", gv.OpenBlockers), fmt.Sprintf("freed when closed: %d", gv.Unblocks))
	}
	if gv.Hidden > 0 {
		lines = append(lines, th.Text(KindMuted, fmt.Sprintf("+%d more not drawn", gv.Hidden)))
	}
	if n := gv.CrossLinks + s.graph.undrawn; n > 0 {
		lines = append(lines, th.Text(KindMuted, fmt.Sprintf("+%d links not drawn", n)))
	}
	lines = append(lines, "", th.Text(KindMuted, "○ open ◐ active ● blocked"), th.Text(KindMuted, "✓ closed ◇ deferred"), th.Text(KindMuted, "* longest chain · ~ cycle"), th.Text(KindMuted, "^ bottleneck · ! self-dep"), th.Text(KindMuted, "? blocker not in tracker"))
	if gv.Mode == core.GraphNeighborhood {
		lines = append(lines, th.Text(KindMuted, "═ focus · ┃ selected"))
	}
	return lines
}

// nodeText is a node's one line in w cells: glyph and id, the title, and at the end the dependency
// marks and flags. The flags are the marks that matter most (chain, cycle, bottleneck, self,
// missing), so their width is reserved before the title is cut, and the id is cut before they are;
// the dependency marks only appear when the title keeps six cells.
func (s *tasksSection) nodeText(n core.GraphNode, w int, deps bool) (styled, plain string) {
	th := s.th
	w = max(w, 1)
	flags := graphFlags(n)
	fw := lipgloss.Width(flags)
	reserve := fw
	if fw > 0 {
		reserve++
	}
	id := trunc(taskGlyph(n.Status)+" "+san(n.ID), max(w-reserve, 1))
	room := w - lipgloss.Width(id) - 1 - reserve // the title and its leading space
	d := ""
	if deps {
		d = graphDeps(n)
	}
	if d != "" && room-lipgloss.Width(d)-1 >= 6 {
		room -= lipgloss.Width(d) + 1
	} else {
		d = ""
	}
	title := ""
	if room >= 2 {
		title = trunc(san(n.Title), room)
	}
	right := strings.TrimSpace(d + " " + flags)
	left := id
	if title != "" {
		left += " " + title
	}
	gap := 0
	if right != "" {
		gap = 1
	}
	pad := max(w-lipgloss.Width(left)-lipgloss.Width(right)-gap, 0)
	plain = trunc(left+strings.Repeat(" ", pad)+strings.Repeat(" ", gap)+right, w)
	styledTitle := title
	if n.Status == "closed" {
		styledTitle = th.Text(KindMuted, title)
	}
	styledLeft := s.glyph(n.Status) + id[len(taskGlyph(n.Status)):]
	if lipgloss.Width(id) < lipgloss.Width(taskGlyph(n.Status)+" "+san(n.ID)) { // cut id: no styling games
		styledLeft = id
	}
	if title != "" {
		styledLeft += " " + styledTitle
	}
	fl := ""
	if d != "" {
		fl = th.Text(KindMuted, d)
		if flags != "" {
			fl += " "
		}
	}
	switch {
	case n.InCycle:
		fl += th.Text(KindFail, flags)
	case n.OnChain:
		fl += th.Text(KindAccent, flags)
	case flags != "":
		fl += th.Text(KindMuted, flags)
	}
	styled = trunc(styledLeft+strings.Repeat(" ", pad+gap)+fl, w)
	return styled, plain
}

// graphRow draws one selectable line: the selected one on the selection style with a marker.
func (s *tasksSection) graphRow(n core.GraphNode, indent, iw int, deps bool) string {
	th := s.th
	pad := strings.Repeat(" ", indent)
	if n.ID == s.graph.sel {
		_, plain := s.nodeText(n, max(iw-2-indent, 1), deps)
		return th.Selected.Render(padTo(trunc("▸ "+pad+plain, iw), iw))
	}
	styled, _ := s.nodeText(n, max(iw-2-indent, 1), deps)
	return trunc("  "+pad+styled, iw)
}

// wholeLines is the layers of the whole graph and the line of the selected task.
func (s *tasksSection) wholeLines(gv core.GraphView, iw int) ([]string, int) {
	th := s.th
	var lines []string
	byID := map[string]core.GraphNode{}
	for _, n := range gv.Nodes {
		byID[n.ID] = n
	}
	cur := 0
	for _, l := range gv.Layers {
		what := ""
		for _, id := range l.Nodes {
			if byID[id].InCycle {
				what = " · includes a cycle (~)"
				break
			}
		}
		n := len(l.Nodes) + l.More
		noun := "tasks"
		if n == 1 {
			noun = "task"
		}
		lines = append(lines, th.Text(KindMuted, trunc(fmt.Sprintf("── layer %d · %d %s%s", l.Layer, n, noun, what), iw)))
		for _, id := range l.Nodes {
			if id == s.graph.sel {
				cur = len(lines)
			}
			lines = append(lines, s.graphRow(byID[id], 0, iw, true))
		}
		if l.More > 0 {
			lines = append(lines, th.Text(KindMuted, fmt.Sprintf("    … +%d more in this layer", l.More)))
		}
	}
	return lines, cur
}

// outlineLines is the neighborhood as an indented outline that never overflows: blockers nested
// above the focus, dependents nested below it, then the chain and the cycles.
func (s *tasksSection) outlineLines(gv core.GraphView, iw int) ([]string, int) {
	th := s.th
	node := func(r core.OutlineRow) core.GraphNode {
		if r.Node != nil {
			return *r.Node
		}
		return core.GraphNode{ID: r.ID, Status: "open"}
	}
	var lines []string
	cur := 0
	section := func(name string, any bool) {
		lines = append(lines, th.Text(KindMuted, name))
		if !any {
			lines = append(lines, th.Text(KindMuted, "  nothing"))
		}
	}
	rowsOf := func(side string) []core.OutlineRow {
		var out []core.OutlineRow
		for _, r := range gv.Outline {
			if r.Side == side {
				out = append(out, r)
			}
		}
		return out
	}
	emit := func(r core.OutlineRow) {
		switch {
		case r.ID == "":
			lines = append(lines, th.Text(KindMuted, trunc(strings.Repeat("  ", r.Depth)+fmt.Sprintf("… +%d more", r.More), iw)))
		case r.Repeat:
			lines = append(lines, th.Text(KindMuted, trunc(strings.Repeat("  ", r.Depth)+san(r.ID)+" (listed above)", iw)))
		default:
			if r.ID == s.graph.sel {
				cur = len(lines)
			}
			lines = append(lines, s.graphRow(node(r), 2*(r.Depth-1), iw, true))
		}
	}
	up, down := rowsOf(core.OutlineUp), rowsOf(core.OutlineDown)
	section("blocked by", len(up) > 0)
	for _, r := range up {
		emit(r)
	}
	lines = append(lines, "")
	var f core.GraphNode
	for _, r := range gv.Outline {
		if r.Side == core.OutlineFocus {
			f = node(r)
		}
	}
	if gv.Focus == s.graph.sel {
		cur = len(lines)
	}
	fl := s.graphRow(f, 0, iw, true)
	if gv.Focus != s.graph.sel {
		fl = trunc(th.Title.Render("◆ "+strings.TrimRight(s.plainNode(f, iw-2), " ")), iw)
	}
	lines = append(lines, fl, "")
	section("blocks", len(down) > 0)
	for _, r := range down {
		emit(r)
	}
	lines = append(lines, "")
	lines = append(lines, s.chainLines(gv, iw)...)
	lines = append(lines, s.cycleLines(gv, iw)...)
	return lines, cur
}

func (s *tasksSection) plainNode(n core.GraphNode, w int) string {
	_, p := s.nodeText(n, w, true)
	return p
}

// ---- boxes ----

type gseg struct {
	x, w int
	text string
	k    uint8
}

// Cell kinds on the box canvas: 0 plain, 1 muted, 2 accent, 3 fail, 4 selected.
const (
	ckPlain uint8 = iota
	ckMuted
	ckAccent
	ckFail
	ckSelected
)

type gcanvas struct {
	w     int
	runes [][]rune
	mask  [][]uint8
	kind  [][]uint8
	segs  []map[int]gseg
}

func newCanvas(w, h int) *gcanvas {
	c := &gcanvas{w: w}
	for y := 0; y < h; y++ {
		c.runes = append(c.runes, []rune(strings.Repeat(" ", w)))
		c.mask = append(c.mask, make([]uint8, w))
		c.kind = append(c.kind, make([]uint8, w))
		c.segs = append(c.segs, map[int]gseg{})
	}
	return c
}

// Connector directions; a cell's mask says which neighbors a line joins.
const (
	dU uint8 = 1 << iota
	dD
	dL
	dR
)

var maskRune = [16]rune{0: ' ', dU: '│', dD: '│', dU | dD: '│', dL: '─', dR: '─', dL | dR: '─',
	dD | dR: '┌', dD | dL: '┐', dU | dR: '└', dU | dL: '┘', dU | dD | dR: '├', dU | dD | dL: '┤',
	dL | dR | dD: '┬', dL | dR | dU: '┴', dU | dD | dL | dR: '┼'}

func (c *gcanvas) link(x1, y1, x2, y2 int) {
	switch {
	case x2 > x1:
		c.mask[y1][x1] |= dR
		c.mask[y2][x2] |= dL
	case x2 < x1:
		c.mask[y1][x1] |= dL
		c.mask[y2][x2] |= dR
	case y2 > y1:
		c.mask[y1][x1] |= dD
		c.mask[y2][x2] |= dU
	case y2 < y1:
		c.mask[y1][x1] |= dU
		c.mask[y2][x2] |= dD
	}
}

// path joins two points with a vertical, a horizontal and a vertical run: from the cell under the
// parent's bottom edge (x1, y1) down to the bus row, across, and down to the cell above the child's
// top edge (x2, y2).
func (c *gcanvas) path(x1, y1, yBus, x2, y2 int) {
	for y := y1; y < yBus; y++ {
		c.link(x1, y, x1, y+1)
	}
	step := 1
	if x2 < x1 {
		step = -1
	}
	for x := x1; x != x2; x += step {
		c.link(x, yBus, x+step, yBus)
	}
	for y := yBus; y < y2; y++ {
		c.link(x2, y, x2, y+1)
	}
	c.mask[y1][x1] |= dU // into the parent's bottom edge
	c.mask[y2][x2] |= dD // into the child's top edge
}

type borderSet struct{ h, v, tl, tr, bl, br, down, up rune }

var (
	lightB = borderSet{'─', '│', '┌', '┐', '└', '┘', '┬', '┴'}
	heavyB = borderSet{'━', '┃', '┏', '┓', '┗', '┛', '┳', '┻'}
	dblB   = borderSet{'═', '║', '╔', '╗', '╚', '╝', '╤', '╧'}
)

type boxPos struct {
	x, y, cx, w int
	layer       int
}

// boxLines draws the neighborhood as boxes and connectors on a character canvas. It returns the
// lines and the rows of the selected box, so the caller can keep it in view.
func (s *tasksSection) boxLines(gv core.GraphView, iw int) ([]string, int, int) {
	bl := s.layoutBoxes(gv, iw)
	return s.renderCanvas(bl.c), bl.selTop, bl.selBot
}

// boxLayout is the box canvas with where everything was put, so a test can read the connectors back.
type boxLayout struct {
	c              *gcanvas
	pos            map[string]boxPos
	layerY, bottom []int // each layer's first row, and the first row under it
	selTop, selBot int
}

func (s *tasksSection) layoutBoxes(gv core.GraphView, iw int) boxLayout {
	nodes := map[string]core.GraphNode{}
	for _, n := range gv.Nodes {
		nodes[n.ID] = n
	}
	// An odd canvas and odd boxes put every lone or middle box on the exact middle column, so the
	// stems of boxes in different layers line up.
	if iw%2 == 0 {
		iw--
	}
	// pass 1: where everything goes
	pos := map[string]boxPos{}
	layerY := make([]int, len(gv.Layers))
	layerBottom := make([]int, len(gv.Layers))
	comps := make([][][]string, len(gv.Layers)) // per layer pair, the groups of linked tasks
	layerIdx := map[string]int{}
	for i, l := range gv.Layers {
		for _, id := range l.Nodes {
			layerIdx[id] = i
		}
	}
	for i, l := range gv.Layers { // columns first: the connector groups need every layer's x
		n := max(len(l.Nodes), 1)
		// a layer with fewer boxes gets wider ones, so a lone focus keeps more of its title
		bw := max(min(boxMaxW, (iw+boxGap)/n-boxGap), 7)
		bw -= 1 - bw%2
		total := n*bw + (n-1)*boxGap
		x0 := max((iw-total)/2, 0)
		for j, id := range l.Nodes {
			x := x0 + j*(bw+boxGap)
			pos[id] = boxPos{x: x, cx: x + bw/2, w: bw, layer: i}
		}
	}
	y := 0
	for i, l := range gv.Layers {
		layerY[i] = y
		for _, id := range l.Nodes {
			p := pos[id]
			p.y = y
			pos[id] = p
		}
		y += boxRows
		if l.More > 0 {
			y++
		}
		layerBottom[i] = y
		gap := 1
		if i+1 < len(gv.Layers) {
			comps[i] = nonOverlapping(pairComponents(gv, layerIdx, i, pos), pos)
			if len(comps[i]) > 0 {
				gap = 2 // one bus row, then the row above the child boxes
			}
		}
		if i+1 < len(gv.Layers) {
			y += gap
		}
	}
	c := newCanvas(iw, max(y, 1))
	// connectors first, boxes over them
	for i, groups := range comps {
		if len(groups) == 0 {
			continue
		}
		kept := map[string]bool{}
		for _, g := range groups {
			for _, id := range g {
				kept[id] = true
			}
		}
		y0 := layerBottom[i]
		for _, e := range gv.Edges {
			if !e.Drawn || layerIdx[e.From] != i || layerIdx[e.To] != i+1 || !kept[e.From] {
				continue
			}
			u, v := pos[e.From], pos[e.To]
			c.path(u.cx, layerY[i]+boxRows, y0, v.cx, layerY[i+1]-1) // the stem runs through the "+N more" row
		}
	}
	for y := range c.runes {
		for x := range c.runes[y] {
			c.runes[y][x] = maskRune[c.mask[y][x]]
			c.kind[y][x] = ckMuted
		}
	}
	selTop, selBot := 0, 0
	for i, l := range gv.Layers {
		for _, id := range l.Nodes {
			p := pos[id]
			n := nodes[id]
			if id == s.graph.sel {
				selTop, selBot = p.y, p.y+boxRows
			}
			s.drawBox(c, n, p)
		}
		if l.More > 0 {
			t := trunc(fmt.Sprintf("+%d more", l.More), iw)
			x0 := max(iw-lipgloss.Width(t), 0) // far right: clear of every box's center stem
			c.segs[layerY[i]+boxRows][x0] = gseg{x0, lipgloss.Width(t), t, ckMuted}
		}
	}
	// edges leave and enter through the box edges
	for _, e := range gv.Edges {
		if !e.Drawn {
			continue
		}
		u, v := pos[e.From], pos[e.To]
		c.runes[u.y+boxRows-1][u.cx] = borderFor(nodes[e.From], gv.Focus, s.graph.sel).down
		c.runes[v.y][v.cx] = borderFor(nodes[e.To], gv.Focus, s.graph.sel).up
	}
	s.graph.undrawn = 0 // links the layout dropped because their bus would cross another's
	for _, e := range gv.Edges {
		i, ok := layerIdx[e.From]
		if !e.Drawn || !ok || layerIdx[e.To] != i+1 || i >= len(comps) {
			continue
		}
		in := false
		for _, g := range comps[i] {
			for _, id := range g {
				in = in || id == e.From
			}
		}
		if !in {
			s.graph.undrawn++
		}
	}
	return boxLayout{c, pos, layerY, layerBottom, selTop, selBot}
}

// nonOverlapping keeps the groups, left to right, whose horizontal extents do not meet: with one bus
// row for the layer pair, two overlapping groups would run into each other and read as one.
func nonOverlapping(groups [][]string, pos map[string]boxPos) [][]string {
	var kept [][]string
	last := -1
	for _, g := range groups {
		lo, hi := 1<<30, -1
		for _, id := range g {
			lo, hi = min(lo, pos[id].cx), max(hi, pos[id].cx)
		}
		if lo > last {
			kept, last = append(kept, g), hi
		}
	}
	return kept
}

func borderFor(n core.GraphNode, focus, sel string) borderSet {
	switch {
	case n.ID == focus:
		return dblB
	case n.ID == sel:
		return heavyB
	}
	return lightB
}

func (s *tasksSection) drawBox(c *gcanvas, n core.GraphNode, p boxPos) {
	bw := p.w
	focusID := ""
	if n.Focus {
		focusID = n.ID
	}
	b := borderFor(n, focusID, s.graph.sel)
	bk := ckPlain
	switch {
	case n.InCycle:
		bk = ckFail
	case n.Focus:
		bk = ckAccent
	case n.Status == "closed":
		bk = ckMuted
	}
	put := func(y, x int, r rune) { c.runes[y][x], c.kind[y][x], c.mask[y][x] = r, bk, 0 }
	for x := p.x; x < p.x+bw; x++ {
		put(p.y, x, b.h)
		put(p.y+2, x, b.h)
	}
	put(p.y, p.x, b.tl)
	put(p.y, p.x+bw-1, b.tr)
	put(p.y+2, p.x, b.bl)
	put(p.y+2, p.x+bw-1, b.br)
	put(p.y+1, p.x, b.v)
	put(p.y+1, p.x+bw-1, b.v)
	styled, plain := s.nodeText(n, bw-4, false)
	k := ckPlain // the text carries its own styling (muted title when closed, accented chain flag)
	text := " " + styled + " "
	if n.ID == s.graph.sel { // the selected box is one block of the selection style
		k, text = ckSelected, " "+plain+" "
	}
	for x := p.x + 1; x < p.x+bw-1; x++ {
		c.runes[p.y+1][x], c.kind[p.y+1][x] = ' ', k
	}
	c.segs[p.y+1][p.x+1] = gseg{p.x + 1, bw - 2, padTo(trunc(text, bw-2), bw-2), k}
}

// pairComponents groups the tasks joined by drawn edges between layer i and i+1, left to right.
func pairComponents(gv core.GraphView, layerIdx map[string]int, i int, pos map[string]boxPos) [][]string {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	for _, e := range gv.Edges {
		if e.Drawn && layerIdx[e.From] == i && layerIdx[e.To] == i+1 {
			parent[find(e.From)] = find(e.To)
		}
	}
	groups := map[string][]string{}
	for id := range parent {
		groups[find(id)] = append(groups[find(id)], id)
	}
	var out [][]string
	for _, g := range groups {
		out = append(out, g)
	}
	least := func(g []string) int {
		m := 1 << 30
		for _, id := range g {
			m = min(m, pos[id].cx)
		}
		return m
	}
	// left to right; the smallest id of a group breaks a tie so the order never depends on map order
	minID := func(g []string) string {
		m := g[0]
		for _, id := range g {
			if id < m {
				m = id
			}
		}
		return m
	}
	for a := range out {
		for b := a + 1; b < len(out); b++ {
			la, lb := least(out[a]), least(out[b])
			if lb < la || (lb == la && minID(out[b]) < minID(out[a])) {
				out[a], out[b] = out[b], out[a]
			}
		}
	}
	return out
}

func (s *tasksSection) styleKind(k uint8, str string) string {
	switch k {
	case ckMuted:
		return s.th.Text(KindMuted, str)
	case ckAccent:
		return s.th.Text(KindAccent, str)
	case ckFail:
		return s.th.Text(KindFail, str)
	case ckSelected:
		return s.th.Selected.Render(str)
	}
	return str
}

// renderCanvas turns the canvas into styled lines, grouping runs of one style.
func (s *tasksSection) renderCanvas(c *gcanvas) []string {
	out := make([]string, len(c.runes))
	for y := range c.runes {
		var sb, run strings.Builder
		var rk uint8
		flush := func() {
			if run.Len() > 0 {
				sb.WriteString(s.styleKind(rk, run.String()))
				run.Reset()
			}
		}
		for x := 0; x < c.w; {
			if sg, ok := c.segs[y][x]; ok {
				flush()
				sb.WriteString(s.styleKind(sg.k, sg.text))
				x += sg.w
				continue
			}
			if k := c.kind[y][x]; k != rk {
				flush()
				rk = k
			}
			run.WriteRune(c.runes[y][x])
			x++
		}
		flush()
		out[y] = strings.TrimRight(sb.String(), " ")
	}
	return out
}

// boxWindow shows rows of the canvas, scrolled so the selected box (rows y0 to y1) is in view, with
// a "+N more" marker in the first or last row when the canvas continues past it.
func (s *tasksSection) boxWindow(lines []string, y0, y1, rows int) []string {
	if rows <= 0 || len(lines) <= rows {
		return lines
	}
	gs := &s.graph
	margin := 1
	gs.top = min(gs.top, y0-margin)
	gs.top = max(gs.top, y1+margin-rows)
	gs.top = max(0, min(gs.top, len(lines)-rows))
	vis := append([]string(nil), lines[gs.top:gs.top+rows]...)
	if gs.top > 0 {
		vis[0] = s.th.moreMarker(gs.top)
	}
	if end := gs.top + rows; end < len(lines) {
		vis[rows-1] = s.th.moreMarker(len(lines) - end)
	}
	return vis
}
