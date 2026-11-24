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

	// reading state: what changed since the person last looked, this session only (nothing is kept)
	stamp     map[string]string // row id -> what its row shows now
	seen      map[string]string // row id -> what it showed when last looked at
	baselined bool              // the first read arrived: what was there then is not "new"
	baseAt    time.Time         // the newest activity on that first read: an older thread that only now enters the read is not new
	mine      map[string]bool   // threads the person just wrote to: their own change is not "new"
	detailOff int               // scroll of the message panel

	// writing (board_compose.go)
	writer     core.BoardWriter
	applier    *core.BoardApplier
	mode       boardMode
	draft      boardDraft
	plan       core.BoardPlan
	planCtx    []string
	seq        int  // bumped to drop the answer of a command that was cancelled
	confirmOff int  // scroll of the confirm screen
	seenEnd    bool // the confirm screen was scrolled to its end
	outcome    string
	refetch    bool // a write landed while a read was in flight: read once more
}

func newBoardSection(p core.BoardProvider) *boardSection {
	s := &boardSection{p: p, th: DefaultTheme(), stamp: map[string]string{}, seen: map[string]string{}, mine: map[string]bool{}}
	if bi, ok := p.(boardInspector); ok {
		s.inspect = bi.InspectCmd
	}
	return s
}

func (s *boardSection) SetTheme(t Theme) { s.th = t }
func (s *boardSection) Capturing() bool  { return s.mode != bmList }

// canInspect: the browser handoff is offered only for a loaded, healthy board.
func (s *boardSection) canInspect() bool { return s.inspect != nil && s.loaded && s.err == nil }

func (s *boardSection) Bindings() []key.Binding {
	if s.mode != bmList {
		return s.composeBindings()
	}
	enter, back := s.bindings(s.width(), len(s.rows()))
	var out []key.Binding
	if s.canWrite() { // writing first: the footer cuts from the end
		out = append(out, keyBoardNew)
		if len(s.rows()) > 0 {
			out = append(out, keyBoardReply)
		}
	}
	out = append(out, moveBindings()...)
	out = append(out, enter, back, keyReload)
	if len(s.rows()) > 0 {
		out = append(out, keyScroll)
	}
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
	id    string
	msgID string // the message a reply goes to
	desc  string // what a reply form says it answers
	body  func() []string
}

func (s *boardSection) rows() []boardRow {
	var out []boardRow
	for _, t := range s.report.Threads {
		out = append(out, boardRow{id: "t:" + t.Root.ID, msgID: t.Root.ID, desc: replyDesc(t.Root), body: func() []string { return threadBody(t) }})
	}
	for _, t := range s.report.Tasks {
		out = append(out, boardRow{id: "k:" + t.ID, msgID: t.ID, desc: replyDesc(core.BoardMessage{ID: t.ID, Channel: t.Channel, Text: t.Text}), body: func() []string { return taskBody(t) }})
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
		s.absorb()
		if s.refetch { // a write landed while this read was in flight: its answer may predate it
			s.refetch = false
			return s, s.fetch()
		}
	case boardDescribedMsg:
		s.described(m)
	case boardAppliedMsg:
		return s, s.applied(m)
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
		s.confirmOff, s.seenEnd = 0, false // a different size shows different lines: read it again
	case tea.KeyMsg:
		s.notice = ""
		if s.mode != bmList {
			return s.compose(m)
		}
		ids := s.ids()
		before := s.cursor
		switch {
		case s.drilled && (key.Matches(m, keyUp) || key.Matches(m, keyDown)): // only the message is on screen: the arrows read it
			s.scrollDetail(m)
		case key.Matches(m, keyUp):
			s.up(ids)
		case key.Matches(m, keyDown):
			s.down(ids)
		case m.Type == tea.KeyPgUp || m.Type == tea.KeyPgDown:
			s.scrollDetail(m)
		case key.Matches(m, keyEnter):
			s.enter(s.width(), len(ids))
			s.detailOff = 0
		case key.Matches(m, keyBack):
			s.back()
			s.detailOff = 0
		case key.Matches(m, keyReload):
			if !s.busy {
				return s, s.fetch()
			}
		case key.Matches(m, keyBoardNew) && s.writer != nil:
			s.startPost()
		case key.Matches(m, keyBoardReply) && s.writer != nil:
			s.startReply()
		case key.Matches(m, keyBrowser) && s.canInspect():
			return s, tea.ExecProcess(s.inspect(), func(err error) tea.Msg { return boardInspectDoneMsg{err} })
		}
		s.remember(ids)
		if s.cursor != before {
			s.detailOff = 0
		}
	}
	s.markSeen()
	return s, nil
}

