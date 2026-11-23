package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

const tasksPollEvery = 30 * time.Second

type tasksMsg struct {
	report core.TasksReport
	err    error
}
type insightsMsg struct{ report core.InsightsReport }
type tasksTickMsg struct{}

// taskVerifiedMsg is the answer to the re-read of an issue before its confirm screen opens.
type taskVerifiedMsg struct {
	id   string
	task core.Task
	err  error
}
type taskAppliedMsg struct {
	res core.TaskResult
	err error
	op  core.TaskOp
}

type taskMode int

const (
	modeList taskMode = iota
	modeForm
	modeConfirm
	modeBusy
	modeVerify // re-reading the issue before the confirm screen
)

type formField struct {
	label  string
	value  string
	edited bool // the user changed it, so it is sent even if it equals the old value
}

// tasksSection is a compact Tasks summary (counts, recent changes, graph insights) and the
// confirmed write path: n new, e edit, x close. It is deliberately not an issue browser;
// `o` hands off to the full view.
type tasksSection struct {
	prov     core.TaskProvider
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
	listNav  // ids are the task IDs; Tasks has no drill (never enter/back)

	mode        taskMode
	op          core.TaskOp
	fields      []formField
	fi          int
	target      core.Task
	plan        core.WritePlan
	formErr     string
	pending     core.TaskChange // the change waiting for the re-read
	changedNote string          // on the confirm screen: the issue changed, or could not be checked
	tasksBusy   bool            // a refresh is in flight: r is ignored
	insBusy     bool
	wantIns     bool // insights are read after the next tasks read: an empty tracker skips bv

	th Theme
	geom
}

func newTasksSection(p core.Providers, cfg *config) *tasksSection {
	s := &tasksSection{prov: p.Tasks, ins: p.Insights, insWhy: p.InsightsWhy, reader: p.Reader, writer: p.Writer, fullView: cfg.fullView, th: DefaultTheme()}
	if p.Writer != nil {
		s.applier = core.NewApplier(p.Writer, cfg.audit)
	}
	s.tick = func() tea.Cmd { return tea.Tick(tasksPollEvery, func(time.Time) tea.Msg { return tasksTickMsg{} }) }
	return s
}

func (s *tasksSection) SetTheme(t Theme) { s.th = t }
func (s *tasksSection) Capturing() bool  { return s.mode != modeList }

func (s *tasksSection) Bindings() []key.Binding {
	switch s.mode {
	case modeForm:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next/review")), keyBack}
	case modeConfirm:
		return []key.Binding{keyYes, keyNo}
	case modeVerify:
		return []key.Binding{keyBack}
	case modeBusy:
		return nil
	}
	return append(moveBindings(), keyNew, keyEdit, keyClose, keyFull, keyReload)
}

func (s *tasksSection) fetchTasks() tea.Cmd {
	s.tasksBusy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		r, err := s.prov.Tasks(ctx)
		return tasksMsg{r, err}
	}
}

// fetchInsights runs only at start and on refresh: it can run several subprocesses.
func (s *tasksSection) fetchInsights() tea.Cmd {
	s.insBusy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*fetchTimeout)
		defer cancel()
		return insightsMsg{core.ReadInsights(ctx, s.ins, s.insWhy)}
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

func (s *tasksSection) recent() []core.Task { return s.report.Recent }

func (s *tasksSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tasksMsg:
		s.report, s.err, s.loaded, s.tasksBusy = m.report, m.err, true, false
		s.reanchor(s.ids(), true) // follow: the clamped row under the cursor is selected
		if s.err == nil {
			wasEmpty := s.insights != nil && s.insights.Empty
			switch {
			case s.report.Counts.Total == 0: // nothing to analyze: not a failure, and bv is not run
				e := core.EmptyInsights()
				s.insights, s.wantIns = &e, false
			case s.wantIns || wasEmpty: // first read, a refresh, or the first task just appeared
				s.wantIns = false
				if !s.insBusy {
					return s, s.fetchInsights()
				}
			}
		}
	case insightsMsg:
		s.insights, s.insBusy = &m.report, false
	case taskVerifiedMsg:
		if s.mode != modeVerify || m.id != s.pending.ID { // cancelled, or an answer for another issue
			return s, nil
		}
		ch := s.pending
		s.changedNote = ""
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
	case tasksTickMsg:
		return s, tea.Batch(s.fetchTasks(), s.tick())
	case taskAppliedMsg:
		s.mode = modeList
		if m.err != nil {
			s.notice = core.DescribeWriteError(m.err)
		} else {
			s.notice = fmt.Sprintf("%sd %s %s", m.op, san(m.res.ID), san(m.res.Title))
		}
		return s, s.fetchTasks()
	case fullViewDoneMsg:
		if m.err != nil {
			s.notice = core.Sanitize(m.err.Error())
		}
	case tea.WindowSizeMsg:
		s.resize(m)
	case tea.KeyMsg:
		return s.key(m)
	}
	return s, nil
}

// ids is one stable id per recent task: its ID.
func (s *tasksSection) ids() []string {
	rec := s.recent()
	out := make([]string, len(rec))
	for i, t := range rec {
		out[i] = t.ID
	}
	return out
}

func (s *tasksSection) current() (core.Task, bool) {
	if s.cursor < len(s.recent()) {
		return s.recent()[s.cursor], true
	}
	return core.Task{}, false
}

