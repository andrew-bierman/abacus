package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// notConnectedSection is what a domain with no usable provider shows: the state, why, and how
// to connect. Board and Tasks also offer the full-view handoff on `o`.
type notConnectedSection struct {
	h        core.Header
	fullView func(dir string) tea.Cmd
	notice   string
	linesSection
}

func newNotConnectedSection(h core.Header, fullView func(string) tea.Cmd) *notConnectedSection {
	return &notConnectedSection{h: h, fullView: fullView, linesSection: linesSection{th: DefaultTheme()}}
}

func (s *notConnectedSection) Init() tea.Cmd { return nil }

func (s *notConnectedSection) Bindings() []key.Binding {
	out := moveBindings()
	if s.fullView != nil {
		out = append(out, keyFull)
	}
	return out
}

func (s *notConnectedSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.resize(m)
	case fullViewDoneMsg:
		if m.err != nil {
			s.notice = core.Sanitize(m.err.Error())
		}
	case tea.KeyMsg:
		s.notice = ""
		if s.scrollKey(m, len(s.lines())) {
			return s, nil
		}
		if key.Matches(m, keyFull) && s.fullView != nil {
			return s, s.fullView(core.FullViewDir(""))
		}
	}
	return s, nil
}

func (s *notConnectedSection) lines() []string {
	th, w := s.th, s.width()
	out := []string{th.Header.Render(san(s.h.Title)) + " " + th.Badge(KindSkip, "not connected")}
	if s.h.Reason != "" {
		out = append(out, "")
		out = append(out, wrap(th.Text(KindWarn, san(s.h.Reason)), w)...)
	}
	out = append(out, "")
	out = append(out, wrap(th.Text(KindAccent, "how to connect: ")+san(s.h.HowToConnect), w)...)
	if s.notice != "" {
		out = append(out, "", th.Text(KindWarn, s.notice))
	}
	return out
}

func (s *notConnectedSection) View() string { return s.render(s.lines()) }

// boardInspector is implemented by a Board provider that can hand the terminal to its local
// web inspector (a command, run only from an explicit keypress).
type boardInspector interface{ InspectCmd() *exec.Cmd }

// hash marks a message id, as the board prints it.
const hash = "\u0023"

const boardPollEvery = 30 * time.Second

type boardMsg struct {
	report core.BoardReport
	err    error
}
type boardTickMsg struct{ gen int }
type boardInspectDoneMsg struct{ err error }

// boardSection shows the Board domain: channels, recent threads and open tasks, with the
// selected one in full below. Reads are bounded by the provider; polling runs only while the
// tab is visible, one read at a time.
type boardSection struct {
	p       core.BoardProvider
	inspect func() *exec.Cmd // nil: the provider has no inspector

	report   core.BoardReport
	err      error
	loaded   bool
	busy     bool
	active   bool
	gen      int // bumped on every (de)activation: a tick from an older visit is dropped
	fetches  int // reads started (a test seam: a poll never starts one while another is in flight)
	cursor   int
	selected string // "t:<id>" or "k:<id>", so a refresh cannot move the cursor to another row
	top      int
	drilled  bool // stacked layout only: showing the detail panel
	notice   string
	th       Theme
	geom
}

func newBoardSection(p core.BoardProvider) *boardSection {
	s := &boardSection{p: p, th: DefaultTheme()}
	if bi, ok := p.(boardInspector); ok {
		s.inspect = bi.InspectCmd
	}
	return s
}

func (s *boardSection) SetTheme(t Theme) { s.th = t }
func (s *boardSection) Capturing() bool  { return false }

// canInspect: the browser handoff is offered only for a loaded, healthy board.
func (s *boardSection) canInspect() bool { return s.inspect != nil && s.loaded && s.err == nil }

func (s *boardSection) Bindings() []key.Binding {
	enter, back := keyEnter, keyBack
	enter.SetEnabled(isStacked(s.width()) && !s.drilled && len(s.rows()) > 0)
	back.SetEnabled(s.drilled)
	out := append(moveBindings(), enter, back, keyReload)
	if s.canInspect() {
		out = append(out, keyBrowser)
	}
	return out
}

