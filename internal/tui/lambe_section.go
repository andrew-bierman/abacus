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
	"github.com/andrew-bierman/abacus/internal/lambe"
)

const (
	pollEvery    = 5 * time.Second
	fetchTimeout = 8 * time.Second
)

func tick() tea.Cmd {
	return tea.Tick(pollEvery, func(time.Time) tea.Msg { return lambeTickMsg{} })
}

type lambeViewMsg struct {
	view lambe.View
	err  error
}
type lambeTickMsg struct{}

// lambeSection renders Lambe's runs, queue and attention, read-only. All data and the
// Rex command come from the lambe core module; this file only draws and routes keys.
type lambeSection struct {
	m       *lambe.Module
	handoff func(argv []string) tea.Cmd
	th      Theme
	geom
	view   lambe.View
	err    error
	loaded bool
	readAt time.Time // when the last read arrived (global search marks a stale one)
	// listNav's selected is the identity of the chosen run (runKey), so a poll refresh that
	// reorders or removes rows cannot move the selection (and the Rex handoff) onto a different run.
	listNav
	notice string
	filter string
	typing bool
	ws     *wsLayer // the optional workspace layer; nil without one
}

func (s *lambeSection) setWorkspace(w *wsLayer) { s.ws = w }
func (s *lambeSection) layer() *wsLayer         { return s.ws }

// wsDetailVisible: the detail pane is drawn beside the list, or on its own once opened.
func (s *lambeSection) wsDetailVisible() bool { return !isStacked(s.width()) || s.drilled }

// wsSelected is the run under the cursor; its issue is the id and the title.
func (s *lambeSection) wsSelected() (core.WorkspaceItem, string, bool) {
	if rs := s.runs(); s.cursor < len(rs) {
		it, ok := runItem(rs[s.cursor])
		return it, it.ID, ok
	}
	return core.WorkspaceItem{}, "", false
}

// runItem is the workspace item of a run: its issue on the machine it carries.
func runItem(r lambe.Run) (core.WorkspaceItem, bool) {
	return itemOf(core.WorkspaceRun, r.Issue, r.Machine)
}

// wsItems are the runs to ask the workspace about.
func (s *lambeSection) wsItems() []core.WorkspaceItem {
	var items []core.WorkspaceItem
	for _, r := range s.view.Snapshot.Runs {
		if it, ok := runItem(r); ok {
			items = append(items, it)
		}
	}
	return itemList(items...)
}

func newLambeSection(m *lambe.Module, handoff func([]string) tea.Cmd) *lambeSection {
	return &lambeSection{m: m, handoff: handoff, th: DefaultTheme()}
}

func (s *lambeSection) SetTheme(t Theme) { s.th = t }
func (s *lambeSection) Capturing() bool  { return s.typing }

func (s *lambeSection) Bindings() []key.Binding {
	enter, back := s.bindings(s.width(), len(s.runs()))
	back.SetEnabled(s.drilled || s.filter != "")
	return append(moveBindings(), enter, back, keyFilter, keyOpen)
}

func (s *lambeSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		v, err := s.m.Read(ctx)
		return lambeViewMsg{view: v, err: err}
	}
}

func (s *lambeSection) refresh() tea.Cmd { return s.fetch() }

func (s *lambeSection) Init() tea.Cmd {
	if s.m.IsDemo() {
		return s.fetch()
	}
	return tea.Batch(s.fetch(), tick())
}

// runs is the filtered run list the cursor indexes.
func (s *lambeSection) runs() []lambe.Run {
	all := s.view.Snapshot.Runs
	if s.filter == "" {
		return all
	}
	needle := strings.ToLower(s.filter)
	var out []lambe.Run
	for _, r := range all {
		if strings.Contains(strings.ToLower(strings.Join([]string{r.Issue, r.State, r.Phase, r.Machine, r.Workflow}, " ")), needle) {
			out = append(out, r)
		}
	}
	return out
}

