package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// slowRetry is how long the list waits to read the whole tracker again after a read timed out.
const slowRetry = 5 * time.Minute

// The read answers carry gen, the generation of the tracker they were started for: an answer from a
// tracker the section has since switched away from is dropped (see resetForTracker).
type tasksMsg struct {
	report core.TasksReport
	err    error
	gen    int
}
type insightsMsg struct {
	report core.InsightsReport
	gen    int
}
type tasksTickMsg struct{}

// taskVerifiedMsg is the answer to the re-read of an issue before its confirm screen opens.
type taskVerifiedMsg struct {
	id   string
	task core.Task
	err  error
}

var errCannotVerify = errors.New("could not verify the current status; nothing was written")

type taskAppliedMsg struct {
	res core.TaskResult
	err error
	op  core.TaskOp
	ch  core.TaskChange
}

type taskMode int

const (
	modeList taskMode = iota
	modeForm
	modeConfirm
	modeBusy
	modeVerify // re-reading the issue before the confirm screen
	// The batch of marked tasks (tasks_marks.go): its confirm screen, the wait, and the result.
	modeBulk
	modeBulkBusy
	modeBulkDone
)

type formField struct {
	label  string
	value  string
	edited bool // the user changed it, so it is sent even if it equals the old value
}

// tasksSection is the Tasks tab: the list and detail of every task (tasks_list.go), the graph
// insights (tasks_views.go) and the confirmed write path: n new, e edit, x close. `o` hands off to
// the full view, which stays an optional extra.
type tasksSection struct {
	prov     core.TaskProvider
	src      core.TaskSource // reads the whole tracker; nil falls back to the summary's recent changes
	ins      core.InsightsProvider
	insWhy   string
	reader   core.TaskReader
	applier  *core.Applier
	writer   core.TaskWriter
	fullView func(string) tea.Cmd
	tick     func() tea.Cmd

	report   core.TasksReport
	err      error
	loaded   bool
	insights *core.InsightsReport
	notice   string
	listNav  // ids are the task IDs; below bpWide enter drills into the detail
	list     tasksList
	graph    graphState
	snapBusy bool // a snapshot read is in flight: another is not started
	// snapDirty: a read was asked for while one was in flight (a write landed), so the answer that
	// arrives may predate it and one more read follows at once.
	snapDirty bool

	mode        taskMode
	op          core.TaskOp
	fields      []formField
	fi          int
	target      core.Task
	plan        core.WritePlan
	formErr     string
	pending     core.TaskChange // the change waiting for the re-read
	changedNote string          // on the confirm screen: the issue changed, or could not be checked
	gen         int             // bumped when the tracker is switched; answers of an older one are dropped
	tasksBusy   bool            // a refresh is in flight: r is ignored
	insBusy     bool
	wantIns     bool // insights are read after the next tasks read: an empty tracker skips the analysis

	ws     *wsLayer // the optional workspace layer; nil without one
	kb     kanban   // the Kanban view's cursor and options (tasks_kanban.go)
	mk     marksState
	insNav insightsNav // the Insights view's cursor and sort (tasks_insights.go)

	// U10: live refresh, history, saved filters and the tracker switcher (tasks_live.go and friends).
	live  liveState
	hist  histState
	fil   filterUI
	rec   recallState
	sw    switchUI
	extra []tea.Cmd // commands queued by code that cannot return one

	th Theme
	geom
}

func (s *tasksSection) setWorkspace(w *wsLayer) { s.ws = w }
func (s *tasksSection) layer() *wsLayer         { return s.ws }

// wsDetailVisible: the list view's detail pane is drawn beside the list, or on its own once opened.
func (s *tasksSection) wsDetailVisible() bool {
	return s.list.cur == viewList && (!isStacked(s.width()) || s.drilled || s.list.detailFocus)
}

// wsSelected is the task under the cursor and its title, for the workspace actions.
func (s *tasksSection) wsSelected() (core.WorkspaceItem, string, bool) {
	if t, ok := s.current(); ok {
		it, ok := taskItem(t)
		return it, t.Title, ok
	}
	return core.WorkspaceItem{}, "", false
}

// taskItem is the workspace item of a task: its id.
func taskItem(t core.Task) (core.WorkspaceItem, bool) { return itemOf(core.WorkspaceTask, t.ID, "") }

