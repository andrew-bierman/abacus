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

// boardSection shows the Board domain when a provider exists (none does today).
type boardSection struct {
	p        core.BoardProvider
	fullView func(string) tea.Cmd
	report   core.BoardReport
	err      error
	loaded   bool
	notice   string
	linesSection
}

type boardMsg struct {
	report core.BoardReport
	err    error
}

func newBoardSection(p core.BoardProvider, fv func(string) tea.Cmd) *boardSection {
	return &boardSection{p: p, fullView: fv, linesSection: linesSection{th: DefaultTheme()}}
}

func (s *boardSection) Bindings() []key.Binding { return append(moveBindings(), keyFull, keyReload) }

func (s *boardSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		r, err := s.p.Board(ctx)
		return boardMsg{r, err}
	}
}

func (s *boardSection) Init() tea.Cmd { return s.fetch() }

func (s *boardSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case boardMsg:
		s.report, s.err, s.loaded = m.report, m.err, true
	case tea.WindowSizeMsg:
		s.resize(m)
	case fullViewDoneMsg:
		if m.err != nil {
			s.notice = core.Sanitize(m.err.Error())
		}
	case tea.KeyMsg:
		s.notice = ""
		switch {
		case s.scrollKey(m, len(s.lines())):
		case key.Matches(m, keyFull):
			return s, s.fullView(core.FullViewDir(""))
		case key.Matches(m, keyReload):
			return s, s.fetch()
		}
	}
	return s, nil
}

func (s *boardSection) lines() []string {
	th := s.th
	switch {
	case !s.loaded:
		return []string{th.Text(KindMuted, "loading…")}
	case s.err != nil:
		return wrap(th.Text(KindWarn, "state unavailable: "+san(s.err.Error())), s.width())
	}
	head := th.Header.Render("Board")
	if src := s.report.Source; src != "" && src != core.SourceLive {
		head += " " + th.Badge(KindWarn, san(src))
	}
	out := []string{head}
	for _, c := range s.report.Columns {
		out = append(out, "", th.Header.Render(san(c.Name))+" "+th.Text(KindMuted, fmt.Sprint(len(c.Cards))))
		for _, k := range c.Cards {
			out = append(out, "  "+san(k.ID)+"  "+san(k.Title))
		}
	}
	if s.notice != "" {
		out = append(out, "", th.Text(KindWarn, s.notice))
	}
	return out
}

func (s *boardSection) View() string { return s.render(s.lines()) }

// ---- Tasks ----

const tasksPollEvery = 30 * time.Second

type tasksMsg struct {
	report core.TasksReport
	err    error
}
type insightsMsg struct{ report core.InsightsReport }
type tasksTickMsg struct{}
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
)

type formField struct {
	label string
	value string
}

// tasksSection is a compact Tasks summary (counts, recent changes, graph insights) and the
// confirmed write path: n new, e edit, x close. It is deliberately not an issue browser;
// `o` hands off to the full view.
type tasksSection struct {
	prov     core.TaskProvider
	ins      core.InsightsProvider
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

	mode    taskMode
	op      core.TaskOp
	fields  []formField
	fi      int
	target  core.Task
	plan    core.WritePlan
	formErr string

	th Theme
	geom
}

func newTasksSection(p core.Providers, cfg *config) *tasksSection {
	s := &tasksSection{prov: p.Tasks, ins: p.Insights, writer: p.Writer, fullView: cfg.fullView, th: DefaultTheme()}
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
	case modeBusy:
		return nil
	}
	return append(moveBindings(), keyNew, keyEdit, keyClose, keyFull, keyReload)
}

func (s *tasksSection) fetchTasks() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		r, err := s.prov.Tasks(ctx)
		return tasksMsg{r, err}
	}
}

// fetchInsights runs only at start and on refresh: it can run several subprocesses.
func (s *tasksSection) fetchInsights() tea.Cmd {
	return func() tea.Msg {
		h := core.Header{Domain: core.DomainTasks, Title: "Insights"}
		if s.ins == nil {
			h.State, h.HowToConnect = core.NotConnected, core.HowToConnectInsights
			return insightsMsg{core.InsightsReport{Header: h}}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*fetchTimeout)
		defer cancel()
		r, err := s.ins.Insights(ctx)
		if err != nil {
			h.State, h.Reason, h.HowToConnect = core.Degraded, core.Sanitize(err.Error()), core.HowToConnectInsights
			return insightsMsg{core.InsightsReport{Header: h}}
		}
		r.State = core.Connected
		if r.Reason != "" {
			r.State = core.Degraded
		}
		return insightsMsg{r}
	}
}

func (s *tasksSection) Init() tea.Cmd {
	return tea.Batch(s.fetchTasks(), s.fetchInsights(), s.tick())
}

func (s *tasksSection) recent() []core.Task { return s.report.Recent }

func (s *tasksSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tasksMsg:
		s.report, s.err, s.loaded = m.report, m.err, true
		s.reanchor()
	case insightsMsg:
		s.insights = &m.report
	case tasksTickMsg:
		return s, tea.Batch(s.fetchTasks(), s.tick())
	case taskAppliedMsg:
		s.mode = modeList
		if m.err != nil {
			s.notice = "not applied: " + core.Sanitize(m.err.Error())
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
		return s, tea.Batch(s.fetchTasks(), s.fetchInsights())
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
		s.fields, s.fi, s.formErr, s.mode = []formField{{"Title", ""}, {"Description", ""}, {"Priority 0-4", ""}}, 0, "", modeForm
	case key.Matches(k, keyEdit):
		t, ok := s.current()
		switch {
		case s.applier == nil:
			s.notice = "writes are not available: no task provider can write"
		case !ok:
			s.notice = "no task selected"
		default:
			s.op, s.target = core.TaskUpdate, t
			s.fields = []formField{{"Title", t.Title}, {"Description (blank: keep)", ""}, {"Priority 0-4", fmt.Sprint(t.Priority)}, {"Status (blank: keep)", ""}}
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
			s.toConfirm(core.TaskChange{Op: core.TaskClose, ID: t.ID, Before: t})
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
			f.value = string(r[:len(r)-1])
		}
	case tea.KeyEnter:
		if s.fi < len(s.fields)-1 {
			s.fi++
			return s, nil
		}
		s.submitForm()
	case tea.KeyRunes, tea.KeySpace:
		f.value += string(k.Runes)
	}
	return s, nil
}