func (s *lambeSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case lambeViewMsg:
		s.view, s.err, s.loaded, s.readAt = msg.view, msg.err, true, nowFn()
		s.reanchor(s.ids(), false)
		return s, s.ws.fetch(s.wsItems())
	case workspaceMsg:
		s.ws.receive(msg)
	case handoffDoneMsg:
		s.ws.done()
		if msg.err != nil {
			s.notice = "rex: " + core.Sanitize(msg.err.Error())
		}
	case lambeTickMsg:
		return s, tea.Batch(s.fetch(), tick())
	case tea.WindowSizeMsg:
		s.resize(msg)
		s.fit(s.width())
	case tea.KeyMsg:
		s.notice = ""
		if s.ws.swallow() {
			return s, nil
		}
		if s.typing {
			s.typeKey(msg)
			return s, nil
		}
		runs := s.runs()
		switch {
		case key.Matches(msg, keyUp):
			s.up(s.ids())
		case key.Matches(msg, keyDown):
			s.down(s.ids())
		case key.Matches(msg, keyEnter):
			s.enter(s.width(), len(runs))
		case key.Matches(msg, keyBack):
			// Esc peels one layer: the detail pane first, then the filter.
			if !s.back() && s.filter != "" {
				s.filter = ""
				s.reanchor(s.ids(), false)
			}
		case key.Matches(msg, keyFilter):
			s.typing, s.drilled = true, false
		case key.Matches(msg, keyOpen):
			if s.cursor >= len(runs) {
				return s, nil
			}
			it, ok := runItem(runs[s.cursor])
			if s.ws.knows(it, ok) { // the workspace knows where this run is: its plan decides
				return s, s.ws.jump(it, ok)
			}
			if s.ws.running() { // a handoff is open or has not reported back yet
				return s, nil
			}
			machine := runs[s.cursor].Machine
			if machine == "" {
				s.notice = "no machine recorded for this run"
				return s, nil
			}
			if !core.ValidHandoffHost(machine) { // never put a provider's text in a command
				s.notice = "this run's machine name is not safe to pass on, so nothing was opened"
				return s, nil
			}
			if s.ws.isDemo() { // fixtures: show the command, never run it
				s.ws.noteCopy("Demo data: nothing is opened.", s.m.RexArgv(machine))
				return s, nil
			}
			if s.ws == nil {
				return s, s.handoff(s.m.RexArgv(machine))
			}
			return s, s.ws.start(s.m.RexArgv(machine))
		}
	}
	return s, nil
}

// typeKey edits the filter: enter keeps it, esc clears it, anything printable extends it.
func (s *lambeSection) typeKey(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEnter:
		s.typing = false
	case tea.KeyEsc:
		s.typing, s.filter = false, ""
	case tea.KeyBackspace:
		if r := []rune(s.filter); len(r) > 0 {
			s.filter = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		s.filter += string(msg.Runes)
	}
	s.reanchor(s.ids(), false)
}

func runKey(r lambe.Run) string { return r.Issue + "\x00" + r.Session }

// ids is one stable id per visible run (the filtered list the cursor indexes): its runKey.
func (s *lambeSection) ids() []string {
	runs := s.runs()
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = runKey(r)
	}
	return out
}

// stateKind is the color role of a run state; the severity itself is decided once, in core.
func stateKind(state core.RunState) Kind { return toneKind(state.Tone()) }

func (s *lambeSection) countLabel(label string, n int) string {
	return s.th.Header.Render(label) + " " + s.th.Text(KindMuted, fmt.Sprint(n))
}

// attentionLines is the Needs attention block. It is pinned below the scrolling list so a
// long run list can never push it out of reach.
func (s *lambeSection) attentionLines() []string {
	n := s.view.Snapshot.NeedsHuman
	if len(n) == 0 {
		return nil
	}
	out := []string{s.countLabel("Needs attention", len(n))}
	for _, h := range n {
		out = append(out, fmt.Sprintf("  %s  %s  %s", san(h.Issue), s.th.Text(KindFail, san(h.Reason)), san(h.Detail)))
	}
	return out
}

// listTitle is the list panel's title: the run count and, when it applies, where the data came from.
func (s *lambeSection) listTitle() string {
	title := fmt.Sprintf("Runs %d", len(s.runs()))
	if len(s.view.Snapshot.Runs) == 0 {
		title = "Queues"
	}
	if s.view.Source == lambe.SourceTracker {
		title += " · " + lambe.SourceTracker
	}
	if s.m.IsDemo() {
		title += " · demo data"
	}
	return title
}