// scrollDetail moves the message panel by a line (the arrows, when only it is shown) or a page.
func (s *boardSection) scrollDetail(k tea.KeyMsg) {
	page := max(s.bodyH(s.notice)-panelChromeH-1, 1)
	step := map[tea.KeyType]int{tea.KeyUp: -1, tea.KeyDown: 1, tea.KeyPgUp: -page, tea.KeyPgDown: page}[k.Type]
	if step == 0 { // k and j
		step = map[string]int{"k": -1, "j": 1}[k.String()]
	}
	s.detailOff = max(0, s.detailOff+step) // the view clamps it to the text
}

// ---- unread markers: this session only, nothing is stored ----

func threadStamp(t core.BoardThread) string {
	last := ""
	if t.Last != nil {
		last = t.Last.ID
	}
	return fmt.Sprintf("%d|%s", t.Replies, last)
}

func taskStamp(t core.BoardTask) string { return t.State + "|" + t.Claimant }

// absorb takes in a new read: the first one is the baseline (everything on it is already seen), a
// later one leaves a row marked new when it appeared or changed. Threads the person just wrote to
// are seen, and rows that left the board are forgotten.
func (s *boardSection) absorb() {
	if s.err != nil {
		return
	}
	st := map[string]string{}
	for _, t := range s.report.Threads {
		st["t:"+t.Root.ID] = threadStamp(t)
	}
	for _, t := range s.report.Tasks {
		st["k:"+t.ID] = taskStamp(t)
	}
	s.stamp = st
	if !s.baselined {
		s.baselined = true
		for id, v := range st {
			s.seen[id] = v
		}
		for _, t := range s.report.Threads {
			if at, err := time.Parse(time.RFC3339, activityAt(t)); err == nil && at.After(s.baseAt) {
				s.baseAt = at
			}
		}
	}
	// a thread that was already old when the first read was made, and only now comes into the bounded
	// read (the channels it is in became more active), is not news
	for _, t := range s.report.Threads {
		id := "t:" + t.Root.ID
		if _, ok := s.seen[id]; ok {
			continue
		}
		if at, err := time.Parse(time.RFC3339, activityAt(t)); err != nil || !at.After(s.baseAt) {
			s.seen[id] = st[id]
		}
	}
	for id := range s.mine {
		if v, ok := st[id]; ok {
			s.seen[id] = v
			delete(s.mine, id)
		}
	}
	for id := range s.seen {
		if _, ok := st[id]; !ok {
			delete(s.seen, id)
		}
	}
	s.markSeen()
}

func (s *boardSection) isNew(id string) bool {
	v, ok := s.stamp[id]
	return s.baselined && ok && s.seen[id] != v
}

// markSeen marks the row whose message is on screen (beside the list, or drilled into) as seen.
func (s *boardSection) markSeen() {
	if !s.loaded || s.err != nil || s.mode != bmList || !(!isStacked(s.width()) || s.drilled) {
		return
	}
	if ids := s.ids(); s.cursor < len(ids) {
		if v, ok := s.stamp[ids[s.cursor]]; ok {
			s.seen[ids[s.cursor]] = v
		}
	}
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
	if s.mode != bmList {
		return joinLines(s.composeView())
	}
	if s.loaded && s.err == nil {
		return s.connectedView()
	}
	return s.stateView()
}

// replyDesc is what a reply form says it answers: the message, where, by whom, and its first line.
func replyDesc(m core.BoardMessage) string {
	d := hash + san(m.ID) + " in " + orDash(m.Channel)
	if m.Author != "" {
		d += " by " + san(m.Author)
	}
	if l := firstLine(m.Text); l != "" {
		d += ": " + san(l)
	}
	return d
}

// ageText is how long ago an ISO time was, as the search results say it ("3m", "15h", "2d").
func ageText(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "-"
	}
	return core.SearchAge(nowFn().Sub(t))
}

// threadCols and taskCols are the board tables: the id (with its new marker) and the text always stay;
// the channel, author, age and claimant drop first as the panel narrows.
var (
	threadCols = table{cols: []column{{min: 9, max: 9, prio: 0}, {min: 6, max: 14, prio: 2}, {min: 6, max: 14, prio: 3}, {min: 3, max: 8, prio: 2}, {min: 8, prio: 1}}, gap: 2}
	taskCols   = table{cols: []column{{min: 9, max: 9, prio: 0}, {min: 5, max: 10, prio: 0}, {min: 6, max: 16, prio: 3}, {min: 8, prio: 1}}, gap: 2}
)

// boardChanRows is how many lines of the list the channel names may take.
const boardChanRows = 2

// chanLines are the channel names, newest activity first, as at most boardChanRows lines and a
// count of the rest (the new-thread form lists them all, filtered as you type).
func chanLines(th Theme, chans []core.BoardChannel, iw int) []string {
	var names []string
	for _, c := range chans {
		name := san(c.Path)
		if c.Threads > 0 { // a board that gives no counts shows none
			name += th.Text(KindMuted, fmt.Sprintf(" (%d)", c.Threads))
		}
		names = append(names, name)
	}
	for shown := len(names); ; shown-- {
		lines := wrap(strings.Join(names[:shown], " · "), iw)
		if shown == 0 {
			return nil
		}
		if shown == len(names) && len(lines) <= boardChanRows {
			return lines
		}
		if len(lines) <= boardChanRows-1 || shown == 1 {
			return append(lines, th.Text(KindMuted, fmt.Sprintf("+%d more channels", len(names)-shown)))
		}
	}
}

