package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// boardWriteFake is a Board provider that can write: nothing here runs a CLI or touches a board.
type boardWriteFake struct {
	boardProv
	ran      [][]string
	err      error
	describe []string
}

func (b *boardWriteFake) BuildBoardArgv(ch core.BoardChange, key string) ([]string, error) {
	dest := ch.Channel
	if ch.Op == core.BoardReply {
		dest = ch.ID
	}
	return []string{"board", string(ch.Op), dest, "--json", "--idempotency-key", key, "--", ch.Text}, nil
}

func (b *boardWriteFake) RunBoard(_ context.Context, argv []string, ch core.BoardChange) (core.BoardResult, error) {
	b.ran = append(b.ran, argv)
	if b.err != nil {
		return core.BoardResult{}, b.err
	}
	return core.BoardResult{ID: "99", Channel: ch.Channel, Text: ch.Text}, nil
}
func (b *boardWriteFake) Target() string       { return "http://127.0.0.1:4100/api/board" }
func (b *boardWriteFake) TargetSource() string { return "board_url (config)" }
func (b *boardWriteFake) Describe(context.Context, core.BoardChange) []string {
	return b.describe
}

func composeRig(t *testing.T, w, h int) (*boardSection, *boardWriteFake, *memAudit) {
	t.Helper()
	f := &boardWriteFake{boardProv: boardFake(), describe: []string{"existing channel dev"}}
	au := &memAudit{}
	s := newBoardSection(f).withWriter(f, au)
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(boardMsg{report: fixtureBoard()})
	return s, f, au
}

func bk(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// drain runs a command and feeds every message it yields back into the section.
func bdrain(s *boardSection, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch m := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range m {
			bdrain(s, c)
		}
	default:
		_, next := s.Update(m)
		bdrain(s, next)
	}
}

func bsend(s *boardSection, keys ...string) {
	for _, k := range keys {
		_, cmd := s.Update(bk(k))
		bdrain(s, cmd)
	}
}

func btype(s *boardSection, text string) {
	for _, r := range text {
		switch r {
		case '\n':
			bsend(s, "enter")
		case ' ':
			bsend(s, " ")
		default:
			bsend(s, string(r))
		}
	}
}

// toConfirm drives a post to the confirm screen: channel, text, review.
func toConfirm(t *testing.T, s *boardSection, channel, text string) {
	t.Helper()
	bsend(s, "n")
	btype(s, channel)
	bsend(s, "enter")
	btype(s, text)
	bsend(s, "ctrl+s")
	if s.mode != bmConfirm {
		t.Fatalf("no confirm screen (mode %d):\n%s", s.mode, s.View())
	}
}

func TestPostFlowShowsTheExactCommandTheKeyAndTheUnknownOutcomeWordingThenAppliesOnce(t *testing.T) {
	s, f, au := composeRig(t, 100, 40)
	toConfirm(t, s, "dev", "deploy is green\nsecond line")
	v := s.View()
	for _, want := range []string{
		"Confirm this post", "post a new thread to channel dev", "second line",
		"existing channel dev",
		"idempotency key: abacus-", "posts once",
		"exact command:", "board post dev --json --idempotency-key abacus-",
		`never "not applied"`,
	} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm screen lacks %q:\n%s", want, v)
		}
	}
	if len(f.ran) != 0 || au.n != 0 {
		t.Fatalf("nothing is written before y: ran=%v audit=%d", f.ran, au.n)
	}
	bsend(s, "y")
	if len(f.ran) != 1 || au.n == 0 {
		t.Fatalf("y applies once through the gate (audited): ran=%v audit=%d", f.ran, au.n)
	}
	if !strings.Contains(strings.Join(f.ran[0], " "), "deploy is green\nsecond line") {
		t.Fatalf("the multi-line text is what ran: %q", f.ran[0])
	}
	if s.mode != bmList || !strings.Contains(s.View(), "posted: #99 in dev") {
		t.Fatalf("back on the board with the outcome (mode %d):\n%s", s.mode, s.View())
	}
	bsend(s, "y")
	if len(f.ran) != 1 {
		t.Fatal("a second y on the board list runs nothing")
	}
}

