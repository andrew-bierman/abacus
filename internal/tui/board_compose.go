package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Posting to the Board from the TUI: `n` starts a thread, `c` replies to the selected one. Both go
// through exactly the path `abacus board post|reply` takes: core.PlanBoardChange (validation, the
// exact argv, the idempotency key), a read-only look at the board for the confirm screen, a single
// explicit `y`, then core.BoardApplier (single use, audit before and after). Nothing is written
// from the form: only `y` on a confirm screen that has been read to the end applies, and a result
// that may or may not have landed is called "result unknown", never "not applied".
//
// The form is a small text editor: typing and pasting append, backspace removes the last
// character, enter adds a line break. There is no cursor movement, so the caret is always at the
// end; the text is capped at core.BoardMaxText characters as it is typed.

var (
	keyBoardNew   = key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new thread"))
	keyBoardReply = key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "reply"))
	keyReview     = key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "review"))
	keyBoardBack  = key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "edit"))
	keyBoardCut   = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
	keyScroll     = key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "scroll"))
)

type boardMode int

const (
	bmList    boardMode = iota
	bmForm              // the draft is being typed
	bmCheck             // reading the board for the confirm screen
	bmConfirm           // the plan and the exact command, waiting for y
	bmBusy              // applying
	bmOutcome           // a write whose result is unknown
)

const (
	boardMaxChannel = 128 // the longest channel name core accepts
	boardPicks      = 4   // channel suggestions shown at once
)

// boardDraft is the post or reply being written.
type boardDraft struct {
	op      core.BoardOp
	channel string
	to      string // reply: the message answered
	toLine  string // reply: who and what, from the report
	text    string
	fi      int // post: 0 is the channel, 1 the text
	pick    int // the highlighted channel suggestion, -1 none
	err     string
}

type (
	boardDescribedMsg struct {
		seq   int
		lines []string
	}
	boardAppliedMsg struct {
		seq int
		res core.BoardResult
		err error
		ch  core.BoardChange
	}
)

// withWriter makes the section able to post. A nil writer (no Board provider that can write, a
// demo) leaves it read-only.
func (s *boardSection) withWriter(w core.BoardWriter, a core.Audit) *boardSection {
	if w != nil {
		s.writer, s.applier = w, core.NewBoardApplier(w, a)
	}
	return s
}

func (s *boardSection) canWrite() bool { return s.writer != nil && s.loaded && s.err == nil }

// startPost opens an empty new-thread form.
func (s *boardSection) startPost() {
	if !s.canWrite() {
		s.notice = "posting is not available here: no board that can be written to"
		return
	}
	s.draft, s.mode = boardDraft{op: core.BoardPost, pick: -1}, bmForm
}

// startReply opens a reply form for the thread or task under the cursor.
func (s *boardSection) startReply() {
	rows := s.rows()
	switch {
	case !s.canWrite():
		s.notice = "replying is not available here: no board that can be written to"
	case s.cursor >= len(rows):
		s.notice = "no thread selected to reply to"
	default:
		r := rows[s.cursor]
		s.draft, s.mode = boardDraft{op: core.BoardReply, to: r.msgID, toLine: r.desc, pick: -1, fi: 1}, bmForm
	}
}

// compose is the whole key handling of every mode but the list; the section is Capturing, so the
// shell's global keys never reach it.
func (s *boardSection) compose(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s.mode {
	case bmForm:
		return s.formKey(k)
	case bmCheck:
		if key.Matches(k, keyBack) {
			s.seq++ // the answer in flight is dropped
			s.mode = bmForm
		}
	case bmConfirm:
		switch {
		case key.Matches(k, keyYes):
			if !s.confirmReadable() { // a clipped screen is not what was reviewed
				return s, nil
			}
			s.mode = bmBusy
			return s, s.apply()
		case key.Matches(k, keyBoardBack):
			s.mode = bmForm
		case key.Matches(k, keyUp), key.Matches(k, keyDown), k.Type == tea.KeyPgUp, k.Type == tea.KeyPgDown:
			s.scrollConfirm(k)
		}
	case bmOutcome:
		switch {
		case k.String() == "e":
			s.mode = bmForm
		case key.Matches(k, keyEnter), key.Matches(k, keyReload), key.Matches(k, keyBack):
			s.mode, s.notice = bmList, "refreshing the board: look for your message before posting again"
			if !s.busy {
				return s, s.fetch()
			}
			s.refetch = true
		}
	}
	return s, nil
}