// markCell is the id cell of a row, led by a dot when the row appeared or changed since the person
// last looked (a space otherwise, so the ids line up).
func (s *boardSection) markCell(th Theme, row, id string) (styled, plain string) {
	if s.isNew(row) {
		return th.Text(KindAccent, "●") + id, "●" + id
	}
	return " " + id, " " + id
}

// connectedView is the board as a framed list (channels, threads, tasks) with the selected message
// or task in a detail panel beside it from bpWide columns, or after Enter below that.
func (s *boardSection) connectedView() string {
	th, w := s.th, s.width()
	r := s.report
	g := duoLayout(w, s.bodyH(s.notice))
	iw := g.listInner()
	rows := s.rows()
	canPost := s.writer != nil

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
		none := "none yet: the first post makes one"
		if canPost {
			none += " (press n)"
		}
		lines = append(lines, wrap(th.Text(KindMuted, none), iw)...)
	}
	lines = append(lines, chanLines(th, r.Channels, iw)...)

	lines = append(lines, "", th.Section("Recent threads"))
	if len(r.Threads) == 0 {
		none := "no threads yet"
		if canPost {
			none += ": press n to start one"
		}
		lines = append(lines, wrap(th.Text(KindMuted, none), iw)...)
	}
	var trows, tplain [][]string
	for _, t := range r.Threads {
		id, idPlain := s.markCell(th, "t:"+t.Root.ID, hash+san(t.Root.ID))
		age := ageText(activityAt(t))
		tplain = append(tplain, []string{idPlain, orDash(t.Root.Channel), orDash(t.Root.Author), age, san(firstLine(t.Root.Text))})
		trows = append(trows, []string{id, orDash(t.Root.Channel), th.Tint(t.Root.Author, orDash(t.Root.Author)), th.Text(KindMuted, age), san(firstLine(t.Root.Text))})
	}
	first := len(lines)
	lines = append(lines, renderRows(th, threadCols, iw, trows, tplain, s.cursor)...)
	lines = append(lines, "", th.Section("Tasks"))
	if len(r.Tasks) == 0 {
		lines = append(lines, th.Text(KindMuted, "no open tasks"))
	}
	var krows, kplain [][]string
	for _, t := range r.Tasks {
		id, idPlain := s.markCell(th, "k:"+t.ID, hash+san(t.ID))
		kplain = append(kplain, []string{idPlain, orDash(t.State), orDash(t.Claimant), san(firstLine(t.Text))})
		krows = append(krows, []string{id, orDash(t.State), th.Tint(t.Claimant, orDash(t.Claimant)), san(firstLine(t.Text))})
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
		if s.cursorCut() {
			detail = append(detail, "", th.Text(KindMuted, fmt.Sprintf("… cut at %d characters: the board has more", core.BoardMaxText)))
		}
	}
	list := windowLines(th, lines, cursorLine, g.inner, &s.top)
	body := g.draw(th, title, list, detailTitle, scrollLines(th, detail, g.inner, &s.detailOff), s.drilled)
	if s.notice != "" {
		body = append(body, noticeLine(th, w, s.notice)...)
	}
	return joinLines(body)
}

// cursorCut is whether the selected message was cut short by the read.
func (s *boardSection) cursorCut() bool {
	ids := s.ids()
	if s.cursor >= len(ids) {
		return false
	}
	if s.cursor < len(s.report.Threads) {
		t := s.report.Threads[s.cursor]
		return t.Root.Cut || (t.Last != nil && t.Last.Cut)
	}
	return s.report.Tasks[s.cursor-len(s.report.Threads)].Cut
}

// activityAt is when a thread last changed: its latest reply, else its root.
func activityAt(t core.BoardThread) string {
	if t.Last != nil && t.Last.At != "" {
		return t.Last.At
	}
	return t.Root.At
}

// scrollLines shows rows lines of lines from *off (rows <= 0: all), with a last row that says which
// lines are shown when it cuts. *off is clamped to the text.
func scrollLines(th Theme, lines []string, rows int, off *int) []string {
	if rows <= 0 || len(lines) <= rows {
		*off = 0
		return lines
	}
	if rows == 1 {
		return lines[:1]
	}
	vis := rows - 1
	*off = max(0, min(*off, len(lines)-vis))
	out := append([]string(nil), lines[*off:*off+vis]...)
	return append(out, th.Text(KindMuted, fmt.Sprintf("↕ %d-%d of %d: pgup/pgdn", *off+1, *off+vis, len(lines))))
}