func (s *boardSection) fetch() tea.Cmd {
	s.busy = true
	s.fetches++
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
		defer cancel()
		r, err := s.p.Board(ctx)
		return boardMsg{r, err}
	}
}

func (s *boardSection) tick() tea.Cmd {
	gen := s.gen
	return tea.Tick(boardPollEvery, func(time.Time) tea.Msg { return boardTickMsg{gen} })
}

func (s *boardSection) Init() tea.Cmd { return nil }

// activate loads on the first visit and then polls while the tab stays visible.
func (s *boardSection) activate() tea.Cmd {
	s.active = true
	s.gen++
	if s.busy {
		return s.tick()
	}
	return tea.Batch(s.fetch(), s.tick())
}

func (s *boardSection) deactivate() { s.active = false; s.gen++ }

// row is one selectable line: a thread or a task.
type boardRow struct {
	id   string
	body []string
}

func (s *boardSection) rows() []boardRow {
	var out []boardRow
	for _, t := range s.report.Threads {
		out = append(out, boardRow{id: "t:" + t.Root.ID, body: threadBody(t)})
	}
	for _, t := range s.report.Tasks {
		out = append(out, boardRow{id: "k:" + t.ID, body: taskBody(t)})
	}
	return out
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

func when(iso string) string {
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.UTC().Format("Jan 2 15:04")
	}
	return ""
}

// textLines splits stored message text into screen lines, each sanitized: the data keeps its
// line breaks, only the renderer flattens or wraps.
func textLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, san(l))
	}
	return out
}

func threadBody(t core.BoardThread) []string {
	out := []string{fmt.Sprintf(hash+"%s in %s by %s %s", san(t.Root.ID), orDash(t.Root.Channel), orDash(t.Root.Author), when(t.Root.At))}
	out = append(out, textLines(t.Root.Text)...)
	if t.Replies == 0 {
		return append(out, "no replies")
	}
	out = append(out, fmt.Sprintf("%d replies, latest:", t.Replies))
	if t.Last != nil {
		out = append(out, fmt.Sprintf(hash+"%s by %s %s", san(t.Last.ID), orDash(t.Last.Author), when(t.Last.At)))
		out = append(out, textLines(t.Last.Text)...)
	}
	return out
}

func taskBody(t core.BoardTask) []string {
	out := []string{fmt.Sprintf("task #%s in %s: %s", san(t.ID), orDash(t.Channel), orDash(t.State))}
	if t.Claimant != "" {
		out = append(out, "held by "+san(t.Claimant)+" until "+orDash(when(t.Until)))
	}
	return append(out, textLines(t.Text)...)
}

func (s *boardSection) reanchor() {
	rows := s.rows()
	for i, r := range rows {
		if s.selected != "" && r.id == s.selected {
			s.cursor = i
			return
		}
	}
	s.cursor = min(s.cursor, max(0, len(rows)-1))
	if s.cursor < len(rows) {
		s.selected = rows[s.cursor].id
	}
}

func (s *boardSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case boardMsg:
		s.report, s.err, s.loaded, s.busy = m.report, m.err, true, false
		s.reanchor()
		s.drilled = s.drilled && len(s.rows()) > 0
	case boardTickMsg:
		if !s.active || m.gen != s.gen { // not visible, or a tick of an earlier visit
			return s, nil
		}
		if s.busy {
			return s, s.tick()
		}
		return s, tea.Batch(s.fetch(), s.tick())
	case boardInspectDoneMsg:
		if m.err != nil {
			s.notice = "the inspector stopped: " + core.Sanitize(m.err.Error())
		}
	case tea.WindowSizeMsg:
		s.resize(m)
		s.drilled = s.drilled && isStacked(s.width())
	case tea.KeyMsg:
		s.notice = ""
		rows := s.rows()
		switch {
		case key.Matches(m, keyUp):
			s.cursor = max(0, s.cursor-1)
		case key.Matches(m, keyDown):
			s.cursor = min(max(0, len(rows)-1), s.cursor+1)
		case key.Matches(m, keyEnter):
			s.drilled = isStacked(s.width()) && len(rows) > 0
		case key.Matches(m, keyBack):
			s.drilled = false
		case key.Matches(m, keyReload):
			if !s.busy {
				return s, s.fetch()
			}
		case key.Matches(m, keyBrowser) && s.canInspect():
			return s, tea.ExecProcess(s.inspect(), func(err error) tea.Msg { return boardInspectDoneMsg{err} })
		}
		if s.cursor < len(rows) {
			s.selected = rows[s.cursor].id
		}
	}
	return s, nil
}