func TestNothingIsWrittenUntilYOnTheConfirmScreen(t *testing.T) {
	s, f, au := composeRig(t, 100, 40)
	bsend(s, "n")
	btype(s, "dev") // y, n, q are text in the form
	bsend(s, "enter")
	btype(s, "yes please quit")
	if s.draft.text != "yes please quit" || s.mode != bmForm {
		t.Fatalf("y typed in the form is text: %+v mode %d", s.draft, s.mode)
	}
	bsend(s, "ctrl+s", "esc")
	if s.mode != bmForm || len(f.ran) != 0 || au.n != 0 {
		t.Fatalf("esc on the confirm screen goes back to the draft and writes nothing: mode %d ran=%v", s.mode, f.ran)
	}
	if s.draft.text != "yes please quit" {
		t.Fatal("the draft survives going back")
	}
	bsend(s, "esc")
	if s.mode != bmList || len(f.ran) != 0 || !strings.Contains(s.View(), "cancelled: nothing was posted") {
		t.Fatalf("esc in the form cancels: mode %d\n%s", s.mode, s.View())
	}
}

func TestConfirmAppliesOnlyAfterTheWholeCommandHasBeenShown(t *testing.T) {
	long := strings.Repeat("a long line of text ", 20)
	s, f, _ := composeRig(t, 60, 14)
	toConfirm(t, s, "dev", long)
	if s.confirmReadable() {
		t.Fatal("a screen that clips the command is not readable yet")
	}
	bsend(s, "y")
	if len(f.ran) != 0 || s.mode != bmConfirm {
		t.Fatalf("y on a clipped confirm screen does nothing: ran=%v mode %d", f.ran, s.mode)
	}
	if v := s.View(); !strings.Contains(v, "j/k or pgup/pgdn to read all of it") {
		t.Fatalf("the screen says how to read it all:\n%s", v)
	}
	for i := 0; i < 200 && !s.confirmReadable(); i++ {
		bsend(s, "j")
	}
	if !s.confirmReadable() {
		t.Fatal("scrolling to the end makes it readable")
	}
	if v := s.View(); !strings.Contains(v, "--idempotency-key") && !strings.Contains(v, "exact command") {
		t.Fatalf("the end of the screen is shown:\n%s", v)
	}
	bsend(s, "y")
	if len(f.ran) != 1 {
		t.Fatalf("y applies once read to the end: %v", f.ran)
	}
}

func TestPagesCannotSkipTheMiddleOfTheConfirmScreen(t *testing.T) {
	s, _, _ := composeRig(t, 60, 14)
	toConfirm(t, s, "dev", strings.Repeat("word ", 150))
	seen := map[string]bool{}
	body := s.confirmBody(false)
	rows := s.confirmRows(len(body))
	for i := 0; i < 100; i++ {
		for _, l := range body[s.confirmOff : s.confirmOff+rows] {
			seen[l] = true
		}
		if s.confirmOff >= s.confirmMaxOff() {
			break
		}
		bsend(s, "pgdown")
	}
	for _, l := range body {
		if !seen[l] {
			t.Fatalf("a line was never on screen: %q", l)
		}
	}
}

func TestResizeMakesThePersonReadTheConfirmScreenAgain(t *testing.T) {
	s, f, _ := composeRig(t, 60, 14)
	toConfirm(t, s, "dev", strings.Repeat("word ", 100))
	for i := 0; i < 100 && !s.confirmReadable(); i++ {
		bsend(s, "pgdown")
	}
	s.Update(tea.WindowSizeMsg{Width: 60, Height: 13})
	bsend(s, "y")
	if len(f.ran) != 0 || s.confirmReadable() {
		t.Fatal("a different size shows different lines: y waits for the new end")
	}
}

func TestTextIsCappedWhileTypedAndWhilePasted(t *testing.T) {
	s, _, _ := composeRig(t, 100, 40)
	bsend(s, "n")
	bsend(s, "enter") // channel -> text (empty channel is fine here)
	long := strings.Repeat("x", core.BoardMaxText+500)
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(long), Paste: true})
	if n := len([]rune(s.draft.text)); n != core.BoardMaxText {
		t.Fatalf("a paste over the limit is cut at %d, got %d", core.BoardMaxText, n)
	}
	if !strings.Contains(s.View(), "the limit is 4000 characters") || !strings.Contains(s.View(), "4000/4000 characters") {
		t.Fatalf("the form says so:\n%s", s.View())
	}
	bsend(s, "y", "enter")
	if n := len([]rune(s.draft.text)); n != core.BoardMaxText {
		t.Fatalf("typing past the limit adds nothing, got %d", n)
	}
	bsend(s, "backspace", "z")
	if n := len([]rune(s.draft.text)); n != core.BoardMaxText || !strings.HasSuffix(s.draft.text, "z") {
		t.Fatalf("room made by backspace can be refilled: %d", n)
	}
}