// wsMaxItems bounds how many tasks the workspace layer is asked about.
const wsMaxItems = 300

// wsItems are the open tasks, whatever the list is filtered to, so a filter change never leaves a
// row without its placement.
func (s *tasksSection) wsItems() []core.WorkspaceItem {
	if s.list.view == nil {
		return nil
	}
	var items []core.WorkspaceItem
	for _, r := range s.list.view.List(taskview.ListOptions{Preset: taskview.PresetOpen}).Rows {
		if it, ok := taskItem(rowTask(r)); ok && len(items) < wsMaxItems {
			items = append(items, it)
		}
	}
	return itemList(items...)
}

// selectedItem is the workspace item under the cursor.
func (s *tasksSection) selectedItem() (core.WorkspaceItem, bool) {
	if t, ok := s.current(); ok {
		return taskItem(t)
	}
	return core.WorkspaceItem{}, false
}

func newTasksSection(p core.Providers, cfg *config) *tasksSection {
	s := &tasksSection{prov: p.Tasks, src: p.Source, list: newTasksList(), ins: p.Insights, insWhy: p.InsightsWhy, reader: p.Reader, writer: p.Writer, fullView: cfg.fullView, th: DefaultTheme()}
	if p.Writer != nil {
		s.applier = core.NewApplier(p.Writer, cfg.audit)
	}
	s.list.tree.store = cfg.treeState
	s.live = newLiveState(p.Fingerprint)
	s.hist.src = p.History
	s.fil.store, s.rec.hist = cfg.filters, cfg.history
	s.sw.sw = cfg.switcher
	if s.sw.sw == nil && p.Switcher != nil {
		s.sw.sw = core.NewSwitcher(core.ConfigStore{Path: configPath()}, cfg.recents, p.Switcher, trackerDirOf(p.Writer), cfg.audit)
	}
	s.tick = func() tea.Cmd { return tea.Tick(tasksPollEvery, func(time.Time) tea.Msg { return tasksTickMsg{} }) }
	return s
}

func (s *tasksSection) SetTheme(t Theme) { s.th = t }
func (s *tasksSection) Capturing() bool {
	return s.mode != modeList || s.list.typing || s.overlayOpen()
}

func (s *tasksSection) baseBindings() []key.Binding {
	switch s.mode {
	case modeForm:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next/review")), keyBack}
	case modeConfirm:
		if !s.confirmWhole() {
			return []key.Binding{keyNo}
		}
		return []key.Binding{keyYes, keyNo}
	case modeVerify:
		return []key.Binding{keyBack}
	case modeBulk, modeBulkBusy, modeBulkDone:
		return s.bulkBindings()
	case modeBusy:
		return nil
	}
	if s.list.typing {
		return s.listBindings()
	}
	if s.list.cur != viewList { // e, x and the jump act on the list's task, which this view does not show
		return append(s.listBindings(), keyNew, keyFull, keyReload)
	}
	it, ok := s.selectedItem()
	// the edit keys come last: a narrow footer is cut from the right, and `?` lists them all
	return append(append(s.listBindings(), keyNew, keyEdit, keyClose, keyFull, s.ws.jumpBinding(it, ok), keyReload, keyTag, keyUntag, keyAssign, keyPrio, keyBlock, keyUnblock, keyComment), s.markBindings()...)
}

func (s *tasksSection) fetchTasks() tea.Cmd {
	s.tasksBusy = true
	prov, gen := s.prov, s.gen // locals: the goroutine never reads the section
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		r, err := prov.Tasks(ctx)
		return tasksMsg{report: r, err: err, gen: gen}
	}, s.fetchSnapshot())
}

// snapshotTimeout bounds one read of the whole tracker (longer than a summary read).
const snapshotTimeout = 3 * fetchTimeout

// fetchSnapshot reads the whole tracker, never two at once; nil without a source.
func (s *tasksSection) fetchSnapshot() tea.Cmd {
	if s.src == nil {
		return nil
	}
	if s.snapBusy {
		s.snapDirty = true
		return nil
	}
	if nowFn().Before(s.list.slowUntil) { // the last read timed out: not again for a few minutes
		return nil
	}
	s.snapBusy = true
	src, fps, gen := s.src, s.live.fps, s.gen // locals: the goroutine never reads the section
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
		defer cancel()
		fp := ""
		if fps != nil { // read before the export: a write that lands during it still moves the next one
			fctx, fcancel := context.WithTimeout(ctx, fingerprintTimeout)
			fp, _ = fps.Fingerprint(fctx) // an error means no fingerprint: the export goes ahead
			fcancel()
		}
		snap, err := src.Snapshot(ctx)
		return taskSnapshotMsg{snap: snap, err: err, fp: fp, gen: gen}
	}
}

