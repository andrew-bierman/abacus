package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// The kanban view renders core's typed board (taskview.Board) and plans card moves with
// taskview.PlanMove; it holds only the cursor and what to show. A move is a write, so it goes the
// one way every Tasks write goes: plan, re-read, confirm screen, single-use apply, audit.

var (
	keyColumn  = key.NewBinding(key.WithKeys("h", "l"), key.WithHelp("h/l", "column"))
	keyMove    = key.NewBinding(key.WithKeys("H", "L"), key.WithHelp("H/L", "move card"))
	keyGroup   = key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "group"))
	keyClosedC = key.NewBinding(key.WithKeys("Z"), key.WithHelp("Z", "closed"))
)

// kbSideMinW is the narrowest a column may be for the columns to sit side by side.
const kbSideMinW = 20

// kanban is the board's view state. The selection is a task id, never an index, so a refresh that
// moves cards between columns keeps the cursor on the same card; col and row only say where to
// land when that card is gone.
type kanban struct {
	sel        string
	col, row   int
	started    bool // the first draw picks the first column that holds a card
	group      string
	showClosed bool
	detail     bool // the selected card in full, instead of the columns
	top        int  // scroll of the active column
}

func (s *tasksSection) board() core.KanbanReport {
	return s.list.view.Board(taskview.BoardOptions{Query: s.list.q, Group: s.kb.group, ShowClosed: s.kb.showClosed})
}

// shownColumns are the columns drawn: folded ones (closed, until Z) are a count in the query line.
func shownColumns(rep core.KanbanReport) []core.KanbanColumn {
	var out []core.KanbanColumn
	for _, c := range rep.Columns {
		if !c.Folded {
			out = append(out, c)
		}
	}
	return out
}

// place puts the selection on a card of the report: the selected id when it is still there,
// otherwise the card now at the remembered position, otherwise nothing (an empty column).
func (s *tasksSection) place(cols []core.KanbanColumn) (ci, ri int, card core.KanbanCard, ok bool) {
	k := &s.kb
	if len(cols) == 0 {
		return 0, 0, core.KanbanCard{}, false
	}
	if k.sel != "" {
		for i, c := range cols {
			for j, cd := range c.Cards {
				if cd.ID == k.sel {
					k.col, k.row = i, j
					return i, j, cd, true
				}
			}
		}
	}
	if !k.started {
		k.started = true
		for i, c := range cols {
			if len(c.Cards) > 0 {
				k.col, k.row = i, 0
				break
			}
		}
	}
	k.col = max(0, min(k.col, len(cols)-1))
	cards := cols[k.col].Cards
	if len(cards) == 0 {
		k.sel = ""
		return k.col, 0, core.KanbanCard{}, false
	}
	k.row = max(0, min(k.row, len(cards)-1))
	k.sel = cards[k.row].ID
	return k.col, k.row, cards[k.row], true
}

// kanbanKey handles the board's keys; false means the key is not the board's.
func (s *tasksSection) kanbanKey(k tea.KeyMsg) (tea.Cmd, bool) {
	l := &s.list
	cols := shownColumns(s.board())
	ci, ri, card, has := s.place(cols)
	kb := &s.kb
	switch {
	case kb.detail:
		switch {
		case key.Matches(k, keyBack):
			kb.detail = false
		case key.Matches(k, keyUp):
			l.dOff = max(0, l.dOff-1)
		case key.Matches(k, keyDown):
			l.dOff++
		default:
			return nil, false
		}
	case key.Matches(k, keyFilter):
		l.typing = true
	case key.Matches(k, keyColumn):
		d := 1
		if k.String() == "h" {
			d = -1
		}
		kb.col, kb.row, kb.sel, kb.top = max(0, min(ci+d, len(cols)-1)), ri, "", 0
	case key.Matches(k, keyUp):
		if has && ri > 0 {
			kb.sel, kb.row = cols[ci].Cards[ri-1].ID, ri-1
		}
	case key.Matches(k, keyDown):
		if has && ri < len(cols[ci].Cards)-1 {
			kb.sel, kb.row = cols[ci].Cards[ri+1].ID, ri+1
		}
	case key.Matches(k, keyGroup):
		kb.group = taskview.NextGroup(orStatus(kb.group))
		if kb.group == taskview.GroupStatus {
			kb.group = ""
		}
		kb.col, kb.row, kb.top, kb.started = 0, 0, 0, false
	case key.Matches(k, keyClosedC):
		if orStatus(kb.group) != taskview.GroupStatus {
			s.notice = "closed cards fold into a count only in the status grouping; s cycles the grouping"
			return nil, true
		}
		kb.showClosed = !kb.showClosed
	case key.Matches(k, keyGraph):
		if !has {
			return nil, true
		}
		s.openGraphOn(card.ID)
		return s.graphRefresh(), true
	case key.Matches(k, keyEnter):
		if has {
			kb.detail, l.dOff = true, 0
		}
	case key.Matches(k, keyBack):
		if l.text == "" {
			return nil, false
		}
		l.text, l.qerr = "", nil
		l.q = query.Query{}
		s.recompute()
	case key.Matches(k, keyMove):
		d := 1
		if k.String() == "H" {
			d = -1
		}
		return s.kbMove(cols, ci, card, has, d), true
	default:
		return nil, false
	}
	return nil, true
}