func TestPastedTextIsCleanedAndKeepsItsLineBreaks(t *testing.T) {
	s, _, _ := composeRig(t, 100, 40)
	bsend(s, "n", "enter")
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("one\r\ntwo\x1b[2J\tthree\x07"), Paste: true})
	if want := "one\ntwo[2J three"; s.draft.text != want {
		t.Fatalf("got %q want %q", s.draft.text, want)
	}
	if strings.ContainsRune(s.View(), 0x1b) {
		t.Fatal("no escape reaches the screen")
	}
}

func TestEmptyOrChannellessDraftsStayInTheFormWithAReason(t *testing.T) {
	s, f, _ := composeRig(t, 100, 40)
	bsend(s, "n", "ctrl+s")
	if s.mode != bmForm || !strings.Contains(s.View(), "choose a channel") {
		t.Fatalf("%d\n%s", s.mode, s.View())
	}
	btype(s, "dev")
	bsend(s, "ctrl+s")
	if s.mode != bmForm || !strings.Contains(s.View(), "the message text is empty") {
		t.Fatalf("%d\n%s", s.mode, s.View())
	}
	bsend(s, "enter", "enter", " ", "ctrl+s")
	if s.mode != bmForm || len(f.ran) != 0 {
		t.Fatalf("a blank message is not a message: %d", s.mode)
	}
}

func TestChannelPickerFiltersPicksAndFlagsANewChannel(t *testing.T) {
	s, _, _ := composeRig(t, 100, 40)
	bsend(s, "n")
	v := s.View()
	for _, want := range []string{"dev/web", "work", "ops"} {
		if !strings.Contains(v, want) {
			t.Errorf("all channels are offered before typing; lacks %q:\n%s", want, v)
		}
	}
	btype(s, "web")
	if v := s.View(); !strings.Contains(v, "dev/web") || strings.Contains(v, "ops") {
		t.Fatalf("typing narrows the list:\n%s", v)
	}
	bsend(s, "down", "enter")
	if s.draft.channel != "dev/web" || s.draft.fi != 1 {
		t.Fatalf("a picked suggestion fills the channel and moves to the text: %+v", s.draft)
	}
	bsend(s, "tab")
	bsend(s, "ctrl+u")
	btype(s, "brand-new")
	if v := s.View(); !strings.Contains(v, "posting creates it") {
		t.Fatalf("a name no channel has says it is new:\n%s", v)
	}
	btype(s, "!? x")
	if s.draft.channel != "brand-newx" {
		t.Fatalf("a channel name only takes characters a channel may have: %q", s.draft.channel)
	}
}

func TestChannelListBeyondThePickerRowsSaysHowManyMore(t *testing.T) {
	s, _, _ := composeRig(t, 100, 40)
	r := fixtureBoard()
	for i := 0; i < 30; i++ {
		r.Channels = append(r.Channels, core.BoardChannel{Path: fmt.Sprintf("team/c%02d", i)})
	}
	s.Update(boardMsg{report: r})
	bsend(s, "n")
	btype(s, "team/")
	if v := s.View(); !strings.Contains(v, "+26 more") {
		t.Fatalf("%s", v)
	}
	bsend(s, "down", "down", "down", "down", "down", "down")
	if s.draft.pick != boardPicks-1 {
		t.Fatalf("the highlight stays on a shown row: %d", s.draft.pick)
	}
}

func TestReplyFormNamesWhatItAnswersAndPlansAReply(t *testing.T) {
	s, f, _ := composeRig(t, 100, 40)
	bsend(s, "c")
	if v := s.View(); !strings.Contains(v, "Reply to #15") || !strings.Contains(v, "replying to #15 in dev/web by ada: checkout times out") {
		t.Fatalf("%s", v)
	}
	btype(s, "on it")
	bsend(s, "ctrl+s")
	if s.mode != bmConfirm || !strings.Contains(s.View(), "board reply 15 --json --idempotency-key") || !strings.Contains(s.View(), "reply in the thread of message 15") {
		t.Fatalf("%s", s.View())
	}
	bsend(s, "y")
	if len(f.ran) != 1 || !strings.Contains(s.View(), "replied: #99") {
		t.Fatalf("%v\n%s", f.ran, s.View())
	}
}