// useFallback lists the summary's recent changes while no snapshot is on its way and none has
// arrived: there is no source, or its read failed.
func (s *tasksSection) useFallback() {
	if s.loaded && s.err == nil && (s.list.view == nil || s.list.fallback) && (s.src == nil || s.list.snapErr != nil || !s.snapBusy) {
		s.setSnapshot(taskview.FromRecent(s.report.Recent, nowFn()))
	}
}

// fetchInsights runs only at start and on refresh: it can run several subprocesses.
func (s *tasksSection) fetchInsights() tea.Cmd {
	s.insBusy = true
	s.graph.insStart = nowFn()
	ins, why, gen := s.ins, s.insWhy, s.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*fetchTimeout)
		defer cancel()
		return insightsMsg{report: core.ReadInsights(ctx, ins, why), gen: gen}
	}
}

// verify re-reads the issue a change targets, before its confirm screen opens.
func (s *tasksSection) verify(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		t, err := s.reader.ReadTask(ctx, id)
		return taskVerifiedMsg{id: id, task: t, err: err}
	}
}

func (s *tasksSection) Init() tea.Cmd {
	s.wantIns = true
	return tea.Batch(s.fetchTasks(), s.tick())
}

// refresh reloads everything now (after a setup step created the tracker).
func (s *tasksSection) refresh() tea.Cmd {
	s.wantIns = true
	return s.fetchTasks()
}

