package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// ---- a live Tasks tab over fakes and a clock the test moves ----

type liveClock struct{ t time.Time }

func (c *liveClock) now() time.Time          { return c.t }
func (c *liveClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// liveProv is every seam the live Tasks tab reads through, over data the test edits. Nothing here
// runs a process or touches a tracker.
type liveProv struct {
	clock   *liveClock
	snap    core.TaskSnapshot
	fp      string
	fpErr   error
	fpCalls int
	reads   int           // full snapshot reads
	took    time.Duration // how long a read takes on the fake clock
	during  func()        // runs inside a read (a write landing while it is read)
	hist    map[string]core.TaskHistoryReport
	histErr error
	histN   int
}

func (p *liveProv) ID() string                                      { return "bd" }
func (p *liveProv) Name() string                                    { return "bd" }
func (p *liveProv) Detect(context.Context) core.Detection           { return core.Detection{State: core.Ready} }
func (p *liveProv) Doctor(context.Context) []core.Check             { return nil }
func (p *liveProv) Status(context.Context) core.Status              { return core.Status{} }
func (p *liveProv) Tasks(context.Context) (core.TasksReport, error) { return demo.TasksReport(), nil }
func (p *liveProv) Snapshot(context.Context) (core.TaskSnapshot, error) {
	p.reads++
	if p.during != nil {
		p.during()
	}
	p.clock.advance(p.took)
	s := p.snap
	s.ReadAt = p.clock.now()
	return s, nil
}
func (p *liveProv) Fingerprint(context.Context) (string, error) {
	p.fpCalls++
	return p.fp, p.fpErr
}
func (p *liveProv) History(_ context.Context, id string, _ int) (core.TaskHistoryReport, error) {
	p.histN++
	if p.histErr != nil {
		return core.TaskHistoryReport{}, p.histErr
	}
	return p.hist[id], nil
}

// edit changes one task of the fake tracker (a write by someone else).
func (p *liveProv) edit(id string, f func(*core.TaskIssue)) {
	is := append([]core.TaskIssue(nil), p.snap.Issues...)
	for i := range is {
		if is[i].ID == id {
			f(&is[i])
		}
	}
	p.snap.Issues = is
}

type liveRig struct {
	t *testing.T
	s *tasksSection
	p *liveProv
	c *liveClock
}

func newLiveRig(t *testing.T, w, h int) *liveRig {
	t.Helper()
	c := &liveClock{t: goldenNow}
	old := nowFn
	nowFn = c.now
	t.Cleanup(func() { nowFn = old })
	p := &liveProv{clock: c, snap: fixtureSnapshot(), fp: "fp-1", took: 50 * time.Millisecond}
	s := newTasksSection(core.Providers{Tasks: p, Source: p, Fingerprint: p, History: p}, &config{})
	s.SetTheme(asciiTheme())
	s.tick = func() tea.Cmd { return nil }
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	r := &liveRig{t: t, s: s, p: p, c: c}
	r.run(s.Init())
	r.run(s.activate())
	send(s, "T")
	return r
}

// run executes a command tree and feeds every message it produces back to the section.
func (r *liveRig) run(cmd tea.Cmd) {
	r.t.Helper()
	for i := 0; cmd != nil && i < 50; i++ {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				r.run(c)
			}
			return
		}
		if msg == nil {
			return
		}
		_, cmd = r.s.Update(msg)
	}
}

// tick is one poll tick after d on the fake clock.
func (r *liveRig) tick(d time.Duration) {
	r.t.Helper()
	r.c.advance(d)
	r.run(func() tea.Msg { return tasksTickMsg{} })
}