func TestReplyToATaskAnswersItsMessage(t *testing.T) {
	s, _, _ := composeRig(t, 100, 40)
	bsend(s, "down", "down", "c") // threads first, then tasks
	if !strings.HasPrefix(s.draft.to, "") || s.draft.to == "" || s.draft.op != core.BoardReply {
		t.Fatalf("%+v", s.draft)
	}
	if want := fixtureBoard().Tasks[0].ID; s.draft.to != want {
		t.Fatalf("the reply goes to the task's message %s, not %s", want, s.draft.to)
	}
}

func TestAnUnknownResultIsNeverCalledNotApplied(t *testing.T) {
	s, f, _ := composeRig(t, 100, 40)
	f.err = core.UnknownOutcome("the board did not answer (timed out); it may have been posted. Refresh the Board before trying again")
	toConfirm(t, s, "dev", "maybe posted")
	bsend(s, "y")
	v := s.View()
	if s.mode != bmOutcome || !strings.Contains(v, "Result unknown") || strings.Contains(strings.ToLower(v), "not applied") || !strings.Contains(v, "will not guess") || !strings.Contains(v, "at most once") {
		t.Fatalf("mode %d\n%s", s.mode, v)
	}
	if !s.Capturing() {
		t.Fatal("the outcome screen owns the keys")
	}
	key1 := f.ran[0][5]
	bsend(s, "e")
	if s.mode != bmForm || s.draft.text != "maybe posted" {
		t.Fatalf("e edits the same draft again: %d %q", s.mode, s.draft.text)
	}
	bsend(s, "ctrl+s", "y")
	if len(f.ran) != 2 || f.ran[1][5] != key1 {
		t.Fatalf("a retry of the same text gets the same idempotency key: %v", f.ran)
	}
	bsend(s, "enter")
	if s.mode != bmList || !strings.Contains(s.notice, "look for your message") || s.fetches == 0 {
		t.Fatalf("enter refreshes the board: %d %q", s.mode, s.notice)
	}
}

func TestARefusedPostKeepsTheDraftAndSaysNotApplied(t *testing.T) {
	s, f, _ := composeRig(t, 100, 40)
	f.err = &core.NotConnectedError{Reason: core.BoardNotSignedInReason, HowToConnect: core.BoardNotSignedIn}
	toConfirm(t, s, "dev", "my careful draft")
	bsend(s, "y")
	if s.mode != bmForm || s.draft.text != "my careful draft" || !strings.Contains(s.View(), "not applied: ") {
		t.Fatalf("mode %d\n%s", s.mode, s.View())
	}
}

func TestWithoutAWriterThereIsNoPostingAndTheKeysAreNotOffered(t *testing.T) {
	s, _, _ := newInspectSection()
	s.Update(boardMsg{report: fixtureBoard()})
	for _, k := range []string{"n", "c"} {
		if hasBinding(s, k) {
			t.Errorf("%s is offered with no writer", k)
		}
		bsend(s, k)
		if s.mode != bmList {
			t.Errorf("%s opened a form with no writer", k)
		}
	}
	s2, _, _ := composeRig(t, 100, 40)
	for _, k := range []string{"n", "c"} {
		if !hasBinding(s2, k) {
			t.Errorf("%s is offered with a writer", k)
		}
	}
	s2.Update(boardMsg{err: errors.New("could not reach the board")})
	bsend(s2, "n")
	if s2.mode != bmList || !strings.Contains(s2.notice, "not available") {
		t.Fatalf("a board that could not be read cannot be posted to: %d %q", s2.mode, s2.notice)
	}
}