func (s *tasksSection) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case histMsg:
		s.histReceive(m)
	case trackerSwitchedMsg:
		return s, s.switched(m)
	case tasksMsg:
		s.report, s.err, s.loaded, s.tasksBusy = m.report, m.err, true, false
		s.useFallback()
		wsRead := s.ws.fetch(s.wsItems())
		if s.err == nil {
			wasEmpty := s.insights != nil && s.insights.Empty
			switch {
			case s.report.Counts.Total == 0: // nothing to analyze: not a failure, and the analysis is not run
				e := core.EmptyInsights()
				s.insights, s.wantIns = &e, false
			case s.wantIns || wasEmpty: // first read, a refresh, or the first task just appeared
				s.wantIns = false
				if !s.insBusy {
					return s, tea.Batch(s.fetchInsights(), wsRead)
				}
			}
		}
		return s, wsRead
	case taskSnapshotMsg:
		s.snapBusy = false
		s.liveSnapshot(m)
		dirty := s.snapDirty
		s.snapDirty = false
		s.list.snapErr = m.err
		if m.err != nil {
			s.list.slow = errors.Is(m.err, core.ErrTimedOut) || errors.Is(m.err, taskview.ErrTooLarge)
			s.list.slowUntil = time.Time{}
			if s.list.slow {
				s.list.slowUntil = nowFn().Add(slowRetry)
			}
			s.list.behind = false
			s.useFallback()
			return s, nil
		}
		s.list.slow, s.list.slowUntil = false, time.Time{}
		s.setSnapshot(m.snap)
		s.list.behind = dirty                                                                // a write landed during this read: it may not show it, so read again now
		if s.list.cur == viewGraph && s.graphInsightsStale() && s.ins != nil && !s.insBusy { // marks must describe this snapshot
			return s, tea.Batch(s.ws.fetch(s.wsItems()), s.fetchInsights(), s.refetchSnapshotIf(dirty))
		}
		if dirty {
			return s, tea.Batch(s.ws.fetch(s.wsItems()), s.fetchSnapshot())
		}
		return s, s.ws.fetch(s.wsItems())
	case workspaceMsg:
		s.ws.receive(m)
	case handoffDoneMsg:
		if s.ws.done() && m.err != nil {
			s.ws.note = []string{"The workspace did not open: " + core.Sanitize(m.err.Error())}
		}
	case insightsMsg:
		s.insights, s.insBusy, s.graph.insAt = &m.report, false, nowFn()
	case taskVerifiedMsg:
		if s.mode != modeVerify || m.id != s.pending.ID { // cancelled, or an answer for another issue
			return s, nil
		}
		ch := s.pending
		s.changedNote = ""
		// A change planned against a status (a card move, a status edit) is refused outright when the task
		// has moved since: the board shows the current state again and the move is planned anew.
		if m.err != nil && (ch.Expect != "" || core.NeedsFresh(ch.Op)) { // a change planned against a status is never confirmed unchecked
			s.mode = modeList
			s.notice = "could not verify the current status; nothing was written"
			return s, nil
		}
		if m.err == nil && ch.Expect != "" && m.task.Status != ch.Expect {
			s.mode = modeList
			s.notice = fmt.Sprintf("%s changed since you opened it (now %s): nothing was written", san(m.id), san(m.task.Status))
			s.list.slowUntil = time.Time{}
			return s, s.fetchTasks()
		}
		if m.err == nil { // an edit that no longer fits (the field moved, the label is there) is refused, not planned anew
			if err := core.CheckFresh(ch, m.task); err != nil {
				s.mode, s.notice = modeList, core.DescribeWriteError(err)
				s.list.slowUntil = time.Time{}
				return s, s.fetchTasks()
			}
		}
		if m.err != nil {
			s.changedNote = "could not re-read the issue (" + core.Sanitize(m.err.Error()) + "): its current values are unverified"
		} else {
			var diff []string
			if m.task.Title != s.target.Title {
				diff = append(diff, "title")
			}
			if m.task.Status != s.target.Status {
				diff = append(diff, "status")
			}
			if m.task.Priority != s.target.Priority {
				diff = append(diff, "priority")
			}
			if len(diff) > 0 {
				s.changedNote = "changed since you opened it: " + strings.Join(diff, ", ") + " (the change below is against the current values)"
			}
			ch.Before = m.task
		}
		s.toConfirm(ch)
	case bulkAppliedMsg:
		return s, s.bulkApplied(m)
	case tasksTickMsg:
		return s, tea.Batch(s.livePoll(), s.tick())
	case taskAppliedMsg:
		s.mode = modeList
		if m.err != nil {
			s.notice = core.DescribeWriteError(m.err)
		} else {
			s.notice = appliedNotice(m)
		}
		s.list.slowUntil = time.Time{} // the tracker changed: look again now
		if m.err == nil {
			s.live.own = true // the snapshot that answers shows this write: not a change by someone else
		}
		return s, s.fetchTasks()
	case fullViewDoneMsg:
		if m.err != nil {
			s.notice = core.Sanitize(m.err.Error())
		}
	case tea.WindowSizeMsg:
		s.resize(m)
		if s.mode == modeBulk { // the lines are wrapped again: what was seen no longer counts
			s.mk.seen, s.mk.off = -1, 0
			s.bulkSee()
		}
		if s.list.view != nil {
			s.recompute() // the tree's branch lines are drawn to fit the width
		}
		s.fit(m.Width) // a resize drops the drill when side by side...
		if isStacked(m.Width) {
			s.list.detailFocus = false // ...and the detail focus when stacked, so j and k move the list again
		}
	case tea.KeyMsg:
		return s.key(m)
	}
	return s, nil
}

