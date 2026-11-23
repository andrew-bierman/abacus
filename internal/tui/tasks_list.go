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

// nowFn is the clock of the Tasks views (ages, staleness); tests replace it so goldens are stable.
var nowFn = time.Now

var (
	keySort   = key.NewBinding(key.WithKeys("s", "S"), key.WithHelp("s/S", "sort"))
	keyPreset = key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "quick filter"))
	keyView   = key.NewBinding(key.WithKeys("v", "V"), key.WithHelp("v", "view"))
)

// taskSnapshotMsg is the answer to a snapshot read of the whole tracker.
type taskSnapshotMsg struct {
	snap core.TaskSnapshot
	err  error
	fp   string // the tracker's change fingerprint just before the read ("" when it has none)
}

// tasksList is the state of the Tasks list view: the snapshot and what is derived from it, the
// query being edited, the quick filter and sort, and the detail pane's scroll. All filtering,
// sorting and deriving is taskview's; this holds state and draws.
type tasksList struct {
	view     *taskview.View
	readAt   time.Time
	fallback bool // the list is the recent-changes summary, not the whole tracker
	// depsUnknown: no dependency data (recent changes), so nothing is ready or blocked.
	depsUnknown bool
	snapErr     error
	slow        bool      // the last read timed out or was too large: back off
	slowUntil   time.Time // no timed read before this
	behind      bool      // a read that may predate the user's last write is on screen; a fresh one is out
	warnings    []string

	preset    taskview.Preset
	presetSet bool // the user chose a quick filter, so a new snapshot leaves it alone
	sort      taskview.Sort
	text      string // the query text, as typed
	q         query.Query
	qerr      *query.Error
	typing    bool
	rep       core.TaskListReport

	detailFocus bool // wide layout: j/k scroll the detail pane
	dOff        int
	cur         taskViewID
	tree        taskTree // the tree mode of this view (tasks_tree.go)
}

func newTasksList() tasksList {
	return tasksList{preset: taskview.PresetOpen, sort: taskview.DefaultSort()}
}

// setSnapshot builds the views from a new snapshot and refreshes the list, keeping the cursor on
// the same task by id.
func (s *tasksSection) setSnapshot(snap core.TaskSnapshot) {
	l := &s.list
	l.view = taskview.Build(snap, nowFn())
	s.loadTree(snap)
	l.readAt, l.fallback, l.depsUnknown, l.warnings = snap.ReadAt, snap.DepsUnknown, snap.DepsUnknown, snap.Warnings
	if !l.presetSet { // until the user picks one: all for a recent-changes list, else open
		l.preset = taskview.DefaultPreset(snap)
	}
	s.recompute()
}

// recompute refilters and resorts, then puts the cursor back on the selected task.
func (s *tasksSection) recompute() {
	l := &s.list
	if l.view == nil {
		l.rep = core.TaskListReport{}
		return
	}
	l.rep = l.view.List(taskview.ListOptions{Query: l.q, Preset: l.preset, Sort: l.sort})
	s.recomputeTree()
	s.climbToListed()
	s.reanchor(s.ids(), true)
	s.settle(len(l.rep.Rows))
	if len(l.rep.Rows) == 0 {
		l.detailFocus = false
	}
}

// rowTask is a list row as the write forms know a task.
func rowTask(r core.TaskRow) core.Task {
	return core.Task{ID: r.ID, Title: r.Title, Status: r.Status, Priority: r.Priority, Type: r.Type, Assignee: r.Assignee, UpdatedAt: r.UpdatedAt}
}

func (s *tasksSection) rows() []core.TaskRow {
	if s.treeOn() {
		return s.list.tree.rows
	}
	return s.list.rep.Rows
}

// detail is the selected task's detail report.
func (s *tasksSection) detail() (core.TaskDetailReport, bool) {
	if s.list.view == nil || s.cursor >= len(s.rows()) {
		return core.TaskDetailReport{}, false
	}
	return s.list.view.Detail(s.rows()[s.cursor].ID)
}

// inDetail is whether j and k scroll the detail pane instead of moving the cursor.
func (s *tasksSection) inDetail() bool {
	return s.list.cur == viewList && (s.drilled || s.list.detailFocus)
}

func (s *tasksSection) moved() { s.list.dOff = 0 }