func (s *tasksSection) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s.mode {
	case modeForm:
		return s.formKey(k)
	case modeConfirm:
		switch {
		case key.Matches(k, keyYes):
			// The only path to a write: an explicit y on the confirm screen.
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
	case modeBusy:
		return s, nil
	}
	s.notice = ""
	switch {
	case key.Matches(k, keyUp):
		s.up(s.ids())
	case key.Matches(k, keyDown):
		s.down(s.ids())
	case key.Matches(k, keyReload):
		if s.tasksBusy {
			return s, nil
		}
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
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
		defer cancel()
		res, err := s.applier.Apply(ctx, p)
		return taskAppliedMsg{res: res, err: err, op: op}
	}
}

func (s *tasksSection) lines() ([]string, int) {
	th, w := s.th, s.width()
	switch s.mode {
	case modeForm:
		title := map[core.TaskOp]string{core.TaskCreate: "New task", core.TaskUpdate: "Edit task " + s.target.ID}[s.op]
		out := []string{th.Header.Render(san(title)), ""}
		for i, f := range s.fields {
			caret := ""
			if i == s.fi {
				caret = "▏"
			}
			out = append(out, th.Label.Render(padTo(f.label, 26))+san(f.value)+caret)
		}
		if s.formErr != "" {
			out = append(out, "", th.Text(KindFail, san(s.formErr)))
		}
		return out, 0
	case modeVerify:
		return []string{th.Header.Render("Checking the current state of " + san(s.pending.ID) + "…"), "", th.Text(KindMuted, "esc cancels")}, 0
	case modeConfirm, modeBusy:
		out := []string{th.Header.Render("Confirm this change"), ""}
		if s.changedNote != "" {
			out = append(out, wrap(th.Text(KindWarn, san(s.changedNote)), w)...)
			out = append(out, "")
		}
		for _, l := range s.plan.Lines {
			out = append(out, wrap("  "+san(l), w)...)
		}
		out = append(out, "", th.Text(KindMuted, "exact command:"))
		out = append(out, wrap("  "+san(core.DisplayArgv(s.plan.Argv)), w)...)
		if s.mode == modeBusy {
			out = append(out, "", th.Text(KindMuted, "applying…"))
		}
		return out, 0
	}
	if !s.loaded {
		return []string{th.Text(KindMuted, "loading…")}, 0
	}
	out := wrap(th.Text(KindWarn, "state unavailable: "+san(s.err.Error())), w)
	if s.notice != "" { // a write outcome, or why a key did nothing, is never lost to a failed read
		out = append(out, "", th.Text(KindWarn, san(s.notice)))
	}
	return out, 0
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

// taskCols are the task table's columns: id, status, priority, title. The id and status stay; the
// title takes the slack and is cut last.
var taskTable = table{cols: []column{{min: 12, max: 12, prio: 0}, {min: 11, max: 11, prio: 0}, {min: 2, max: 3, prio: 1}, {min: 8, prio: 1}}, gap: 2}

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

// panelView is the Tasks list: the counts and a table of recent changes in a framed list panel and
// the graph insights in a panel of their own, beside it from bpWide columns and below it under that.
// The forms and the confirm screen are not drawn here.
func (s *tasksSection) panelView() string {
	th, w := s.th, s.width()
	h := s.bodyH(s.notice)
	g := duoLayout(w, h)
	iw := g.listInner()
	insW := max(w-panelChromeW, 1)
	if g.wide {
		insW = max(g.detail-panelChromeW, 1)
	}

	title := titled("Tasks", s.report.Source)
	c := s.report.Counts
	var lines []string
	if bar := th.stackBar(min(iw, taskBarMax), taskBarSegments(c)); bar != "" {
		lines = append(lines, bar)
	}
	lines = append(lines, wrap(fmt.Sprintf("%d tasks: %d open · %d in progress · %d blocked · %d deferred · %d closed", c.Total, c.Open, c.InProgress, c.Blocked, c.Deferred, c.Closed), iw)...)
	lines = append(lines, "ready "+th.Text(KindOK, fmt.Sprint(c.Ready)), "", th.Section("Recent changes"))
	cursorLine := len(lines)
	if len(s.recent()) == 0 {
		if c.Total == 0 {
			lines = append(lines, th.Text(KindMuted, "no tasks yet — press n to add the first one"))
		} else {
			lines = append(lines, th.Text(KindMuted, "none"))
		}
	}
	var styled, plain [][]string
	for _, t := range s.recent() {
		status, prio := orDash(t.Status), fmt.Sprintf("P%d", t.Priority)
		plain = append(plain, []string{san(t.ID), status, prio, san(t.Title)})
		if k, ok := taskStatusKind(t.Status); ok {
			status = th.Text(k, status)
		}
		if k, ok := taskPriorityKind(t.Priority); ok {
			prio = th.Text(k, prio)
		}
		styled = append(styled, []string{san(t.ID), status, prio, san(t.Title)})
	}
	lines = append(lines, renderRows(th, taskTable, iw, styled, plain, s.cursor)...)
	cursorLine += s.cursor

	sub, ins := s.insightParts(insW)
	insTitle := "Insights"
	if sub != "" {
		insTitle += " · " + sub
	}
	var body []string
	if g.wide {
		body = g.draw(th, title, windowLines(th, lines, cursorLine, g.inner, &s.top), insTitle, clipLines(th, ins, g.inner), false)
	} else {
		topRows, botRows := stackRows(h, len(lines), len(ins))
		body = stackPanels(th, w, title, windowLines(th, lines, cursorLine, topRows, &s.top), topRows, insTitle, clipLines(th, ins, botRows), botRows)
	}
	if s.notice != "" {
		body = append(body, noticeLine(th, w, s.notice)...)
	}
	return joinLines(body)
}

func (s *tasksSection) View() string {
	if s.mode == modeList && s.loaded && s.err == nil {
		return s.panelView()
	}
	lines, cursorLine := s.lines()
	return renderWindow(s.th, lines, cursorLine, s.geom, &s.top)
}