// ids is one stable id per listed task: its ID.
func (s *tasksSection) ids() []string {
	rows := s.rows()
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func (s *tasksSection) current() (core.Task, bool) {
	if s.cursor < len(s.rows()) {
		return rowTask(s.rows()[s.cursor]), true
	}
	return core.Task{}, false
}

// confirmWhole is whether the confirm screen shows the whole plan and exact command at this size.
func (s *tasksSection) confirmWhole() bool {
	return confirmFits(s.th, s.changedNote, s.plan.Lines, s.plan.Argv, s.width(), s.h)
}

func (s *tasksSection) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s.mode {
	case modeForm:
		return s.formKey(k)
	case modeConfirm:
		switch {
		case key.Matches(k, keyYes):
			// The only path to a write: an explicit y on the confirm screen, and only while that
			// screen shows the whole plan and command: a clipped one is not what was reviewed.
			if !s.confirmWhole() {
				return s, nil
			}
			s.mode = modeBusy
			return s, s.apply(s.plan.Confirm())
		case key.Matches(k, keyNo):
			s.mode, s.notice = modeList, "cancelled: nothing was changed"
		}
		return s, nil
	case modeVerify:
		if key.Matches(k, keyBack) {
			s.mode, s.notice = modeList, "cancelled: nothing was changed"
		}
		return s, nil
	case modeBulk, modeBulkDone:
		return s.bulkKeyMode(k)
	case modeBusy, modeBulkBusy:
		return s, nil
	}
	s.notice, s.mk.form = "", false
	if s.ws.swallow() {
		return s, nil
	}
	if cmd, ok := s.liveKey(k); ok {
		return s, cmd
	}
	if cmd, ok := s.listKey(k); ok {
		return s, cmd
	}
	if s.list.cur == viewList && s.markOn() && !s.inDetail() {
		if cmd, ok := s.bulkKey(k); ok {
			return s, cmd
		}
	}
	switch {
	case key.Matches(k, keyWorkspace):
		it, ok := s.selectedItem()
		return s, s.ws.jump(it, ok)
	case key.Matches(k, keyReload):
		if s.tasksBusy {
			return s, nil
		}
		s.list.slowUntil = time.Time{} // an explicit refresh always tries
		s.wantIns = true
		return s, s.fetchTasks()
	case key.Matches(k, keyFull):
		dir := ""
		if s.writer != nil {
			dir = s.writer.TrackerDir()
		}
		return s, s.fullView(core.FullViewDir(dir))
	case key.Matches(k, keyNew):
		if s.applier == nil {
			s.notice = "writes are not available: no task provider can write"
			return s, nil
		}
		s.op, s.target = core.TaskCreate, core.Task{}
		s.fields, s.fi, s.formErr, s.mode = []formField{{label: "Title"}, {label: "Description"}, {label: "Priority 0-4"}}, 0, "", modeForm
	case key.Matches(k, keyEdit):
		t, ok := s.current()
		switch {
		case s.applier == nil:
			s.notice = "writes are not available: no task provider can write"
		case !ok:
			s.notice = "no task selected"
		default:
			s.op, s.target = core.TaskUpdate, t
			s.fields = []formField{{label: "Title", value: t.Title}, {label: "Description (blank: keep)"}, {label: "Priority 0-4", value: fmt.Sprint(t.Priority)}, {label: "Status (blank: keep)"}}
			s.fi, s.formErr, s.mode = 0, "", modeForm
		}
	case key.Matches(k, keyTag, keyUntag, keyAssign, keyPrio, keyBlock, keyUnblock, keyComment):
		s.openEdit(k)
	case key.Matches(k, keyClose):
		t, ok := s.current()
		switch {
		case s.applier == nil:
			s.notice = "writes are not available: no task provider can write"
		case !ok:
			s.notice = "no task selected"
		default:
			s.op, s.target = core.TaskClose, t
			s.plan, s.formErr = core.WritePlan{}, ""
			return s, s.beginConfirm(core.TaskChange{Op: core.TaskClose, ID: t.ID, Before: t})
		}
	}
	return s, nil
}

func (s *tasksSection) formKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := &s.fields[s.fi]
	switch k.Type {
	case tea.KeyEsc:
		s.mode, s.notice = modeList, "cancelled: nothing was changed"
	case tea.KeyTab, tea.KeyDown:
		s.fi = (s.fi + 1) % len(s.fields)
	case tea.KeyShiftTab, tea.KeyUp:
		s.fi = (s.fi + len(s.fields) - 1) % len(s.fields)
	case tea.KeyBackspace:
		if r := []rune(f.value); len(r) > 0 {
			f.value, f.edited = string(r[:len(r)-1]), true
		}
	case tea.KeyEnter:
		if s.fi < len(s.fields)-1 {
			s.fi++
			return s, nil
		}
		return s, s.submitForm()
	case tea.KeyRunes, tea.KeySpace:
		f.value, f.edited = f.value+string(k.Runes), true
	}
	return s, nil
}