func (s *boardSection) lines() ([]string, int) {
	th, w := s.th, s.width()
	switch {
	case !s.loaded:
		return []string{th.Text(KindMuted, "loading…")}, 0
	case s.err != nil:
		var nc *core.NotConnectedError
		if errors.As(s.err, &nc) { // the tool is there but no board is set up: say how to connect
			out := []string{th.Header.Render("Board") + " " + th.Badge(KindSkip, "not connected"), ""}
			out = append(out, wrap(th.Text(KindWarn, san(nc.Reason)), w)...)
			if nc.HowToConnect != nc.Reason {
				out = append(out, "")
				out = append(out, wrap(th.Text(KindAccent, "how to connect: ")+san(nc.HowToConnect), w)...)
			}
			return out, 0
		}
		out := wrap(th.Text(KindWarn, "state unavailable: "+san(s.err.Error())), w)
		return append(append(out, ""), wrap(th.Text(KindMuted, strings.ToLower(core.HowToConnectBoardDegraded[:1])+core.HowToConnectBoardDegraded[1:]), w)...), 0
	}
	return nil, 0 // connected: View draws the panels instead
}

func (s *boardSection) View() string {
	if s.loaded && s.err == nil {
		return s.connectedView()
	}
	lines, cursorLine := s.lines()
	return renderWindow(s.th, lines, cursorLine, s.geom, &s.top)
}

// threadCols and taskCols are the board tables: the id and the text always stay, the channel,
// author and claimant drop first as the panel narrows.
var (
	threadCols = table{cols: []column{{min: 8, max: 8, prio: 0}, {min: 6, max: 14, prio: 2}, {min: 6, max: 14, prio: 3}, {min: 8, prio: 1}}, gap: 2}
	taskCols   = table{cols: []column{{min: 8, max: 8, prio: 0}, {min: 5, max: 10, prio: 0}, {min: 6, max: 16, prio: 3}, {min: 8, prio: 1}}, gap: 2}
)

