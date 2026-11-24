package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Marks and bulk changes on the Tasks list and tree. Space marks a task, and every edit key then acts
// on the marked tasks instead of the one under the cursor: the plan lists every task with its exact
// command, one y applies it, each task is read again right before its own write and skipped (and
// reported) if it changed, and the screen after says what became of each one.

var (
	keyMark      = key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "mark"))
	keyMarkRange = key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "mark range"))
	keyMarkFlip  = key.NewBinding(key.WithKeys("I"), key.WithHelp("I", "invert marks"))
	keyUnmark    = key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "clear marks"))
)

// bulkAppliedMsg is the answer to a batch.
type bulkAppliedMsg struct {
	rep core.BulkReport
	err error
}

// marksState holds the marked task ids (by id, so a refresh never moves a mark onto another task) and
// the batch being planned, confirmed or reported.
type marksState struct {
	set    map[string]bool
	anchor string // the task last marked, where R starts

	form   bool // the open one-field form is for a batch, not for the task under the cursor
	spec   core.BulkSpec
	plan   core.BulkPlan
	rep    core.BulkReport
	titles map[string]string // id -> title of the batch's tasks, for the result screen
	off    int               // scroll of the confirm and result screens
}

// markedRows are the marked tasks that are listed now, in list order, as the batch knows them.
func (s *tasksSection) markedRows() []core.Task {
	var out []core.Task
	for _, r := range s.rows() {
		if s.mk.set[r.ID] {
			t := rowTask(r)
			t.Labels = r.Labels
			out = append(out, t)
		}
	}
	return out
}

// markCount is how many marks there are and how many of them are not in the list now (filtered out or
// folded away): those are never part of a batch.
func (s *tasksSection) markCount() (shown, hidden int) {
	shown = len(s.markedRows())
	return shown, len(s.mk.set) - shown
}

func (s *tasksSection) markOn() bool { return len(s.mk.set) > 0 }

// pruneMarks drops marks of tasks the tracker no longer has.
func (s *tasksSection) pruneMarks() {
	if s.list.view == nil {
		return
	}
	for id := range s.mk.set {
		if _, ok := s.list.view.Detail(id); !ok {
			delete(s.mk.set, id)
		}
	}
}

func (s *tasksSection) setMark(id string, on bool) {
	if s.mk.set == nil {
		s.mk.set = map[string]bool{}
	}
	if on {
		s.mk.set[id] = true
	} else {
		delete(s.mk.set, id)
	}
}

// markKey handles space, R, I and U on the list and tree.
func (s *tasksSection) markKey(k tea.KeyMsg) bool {
	rows := s.rows()
	switch {
	case key.Matches(k, keyMark):
		if s.cursor >= len(rows) {
			return true
		}
		id := rows[s.cursor].ID
		s.setMark(id, !s.mk.set[id])
		s.mk.anchor = id
		s.down(s.ids())
		s.moved()
	case key.Matches(k, keyMarkRange):
		if s.cursor >= len(rows) {
			return true
		}
		from := s.cursor
		for i, r := range rows {
			if r.ID == s.mk.anchor {
				from = i
			}
		}
		lo, hi := min(from, s.cursor), max(from, s.cursor)
		for _, r := range rows[lo : hi+1] {
			s.setMark(r.ID, true)
		}
		s.mk.anchor = rows[s.cursor].ID
	case key.Matches(k, keyMarkFlip):
		for _, r := range rows {
			s.setMark(r.ID, !s.mk.set[r.ID])
		}
	case key.Matches(k, keyUnmark):
		s.mk = marksState{}
	default:
		return false
	}
	return true
}