func TestTheFormOwnsEveryKeyIncludingQuitAndTabJumps(t *testing.T) {
	f := &boardWriteFake{boardProv: boardFake()}
	en := []core.Entry{{Module: f, Detection: f.Det}}
	m := New(en, WithTheme(asciiTheme()), WithFullView(noFullView), WithAudit(&memAudit{}))
	var mm tea.Model = m
	mm = sized(mm, 100, 32)
	mm, _ = mm.Update(boardMsg{report: fixtureBoard()})
	mm = press(mm, "n")
	for _, k := range []string{"q", "2", "?", ":"} {
		mm = press(mm, k)
	}
	if !strings.Contains(mm.View(), "[Board]") || strings.Contains(mm.View(), "Commands") {
		t.Fatalf("keys typed in the channel field went nowhere else:\n%s", mm.View())
	}
	if got := m.active().(*boardSection).draft.channel; got != "q2" {
		t.Fatalf("q and 2 are text (? and : are not channel characters): %q", got)
	}
	mm = sized(mm, 30, 10) // too small to draw the form
	mm = press(mm, "esc")
	if m.active().(*boardSection).mode != bmForm {
		t.Fatal("a form that is not drawn takes no keys")
	}
}

func TestATooSmallTerminalCannotApplyAHiddenBoardConfirm(t *testing.T) {
	f := &boardWriteFake{boardProv: boardFake(), describe: nil}
	en := []core.Entry{{Module: f, Detection: f.Det}}
	m := New(en, WithTheme(asciiTheme()), WithFullView(noFullView), WithAudit(&memAudit{}))
	var mm tea.Model = m
	mm = sized(mm, 100, 40)
	mm, _ = mm.Update(boardMsg{report: fixtureBoard()})
	sec := m.active().(*boardSection)
	toConfirm(t, sec, "dev", "hello")
	mm = sized(mm, 30, 8)
	mm = press(mm, "y")
	if len(f.ran) != 0 || sec.mode != bmConfirm {
		t.Fatalf("a hidden confirm is not answered: ran=%v mode %d", f.ran, sec.mode)
	}
}

func TestAWriteLandingDuringAReadReadsOnceMore(t *testing.T) {
	s, _, _ := composeRig(t, 100, 40)
	s.busy = true
	toConfirm(t, s, "dev", "hello")
	_, cmd := s.Update(bk("y"))
	before := s.fetches
	bdrain(s, cmd)
	if !s.refetch && s.fetches == before {
		t.Fatal("the board is read again after a write")
	}
}

// ---- reading state: new markers, bounded channel list, message scroll ----

func TestRowsThatAppearOrChangeAfterTheFirstReadAreMarkedNewUntilSeen(t *testing.T) {
	s, _, _ := composeRig(t, 60, 30) // stacked: the message is shown only when drilled into
	if strings.Contains(s.View(), "●") {
		t.Fatalf("nothing is new on the first read:\n%s", s.View())
	}
	r := fixtureBoard()
	r.Threads[1].Replies++
	r.Threads[1].Last = &core.BoardMessage{ID: "50", Channel: "dev", Author: "bo", At: "2026-11-23T11:00:00Z", Text: "reply"}
	r.Threads = append(r.Threads, core.BoardThread{Root: core.BoardMessage{ID: "60", Channel: "ops", Author: "cy", At: "2026-11-23T11:30:00Z", Text: "brand new thread"}})
	s.Update(boardMsg{report: r})
	if n := strings.Count(s.View(), "●"); n != 2 {
		t.Fatalf("the changed thread and the new one are marked: %d\n%s", n, s.View())
	}
	bsend(s, "down", "enter") // look at the changed thread
	bsend(s, "esc")
	if n := strings.Count(s.View(), "●"); n != 1 {
		t.Fatalf("looking at a thread clears its marker only: %d\n%s", n, s.View())
	}
	s.Update(boardMsg{report: r})
	if n := strings.Count(s.View(), "●"); n != 1 {
		t.Fatalf("a refresh with nothing changed adds none: %d", n)
	}
}

func TestAnOldThreadThatOnlyNowEntersTheReadIsNotNews(t *testing.T) {
	s, _, _ := composeRig(t, 60, 30)
	r := fixtureBoard()
	r.Threads = append(r.Threads, core.BoardThread{Root: core.BoardMessage{ID: "3", Channel: "ops", Author: "cy", At: "2026-11-01T10:00:00Z", Text: "from last month"}})
	s.Update(boardMsg{report: r})
	if strings.Contains(s.View(), "●") {
		t.Fatalf("a thread older than anything on the first read is not new:\n%s", s.View())
	}
	r.Threads[len(r.Threads)-1].Replies, r.Threads[len(r.Threads)-1].Last = 1, &core.BoardMessage{ID: "70", Channel: "ops", At: "2026-11-23T11:00:00Z", Text: "reply"}
	s.Update(boardMsg{report: r})
	if strings.Count(s.View(), "●") != 1 {
		t.Fatalf("but a reply to it is:\n%s", s.View())
	}
}