// carrySelection keeps the task you were looking at when you leave the board: the List's cursor goes
// to the selected card. (The board itself opens on its first card, whatever the list had selected.)
func (s *tasksSection) carrySelection(from, to taskViewID) {
	switch {
	case from == viewKanban && to != viewKanban && s.kb.sel != "":
		if to == viewList {
			s.selected = s.kb.sel
			s.reanchor(s.ids(), false)
		}
	}
}

// openGraphOn shows the neighborhood of a task chosen on the board (the Graph's own openGraph reads
// the list's cursor).
func (s *tasksSection) openGraphOn(id string) {
	gs := &s.graph
	gs.focus, gs.sel, gs.whole, gs.top = id, id, false, 0
	s.list.cur, s.drilled, s.list.detailFocus = viewGraph, false, false
}

// enterable is whether a card can be moved into the column: the four working statuses and closed.
func enterable(key string) bool { return key == "closed" || slices.Contains(core.TaskStatuses, key) }

func orStatus(g string) string {
	if g == "" {
		return taskview.GroupStatus
	}
	return g
}

// kbMove plans moving the selected card one column over. Nothing is written here: the plan goes to
// the same re-read and confirm screen every Tasks write uses.
func (s *tasksSection) kbMove(cols []core.KanbanColumn, ci int, card core.KanbanCard, has bool, d int) tea.Cmd {
	switch {
	case orStatus(s.kb.group) != taskview.GroupStatus:
		s.notice = "cards move between status columns only: press s until the grouping is status"
		return nil
	case s.applier == nil:
		s.notice = "writes are not available: no task provider can write"
		return nil
	case !has:
		s.notice = "no card selected"
		return nil
	}
	to := ci + d
	if card.Status == "closed" && d < 0 { // out of closed there is one place to go: open
		to = -1
		for i, c := range cols {
			if c.Key == "open" {
				to = i
			}
		}
	}
	if card.Status != "closed" || d > 0 { // stepping skips columns a card cannot enter (a custom status is move-out-only)
		for to >= 0 && to < len(cols) && !enterable(cols[to].Key) {
			to += d
		}
	}
	if to < 0 || to >= len(cols) {
		s.notice = "no column that way"
		return nil
	}
	row, ok := s.list.view.Row(card.ID)
	if !ok {
		s.notice = "that card is no longer in the tracker: refresh with r"
		return nil
	}
	ch, err := taskview.PlanMove(row, cols[to].Key)
	if err == nil {
		_, err = core.PlanTaskChange(s.writer, ch)
	}
	if err != nil {
		s.notice = core.Sanitize(err.Error())
		return nil
	}
	s.op, s.target, s.plan, s.formErr = ch.Op, rowTask(row), core.WritePlan{}, ""
	return s.beginConfirm(ch)
}

func (s *tasksSection) kanbanBindings() []key.Binding {
	enter, back := keyEnter, keyBack
	enter.SetEnabled(len(s.board().Columns) > 0)
	back.SetEnabled(s.kb.detail || s.list.text != "")
	if s.kb.detail {
		return []key.Binding{key.NewBinding(key.WithKeys("up", "k", "down", "j"), key.WithHelp("↑/↓", "scroll")), back}
	}
	return []key.Binding{keyMove, keyColumn, moveBindings()[0], enter, keyGroup, keyClosedC, keyGraph, keyFilter, keyView, back}
}

// ---- drawing ----

// cardMarks are the dependency marks of a card: ⊘N held by N unfinished tasks, →N holds N.
func cardMarks(k core.KanbanCard) string {
	var p string
	switch {
	case k.BlockedBy > 0:
		p = fmt.Sprintf("⊘%d", k.BlockedBy)
	case k.Blocked:
		p = "⊘↑"
	}
	if k.Blocks > 0 {
		p += fmt.Sprintf("→%d", k.Blocks)
	}
	return p
}