func TestLiveUnchangedTrackerCostsOneCheapCheckAndNoRead(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	reads, checks := r.p.reads, r.p.fpCalls
	for i := 0; i < 6; i++ {
		r.tick(tasksPollEvery)
	}
	if r.p.reads != reads {
		t.Errorf("an unchanged tracker was read %d more times", r.p.reads-reads)
	}
	if r.p.fpCalls-checks != 6 {
		t.Errorf("%d fingerprint checks for 6 intervals", r.p.fpCalls-checks)
	}
	if strings.Contains(r.s.View(), "· stale ") {
		t.Errorf("a tracker confirmed unchanged every interval is not stale:\n%s", r.s.View())
	}
	if r.s.changedMarker() != "" {
		t.Error("nothing changed, so no marker")
	}
}

func TestLiveChangeIsReadOnceAndMarkedAndFollowed(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	before := r.p.reads
	r.c.advance(time.Second)
	r.p.edit("ac-09", func(i *core.TaskIssue) { i.Title = "queue attention digest, renamed"; i.UpdatedAt = r.c.now() })
	r.p.fp = "fp-2"
	r.tick(tasksPollEvery)
	if r.p.reads != before+1 {
		t.Fatalf("a changed fingerprint is one read, got %d", r.p.reads-before)
	}
	if m := r.s.changedMarker(); m != "● 1 changed just now" {
		t.Errorf("marker = %q", m)
	}
	if !strings.Contains(r.s.View(), "● 1 changed just now") {
		t.Errorf("the panel title carries the marker:\n%s", r.s.View())
	}
	r.tick(tasksPollEvery) // the same fingerprint again: no second read
	if r.p.reads != before+1 {
		t.Errorf("the same fingerprint was read again")
	}
	send(r.s, "F")
	if cur, _ := r.s.current(); cur.ID != "ac-09" || !strings.Contains(r.s.notice, "following ac-09") {
		t.Errorf("F follows to ac-09: cursor %q notice %q", cur.ID, r.s.notice)
	}
	r.c.advance(changedFor + time.Second)
	if r.s.changedMarker() != "" {
		t.Error("the marker expires")
	}
	send(r.s, "j")
	send(r.s, "F") // but F still finds the most recent change
	if cur, _ := r.s.current(); cur.ID != "ac-09" {
		t.Errorf("F after the marker expired: %q", cur.ID)
	}
}

func TestLiveFollowWhenNothingChangedOrTheTaskIsFilteredOut(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	send(r.s, "F")
	if !strings.Contains(r.s.notice, "nothing has changed") {
		t.Errorf("notice %q", r.s.notice)
	}
	r.c.advance(time.Second)
	r.p.edit("ac-09", func(i *core.TaskIssue) { i.Priority = 4; i.UpdatedAt = r.c.now() })
	r.p.fp = "fp-2"
	r.tick(tasksPollEvery)
	send(r.s, "/")
	typeIn(r.s, "label:ui")
	send(r.s, "enter")
	send(r.s, "F")
	if !strings.Contains(r.s.notice, "not in this list") {
		t.Errorf("a changed task hidden by the query is said so: %q", r.s.notice)
	}
}

func TestLiveSelectionSurvivesAChangeByID(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	send(r.s, "j", "j", "j")
	was, _ := r.s.current()
	r.c.advance(time.Second)
	r.p.edit("ac-30", func(i *core.TaskIssue) { i.Priority = 0; i.UpdatedAt = r.c.now() }) // reorders the list
	r.p.fp = "fp-2"
	r.tick(tasksPollEvery)
	if now, _ := r.s.current(); now.ID != was.ID {
		t.Errorf("the cursor moved from %s to %s", was.ID, now.ID)
	}
}