func TestAWriteOfMyOwnIsNotMarkedNew(t *testing.T) {
	s, _, _ := composeRig(t, 60, 30)
	toConfirm(t, s, "dev", "mine")
	bsend(s, "y")
	r := fixtureBoard()
	r.Threads = append(r.Threads, core.BoardThread{Root: core.BoardMessage{ID: "99", Channel: "dev", Author: "me", At: "2026-11-23T11:59:00Z", Text: "mine"}})
	s.Update(boardMsg{report: r})
	if strings.Contains(s.View(), "●") {
		t.Fatalf("my own post is not new to me:\n%s", s.View())
	}
}

func TestRecentActivityShowsAsAnAge(t *testing.T) {
	s, _, _ := composeRig(t, 120, 30)
	if v := s.View(); !strings.Contains(v, "15h") { // goldenNow is 2026-11-23 12:00, the newest thread 2026-11-22 20:14
		t.Fatalf("%s", v)
	}
}

func TestAHugeChannelListTakesAtMostTwoLinesAndCountsTheRest(t *testing.T) {
	th := asciiTheme()
	var chans []core.BoardChannel
	for i := 0; i < 200; i++ {
		chans = append(chans, core.BoardChannel{Path: fmt.Sprintf("team/channel-%03d", i), Threads: i})
	}
	for _, w := range []int{20, 36, 56, 100} {
		got := chanLines(th, chans, w)
		if len(got) > boardChanRows+1 {
			t.Errorf("w=%d: %d lines", w, len(got))
		}
		if last := got[len(got)-1]; !strings.Contains(last, "more channels") {
			t.Errorf("w=%d: the rest is counted: %q", w, last)
		}
	}
	if got := chanLines(th, chans[:3], 100); len(got) != 1 || strings.Contains(got[0], "more") {
		t.Errorf("a short list is shown whole: %q", got)
	}
}

func TestALongMessageScrollsAndSaysWhenItWasCut(t *testing.T) {
	s, _, _ := composeRig(t, 100, 24)
	r := fixtureBoard()
	var b []string
	for i := 0; i < 60; i++ {
		b = append(b, fmt.Sprintf("line %02d of a long message", i))
	}
	r.Threads[0].Root.Text, r.Threads[0].Root.Cut = strings.Join(b, "\n"), true
	s.Update(boardMsg{report: r})
	first := s.View()
	if !strings.Contains(first, "line 00") || strings.Contains(first, "line 59") || !strings.Contains(first, "pgup/pgdn") {
		t.Fatalf("%s", first)
	}
	for i := 0; i < 8; i++ {
		bsend(s, "pgdown")
	}
	end := s.View()
	if !strings.Contains(end, "line 59") && !strings.Contains(end, "cut at 4000 characters") && !strings.Contains(end, "line 5") {
		t.Fatalf("paging reaches the end:\n%s", end)
	}
	bsend(s, "down")
	if s.detailOff != 0 {
		t.Fatal("moving to another thread starts its message at the top")
	}
}

func TestDrilledArrowsScrollTheMessageNotTheList(t *testing.T) {
	s, _, _ := composeRig(t, 60, 14)
	r := fixtureBoard()
	var b []string
	for i := 0; i < 40; i++ {
		b = append(b, fmt.Sprintf("row %02d", i))
	}
	r.Threads[0].Root.Text = strings.Join(b, "\n")
	s.Update(boardMsg{report: r})
	bsend(s, "enter", "j", "j", "j")
	if s.cursor != 0 || s.detailOff != 3 {
		t.Fatalf("cursor %d off %d", s.cursor, s.detailOff)
	}
	bsend(s, "esc", "j")
	if s.cursor != 1 || s.detailOff != 0 {
		t.Fatalf("back on the list the arrows move the cursor: %d %d", s.cursor, s.detailOff)
	}
}