// cardLines are a card's two lines, styled (and plain, which the selected card is drawn from).
func (s *tasksSection) cardLines(k core.KanbanCard, byStatus bool) (styled1, plain1, title string) {
	th := s.th
	head := san(k.ID) + " "
	if !byStatus {
		head = taskGlyph(k.Status) + " " + head
	}
	prio, marks := fmt.Sprintf("P%d", k.Priority), cardMarks(k)
	plain1, styled1 = head+prio, head+prio
	if pk, ok := taskPriorityKind(k.Priority); ok {
		styled1 = head + th.Text(pk, prio)
	}
	if marks != "" {
		plain1 += " " + marks
		if k.Blocked {
			styled1 += " " + th.Text(KindFail, marks)
		} else {
			styled1 += " " + th.Text(KindMuted, marks)
		}
	}
	if k.Assignee != "" {
		plain1 += " " + san(k.Assignee)
		styled1 += " " + th.Text(KindMuted, san(k.Assignee))
	}
	return styled1, plain1, san(k.Title)
}

// columnBody is one column's content: its counts, then three rows per card (two lines and a gap),
// windowed around the selected card when it is the active column.
func (s *tasksSection) columnBody(c core.KanbanColumn, byStatus, active bool, iw, rows int) []string {
	th := s.th
	var lines []string
	if sum := columnSummary(c); sum != "" {
		lines = append(lines, th.Text(KindMuted, trunc(sum, iw)))
	}
	if len(c.Cards) == 0 {
		lines = append(lines, th.Text(KindMuted, "no cards"))
		return clipLines(th, lines, rows)
	}
	cursor := 0
	for _, k := range c.Cards {
		st, pl, title := s.cardLines(k, byStatus)
		sel := active && k.ID == s.kb.sel
		if sel {
			cursor = len(lines) + 1
			lines = append(lines, th.Selected.Render(padTo(trunc("▸ "+pl, iw), iw)), th.Selected.Render(padTo(trunc("  "+title, iw), iw)))
		} else {
			t := "  " + title
			if k.Status == "closed" {
				t = th.Text(KindMuted, t)
			}
			lines = append(lines, "  "+st, t)
		}
		lines = append(lines, "")
	}
	if active {
		return windowLines(th, lines, cursor, rows, &s.kb.top)
	}
	return clipLines(th, lines, rows)
}

// columnSummary is the WIP counts of a column: urgent (P0 and P1) and held by a dependency.
func columnSummary(c core.KanbanColumn) string {
	var p []string
	if c.P0P1 > 0 {
		p = append(p, fmt.Sprintf("%d urgent", c.P0P1))
	}
	if c.Blocked > 0 {
		p = append(p, fmt.Sprintf("%d held", c.Blocked))
	}
	return strings.Join(p, " · ")
}