// Never two reads at once: while a check's read is in flight no further check starts, however
// many ticks arrive.
func TestLiveNeverOverlapsReads(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.p.fp = "fp-2"
	r.c.advance(tasksPollEvery)
	cmd := r.s.livePoll() // the check starts; its answer is held back
	if cmd == nil {
		t.Fatal("a due check starts")
	}
	checks := r.p.fpCalls
	for i := 0; i < 5; i++ {
		r.c.advance(tasksPollEvery)
		if c := r.s.livePoll(); c != nil {
			t.Fatal("a second check started while the first is unanswered")
		}
	}
	_, read := r.s.Update(cmd()) // the answer: changed, so a read starts
	snapStarted := r.s.snapBusy
	if read == nil || !snapStarted {
		t.Fatal("the changed fingerprint starts a read")
	}
	for i := 0; i < 5; i++ {
		r.c.advance(tasksPollEvery)
		if c := r.s.livePoll(); c != nil {
			t.Fatal("a check started while a read is in flight")
		}
	}
	if r.p.fpCalls != checks+1 {
		t.Errorf("fingerprint reads: %d", r.p.fpCalls-checks)
	}
	r.run(read)
	if r.s.pollEvery() <= tasksPollEvery { // the check took 30s on the fake clock: a slow read, so the wait doubled
		t.Errorf("wait after a very slow check = %v", r.s.pollEvery())
	}
	r.c.advance(r.s.pollEvery())
	if r.s.livePoll() == nil {
		t.Error("after the read answered the next check is due")
	}
}

// A read the user asked for (r, or after a write) is in flight: the poll never starts a check beside it.
func TestLiveNoCheckWhileAUserReadIsInFlight(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	cmd := r.s.fetchTasks() // r pressed: both reads are started, not yet answered
	r.c.advance(time.Hour)
	if c := r.s.livePoll(); c != nil {
		t.Fatal("a check started beside a read in flight")
	}
	r.run(cmd)
	if r.s.livePoll() == nil {
		t.Error("after the reads answered a check is due again")
	}
}

func TestLiveSlowReadBacksOffThenRecovers(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.p.took = 3 * time.Second // slower than tasksSlowRead
	r.p.fp = "fp-2"
	r.tick(tasksPollEvery)
	if r.s.pollEvery() != 2*tasksPollEvery {
		t.Fatalf("after a slow read the wait is %v", r.s.pollEvery())
	}
	reads := r.p.reads
	r.p.took = 10 * time.Millisecond
	r.p.fp = "fp-3"
	r.tick(tasksPollEvery) // 5s after: still inside the doubled wait
	if r.p.reads != reads {
		t.Error("polled again inside the backed-off wait")
	}
	r.tick(tasksPollEvery + time.Second) // now past it
	if r.p.reads != reads+1 {
		t.Errorf("not polled after the wait: %d", r.p.reads-reads)
	}
	if r.s.pollEvery() != tasksPollEvery {
		t.Errorf("a quick read returns to the base wait, got %v", r.s.pollEvery())
	}
}

func TestLiveFingerprintFailureBacksOffAndNeverReads(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	reads := r.p.reads
	r.p.fpErr = errors.New("permission denied")
	r.tick(tasksPollEvery)
	if r.p.reads != reads || r.s.pollEvery() != 2*tasksPollEvery {
		t.Errorf("a failed check reads nothing and waits longer: reads %d wait %v", r.p.reads-reads, r.s.pollEvery())
	}
}

func TestLiveHiddenTabNeverChecksAndResumesAtOnce(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.s.deactivate()
	checks := r.p.fpCalls
	for i := 0; i < 5; i++ {
		r.tick(tasksPollEvery)
	}
	if r.p.fpCalls != checks {
		t.Errorf("a hidden tab made %d checks", r.p.fpCalls-checks)
	}
	r.p.fp = "fp-2"
	r.c.advance(time.Second)
	r.run(r.s.activate())
	if r.p.fpCalls == checks {
		t.Errorf("showing the tab checks at once")
	}
	if r.p.reads < 2 {
		t.Error("and reads the change it finds")
	}
}