// The Board's keys never reuse a key the shell owns, in the list and in each writing mode that is
// not capturing (the capturing ones own every key by design), with a writer present.
func TestBoardKeysNeverCollideWithTheShellsWhenItCanWrite(t *testing.T) {
	f := &boardWriteFake{boardProv: boardFake()}
	m0 := New([]core.Entry{{Module: f, Detection: f.Det}}, WithTheme(asciiTheme()), WithFullView(noFullView), WithAudit(&memAudit{}))
	shell := map[string]bool{}
	for _, b := range []interface{ Keys() []string }{keyQuit, keyNext, keyPrev, keyJump, keyHelp, keySearch, keyPrompt} {
		for _, k := range b.Keys() {
			shell[k] = true
		}
	}
	for _, w := range []int{60, 120} {
		var m tea.Model = m0
		m = sized(m, w, 32)
		m, _ = m.Update(boardMsg{report: fixtureBoard()})
		sec := m0.active().(*boardSection)
		if sec.Capturing() {
			t.Fatal("the list does not capture")
		}
		own := keyStrings(sec.Bindings())
		for _, k := range []string{"n", "c"} {
			found := false
			for _, o := range own {
				found = found || o == k
			}
			if !found {
				t.Errorf("%d: %s is not offered", w, k)
			}
		}
		for _, k := range own {
			if shell[k] && k != "esc" {
				t.Errorf("%d: the Board binds %q, which is the shell's", w, k)
			}
			if k == "d" || k == "y" {
				t.Errorf("%d: the Board binds %q, a describe key", w, k)
			}
		}
	}
	// the form, the check, the confirm and the outcome all capture, so the shell's keys are never
	// reached in them
	for mode, setup := range map[string]func(*boardSection){
		"form":    func(s *boardSection) { bsend(s, "n") },
		"confirm": func(s *boardSection) { toConfirm(t, s, "dev", "x") },
	} {
		s, _, _ := composeRig(t, 100, 32)
		setup(s)
		if !s.Capturing() {
			t.Errorf("%s does not capture", mode)
		}
	}
}

func TestNoKeyAppliesBeforeTheConfirmScreen(t *testing.T) {
	keys := []string{"y", "Y", "enter", "tab", "ctrl+y", "ctrl+s", "pgdown", "n"}
	for _, stage := range []string{"channel", "text", "check"} {
		s, f, au := composeRig(t, 100, 40)
		bsend(s, "n")
		btype(s, "dev")
		if stage != "channel" {
			bsend(s, "enter")
			btype(s, "hello")
		}
		if stage == "check" {
			_, cmd := s.Update(bk("ctrl+s"))
			if s.mode != bmCheck || cmd == nil {
				t.Fatalf("%s: not checking: %d", stage, s.mode)
			}
		}
		for _, k := range keys {
			msg := bk(k)
			if k == "ctrl+y" {
				msg = tea.KeyMsg{Type: tea.KeyCtrlY}
			}
			s.Update(msg) // a command a key returns is not run: only a confirm screen may start the write
			if len(f.ran) != 0 || au.n != 0 || s.mode == bmBusy {
				t.Fatalf("%s: %q applied (mode %d)", stage, k, s.mode)
			}
			if s.mode == bmConfirm {
				break // the check was never answered here, so this is not reachable; stop if it is
			}
		}
	}
}

func TestAnswersOfAnAbandonedCheckOrWriteAreDropped(t *testing.T) {
	s, f, _ := composeRig(t, 100, 40)
	bsend(s, "n")
	btype(s, "dev")
	bsend(s, "enter")
	btype(s, "hi")
	s.Update(bk("ctrl+s"))
	late := boardDescribedMsg{seq: s.seq, lines: []string{"late"}}
	bsend(s, "esc") // back to the draft: the check is abandoned
	if s.mode != bmForm {
		t.Fatalf("%d", s.mode)
	}
	s.Update(late)
	if s.mode != bmForm || strings.Contains(strings.Join(s.planCtx, ""), "late") {
		t.Fatalf("a late answer must not open the confirm screen: mode %d", s.mode)
	}
	s.Update(boardAppliedMsg{seq: s.seq, res: core.BoardResult{ID: "5"}})
	if s.mode != bmForm || s.notice != "" || len(f.ran) != 0 {
		t.Fatalf("an applied answer nobody asked for changes nothing: mode %d %q", s.mode, s.notice)
	}
}