// runCols are the run table's columns: issue, state, a stalled flag only when some visible run is
// stalled, then phase, machine, workflow and last event. A higher prio number drops first as the
// panel narrows, so the last two appear only on a wide terminal.
func runCols(withFlag bool) []column {
	cols := []column{{min: 14, max: 14, prio: 0}, {min: 12, max: 12, prio: 0}}
	if withFlag {
		cols = append(cols, column{min: 9, max: 9, prio: 1})
	}
	return append(cols,
		column{min: 8, max: 16, prio: 2}, column{min: 10, max: 20, prio: 3},
		column{min: 8, max: 16, prio: 4}, column{min: 5, max: 8, prio: 5, right: true})
}

// runStateGlyph is a shape per run state, so the mark before the state says something without color.
func runStateGlyph(state core.RunState) string {
	switch state {
	case core.RunRunning:
		return "●"
	case core.RunFailed:
		return "✕"
	case core.RunDeferred:
		return "◐"
	case core.RunPending:
		return "◌"
	case core.RunSettled, core.RunCanceled:
		return "○"
	}
	return "·"
}

// listLines is the scrolling part of the list panel (the title carries the count) and the line the
// cursor is on.
func (s *lambeSection) listLines(iw int) ([]string, int) {
	th := s.th
	runs := s.runs()
	var out []string
	if s.view.Source == lambe.SourceTracker {
		out = append(out, th.Text(KindMuted, "no live daemon, no phases or queue"))
	}
	if s.typing || s.filter != "" {
		caret := ""
		if s.typing {
			caret = "▏"
		}
		out = append(out, th.Text(KindAccent, "/"+san(s.filter)+caret)+" "+th.Text(KindMuted, fmt.Sprintf("%d/%d", len(runs), len(s.view.Snapshot.Runs))))
	}
	cursorLine := len(out)
	if len(runs) == 0 {
		msg := "no runs yet"
		if len(s.view.Snapshot.Runs) > 0 {
			msg = "no runs match — press esc to clear the filter"
		}
		out = append(out, wrap(th.Text(KindMuted, msg), iw)...)
	}
	stalled := false
	for _, r := range runs {
		stalled = stalled || r.Stalled
	}
	tb := table{cols: runCols(stalled), gap: 2}
	var styled, plain [][]string // the selected row is drawn without per-cell color
	for _, r := range runs {
		state := orDash(r.State)
		flag, plainFlag := "", ""
		if r.Stalled {
			flag, plainFlag = th.Badge(KindWarn, "stalled"), "(stalled)"
		}
		mk := func(state, flag, machine string) []string {
			cells := []string{san(r.Issue), state}
			if stalled {
				cells = append(cells, flag)
			}
			return append(cells, orDash(r.Phase), machine, orDash(r.Workflow), orDash(r.LastEventAge))
		}
		kind := stateKind(r.RunState())
		glyph := runStateGlyph(r.RunState())
		styled = append(styled, mk(th.Text(kind, glyph)+" "+th.Text(kind, state), flag, th.Tint(r.Machine, orDash(r.Machine))))
		plain = append(plain, mk(glyph+" "+state, plainFlag, orDash(r.Machine)))
	}
	if s.ws.has() {
		cells := make([]string, len(runs))
		plainCells := make([]string, len(runs))
		for i, r := range runs {
			it, ok := runItem(r)
			cells[i], plainCells[i] = s.ws.place(th, it, ok, false), s.ws.place(th, it, ok, true)
		}
		if wtb, ok := s.ws.whereCol(tb, s.width(), iw, styled, cells); ok {
			tb = wtb
			for i := range styled {
				styled[i], plain[i] = append(styled[i], cells[i]), append(plain[i], plainCells[i])
			}
		}
	}
	out = append(out, renderRows(th, tb, iw, styled, plain, s.cursor)...)
	if s.view.Source != lambe.SourceTracker {
		out = append(out, "", s.countLabel("Queue", len(s.view.Queue.Rows)))
		qt := table{cols: []column{{min: 14, max: 14, prio: 0}, {min: 9, max: 9, prio: 0}, {min: 4, prio: 1}}, gap: 2}
		var qrows [][]string
		for _, q := range s.view.Queue.Rows {
			state := th.Text(KindOK, "ready")
			if q.Blocked {
				state = th.Badge(KindWarn, "blocked")
			}
			qrows = append(qrows, []string{san(q.ID), state, san(q.Title)})
		}
		qw := qt.fitRows(max(iw-2, 1), qrows)
		for _, r := range qrows {
			out = append(out, "  "+qt.row(qw, r))
		}
	}
	return out, cursorLine + s.cursor
}

