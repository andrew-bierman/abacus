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

	report  core.BoardReport
	err     error
	loaded  bool
	readAt  time.Time // when the last read arrived (global search marks a stale one)
	busy    bool
	active  bool
	gen     int // bumped on every (de)activation: a tick from an older visit is dropped
	fetches int // reads started (a test seam: a poll never starts one while another is in flight)
	notice  string
	th      Theme
	geom
	listNav // ids are "t:<id>" (threads) then "k:<id>" (tasks), cursor order
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
	enter, back := s.bindings(s.width(), len(s.rows()))
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

// boardRow is one selectable line: a thread or a task. Its detail body is built only when asked
// for, so a render builds one body (the selected row's), not one per row.
type boardRow struct {
	id   string
	body func() []string
}

func (s *boardSection) rows() []boardRow {
	var out []boardRow
	for _, t := range s.report.Threads {
		out = append(out, boardRow{id: "t:" + t.Root.ID, body: func() []string { return threadBody(t) }})
	}
	for _, t := range s.report.Tasks {
		out = append(out, boardRow{id: "k:" + t.ID, body: func() []string { return taskBody(t) }})
	}
	return out
}

// ids is one stable id per row, in cursor order.
func (s *boardSection) ids() []string {
	out := make([]string, 0, len(s.report.Threads)+len(s.report.Tasks))
	for _, t := range s.report.Threads {
		out = append(out, "t:"+t.Root.ID)
	}
	for _, t := range s.report.Tasks {
		out = append(out, "k:"+t.ID)
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

func (s *boardSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case boardMsg:
		s.report, s.err, s.loaded, s.busy, s.readAt = m.report, m.err, true, false, nowFn()
		s.reanchor(s.ids(), true) // follow: the clamped row under the cursor is selected
		s.settle(len(s.report.Threads) + len(s.report.Tasks))
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
		s.fit(s.width())
	case tea.KeyMsg:
		s.notice = ""
		ids := s.ids()
		switch {
		case key.Matches(m, keyUp):
			s.up(ids)
		case key.Matches(m, keyDown):
			s.down(ids)
		case key.Matches(m, keyEnter):
			s.enter(s.width(), len(ids))
		case key.Matches(m, keyBack):
			s.back()
		case key.Matches(m, keyReload):
			if !s.busy {
				return s, s.fetch()
			}
		case key.Matches(m, keyBrowser) && s.canInspect():
			return s, tea.ExecProcess(s.inspect(), func(err error) tea.Msg { return boardInspectDoneMsg{err} })
		}
		s.remember(ids)
	}
	return s, nil
}

// stateLines is the Board before it can list anything, as one framed panel: loading, not connected
// (the tool is there but no board is set up), or why the read failed.
func (s *boardSection) stateView() string {
	th, w := s.th, s.width()
	inner := innerW(w)
	if !s.loaded {
		return joinLines([]string{th.Text(KindMuted, "loading…")})
	}
	var nc *core.NotConnectedError
	if errors.As(s.err, &nc) { // the tool is there but no board is set up: say how to connect
		how := nc.HowToConnect
		if how == nc.Reason {
			how = ""
		}
		return joinLines(notConnectedBox(th, "Board", nc.Reason, how, w, s.h))
	}
	body := unavailableBody(th, s.err.Error(), inner)
	body = append(body, "")
	body = append(body, wrap(th.Text(KindMuted, strings.ToLower(core.HowToConnectBoardDegraded[:1])+core.HowToConnectBoardDegraded[1:]), inner)...)
	return joinLines(box(th, "Board", body, w, s.h))
}

func (s *boardSection) View() string {
	if s.loaded && s.err == nil {
		return s.connectedView()
	}
	return s.stateView()
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
		name := san(c.Path)
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
		trows = append(trows, []string{hash + san(t.Root.ID), orDash(t.Root.Channel), th.Tint(t.Root.Author, orDash(t.Root.Author)), san(firstLine(t.Root.Text))})
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
		for _, b := range rows[s.cursor].body() {
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