// listKey handles the keys of the task views; it reports whether the key was one of them.
func (s *tasksSection) listKey(k tea.KeyMsg) (tea.Cmd, bool) {
	l := &s.list
	if l.typing {
		s.typeKey(k)
		return nil, true
	}
	switch {
	case key.Matches(k, keyView):
		n := len(taskViews)
		d := 1
		if k.String() == "V" {
			d = n - 1
		}
		return s.showView(taskViewID((int(l.cur) + d) % n)), true
	}
	if l.cur == viewKanban {
		if cmd, ok := s.kanbanKey(k); ok {
			return cmd, true
		}
	}
	if l.cur == viewGraph {
		if cmd, ok := s.graphKeys(k); ok {
			return cmd, true
		}
	}
	if l.cur != viewList {
		// e, x and the workspace jump act on the list's current task, which this view does not show.
		if key.Matches(k, keyEdit, keyClose, keyWorkspace) {
			s.notice = "switch to the List view (v) to edit, close or open a task"
			return nil, true
		}
		return nil, false
	}
	switch {
	case key.Matches(k, keyTree) || (s.treeOn() && key.Matches(k, keyFold, keyFoldAll)):
		s.treeKey(k.String())
	case key.Matches(k, keyFilter):
		l.typing = true
	case key.Matches(k, keySort):
		if k.String() == "S" {
			l.sort = l.sort.Flip()
		} else {
			l.sort = l.sort.Next()
		}
		s.recompute()
		s.moved()
	case key.Matches(k, keyGraph):
		s.openGraph()
		return s.graphRefresh(), true
	case key.Matches(k, keyPreset):
		l.preset, l.presetSet = l.preset.Next(l.depsUnknown), true
		if l.depsUnknown {
			s.notice = "ready and blocked need the whole tracker: not available in recent changes"
		}
		s.recompute()
		s.moved()
	case key.Matches(k, keyEnter):
		switch {
		case len(s.rows()) == 0:
		case isStacked(s.width()):
			s.enter(s.width(), len(s.rows()))
		default:
			l.detailFocus = true
		}
		s.moved()
	case key.Matches(k, keyBack):
		switch {
		case s.drilled || l.detailFocus:
			s.back()
			l.detailFocus = false
		case l.text != "":
			l.text, l.q, l.qerr = "", query.Query{}, nil
			s.recompute()
		default:
			return nil, false
		}
		s.moved()
	case key.Matches(k, keyUp):
		if s.inDetail() {
			l.dOff = max(0, l.dOff-1)
		} else {
			s.up(s.ids())
			s.moved()
		}
	case key.Matches(k, keyDown):
		if s.inDetail() {
			l.dOff++
		} else {
			s.down(s.ids())
			s.moved()
		}
	default:
		return nil, false
	}
	return nil, true
}

// showView switches the Tasks tab to a view: the one entry both `v` and the `:` prompt use, so a view
// is initialized the same way however it is reached (the drill closes, the board's selected card
// carries to the list, the graph opens on the selected task or card).
func (s *tasksSection) showView(next taskViewID) tea.Cmd {
	l := &s.list
	prev := l.cur
	l.cur = next
	s.drilled, l.detailFocus, s.kb.detail = false, false, false
	s.carrySelection(prev, next)
	if next == viewGraph && prev != viewGraph { // the graph is about the task selected now, not the one it last showed
		if prev == viewKanban && s.kb.sel != "" { // ...and on the board that is the selected card
			s.openGraphOn(s.kb.sel)
		} else {
			s.openGraph()
		}
		return s.graphRefresh()
	}
	return nil
}