// bulkKey handles an edit key while there are marks: it opens the batch form (or the plan, for close).
// The keys that cannot be a batch say so instead of acting on the one task under the cursor.
func (s *tasksSection) bulkKey(k tea.KeyMsg) (tea.Cmd, bool) {
	var spec core.BulkSpec
	var f formField
	switch {
	case key.Matches(k, keyTag):
		spec, f = core.BulkSpec{Op: core.TaskLabelAdd}, formField{label: "Label"}
	case key.Matches(k, keyUntag):
		spec, f = core.BulkSpec{Op: core.TaskLabelRemove}, formField{label: "Label"}
	case key.Matches(k, keyAssign):
		spec, f = core.BulkSpec{Op: core.TaskAssign}, formField{label: "Assignee (blank: unassign)"}
	case key.Matches(k, keyPrio):
		spec, f = core.BulkSpec{Op: core.TaskPriority}, formField{label: "Priority 0-4"}
	case key.Matches(k, keyEdit):
		spec, f = core.BulkSpec{Op: core.TaskUpdate}, formField{label: "Status: " + strings.Join(core.TaskStatuses, ", ")}
	case key.Matches(k, keyClose):
		spec = core.BulkSpec{Op: core.TaskClose}
	case key.Matches(k, keyBlock, keyUnblock, keyComment):
		s.notice = "blockers and comments change one task at a time: clear the marks (U) first"
		return nil, true
	default:
		return nil, false
	}
	switch shown, _ := s.markCount(); {
	case s.applier == nil:
		s.notice = "writes are not available: no task provider can write"
		return nil, true
	case shown == 0:
		s.notice = "none of the marked tasks is listed now: clear the filter or the marks (U)"
		return nil, true
	}
	s.mk.spec, s.op, s.target = spec, spec.Op, core.Task{}
	if spec.Op == core.TaskClose {
		s.planBulk(spec, false)
		return nil, true
	}
	s.fields, s.fi, s.formErr, s.mode, s.mk.form = []formField{f}, 0, "", modeForm, true
	return nil, true
}

// submitBulk turns the one-field form into a batch spec and plans it.
func (s *tasksSection) submitBulk() tea.Cmd {
	v := strings.TrimSpace(s.fields[0].value)
	spec := s.mk.spec
	switch spec.Op {
	case core.TaskUpdate:
		spec.Status = v
	case core.TaskPriority:
		n, err := strconv.Atoi(v)
		if err != nil || strconv.Itoa(n) != v {
			s.formErr = "priority must be a number 0-4"
			return nil
		}
		spec.Priority = &n
	case core.TaskLabelAdd, core.TaskLabelRemove:
		spec.Label = v
	case core.TaskAssign:
		spec.Assignee = &v
	}
	s.planBulk(spec, true)
	return nil
}

// planBulk plans the batch for the marked tasks that are listed and opens the confirm screen, or says why
// not (in the form when there is one, else in the notice).
func (s *tasksSection) planBulk(spec core.BulkSpec, form bool) {
	fail := func(msg string) {
		if form {
			s.formErr = msg
			return
		}
		s.notice, s.mode = msg, modeList
	}
	if s.reader == nil {
		s.mode, s.notice, s.mk.form = modeList, "could not verify the current status; nothing was written", false
		return
	}
	plan, err := core.PlanBulk(s.writer, spec, s.markedRows())
	if err != nil {
		fail(core.Sanitize(err.Error()))
		return
	}
	s.mk.plan, s.mk.spec, s.mk.off, s.mk.form = plan, spec, 0, false
	s.formErr, s.mode = "", modeBulk
}

// bulkConfirmLines is the confirm screen: what the batch does, what was left out, then every task with
// its exact command, and what the keys do.
func (s *tasksSection) bulkConfirmLines(inner int) []string {
	th, p := s.th, s.mk.plan
	out := wrap(th.Header.Render(p.Spec.Summary()+" "+core.Plural(len(p.Items), "task")), inner)
	for _, k := range p.Skipped {
		out = append(out, hang("  ", th.Text(KindMuted, "left out: "+san(k.ID)+" ("+san(k.Reason)+")"), inner)...)
	}
	out = append(out, "")
	out = append(out, wrap(th.Text(KindMuted, "each task is read again right before its own write; one that changed is skipped"), inner)...)
	out = append(out, "")
	for _, it := range p.Items {
		out = append(out, hang("  ", san(core.BulkItemText(it.Change)), inner)...)
		out = append(out, hang("    ", th.Text(KindMuted, "command: ")+san(core.DisplayArgv(it.Argv)), inner)...)
	}
	all := fmt.Sprintf("all %d", len(p.Items))
	if len(p.Items) == 1 {
		all = "it"
	}
	return append(out, "", th.Text(KindMuted, "y applies "+all+" · n or esc cancels"))
}