// submitForm turns the form into a change and moves toward the confirm screen. Nothing is
// written here: a plan only describes the change. Only fields the user edited are sent.
func (s *tasksSection) submitForm() tea.Cmd {
	if s.mk.form {
		return s.submitBulk()
	}
	if core.NeedsFresh(s.op) {
		return s.submitEdit()
	}
	v := func(i int) string { return strings.TrimSpace(s.fields[i].value) }
	ch := core.TaskChange{Op: s.op, Before: s.target}
	update := s.op == core.TaskUpdate
	if !update || s.fields[0].edited {
		if update && v(0) == "" {
			s.formErr = "title must not be empty"
			return nil
		}
		ch.Title = v(0)
	}
	if !update || s.fields[1].edited {
		ch.Description = v(1)
	}
	if p := v(2); p != "" && (!update || s.fields[2].edited) {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil || fmt.Sprint(n) != p {
			s.formErr = "priority must be a number 0-4"
			return nil
		}
		ch.Priority = &n
	}
	if update {
		ch.ID, ch.Status = s.target.ID, v(3)
		if ch.Status != "" { // a status edit is guarded like a card move: it names the status it was made against
			ch.IfStatus, ch.Expect = s.target.Status, s.target.Status
		}
	}
	return s.beginConfirm(ch)
}

// beginConfirm validates the change, then (for an existing issue, when it can be re-read)
// re-reads the issue first so the confirm screen shows the current values.
func (s *tasksSection) beginConfirm(ch core.TaskChange) tea.Cmd {
	s.changedNote = ""
	if _, err := core.PlanTaskChange(s.writer, ch); err != nil {
		s.formErr = core.Sanitize(err.Error())
		if ch.Op == core.TaskClose {
			s.notice, s.mode = s.formErr, modeList
		}
		return nil
	}
	if (ch.Expect != "" || core.NeedsFresh(ch.Op)) && s.reader == nil {
		s.notice, s.mode = "could not verify the current status; nothing was written", modeList
		return nil
	}
	if ch.Op == core.TaskCreate || s.reader == nil {
		s.toConfirm(ch)
		return nil
	}
	s.pending, s.formErr, s.mode = ch, "", modeVerify
	return s.verify(ch.ID)
}

func (s *tasksSection) toConfirm(ch core.TaskChange) {
	plan, err := core.PlanTaskChange(s.writer, ch)
	if err != nil {
		s.formErr = core.Sanitize(err.Error())
		if s.op == core.TaskClose {
			s.notice, s.mode = s.formErr, modeList
		}
		return
	}
	s.plan, s.formErr, s.mode = plan, "", modeConfirm
}

func (s *tasksSection) apply(p core.WritePlan) tea.Cmd {
	op := s.op
	recheck := taskview.RecheckDep(s.src, nowFn)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
		defer cancel()
		// close and reopen have no tracker-side guard, so the status is checked once more right before
		// the write; an aborted plan runs nothing and writes no audit line.
		if exp := p.Change.Expect; exp != "" {
			if s.reader == nil {
				return taskAppliedMsg{err: errCannotVerify, op: op}
			}
			t, err := s.reader.ReadTask(ctx, p.Change.ID)
			switch {
			case err != nil:
				return taskAppliedMsg{err: errCannotVerify, op: op}
			case t.Status != exp:
				return taskAppliedMsg{err: core.ErrChanged, op: op}
			}
		}
		if err := core.FreshDep(ctx, s.reader, p.Change, recheck); err != nil { // the edits without a tracker-side guard: read once more right before the write
			return taskAppliedMsg{err: err, op: op, ch: p.Change}
		}
		res, err := s.applier.Apply(ctx, p)
		return taskAppliedMsg{res: res, err: err, op: op, ch: p.Change}
	}
}

// modalView is the screen for every mode but the list: the form, the check of the current state,
// the confirm screen and its busy state, each one framed panel.
func (s *tasksSection) modalView() []string {
	th, w, inner := s.th, s.width(), innerW(s.width())
	switch s.mode {
	case modeForm:
		title := editTitle(s.op, s.target.ID)
		if s.mk.form {
			title = s.bulkFormTitle()
		}
		body := fieldRows(th, s.fields, s.fi, inner)
		if s.formErr != "" {
			body = append(body, "")
			body = append(body, wrap(th.Text(KindFail, san(s.formErr)), inner)...)
		}
		return box(th, san(title), body, w, s.h)
	case modeBulk, modeBulkBusy, modeBulkDone:
		return s.bulkView()
	case modeVerify:
		body := wrap("reading "+san(s.pending.ID)+" before the confirm screen", inner)
		body = append(body, "", th.Text(KindMuted, "esc cancels"))
		return box(th, "Checking the current state…", body, w, s.h)
	}
	title := "Confirm this change"
	if s.mode == modeBusy {
		title = "Applying this change"
	}
	return reviewBox(th, title, confirmBody(th, s.changedNote, s.plan.Lines, s.plan.Argv, s.mode == modeBusy, inner), w, s.h)
}