// typeKey edits the query: every change filters live, enter keeps the query, esc clears it.
func (s *tasksSection) typeKey(k tea.KeyMsg) {
	l := &s.list
	switch k.Type {
	case tea.KeyEsc:
		l.typing, l.text, l.q, l.qerr = false, "", query.Query{}, nil
	case tea.KeyEnter:
		l.typing = false
	case tea.KeyUp:
		s.up(s.ids())
		s.moved()
		return
	case tea.KeyDown:
		s.down(s.ids())
		s.moved()
		return
	case tea.KeyBackspace:
		if r := []rune(l.text); len(r) > 0 {
			l.text = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		l.text = ""
	case tea.KeyRunes, tea.KeySpace:
		l.text += string(k.Runes)
	default:
		return
	}
	s.reparse()
}

// reparse parses the query text: a good query filters, a bad one keeps the last good filter and
// shows what is wrong under the cursor's column.
func (s *tasksSection) reparse() {
	l := &s.list
	q, err := query.Parse(l.text)
	if err != nil {
		if qe, ok := err.(*query.Error); ok {
			l.qerr = qe
		}
		return
	}
	l.q, l.qerr = q, nil
	s.recompute()
	s.moved()
}

func (s *tasksSection) listBindings() []key.Binding {
	enter, back := keyEnter, keyBack
	l := s.list
	inDetail := s.drilled || l.detailFocus
	enter.SetEnabled(!inDetail && len(s.rows()) > 0)
	back.SetEnabled(inDetail || l.text != "")
	if l.typing {
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "keep")), key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear"))}
	}
	if l.cur == viewGraph {
		return s.graphBindings()
	}
	if l.cur == viewKanban {
		return s.kanbanBindings()
	}
	if l.cur != viewList {
		return []key.Binding{keyView}
	}
	if inDetail {
		return []key.Binding{key.NewBinding(key.WithKeys("up", "k", "down", "j"), key.WithHelp("↑/↓", "scroll")), back}
	}
	// The filter, sort and view keys lead: a narrow footer is cut from the right, and these are
	// the ones a new user has to find.
	bs := append([]key.Binding{keyFilter}, s.treeLead()...)
	bs = append(bs, keySort, keyView, enter, back)
	bs = append(bs, s.treeBindings()...)
	return append(bs, keyPreset, keyGraph, moveBindings()[0])
}

// ---- drawing ----

func (s *tasksSection) typeLabel(t string) string {
	if t == "feature" {
		return "feat"
	}
	return t
}

// depCell is the blocked-by and blocks marks of a row: ⊘N held by N unfinished tasks, →N holds N.
func depCell(r core.TaskRow) string {
	var p string
	switch {
	case r.BlockedBy > 0:
		p = fmt.Sprintf("⊘%d", r.BlockedBy)
	case r.HeldBy != "":
		p = "⊘↑"
	}
	if r.Blocks > 0 {
		p += fmt.Sprintf("→%d", r.Blocks)
	}
	return p
}

// task list columns: id, title, status glyph and priority stay (the title takes the slack); the
// dependency marks, then the type and age, then the assignee, then labels go as the width shrinks.
var listTable = table{cols: []column{
	{min: 5, max: 12, prio: 0}, {min: 14, prio: 0}, {min: 2, max: 2, prio: 0}, {min: 2, max: 2, prio: 0},
	{min: 4, max: 4, prio: 1}, {min: 5, max: 5, prio: 2}, {min: 3, max: 4, prio: 2}, {min: 6, max: 6, prio: 3},
}, gap: 1}

var listHeader = []string{"id", "title", "st", "P", "dep", "type", "age", "who"}

// labelsCol is the last column, drawn from bpUltra columns only (the detail always shows labels).
var labelsCol = column{min: 6, max: 24, prio: 4}

// listCells are one row's cells, styled and plain (the selected row is drawn from the plain ones).
func (s *tasksSection) listCells(r core.TaskRow) (styled, plain []string) {
	th := s.th
	title := san(r.Title)
	if r.ChildTotal > 0 {
		title += fmt.Sprintf(" %d/%d", r.ChildDone, r.ChildTotal)
	}
	prio := fmt.Sprintf("P%d", r.Priority)
	var labels []string
	for _, l := range r.Labels {
		labels = append(labels, san(l))
	}
	plain = []string{san(r.ID), title, taskGlyph(r.Status), prio, depCell(r), s.typeLabel(orDash(r.Type)), orDash(r.Age), orDash(r.Assignee), strings.Join(labels, ",")}
	styled = append([]string(nil), plain...)
	styled[2] = s.glyph(r.Status)
	if k, ok := taskPriorityKind(r.Priority); ok {
		styled[3] = th.Text(k, prio)
	}
	if r.Blocked {
		styled[4] = th.Text(KindFail, plain[4])
	} else if plain[4] != "" {
		styled[4] = th.Text(KindMuted, plain[4])
	}
	if r.Status == "closed" {
		styled[1] = th.Text(KindMuted, title)
	}
	return styled, plain
}