// A write that lands while the tracker is being read is not lost: the fingerprint kept is the one
// taken before the read began, so the next check sees the difference.
func TestLiveWriteDuringARead(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.p.fp = "fp-2"
	r.p.during = func() { r.p.fp = "fp-3"; r.p.during = nil }
	r.tick(tasksPollEvery)
	reads := r.p.reads
	r.tick(tasksPollEvery)
	if r.p.reads != reads+1 {
		t.Errorf("the write that landed mid-read was not picked up: %d", r.p.reads-reads)
	}
}

func TestLiveWithoutACheapFingerprintReadsSlowly(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.p.fpErr = core.ErrNoFingerprint
	reads := r.p.reads
	r.tick(tasksPollEvery) // the first check learns there is no fingerprint and reads
	if r.p.reads != reads+1 || r.s.pollEvery() != tasksSlowPollEvery {
		t.Fatalf("reads %d wait %v", r.p.reads-reads, r.s.pollEvery())
	}
	r.tick(tasksSlowPollEvery - time.Second)
	r.tick(time.Second)
	if r.p.reads != reads+2 {
		t.Errorf("one full read per slow interval, got %d", r.p.reads-reads)
	}
	if strings.Contains(r.s.View(), "· stale ") {
		t.Error("not stale within its own interval")
	}
}

func TestLiveMarkerAndFollowAtBothSizes(t *testing.T) {
	for _, sz := range [][2]int{{100, 32}, {60, 24}} {
		r := newLiveRig(t, sz[0], sz[1])
		r.c.advance(time.Second)
		r.p.edit("ac-12", func(i *core.TaskIssue) { i.Status = "blocked"; i.UpdatedAt = r.c.now() })
		r.p.fp = "fp-2"
		r.tick(tasksPollEvery)
		out := r.s.View()
		if !strings.Contains(out, "changed") {
			t.Errorf("%dx%d: no marker:\n%s", sz[0], sz[1], out)
		}
		for _, l := range strings.Split(out, "\n") {
			if w := len([]rune(l)); w > sz[0]+8 { // runes, not cells: a loose bound; the grid test is exact
				t.Errorf("%dx%d: line of %d runes", sz[0], sz[1], w)
			}
		}
	}
}

// ---- history in the detail pane ----

func histFixture(t *testing.T) map[string]core.TaskHistoryReport {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", "history-events.json"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := taskview.ParseHistory(b, "ac-12", 50, goldenNow)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rep.Events { // the fixture is a few minutes old; make it read as a few days
		rep.Events[i].At = goldenNow.Add(-time.Duration(i+1) * 26 * time.Hour)
	}
	hostile, _ := taskview.ParseHistory([]byte(`[{"event_type":"upda\u001b[2Jted","actor":"evil\u202e","new_value":"{\"title\":\"\\u001b]0;pwned\\u0007\"}","created_at":"2026-11-23T05:00:00Z"}]`), "ac-09", 50, goldenNow)
	return map[string]core.TaskHistoryReport{"ac-12": rep, "ac-09": hostile}
}

func (r *liveRig) selectTask(id string) {
	r.t.Helper()
	r.s.selected = id
	r.s.reanchor(r.s.ids(), true)
	if cur, _ := r.s.current(); cur.ID != id {
		r.t.Fatalf("cannot select %s (list %s)", id, rowIDs(r.s))
	}
}