// bulkResultLines is the screen after a batch: one line per task, and why for each that was not applied.
func (s *tasksSection) bulkResultLines(inner int) []string {
	th, r := s.th, s.mk.rep
	out := wrap(th.Header.Render(r.Summary()), inner)
	out = append(out, "")
	for _, it := range r.Items {
		kind := KindWarn
		switch it.Outcome {
		case core.BulkApplied:
			kind = KindOK
		case core.BulkFailed:
			kind = KindFail
		}
		line := th.Text(kind, fmt.Sprintf("%-8s", string(it.Outcome))) + " " + san(it.ID)
		if t := s.mk.titles[it.ID]; t != "" {
			line += "  " + san(t)
		}
		out = append(out, hang("", line, inner)...)
		if it.Detail != "" {
			out = append(out, hang("    ", th.Text(KindMuted, san(it.Detail)), inner)...)
		}
	}
	return append(out, "", th.Text(KindMuted, "batch "+san(r.BatchID)+" · esc closes"))
}

// bulkRows is how many body lines the confirm and result screens show at this height.
func (s *tasksSection) bulkRows() int {
	if s.h <= 0 {
		return 1 << 20
	}
	return max(s.h-panelChromeH, 1)
}

// bulkLines are the lines of the screen now showing.
func (s *tasksSection) bulkLines() []string {
	if s.mode == modeBulkDone {
		return s.bulkResultLines(innerW(s.width()))
	}
	return s.bulkConfirmLines(innerW(s.width()))
}

// bulkAtEnd is whether the whole of the screen has been shown: the last line is in view. A batch cannot
// be applied before that, so every task and command has been on screen.
func (s *tasksSection) bulkAtEnd() bool {
	n, rows := len(s.bulkLines()), s.bulkRows()
	return n <= rows || s.mk.off >= n-rows
}

// bulkScroll handles the scroll keys of the batch screens.
func (s *tasksSection) bulkScroll(k tea.KeyMsg) {
	n, rows := len(s.bulkLines()), s.bulkRows()
	maxOff := max(n-rows, 0)
	switch k.String() {
	case "up", "k":
		s.mk.off--
	case "down", "j":
		s.mk.off++
	case "pgup":
		s.mk.off -= max(rows-2, 1)
	case "pgdown":
		s.mk.off += max(rows-2, 1)
	case "home", "g":
		s.mk.off = 0
	case "end", "G":
		s.mk.off = maxOff
	}
	s.mk.off = max(0, min(s.mk.off, maxOff))
}

// bulkKeyMode handles keys on the confirm and result screens.
func (s *tasksSection) bulkKeyMode(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if s.mode == modeBulkDone {
		if key.Matches(k, keyBack, keyNo, keyEnter) {
			s.mode, s.notice = modeList, s.mk.rep.Summary()
			s.mk.plan, s.mk.rep, s.mk.titles = core.BulkPlan{}, core.BulkReport{}, nil
			return s, nil
		}
		s.bulkScroll(k)
		return s, nil
	}
	switch {
	case key.Matches(k, keyYes):
		if !s.bulkAtEnd() { // nothing is applied that has not been on screen
			return s, nil
		}
		s.mode = modeBulkBusy
		return s, s.applyBulk(s.mk.plan.Confirm())
	case key.Matches(k, keyNo):
		s.mode, s.notice = modeList, "cancelled: nothing was changed"
		s.mk.plan = core.BulkPlan{}
	default:
		s.bulkScroll(k)
	}
	return s, nil
}