// listLines is the list panel's content: a header row, then the rows with the cursor row drawn
// on the selection style. It returns the lines and the line of the cursor.
func (s *tasksSection) listLines(iw int) ([]string, int) {
	th := s.th
	rows := s.rows()
	styled, plain := make([][]string, len(rows)), make([][]string, len(rows))
	for i, r := range rows {
		styled[i], plain[i] = s.listCells(r)
	}
	tb := listTable
	hdr := append([]string(nil), listHeader...)
	if s.width() >= bpUltra {
		tb = table{cols: append(append([]column(nil), tb.cols...), labelsCol), gap: tb.gap}
		hdr = append(hdr, "labels")
	} else {
		for i := range styled { // the labels cell is only shown from bpUltra columns
			styled[i], plain[i] = styled[i][:len(listHeader)], plain[i][:len(listHeader)]
		}
	}
	if s.treeOn() {
		tb, hdr, styled, plain = s.treeColumns(iw, tb, hdr, styled, plain)
	}
	if s.ws.has() && len(rows) > 0 {
		cells, plainCells := make([]string, len(rows)), make([]string, len(rows))
		for i, r := range rows {
			it, ok := taskItem(rowTask(r))
			cells[i], plainCells[i] = s.ws.place(th, it, ok, false), s.ws.place(th, it, ok, true)
		}
		if wtb, ok := s.ws.whereCol(tb, s.width(), iw, styled, cells); ok {
			tb = wtb
			hdr = append(hdr, "where")
			for i := range styled {
				styled[i], plain[i] = append(styled[i], cells[i]), append(plain[i], plainCells[i])
			}
		}
	}
	widths := tb.fitRows(max(iw-2, 1), append([][]string{hdr}, styled...))
	lines := []string{"  " + th.Text(KindMuted, tb.row(widths, hdr))}
	for i := range rows {
		if i == s.cursor {
			lines = append(lines, th.Selected.Render(padTo(trunc("▸ "+tb.row(widths, plain[i]), iw), iw)))
			continue
		}
		lines = append(lines, "  "+tb.row(widths, styled[i]))
	}
	return lines, s.cursor + 1
}