// submitForm turns the form into a change and moves to the confirm screen. Nothing is
// written here: a plan only describes the change.
func (s *tasksSection) submitForm() {
	v := func(i int) string { return strings.TrimSpace(s.fields[i].value) }
	ch := core.TaskChange{Op: s.op, Title: v(0), Description: v(1), Before: s.target}
	if p := v(2); p != "" {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil || fmt.Sprint(n) != p {
			s.formErr = "priority must be a number 0-4"
			return
		}
		if s.op != core.TaskUpdate || n != s.target.Priority {
			ch.Priority = &n
		}
	}
	if s.op == core.TaskUpdate {
		ch.ID, ch.Status = s.target.ID, v(3)
		if ch.Title == s.target.Title {
			ch.Title = ""
		}
	}
	s.toConfirm(ch)
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
	case modeConfirm, modeBusy:
		out := []string{th.Header.Render("Confirm this change"), ""}
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
	var out []string
	if s.err != nil {
		out = wrap(th.Text(KindWarn, "state unavailable: "+san(s.err.Error())), w)
	} else {
		head := th.Header.Render("Tasks")
		if src := s.report.Source; src != "" && src != core.SourceLive {
			head += " " + th.Badge(KindWarn, san(src))
		}
		c := s.report.Counts
		out = append(out, head,
			fmt.Sprintf("%d tasks: %d open · %d in progress · %d blocked · %d deferred · %d closed", c.Total, c.Open, c.InProgress, c.Blocked, c.Deferred, c.Closed),
			"ready "+th.Text(KindOK, fmt.Sprint(c.Ready)), "")
	}
	cursorLine := len(out)
	if s.err == nil {
		out = append(out, th.Header.Render("Recent changes"))
		cursorLine = len(out)
		if len(s.recent()) == 0 {
			out = append(out, th.Text(KindMuted, "  none"))
		}
		for i, t := range s.recent() {
			row := fmt.Sprintf("%s  %s  P%d  %s", padTo(trunc(san(t.ID), 12), 12), orDash(t.Status), t.Priority, san(t.Title))
			if i == s.cursor {
				out = append(out, th.Selected.Render(padTo(trunc("▸ "+row, w), w)))
			} else {
				out = append(out, "  "+row)
			}
		}
	}
	out = append(out, "")
	out = append(out, s.insightLines(w)...)
	if s.notice != "" {
		out = append(out, "", th.Text(KindWarn, san(s.notice)))
	}
	return out, cursorLine + s.cursor
}

func (s *tasksSection) insightLines(w int) []string {
	th := s.th
	in := s.insights
	head := th.Header.Render("Insights")
	switch {
	case in == nil:
		return []string{head, th.Text(KindMuted, "  loading…")}
	case in.State == core.NotConnected:
		return append([]string{head + " " + th.Badge(KindSkip, "not connected")}, wrap(san(in.HowToConnect), w)...)
	case in.State == core.Degraded && in.Reason != "" && len(in.TopPicks) == 0 && len(in.Cycles) == 0 && in.TotalActionable == 0:
		return append([]string{head + " " + th.Badge(KindWarn, "unavailable")}, wrap(san(in.Reason), w)...)
	}
	out := []string{head + " " + th.Badge(KindMuted, san(in.Source))}
	out = append(out, fmt.Sprintf("  %d actionable · %d blocked · %d cycles", in.TotalActionable, in.TotalBlocked, len(in.Cycles)))
	for _, p := range in.TopPicks {
		out = append(out, "  next     "+san(p.ID)+"  "+san(p.Title)+th.Text(KindMuted, fmt.Sprintf("  (unblocks %d)", p.Unblocks)))
	}
	for _, p := range in.Blockers {
		out = append(out, "  blocker  "+san(p.ID)+"  "+san(p.Title)+th.Text(KindMuted, fmt.Sprintf("  (unblocks %d)", p.Unblocks)))
	}
	if len(in.CriticalPath) > 0 {
		var ids []string
		for _, r := range in.CriticalPath {
			ids = append(ids, san(r.ID))
		}
		out = append(out, "  critical path  "+strings.Join(ids, " > "))
	} else if in.CriticalPathNote != "" {
		out = append(out, "  "+th.Text(KindMuted, san(in.CriticalPathNote)))
	}
	for _, c := range in.Cycles {
		var ids []string
		for _, id := range c {
			ids = append(ids, san(id))
		}
		out = append(out, "  "+th.Text(KindWarn, "cycle")+"  "+strings.Join(ids, " -> "))
	}
	if in.Reason != "" {
		out = append(out, "  "+th.Text(KindWarn, san(in.Reason)))
	}
	return out
}

func (s *tasksSection) View() string {
	lines, cursorLine := s.lines()
	h := s.h
	var body []string
	vis, top, hidden := windowMore(lines, cursorLine, h, s.top)
	s.top = top
	for _, l := range vis {
		body = append(body, trunc(l, s.width()))
	}
	if hidden > 0 && h >= 2 {
		body = append(body, s.th.moreMarker(hidden))
	}
	return strings.Join(body, "\n") + "\n"
}