// applyBulk runs the batch: each task is read again and checked right before its own write.
func (s *tasksSection) applyBulk(p core.BulkPlan) tea.Cmd {
	ap, rd := s.applier, s.reader
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), core.BulkTotalTimeout)
		defer cancel()
		rep, err := ap.ApplyBulk(ctx, p, rd)
		return bulkAppliedMsg{rep: rep, err: err}
	}
}

// bulkApplied handles the answer: the tasks that were not applied stay marked, so the same batch can be
// tried again after a look at why.
func (s *tasksSection) bulkApplied(m bulkAppliedMsg) tea.Cmd {
	s.list.slowUntil = time.Time{}
	if m.err != nil {
		s.mode, s.notice = modeList, core.DescribeWriteError(m.err)
		return s.fetchTasks()
	}
	s.mk.titles = map[string]string{}
	for _, it := range s.mk.plan.Items {
		s.mk.titles[it.Change.ID] = it.Change.Before.Title
	}
	for _, it := range m.rep.Items {
		if it.Outcome == core.BulkApplied {
			delete(s.mk.set, it.ID)
		}
	}
	s.mk.rep, s.mk.off, s.mode = m.rep, 0, modeBulkDone
	s.live.own = m.rep.Applied > 0 // the snapshot that answers shows these writes: not a change by someone else
	return s.fetchTasks()
}

// bulkView is the confirm, busy and result screens.
func (s *tasksSection) bulkView() []string {
	th, w := s.th, s.width()
	if s.mode == modeBulkBusy {
		body := wrap("applying "+core.Plural(len(s.mk.plan.Items), "change")+", one task at a time…", innerW(w))
		return box(th, "Applying this batch", body, w, s.h)
	}
	title := "Confirm " + core.Plural(len(s.mk.plan.Items), "change")
	if s.mode == modeBulkDone {
		title = "Batch result"
	}
	lines := s.bulkLines()
	vis, off := scrollWindow(th, lines, s.mk.off, s.bulkRows())
	s.mk.off = off
	return panel(th, title, vis, w, len(vis)+panelChromeH, true)
}

// bulkBindings are the footer keys of the batch screens.
func (s *tasksSection) bulkBindings() []key.Binding {
	scroll := key.NewBinding(key.WithKeys("up", "k", "down", "j", "pgup", "pgdown"), key.WithHelp("↑/↓", "scroll"))
	switch s.mode {
	case modeBulkDone:
		return []key.Binding{keyBack, scroll}
	case modeBulkBusy:
		return nil
	}
	if s.bulkAtEnd() { // only y and n at the end, so a narrow footer keeps q quit
		return []key.Binding{keyYes, keyNo}
	}
	return []key.Binding{scroll, keyNo}
}

// markBindings are the mark keys the list view offers.
func (s *tasksSection) markBindings() []key.Binding {
	bs := []key.Binding{keyMark, keyMarkRange, keyMarkFlip}
	if s.markOn() {
		bs = append(bs, keyUnmark)
	}
	return bs
}

// bulkFormTitle names a batch form: what it does and to how many tasks.
func (s *tasksSection) bulkFormTitle() string {
	shown, _ := s.markCount()
	verb := map[core.TaskOp]string{core.TaskLabelAdd: "Add a label to", core.TaskLabelRemove: "Remove a label from", core.TaskAssign: "Assign",
		core.TaskPriority: "Set the priority of", core.TaskUpdate: "Set the status of"}[s.mk.spec.Op]
	return verb + " " + core.Plural(shown, "marked task")
}

// marksTitle is the list title's marks count, empty without marks.
func (s *tasksSection) marksTitle() string {
	if !s.markOn() {
		return ""
	}
	shown, hidden := s.markCount()
	out := fmt.Sprintf("%d marked", shown)
	if hidden > 0 {
		out += fmt.Sprintf(" (+%d not listed)", hidden)
	}
	return out
}