// queryLine is the row under the view strip: the quick filter and the query on the left, how
// many tasks match and the sort on the right, or what is wrong with the query.
func (s *tasksSection) queryLine(w int) string {
	th, l := s.th, s.list
	left := th.Text(KindAccent, "["+string(l.preset)+"]") + " "
	switch {
	case l.text == "" && !l.typing:
		left += th.Text(KindMuted, "/ to filter")
	default:
		left += th.Text(KindAccent, "/") + san(l.text)
		if l.typing {
			left += "▏"
		}
	}
	dir := "↑"
	if l.sort.Desc {
		dir = "↓"
	}
	right := th.Text(KindMuted, fmt.Sprintf("%d of %d · sort: %s %s", l.rep.Matched, l.rep.Total, l.sort.Key, dir))
	if l.qerr != nil {
		right = th.Text(KindFail, san(l.qerr.Msg)+fmt.Sprintf(" (col %d)", l.qerr.Col))
	}
	if lipgloss.Width(left)+1+lipgloss.Width(right) > w {
		if l.qerr != nil { // the error outranks the query text
			return trunc(right, w)
		}
		left = trunc(left, max(w-lipgloss.Width(right)-1, 12))
	}
	gap := max(w-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return trunc(left+strings.Repeat(" ", gap)+right, w)
}

// listView is the Tasks list: the view strip, the query line, and the list beside the detail
// (stacked below bpWide, where enter opens the detail full width).
func (s *tasksSection) listView() string {
	th, w := s.th, s.width()
	l := &s.list
	h := s.bodyH(s.noticeText())
	panelH := h
	if h > 0 {
		panelH = max(h-2, 1)
	}
	g := duoLayout(w, panelH)
	g.fill = true
	title := "Tasks"
	if t := san(l.rep.Tracker); t != "" {
		title += " · " + t
	}
	if l.fallback {
		title += " · recent only"
	}
	if seen := s.seenAt(); !seen.IsZero() && !taskview.Fresh(seen, nowFn(), s.pollEvery()) {
		title += " · " + th.Text(KindWarn, "stale "+taskview.Age(seen, nowFn()))
	}
	if m := s.changedMarker(); m != "" {
		title += " · " + th.Text(KindAccent, m)
	}
	iw := g.listInner()
	var list []string
	cursorLine := 0
	switch {
	case l.rep.Total == 0:
		list = []string{th.Text(KindMuted, "no tasks yet — press n to add the first one")}
	case len(l.rep.Rows) == 0:
		list = wrap(th.Text(KindMuted, "no tasks match: esc clears the query, f changes the quick filter"), iw)
	default:
		list, cursorLine = s.listLines(iw)
	}
	rows := g.inner
	if rows <= 0 && h > 0 {
		rows = max(h-4, 1)
	}
	legend := s.legend(iw, rows)
	list = windowLines(th, list, cursorLine, rows-len(legend), &s.top)
	if len(legend) > 0 { // the legend sits on the last rows of the panel, under the list
		list = append(padRows(list, rows-len(legend)), legend...)
	}

	detailTitle, detail := "Task", []string(nil)
	if d, ok := s.detail(); ok {
		detailTitle = san(d.Row.ID)
		if l.detailFocus && g.wide {
			detailTitle += " · scrolling"
		}
		var off int
		dl := s.detailLines(d, max(g.detail-panelChromeW, 1))
		if hl := s.historyLines(d.Row.ID, max(g.detail-panelChromeW, 1)); len(hl) > 0 {
			if s.hist.reveal { // H just opened it: put the History heading at the top of the pane
				l.dOff, s.hist.reveal = len(dl)+1, false
			}
			dl = append(dl, hl...)
		}
		if it, ok := s.selectedItem(); s.ws.has() { // where the task lives, from the optional workspace layer
			if det := s.ws.detail(th, it, ok, 8); len(det) > 1 {
				dl = append(append(dl, "", th.Section("Workspace")), det[1:]...)
			}
		}
		detail, off = scrollWindow(th, dl, l.dOff, rows)
		l.dOff = off
	} else {
		detail = []string{th.Text(KindMuted, "no task selected")}
	}
	body := []string{s.strip(w), s.queryLine(w)}
	body = append(body, g.draw(th, title, list, detailTitle, detail, s.drilled)...)
	if n := s.noticeText(); n != "" {
		body = append(body, noticeLine(th, w, n)...)
	}
	return joinLines(body)
}

// legend explains the marks of the list in two muted rows at the foot of the panel, when the panel
// is tall enough to spare them.
func (s *tasksSection) legend(iw, rows int) []string {
	if rows < 10 || s.list.rep.Total == 0 || len(s.rows()) == 0 {
		return nil
	}
	th := s.th
	status := trunc(th.Text(KindMuted, "○ open ◐ active ● blocked ✓ closed ◇ deferred"), iw)
	if s.list.depsUnknown {
		return []string{status}
	}
	if s.treeOn() && s.treeLegendWanted(rows) {
		return []string{s.treeLegend(iw), trunc(th.Text(KindMuted, "⊘N held by N · →N holds N · ⊘↑ held by parent"), iw), status}
	}
	return []string{trunc(th.Text(KindMuted, "⊘N held by N · →N holds N · ⊘↑ held by parent"), iw), status}
}

// noticeText is the row under the panels: what just happened, else a read problem.
func (s *tasksSection) noticeText() string {
	behind := "this list may not show your last change yet: reading again"
	switch {
	case s.notice != "" && s.list.behind:
		return s.notice + " · " + behind
	case s.notice != "":
		return s.notice
	case s.list.behind:
		return behind
	case s.list.slow:
		return "tracker too large to load quickly: showing recent changes only, trying again every 5 minutes (r tries now)"
	case s.list.snapErr != nil && !s.list.fallback:
		return "refresh failed: " + core.Sanitize(s.list.snapErr.Error()) + " (showing the last read)"
	case s.list.snapErr != nil:
		return "the full list could not be read: " + core.Sanitize(s.list.snapErr.Error())
	case len(s.list.warnings) > 0:
		return "the read looks incomplete: " + core.Sanitize(s.list.warnings[0])
	}
	return ""
}