// detailLines describes the selected run.
func (s *lambeSection) detailLines() []string {
	th := s.th
	runs := s.runs()
	if s.cursor >= len(runs) {
		return nil
	}
	r := runs[s.cursor]
	head := th.Header.Render(orDash(r.Issue)) + " " + th.Badge(stateKind(r.RunState()), orDash(r.State))
	if r.Stalled {
		head += " " + th.Badge(KindWarn, "stalled")
	}
	field := func(label, v string) string { return th.Label.Render(padTo(label, 11)) + orDash(v) }
	out := []string{head, "",
		field("phase", r.Phase), field("workflow", r.Workflow), field("session", r.Session),
		field("attempt", fmt.Sprint(r.Attempt)), field("machine", r.Machine), field("actor", r.Actor),
		field("last event", r.LastEventAge)}
	it, ok := runItem(r)
	out = append(out, s.ws.detail(th, it, ok, 11)...)
	for _, h := range s.view.Snapshot.NeedsHuman {
		if h.Issue == r.Issue {
			out = append(out, "", th.Text(KindFail, "needs attention: "+san(h.Reason)), "  "+san(h.Detail))
		}
	}
	return out
}

func (s *lambeSection) View() string {
	w := s.width()
	var body []string
	switch {
	case !s.loaded:
		body = []string{s.th.Text(KindMuted, "loading…")}
	case errors.Is(s.err, lambe.ErrNoDaemon):
		// Not a failure: nothing is supervising. Say so plainly and how to start it.
		body = notConnectedBox(s.th, "Queues", lambe.NoDaemonReason, s.m.StartHint(), w, s.bodyH(s.notice))
	case s.err != nil:
		body = box(s.th, "Queues", unavailableBody(s.th, s.err.Error(), innerW(w)), w, s.bodyH(s.notice))
	default:
		if box, ok := s.ws.noteBox(s.th, w, s.h); ok {
			return joinLines(box)
		}
		g := duoLayout(w, s.bodyH(s.notice))
		scrolling, cursorLine := s.listLines(g.listInner())
		list := s.compose(scrolling, cursorLine, g.inner)
		body = g.draw(s.th, s.listTitle(), list, "Run", s.detailLines(), s.drilled)
	}
	if s.notice != "" {
		body = append(body, noticeLine(s.th, w, s.notice)...)
	}
	return joinLines(body)
}

// compose stacks the scrolling list over the pinned attention block inside h rows (h <= 0:
// unconstrained), marking clipped content with "+N more".
func (s *lambeSection) compose(scrolling []string, cursorLine, h int) []string {
	pinned := s.attentionLines()
	if h <= 0 {
		if len(pinned) > 0 {
			return append(append(scrolling, ""), pinned...)
		}
		return scrolling
	}
	a := min(len(pinned), max(1, h/3), max(h-1, 0))
	if a < len(pinned) && a >= 2 {
		pinned = append(pinned[:a-1], s.th.Text(KindMuted, fmt.Sprintf("  +%d more", len(pinned)-a+1)))
	} else {
		pinned = pinned[:a]
	}
	vis, top, hidden := windowMore(scrolling, cursorLine, h-a, s.top)
	s.top = top
	out := append([]string{}, vis...)
	if hidden > 0 && h-a >= 2 {
		out = append(out, s.th.moreMarker(hidden))
	}
	if a > 0 && len(out)+a < h {
		out = append(out, "")
	}
	return append(out, pinned...)
}