// connectedView is the board as a framed list (channels, threads, tasks) with the selected message
// or task in a detail panel beside it from bpWide columns, or after Enter below that.
func (s *boardSection) connectedView() string {
	th, w := s.th, s.width()
	r := s.report
	g := duoLayout(w, s.bodyH(s.notice))
	iw := g.listInner()
	rows := s.rows()

	title := titled("Board", r.Source)
	lines := wrap(r.Summarize(), iw)
	if r.Reason != "" {
		lines = append(lines, wrap(th.Text(KindWarn, san(r.Reason)), iw)...)
	}
	if r.Truncated != "" {
		lines = append(lines, wrap(th.Text(KindMuted, san(r.Truncated)), iw)...)
	}
	lines = append(lines, "", th.Section("Channels"))
	if len(r.Channels) == 0 {
		lines = append(lines, th.Text(KindMuted, "none yet: the first post makes one"))
	}
	var chans []string
	for _, c := range r.Channels {
		name := th.Tint(c.Path, san(c.Path))
		if c.Threads > 0 { // a board that gives no counts shows none
			name += th.Text(KindMuted, fmt.Sprintf(" (%d)", c.Threads))
		}
		chans = append(chans, name)
	}
	if len(chans) > 0 {
		lines = append(lines, wrap(strings.Join(chans, " · "), iw)...)
	}

	lines = append(lines, "", th.Section("Recent threads"))
	if len(r.Threads) == 0 {
		lines = append(lines, th.Text(KindMuted, "no threads yet"))
	}
	var trows, tplain [][]string
	for _, t := range r.Threads {
		tplain = append(tplain, []string{hash + san(t.Root.ID), orDash(t.Root.Channel), orDash(t.Root.Author), san(firstLine(t.Root.Text))})
		trows = append(trows, []string{hash + san(t.Root.ID), th.Tint(t.Root.Channel, orDash(t.Root.Channel)), th.Tint(t.Root.Author, orDash(t.Root.Author)), san(firstLine(t.Root.Text))})
	}
	first := len(lines)
	lines = append(lines, renderRows(th, threadCols, iw, trows, tplain, s.cursor)...)
	lines = append(lines, "", th.Section("Tasks"))
	if len(r.Tasks) == 0 {
		lines = append(lines, th.Text(KindMuted, "no open tasks"))
	}
	var krows, kplain [][]string
	for _, t := range r.Tasks {
		kplain = append(kplain, []string{hash + san(t.ID), orDash(t.State), orDash(t.Claimant), san(firstLine(t.Text))})
		krows = append(krows, []string{hash + san(t.ID), orDash(t.State), th.Tint(t.Claimant, orDash(t.Claimant)), san(firstLine(t.Text))})
	}
	// the cursor runs over threads then tasks: draw the task rows with the cursor shifted past the threads
	lines = append(lines, renderRows(th, taskCols, iw, krows, kplain, s.cursor-len(r.Threads))...)
	var cursorLine int
	if s.cursor < len(r.Threads) {
		cursorLine = first + s.cursor
	} else {
		cursorLine = len(lines) - len(krows) + (s.cursor - len(r.Threads))
	}

	detailTitle, detail := "Message", []string{th.Text(KindMuted, "nothing selected")}
	if s.cursor < len(rows) {
		if strings.HasPrefix(rows[s.cursor].id, "k:") {
			detailTitle = "Task"
		} else {
			detailTitle = "Thread"
		}
		detail = nil
		for _, b := range rows[s.cursor].body {
			detail = append(detail, wrap(b, g.detail-panelChromeW)...)
		}
	}
	list := windowLines(th, lines, cursorLine, g.inner, &s.top)
	body := g.draw(th, title, list, detailTitle, clipLines(th, detail, g.inner), s.drilled)
	if s.notice != "" {
		body = append(body, noticeLine(th, w, s.notice)...)
	}
	return joinLines(body)
}

// renderWindow draws lines in the section's height, keeping cursorLine in view (top is the scroll
// state), truncated to the width, with a "+N more" marker when clipped. Shared by the sections
// that have a cursor.
func renderWindow(th Theme, lines []string, cursorLine int, g geom, top *int) string {
	vis, newTop, hidden := windowMore(lines, cursorLine, g.h, *top)
	*top = newTop
	body := make([]string, 0, len(vis)+1)
	for _, l := range vis {
		body = append(body, trunc(l, g.width()))
	}
	if hidden > 0 && g.h >= 2 {
		body = append(body, th.moreMarker(hidden))
	}
	return strings.Join(body, "\n") + "\n"
}

// ---- Tasks ----

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
	cursor   int
	selected string // id of the selected task, so a refresh cannot move the cursor to another
	top      int
	notice   string

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
		s.reanchor()
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

func (s *tasksSection) reanchor() {
	for i, t := range s.recent() {
		if s.selected != "" && t.ID == s.selected {
			s.cursor = i
			return
		}
	}
	s.cursor = min(s.cursor, max(0, len(s.recent())-1))
	if t, ok := s.current(); ok {
		s.selected = t.ID
	}
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
		s.cursor = max(0, s.cursor-1)
		s.remember()
	case key.Matches(k, keyDown):
		s.cursor = min(max(0, len(s.recent())-1), s.cursor+1)
		s.remember()
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

func (s *tasksSection) remember() {
	if t, ok := s.current(); ok {
		s.selected = t.ID
	}
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

// taskPriorityKind is the color role of a priority: P0 and P1 call for attention, P4 is quiet, and
// the middle priorities are plain.
func taskPriorityKind(p int) (Kind, bool) {
	switch p {
	case 0:
		return KindFail, true
	case 1:
		return KindWarn, true
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
	if bar := th.stackBar(iw, taskBarSegments(c)); bar != "" {
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