func TestHistoryShowsEventsNewestFirstInTheDetail(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.p.hist = histFixture(t)
	r.selectTask("ac-12")
	if strings.Contains(r.s.View(), "History") {
		t.Fatal("history is off until H")
	}
	_, cmd := r.s.Update(keyRunes("H"))
	_ = r.s.View() // drawn once before the answer arrives, as the real loop does
	r.run(cmd)
	out := r.s.View()
	for _, want := range []string{"History", "reopened", "closed", "shipped", "dependency_added", "1d"} {
		if !strings.Contains(out, want) {
			t.Errorf("history lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "reopened") > strings.Index(out, "closed") {
		t.Error("newest first")
	}
	if !strings.Contains(out, "8d   created") { // the pane is scrolled to the heading, so the oldest event is in view too
		t.Errorf("the whole history is in view once it is read:\n%s", out)
	}
	if r.p.histN != 1 {
		t.Errorf("%d history reads", r.p.histN)
	}
	// moving away and back reads nothing more under the same snapshot
	send(r.s, "esc")
	_, cmd = r.s.Update(keyRunes("j"))
	r.run(cmd)
	_, cmd = r.s.Update(keyRunes("k"))
	r.run(cmd)
	if r.p.histN > 2 {
		t.Errorf("%d history reads for two tasks", r.p.histN)
	}
	_, cmd = r.s.Update(keyRunes("H")) // off again
	r.run(cmd)
	if strings.Contains(r.s.View(), "History") {
		t.Error("H toggles it off")
	}
}

func TestHistoryEmptyErrorAndUnavailable(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.selectTask("ac-12")
	_, cmd := r.s.Update(keyRunes("H"))
	r.run(cmd)
	if out := r.s.View(); !strings.Contains(out, "no history here") {
		t.Errorf("an empty history says so:\n%s", out)
	}
	_, cmd = r.s.Update(keyRunes("H"))
	r.run(cmd)
	r.p.histErr = errors.New("bd history exited 1")
	r.s.histClear()
	_, cmd = r.s.Update(keyRunes("H"))
	r.run(cmd)
	if out := r.s.View(); !strings.Contains(out, "could not read the history") {
		t.Errorf("a failed read says why:\n%s", out)
	}
	s := newTasksSection(core.Providers{}, &config{})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	send(s, "H")
	if !strings.Contains(s.notice, "not available") {
		t.Errorf("no history source: %q", s.notice)
	}
}

func TestHistoryIsRereadAfterANewSnapshotAndNeverTwiceAtOnce(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.p.hist = histFixture(t)
	r.selectTask("ac-12")
	_, cmd := r.s.Update(keyRunes("H"))
	if cmd == nil || !r.s.hist.busy {
		t.Fatal("H starts a read")
	}
	if c := r.s.histEnsure(); c != nil {
		t.Error("a second history read started while one is in flight")
	}
	r.run(cmd)
	n := r.p.histN
	r.c.advance(time.Second)
	r.p.edit("ac-12", func(i *core.TaskIssue) { i.Priority = 3; i.UpdatedAt = r.c.now() })
	r.p.fp = "fp-2"
	r.tick(tasksPollEvery)
	if r.p.histN != n+1 {
		t.Errorf("a new snapshot makes the shown history stale: %d reads", r.p.histN-n)
	}
}

func TestHistoryHostileTextIsRenderedInert(t *testing.T) {
	for _, sz := range [][2]int{{100, 32}, {60, 24}} {
		r := newLiveRig(t, sz[0], sz[1])
		r.p.hist = histFixture(t)
		r.selectTask("ac-09")
		_, cmd := r.s.Update(keyRunes("H"))
		r.run(cmd)
		out := r.s.View()
		for _, c := range out {
			if c == 0x1b || c == 0x07 || c == 0x202e {
				t.Fatalf("%dx%d: control or bidi character %q in the render", sz[0], sz[1], c)
			}
		}
		if !strings.Contains(out, "History") {
			t.Errorf("%dx%d: no history shown:\n%s", sz[0], sz[1], out)
		}
	}
}

func TestHistoryNarrowOpensTheDetailAndShowsTheHeading(t *testing.T) {
	r := newLiveRig(t, 60, 24)
	r.p.hist = histFixture(t)
	r.selectTask("ac-12")
	_, cmd := r.s.Update(keyRunes("H"))
	r.run(cmd)
	out := r.s.View()
	if !r.s.drilled || !strings.Contains(out, "History") || !strings.Contains(out, "closed") {
		t.Errorf("drilled=%v:\n%s", r.s.drilled, out)
	}
}