// stateView is the Tasks tab before it has anything to list: loading, or why the read failed.
func (s *tasksSection) stateView() string {
	th, w := s.th, s.width()
	if !s.loaded {
		return joinLines([]string{th.Text(KindMuted, "loading…")})
	}
	body := unavailableBody(th, s.err.Error(), innerW(w))
	if s.notice != "" { // a write outcome, or why a key did nothing, is never lost to a failed read
		body = append(body, "")
		body = append(body, wrap(th.Text(KindWarn, san(s.notice)), innerW(w))...)
	}
	return joinLines(box(th, "Tasks", body, w, s.h))
}

func (s *tasksSection) emptyTasksHint() string {
	if s.applier != nil {
		return "no tasks yet — press n to add the first one"
	}
	return "no tasks yet"
}

func (s *tasksSection) insightParts(iw int) (string, []string) {
	th := s.th
	in := s.insights
	switch {
	case in == nil && s.err != nil:
		return "", nil
	case in == nil:
		return "", []string{th.Text(KindMuted, "loading…")}
	case in.Empty:
		return "no tasks yet", wrap(th.Text(KindMuted, "next picks, blockers and the critical path appear once there are tasks"), iw)
	case in.State == core.NotConnected:
		return "not connected", wrap(san(in.HowToConnect), iw)
	case in.State == core.Degraded && in.Reason != "" && len(in.TopPicks) == 0 && len(in.Cycles) == 0 && in.TotalActionable == 0:
		return "unavailable", wrap(san(in.Reason), iw)
	}
	out := []string{fmt.Sprintf("%d actionable · %d blocked · %d cycles", in.TotalActionable, in.TotalBlocked, len(in.Cycles))}
	for _, p := range in.TopPicks {
		out = append(out, hang("next     ", san(p.ID)+"  "+san(p.Title)+th.Text(KindMuted, fmt.Sprintf("  (unblocks %d)", p.Unblocks)), iw)...)
	}
	for _, p := range in.Blockers {
		out = append(out, hang("blocker  ", san(p.ID)+"  "+san(p.Title)+th.Text(KindMuted, fmt.Sprintf("  (unblocks %d)", p.Unblocks)), iw)...)
	}
	if len(in.CriticalPath) > 0 {
		var ids []string
		for _, r := range in.CriticalPath {
			ids = append(ids, san(r.ID))
		}
		out = append(out, hang("critical path  ", strings.Join(ids, " > "), iw)...)
	} else if in.CriticalPathNote != "" {
		out = append(out, wrap(th.Text(KindMuted, san(in.CriticalPathNote)), iw)...)
	}
	for _, c := range in.Cycles {
		var ids []string
		for _, id := range c {
			ids = append(ids, san(id))
		}
		out = append(out, hang(th.Text(KindWarn, "cycle")+"  ", strings.Join(ids, " -> "), iw)...)
	}
	if in.Reason != "" {
		out = append(out, wrap(th.Text(KindWarn, san(in.Reason)), iw)...)
	}
	return san(in.Source), out
}

// taskStatusKind is the color role of a task status; a plain status (open) is not colored.
func taskStatusKind(status string) (Kind, bool) {
	switch status {
	case "blocked":
		return KindFail, true
	case "in_progress":
		return KindAccent, true
	case "deferred":
		return KindWarn, true
	case "closed":
		return KindMuted, true
	}
	return 0, false
}

// taskPriorityKind is the color role of a priority: P0 calls for attention and P4 is quiet; the rest
// are plain, so amber still means one thing (deferred).
func taskPriorityKind(p int) (Kind, bool) {
	switch p {
	case 0:
		return KindFail, true
	case 4:
		return KindMuted, true
	}
	return 0, false
}

func (s *tasksSection) View() string {
	if s.mode == modeList && s.loaded && s.err == nil {
		if box, ok := s.ws.noteBox(s.th, s.width(), s.h); ok {
			return joinLines(box)
		}
		if v, ok := s.overlayView(); ok {
			return v
		}
		if s.list.view == nil { // the snapshot is on its way
			return joinLines([]string{s.th.Text(KindMuted, "loading…")})
		}
		return s.body()
	}
	if s.mode != modeList {
		return joinLines(s.modalView())
	}
	return s.stateView()
}