func (s *boardSection) formKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &s.draft
	post := d.op == core.BoardPost
	d.err = ""
	switch k.Type {
	case tea.KeyEsc:
		s.mode, s.notice = bmList, "cancelled: nothing was posted"
		return s, nil
	case tea.KeyCtrlS:
		return s, s.review()
	case tea.KeyTab, tea.KeyShiftTab:
		if post {
			s.acceptPick()
			d.fi = 1 - d.fi
		}
		return s, nil
	}
	if post && d.fi == 0 {
		s.channelKey(k)
		return s, nil
	}
	switch k.Type {
	case tea.KeyEnter:
		d.text = s.appendText(d.text, "\n")
	case tea.KeyBackspace:
		if r := []rune(d.text); len(r) > 0 {
			d.text = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		d.text = ""
	case tea.KeyRunes, tea.KeySpace:
		d.text = s.appendText(d.text, string(k.Runes))
	}
	return s, nil
}

// appendText adds typed or pasted text, cleaned the way stored message text is, and never grows
// the draft past core.BoardMaxText: the rest is dropped and the form says so.
func (s *boardSection) appendText(have, add string) string {
	add = core.SanitizeText(add)
	room := core.BoardMaxText - len([]rune(have))
	if r := []rune(add); len(r) > room {
		add = string(r[:max(room, 0)])
		s.draft.err = fmt.Sprintf("the limit is %d characters: the rest was not added", core.BoardMaxText)
	}
	return have + add
}

// matches are the channels the typed name could mean: those starting with it, then those
// containing it, newest activity first.
func (s *boardSection) matches() []core.BoardChannel {
	typed := strings.ToLower(s.draft.channel)
	var pre, in []core.BoardChannel
	for _, c := range s.report.Channels {
		p := strings.ToLower(c.Path)
		switch {
		case strings.HasPrefix(p, typed):
			pre = append(pre, c)
		case strings.Contains(p, typed):
			in = append(in, c)
		}
	}
	return append(pre, in...)
}

func (s *boardSection) acceptPick() {
	d := &s.draft
	if m := s.matches(); d.pick >= 0 && d.pick < len(m) {
		d.channel, d.pick = m[d.pick].Path, -1
	}
}

func (s *boardSection) channelKey(k tea.KeyMsg) {
	d := &s.draft
	switch k.Type {
	case tea.KeyUp:
		d.pick = max(d.pick-1, -1)
	case tea.KeyDown:
		d.pick = min(d.pick+1, min(len(s.matches()), boardPicks)-1)
	case tea.KeyEnter:
		s.acceptPick()
		d.fi = 1
	case tea.KeyBackspace:
		if r := []rune(d.channel); len(r) > 0 {
			d.channel, d.pick = string(r[:len(r)-1]), -1
		}
	case tea.KeyCtrlU:
		d.channel, d.pick = "", -1
	case tea.KeyRunes:
		for _, r := range k.Runes {
			if len([]rune(d.channel)) < boardMaxChannel && channelRune(r) {
				d.channel += string(r)
			}
		}
		d.pick = -1
	}
}

// channelRune is a character a channel name may hold.
func channelRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._/-", r)
}

// review turns the draft into a plan (nothing is written) and reads the board for the confirm
// screen. A draft core refuses stays in the form with the reason.
func (s *boardSection) review() tea.Cmd {
	d := &s.draft
	ch := core.BoardChange{Op: d.op, Channel: strings.TrimSpace(d.channel), ID: d.to, Text: strings.TrimRight(d.text, " \t\n")}
	if d.op == core.BoardPost && ch.Channel == "" {
		d.err = "choose a channel, or type a new one"
		return nil
	}
	plan, err := core.PlanBoardChange(s.writer, ch)
	if err != nil {
		d.err = core.Sanitize(err.Error())
		return nil
	}
	s.plan, s.planCtx, s.mode = plan, nil, bmCheck
	s.seq++
	seq, w := s.seq, s.writer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
		defer cancel()
		return boardDescribedMsg{seq, w.Describe(ctx, ch)}
	}
}

// unknownWarning is the line every confirm screen carries about the one thing a post cannot promise.
const unknownWarning = `If the board does not answer, abacus cannot tell whether this landed: it will say "result unknown", never "not applied".`

func (s *boardSection) confirmBody(busy bool) []string {
	plan := append(append(append([]string(nil), s.plan.Lines...), s.planCtx...), unknownWarning)
	return confirmBody(s.th, "", plan, s.plan.Argv, busy, innerW(s.width()))
}

// confirmRows is how many body rows the confirm panel shows (0: the height is not known yet).
func (s *boardSection) confirmRows(total int) int {
	if s.h <= 0 || total+panelChromeH <= s.h {
		return total
	}
	return max(s.h-panelChromeH-1, 1) // one row goes to the scroll position
}

func (s *boardSection) confirmMaxOff() int {
	n := len(s.confirmBody(false))
	return n - s.confirmRows(n)
}