// kbQueryLine is the row under the view strip: the grouping, the folded closed count and the
// query on the left, how many tasks match on the right (or what is wrong with the query).
func (s *tasksSection) kbQueryLine(rep core.KanbanReport, w int) string {
	th, l := s.th, s.list
	left := th.Text(KindMuted, "group: "+rep.Group)
	switch {
	case rep.ClosedHidden > 0:
		left += th.Text(KindMuted, fmt.Sprintf(" · closed %d hidden (Z)", rep.ClosedHidden))
	case s.kb.showClosed && rep.Group == taskview.GroupStatus:
		left += th.Text(KindMuted, " · closed shown (Z)")
	}
	left += " · "
	if l.text == "" && !l.typing {
		left += th.Text(KindMuted, "/ to filter")
	} else {
		left += th.Text(KindAccent, "/") + san(l.text)
		if l.typing {
			left += "▏"
		}
	}
	right := th.Text(KindMuted, fmt.Sprintf("%d of %d", rep.Shown+rep.ClosedHidden, rep.Total))
	if l.qerr != nil {
		right = th.Text(KindFail, san(l.qerr.Msg)+fmt.Sprintf(" (col %d)", l.qerr.Col))
	}
	if lipgloss.Width(left)+1+lipgloss.Width(right) > w {
		if l.qerr != nil {
			return trunc(right, w)
		}
		left = trunc(left, max(w-lipgloss.Width(right)-1, 12))
	}
	gap := max(w-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return trunc(left+strings.Repeat(" ", gap)+right, w)
}

// tabStrip is the narrow layout's row of columns, the active one in brackets so it reads without
// color; when they do not all fit it keeps a window around the active one with ‹ and › marks.
func (s *tasksSection) tabStrip(cols []core.KanbanColumn, active, w int) string {
	th := s.th
	item := func(i int) (string, int) {
		name := fmt.Sprintf("%s %d", san(cols[i].Title), cols[i].Count)
		if i == active {
			return th.Title.Render("[" + name + "]"), lipgloss.Width(name) + 2
		}
		return " " + name + " ", lipgloss.Width(name) + 2
	}
	build := func(lo, hi int) (string, int) {
		var b strings.Builder
		width := 0
		if lo > 0 {
			b.WriteString("‹")
			width++
		}
		for i := lo; i <= hi; i++ {
			it, iw := item(i)
			if i > lo {
				b.WriteString("│")
				width++
			}
			b.WriteString(it)
			width += iw
		}
		if hi < len(cols)-1 {
			b.WriteString("›")
			width++
		}
		return b.String(), width
	}
	lo, hi := 0, len(cols)-1
	for {
		out, width := build(lo, hi)
		if width <= w || lo == hi {
			return trunc(out, w)
		}
		if active-lo > hi-active { // drop the far end
			lo++
		} else {
			hi--
		}
	}
}

// kanbanView is the board: the view strip, the query line, then the columns side by side from
// bpWide columns (when each gets kbSideMinW), else a tab strip with one column at a time.
func (s *tasksSection) kanbanView() (out string) {
	// A board that cannot be drawn must not take the whole TUI down: say so and keep the keys alive.
	defer func() {
		if recover() != nil {
			out = joinLines([]string{trunc(s.th.Text(KindWarn, "the board could not be drawn: press v for another view or r to refresh"), s.width())})
		}
	}()
	th, w := s.th, s.width()
	l := &s.list
	rep := s.board()
	h := s.bodyH(s.noticeText())
	panelH := h
	if h > 0 {
		panelH = max(h-2, 1)
	}
	body := []string{s.strip(w), s.kbQueryLine(rep, w)}
	cols := shownColumns(rep)
	ci, _, card, has := s.place(cols)
	byStatus := rep.Group == taskview.GroupStatus
	rowsOf := func(ph int) int { // content rows of a panel panelH tall
		if ph > 0 {
			return max(ph-panelChromeH, 1)
		}
		return 12
	}
	switch {
	case rep.Total == 0:
		body = append(body, panel(th, "Kanban", padRows(wrap(th.Text(KindMuted, s.emptyTasksHint()), innerW(w)), rowsOf(panelH)), w, panelH, true)...)
	case len(cols) == 0:
		hint := "no cards in this grouping: press s for another grouping, or esc to clear the filter"
		body = append(body, panel(th, "Kanban", padRows(wrap(th.Text(KindMuted, hint), max(w-panelChromeW, 1)), rowsOf(panelH)), w, panelH, true)...)
	case s.kb.detail && has:
		d, _ := s.list.view.Detail(card.ID)
		rows := rowsOf(panelH)
		dl, off := scrollWindow(th, s.detailLines(d, max(w-panelChromeW, 1)), l.dOff, rows)
		l.dOff = off
		body = append(body, panel(th, san(card.ID), padRows(dl, rows), w, panelH, true)...)
	case w >= bpWide && w/len(cols) >= kbSideMinW:
		// side by side: no gap, the panels share the width, the last takes the remainder
		ph := panelH
		var panels [][]string
		for i, c := range cols {
			cw := w / len(cols)
			if i < w%len(cols) {
				cw++
			}
			rows := rowsOf(ph)
			title := fmt.Sprintf("%s %d", san(c.Title), c.Count)
			panels = append(panels, panel(th, title, padRows(s.columnBody(c, byStatus, i == ci, max(cw-panelChromeW, 1), rows), rows), cw, ph, i == ci))
		}
		body = append(body, joinPanels(panels)...)
	default:
		body = append(body, s.tabStrip(cols, ci, w))
		ph := panelH
		if ph > 0 {
			ph = max(ph-1, 1)
		}
		rows := rowsOf(ph)
		c := cols[ci]
		title := fmt.Sprintf("%s  %d", san(c.Title), c.Count)
		body = append(body, panel(th, title, padRows(s.columnBody(c, byStatus, true, max(w-panelChromeW, 1), rows), rows), w, ph, true)...)
	}
	if n := s.noticeText(); n != "" {
		body = append(body, noticeLine(th, w, n)...)
	}
	return joinLines(body)
}

// joinPanels puts equally tall panels side by side, row by row.
func joinPanels(ps [][]string) []string {
	if len(ps) == 0 {
		return nil
	}
	out := make([]string, len(ps[0]))
	for _, p := range ps {
		for i := range out {
			if i < len(p) {
				out[i] += p[i]
			}
		}
	}
	return out
}

// opVerb is the past tense the notice after a write uses.
func opVerb(op core.TaskOp) string {
	switch op {
	case core.TaskCreate:
		return "created"
	case core.TaskUpdate:
		return "updated"
	case core.TaskClose:
		return "closed"
	case core.TaskReopen:
		return "reopened"
	case core.TaskLabelAdd:
		return "labeled"
	case core.TaskLabelRemove:
		return "unlabeled"
	case core.TaskPriority:
		return "reprioritized"
	case core.TaskAssign:
		return "assigned"
	case core.TaskDepAdd:
		return "linked"
	case core.TaskDepRemove:
		return "unlinked"
	case core.TaskAddComment:
		return "commented on"
	}
	return string(op) + "d"
}