// confirmReadable is whether the person has been shown the whole plan and exact command: it fits,
// or it was scrolled to the end (every scroll step is shorter than a page, so nothing is skipped).
func (s *boardSection) confirmReadable() bool { return s.confirmMaxOff() == 0 || s.seenEnd }

func (s *boardSection) scrollConfirm(k tea.KeyMsg) {
	n := len(s.confirmBody(false))
	page := max(s.confirmRows(n)-1, 1)
	step := map[tea.KeyType]int{tea.KeyUp: -1, tea.KeyDown: 1, tea.KeyPgUp: -page, tea.KeyPgDown: page}[k.Type]
	if step == 0 { // k and j
		step = map[string]int{"k": -1, "j": 1}[k.String()]
	}
	s.confirmOff = max(0, min(s.confirmOff+step, s.confirmMaxOff()))
	if s.confirmOff >= s.confirmMaxOff() {
		s.seenEnd = true
	}
}

// apply runs the confirmed plan through the gate. The only caller is the y key on a readable
// confirm screen.
func (s *boardSection) apply() tea.Cmd {
	p, a, seq := s.plan.Confirm(), s.applier, s.seq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*fetchTimeout)
		defer cancel()
		res, err := a.Apply(ctx, p)
		return boardAppliedMsg{seq, res, err, p.Change}
	}
}

// described and applied are the answers of the two commands above.
func (s *boardSection) described(m boardDescribedMsg) {
	if m.seq != s.seq || s.mode != bmCheck {
		return
	}
	for _, l := range m.lines {
		s.planCtx = append(s.planCtx, core.Sanitize(l))
	}
	s.mode, s.confirmOff, s.seenEnd = bmConfirm, 0, false
}

func (s *boardSection) applied(m boardAppliedMsg) tea.Cmd {
	if m.seq != s.seq || s.mode != bmBusy {
		return nil
	}
	switch {
	case m.err == nil:
		s.mode, s.draft = bmList, boardDraft{}
		if m.ch.Op == core.BoardReply {
			s.mine["t:"+m.ch.ID] = true
			s.notice = "replied: #" + san(m.res.ID)
		} else {
			s.mine["t:"+m.res.ID] = true
			s.notice = "posted: #" + san(m.res.ID) + " in " + san(m.res.Channel)
		}
		if !s.busy {
			return s.fetch()
		}
		s.refetch = true
	case errors.Is(m.err, core.ErrOutcomeUnknown) || errors.Is(m.err, core.ErrAppliedMismatch):
		s.mode, s.outcome = bmOutcome, core.DescribeWriteError(m.err)
	default: // refused before anything landed: the draft stays, so nothing typed is lost
		s.mode, s.draft.err = bmForm, core.DescribeWriteError(m.err)
	}
	return nil
}

// composeBindings are the keys of the current mode, for the footer.
func (s *boardSection) composeBindings() []key.Binding {
	switch s.mode {
	case bmForm:
		return []key.Binding{keyReview, keyBoardCut}
	case bmCheck:
		return []key.Binding{keyBack}
	case bmConfirm:
		out := []key.Binding{keyScroll}
		if s.confirmReadable() {
			out = append([]key.Binding{keyYes}, out...)
		}
		return append(out, keyBoardBack)
	case bmOutcome:
		return []key.Binding{keyReload, keyBack}
	}
	return nil
}

// composeView is the screen of every mode but the list: one framed panel.
func (s *boardSection) composeView() []string {
	th, w, inner := s.th, s.width(), innerW(s.width())
	switch s.mode {
	case bmForm:
		return s.formView()
	case bmCheck:
		body := wrap("reading the board before the confirm screen", inner)
		body = append(body, "", th.Text(KindMuted, "esc goes back to the draft"))
		return box(th, "Checking the board…", body, w, s.h)
	case bmConfirm:
		title := "Confirm this post"
		if s.draft.op == core.BoardReply {
			title = "Confirm this reply"
		}
		return scrollPanel(th, title, s.confirmBody(false), w, s.h, s.confirmOff)
	case bmBusy:
		return reviewBox(th, "Posting…", s.confirmBody(true), w, s.h)
	}
	body := wrap(th.Text(KindWarn, san(s.outcome)), inner)
	body = append(body, "")
	body = append(body, wrap("abacus did not retry and will not guess. Refresh the board and look for your message before posting again.", inner)...)
	body = append(body, "")
	body = append(body, wrap(th.Text(KindMuted, fmt.Sprintf("Posting the same text to the same place within %d minutes reuses the same key, so the board takes it at most once.", int(core.BoardKeyWindow.Minutes()))), inner)...)
	body = append(body, "")
	body = append(body, wrap(th.Key("enter")+" refresh and look · "+th.Key("e")+" edit again · "+th.Key("esc")+" back", inner)...)
	return box(th, "Result unknown", body, w, s.h)
}

// scrollPanel draws body in a framed panel at most h rows tall, from row off, and a last row that
// says which lines are shown when it does not all fit.
func scrollPanel(th Theme, title string, body []string, w, h, off int) []string {
	rows := len(body)
	if h > 0 && len(body)+panelChromeH > h {
		rows = max(h-panelChromeH-1, 1)
	}
	if rows >= len(body) {
		return panel(th, title, body, w, len(body)+panelChromeH, true)
	}
	off = max(0, min(off, len(body)-rows))
	vis := append([]string(nil), body[off:off+rows]...)
	vis = append(vis, th.Text(KindMuted, fmt.Sprintf("↕ lines %d-%d of %d: j/k or pgup/pgdn to read all of it", off+1, off+rows, len(body))))
	return panel(th, title, vis, w, rows+1+panelChromeH, true)
}

func (s *boardSection) formView() []string {
	th, w, d := s.th, s.width(), s.draft
	inner := innerW(w)
	title := "New thread"
	if d.op == core.BoardReply {
		title = "Reply to #" + san(d.to)
	}
	mark := func(on bool, label string) string {
		if on {
			return "▸ " + th.Header.Render(label)
		}
		return "  " + th.Label.Render(label)
	}
	var head []string
	post := d.op == core.BoardPost
	if post {
		val := san(d.channel)
		if d.fi == 0 {
			val += "▏"
		}
		head = append(head, mark(d.fi == 0, "Channel")+"  "+val)
		if d.fi == 0 {
			head = append(head, s.pickRows(inner)...)
		}
	} else {
		head = append(head, hang("  ", th.Text(KindMuted, "replying to ")+san(d.toLine), inner)...)
	}
	n := len([]rune(d.text))
	count := fmt.Sprintf("%d/%d characters", n, core.BoardMaxText)
	kind := KindMuted
	if n*10 >= core.BoardMaxText*9 {
		kind = KindWarn
	}
	hint := "enter new line · ctrl+s review · esc cancel"
	if post {
		hint = "↑↓ pick · enter next · tab message · ctrl+s review · esc cancel"
		if d.fi == 1 {
			hint = "enter new line · tab channel · ctrl+s review · esc cancel"
		}
	}
	tail := []string{th.Text(kind, "  "+count)}
	if d.err != "" {
		tail = append(tail, wrap(th.Text(KindFail, san(d.err)), inner)...)
	}
	tail = append(tail, hang("  ", th.Text(KindMuted, hint), inner)...)

	rows := 0 // text rows: whatever the frame has left, but never fewer than three
	if s.h > 0 {
		rows = max(s.h-panelChromeH-len(head)-1-len(tail), 3)
	}
	body := append(head, mark(d.fi == 1 || !post, "Message"))
	body = append(body, textTail(th, d.text, d.fi == 1 || !post, inner-4, rows)...)
	body = append(body, tail...)
	return box(th, title, body, w, s.h)
}

// textTail is the last rows lines of the text, wrapped, with the caret at the end when focused. A
// text longer than that says how many earlier lines are not shown.
func textTail(th Theme, text string, caret bool, width, rows int) []string {
	if caret {
		text += "▏"
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		for _, p := range wrap(san(l), max(width, 1)) {
			lines = append(lines, "    "+p)
		}
	}
	if rows <= 0 || len(lines) <= rows {
		return lines
	}
	keep := max(rows-1, 1)
	return append([]string{th.Text(KindMuted, fmt.Sprintf("    ↑ %d earlier lines", len(lines)-keep))}, lines[len(lines)-keep:]...)
}

// pickRows are the channel suggestions under the Channel field.
func (s *boardSection) pickRows(inner int) []string {
	th, d := s.th, s.draft
	ms := s.matches()
	var out []string
	if len(s.report.Channels) == 0 {
		return []string{th.Text(KindMuted, "    no channels yet: the first post makes one")}
	}
	if len(ms) == 0 {
		return []string{th.Text(KindMuted, "    no channel has that name: posting creates it")}
	}
	for i, c := range ms[:min(len(ms), boardPicks)] {
		line := san(c.Path)
		if c.Threads > 0 {
			line += th.Text(KindMuted, fmt.Sprintf(" (%d)", c.Threads))
		}
		if i == d.pick {
			out = append(out, "  ▸ "+th.Selected.Render(line))
		} else {
			out = append(out, "    "+line)
		}
	}
	if more := len(ms) - boardPicks; more > 0 {
		out = append(out, th.Text(KindMuted, fmt.Sprintf("    +%d more: keep typing to narrow them", more)))
	}
	return out
}
