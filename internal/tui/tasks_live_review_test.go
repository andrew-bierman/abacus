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
)

// slowIns is an insights provider whose read is a no-op: only the message it produces matters.
type slowIns struct{}

func (slowIns) Insights(context.Context) (core.InsightsReport, error) {
	return core.InsightsReport{Empty: true}, nil
}

// runAll executes a command and feeds each message it produces to the model, and the commands those
// produce in turn (run, the older helper, stops after one step).
func runAll(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	for i := 0; cmd != nil && i < 100; i++ {
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				m = runAll(t, m, c)
			}
			return m
		case nil:
			return m
		default:
			m, cmd = m.Update(msg)
		}
	}
	return m
}

// ---- 1: a switch while reads are in flight ----

// Every read of the old tracker is in flight (snapshot, summary, insights, history) when the switch
// happens. Their answers arrive after the new tracker's: none may paint old data under the new title,
// and the snapshot must not be diffed against the new one (a mass "changed" marker).
func TestSwitchWhileEveryReadIsInFlight(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.s.ins = slowIns{}
	r.p.hist = histFixture(t)
	r.selectTask("ac-12")
	_, hcmd := r.s.Update(keyRunes("H"))
	r.run(hcmd) // history shown and loaded for the old tracker
	r.s.histClear()
	r.s.hist.cache = nil
	// the old tracker's reads start, and run to completion at their source, but are not delivered yet
	r.p.snap.Tracker = "old"
	oldReads := []tea.Cmd{r.s.fetchTasks(), r.s.fetchInsights(), r.s.histEnsure()}
	var old []tea.Msg
	var collect func(c tea.Cmd)
	collect = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch m := c().(type) {
		case tea.BatchMsg:
			for _, x := range m {
				collect(x)
			}
		case nil:
		default:
			old = append(old, m)
		}
	}
	for _, c := range oldReads {
		collect(c)
	}
	if len(old) < 4 {
		t.Fatalf("expected snapshot, summary, insights and history answers in flight, got %d", len(old))
	}
	// the switch: a different tracker with different tasks
	other := fixtureSnapshot()
	other.Tracker = "newtracker"
	other.Issues = other.Issues[:3]
	r.p.snap = other
	r.s.resetForTracker()
	r.run(r.s.refresh()) // the new read waits behind the old snapshot read (snapDirty)
	for _, m := range old {
		if _, ok := m.(taskSnapshotMsg); ok { // its answer is dropped and starts the deferred read
			_, c := r.s.Update(m)
			r.run(c)
		}
	}
	if !strings.Contains(r.s.View(), "newtracker") || r.s.list.rep.Total != 3 {
		t.Fatalf("the new tracker is on screen:\n%s", r.s.View())
	}
	for _, m := range old { // the other old answers arrive late, after the new tracker's data
		if _, ok := m.(taskSnapshotMsg); !ok {
			r.s.Update(m)
		}
	}
	out := r.s.View()
	if !strings.Contains(out, "newtracker") || strings.Contains(out, "Tasks · old") || r.s.list.rep.Total != 3 {
		t.Errorf("old data was painted under the new tracker:\n%s", out)
	}
	if m := r.s.changedMarker(); m != "" {
		t.Errorf("a phantom change marker after the switch: %q", m)
	}
	if r.s.insights != nil && !r.s.insights.Empty {
		t.Error("old insights were taken")
	}
	if r.s.snapBusy {
		t.Error("the snapshot read is over: snapBusy must be clear")
	}
	if r.s.live.newest != "" {
		t.Errorf("F target %q came from the old tracker", r.s.live.newest)
	}
}

// A read asked for while an old-tracker read is in flight is started when the old one answers, and
// the old answer itself is dropped.
func TestStaleSnapshotAnswerStillHonorsSnapDirty(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	oldCmd := r.s.fetchSnapshot() // in flight
	var oldMsg tea.Msg = oldCmd()
	r.s.resetForTracker()
	cmd := r.s.refresh() // asks for a read while the old one is unanswered: snapDirty
	if !r.s.snapDirty {
		t.Fatal("the new read is deferred behind the old one")
	}
	_ = cmd
	reads := r.p.reads
	_, next := r.s.Update(oldMsg)
	if r.s.snapBusy != true || next == nil {
		t.Fatalf("the dropped answer must start the deferred read: busy=%v cmd=%v", r.s.snapBusy, next)
	}
	r.run(next)
	if r.p.reads != reads+1 || r.s.snapDirty || r.s.list.view == nil {
		t.Errorf("reads %d dirty %v", r.p.reads-reads, r.s.snapDirty)
	}
}

// ---- 2: a fingerprint that hangs or fails never delays the export ----

type hangFP struct{ *liveProv }

func (h hangFP) Fingerprint(ctx context.Context) (string, error) {
	<-ctx.Done() // a hung filesystem: it only returns when its deadline does
	return "", ctx.Err()
}

func TestSnapshotReadGoesAheadWhenTheFingerprintHangs(t *testing.T) {
	old := fingerprintTimeout
	fingerprintTimeout = 50 * time.Millisecond
	t.Cleanup(func() { fingerprintTimeout = old })
	r := newLiveRig(t, 100, 32)
	r.s.live.fps = hangFP{r.p}
	start := time.Now()
	msg := r.s.fetchSnapshot()()
	m, ok := msg.(taskSnapshotMsg)
	if !ok || m.err != nil || m.fp != "" || len(m.snap.Issues) == 0 {
		t.Fatalf("%T %+v", msg, m)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the export waited %v for a hung fingerprint", d)
	}
}

// ---- 3: the fallback and its recovery ----

func TestNoFingerprintIsLatchedOnlyForNotEmbeddedAndClearsWhenOneWorks(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.p.fpErr = core.ErrFingerprintFailed // an I/O failure: not "no fingerprint"
	r.tick(tasksPollEvery)
	if r.s.live.noFP || r.s.live.poll.Base != tasksPollEvery {
		t.Fatal("an I/O failure demoted the session to the slow poll")
	}
	r.p.fpErr = core.ErrNoFingerprint
	r.tick(2 * tasksPollEvery)
	if !r.s.live.noFP || r.s.pollEvery() < tasksSlowPollEvery {
		t.Fatalf("not embedded: latched=%v wait=%v", r.s.live.noFP, r.s.pollEvery())
	}
	r.p.took = 3 * time.Second // a slow read in the fallback must not back off below its 30 s base
	r.tick(tasksSlowPollEvery)
	if r.s.pollEvery() < tasksSlowPollEvery {
		t.Errorf("the wait fell to %v, below the fallback's base", r.s.pollEvery())
	}
	r.p.took, r.p.fpErr, r.p.fp = time.Millisecond, nil, "fp-ok"
	r.tick(tasksSlowPollMax)
	if r.s.live.noFP || r.s.live.poll.Base != tasksPollEvery {
		t.Errorf("a fingerprint that works again ends the fallback: latched=%v base=%v", r.s.live.noFP, r.s.live.poll.Base)
	}
}

func TestPollEveryIsFiveSecondsByLiteral(t *testing.T) {
	if tasksPollEvery != 5*time.Second || tasksPollMax != time.Minute || tasksSlowRead != 2*time.Second || tasksSlowPollEvery != 30*time.Second {
		t.Errorf("the poll constants moved: %v %v %v %v", tasksPollEvery, tasksPollMax, tasksSlowRead, tasksSlowPollEvery)
	}
}

// ---- 4: a saved filter that no longer parses ----

func TestApplyingASavedFilterThatNoLongerParsesKeepsTheQueryInForce(t *testing.T) {
	s, store, _ := filterSection(t, 100, 32)
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	// a file edited by hand, or written by a build with another grammar
	if err := os.WriteFile(store.Path, []byte(`{"schema_version":1,"filters":[{"name":"old","query":"nofield:x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	send(s, "/")
	typeIn(s, "label:ui")
	send(s, "enter")
	rows := rowIDs(s)
	ctrl(s, tea.KeyCtrlO)
	send(s, "enter")
	if s.list.text != "label:ui" || rowIDs(s) != rows {
		t.Errorf("the query bar says %q and the rows %q: the old query must stay whole", s.list.text, rowIDs(s))
	}
	if s.notice != `filter "old" no longer parses` {
		t.Errorf("notice %q", s.notice)
	}
	if !strings.Contains(s.View(), "/label:ui") {
		t.Errorf("the bar still shows the query in force:\n%s", s.View())
	}
}

// ---- 5: search over a live Tasks tab ----

func TestSearchOverlayOnAQuietLiveTabNeverGoesStale(t *testing.T) {
	c := &liveClock{t: goldenNow}
	old := nowFn
	nowFn = c.now
	t.Cleanup(func() { nowFn = old })
	p := &liveProv{clock: c, snap: fixtureSnapshot(), fp: "fp-1", took: 10 * time.Millisecond}
	mm := New([]core.Entry{{Module: p, Detection: core.Detection{State: core.Ready}}}, WithTheme(asciiTheme()), WithAudit(&memAudit{}), WithFullView(noFullView), WithSearchHistory(core.SearchHistory{}))
	mm.tabs[mm.tabIndex(core.DomainTasks.Title())].sec.(*tasksSection).tick = func() tea.Cmd { return nil } // never sleep in a test
	var m tea.Model = mm
	m = sized(m, 100, 32)
	m, _ = m.Update(tasksMsg{report: demo.TasksReport()})
	m, _ = m.Update(taskSnapshotMsg{snap: p.snap})
	m = press(m, "2")
	for _, k := range ctrlK("lease") {
		m = press(m, k)
	}
	if !overlayOpen(m) {
		t.Fatal("the overlay is open")
	}
	for i := 0; i < 8; i++ { // 40 seconds with the overlay open: every check finds the tracker unchanged
		c.advance(tasksPollEvery)
		m = runAll(t, m, func() tea.Msg { return tasksTickMsg{} })
		if strings.Contains(m.View(), "may be out of date") {
			t.Fatalf("after %d s the Tasks hits turned stale though the tab is live:\n%s", 5*(i+1), m.View())
		}
	}
}

// ---- 6: own writes and F in the tree ----

func TestOwnWriteIsNotAChangeBySomeoneElse(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.c.advance(time.Second)
	r.p.edit("ac-09", func(i *core.TaskIssue) { i.Title = "mine"; i.UpdatedAt = r.c.now() })
	r.p.fp = "fp-2"
	_, cmd := r.s.Update(taskAppliedMsg{res: core.TaskResult{ID: "ac-09", Title: "mine"}, op: core.TaskUpdate})
	r.run(cmd)
	if m := r.s.changedMarker(); m != "" || r.s.live.newest != "" {
		t.Errorf("the user's own write was marked as a change: %q newest %q", m, r.s.live.newest)
	}
	r.c.advance(time.Second)
	r.p.edit("ac-30", func(i *core.TaskIssue) { i.Title = "theirs"; i.UpdatedAt = r.c.now() })
	r.p.fp = "fp-3"
	r.tick(tasksPollEvery)
	if m := r.s.changedMarker(); m != "● 1 changed just now" || r.s.live.newest != "ac-30" {
		t.Errorf("someone else's change after it is marked: %q newest %q", m, r.s.live.newest)
	}
}

func TestFollowUnfoldsAFoldedParentInTheTree(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	send(r.s, "T") // back to the tree (the rig opens the flat list)
	if !r.s.treeOn() {
		t.Skip("this fixture has no tree")
	}
	r.selectTask("ac-01")
	send(r.s, "h") // fold the epic
	for _, row := range r.s.rows() {
		if row.ID == "ac-12" {
			t.Fatal("ac-12 should be hidden by the fold")
		}
	}
	r.c.advance(time.Second)
	r.p.edit("ac-12", func(i *core.TaskIssue) { i.Priority = 4; i.UpdatedAt = r.c.now() })
	r.p.fp = "fp-2"
	r.tick(tasksPollEvery)
	send(r.s, "F")
	if cur, _ := r.s.current(); cur.ID != "ac-12" || !strings.Contains(r.s.notice, "unfolded its parents") {
		t.Errorf("cursor %q notice %q: F opens the way to a task inside a folded parent", cur.ID, r.s.notice)
	}
}

// ---- 8: marker lifetime, ordering, Init ----

func TestMarkerLifetimeBoundaries(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	r.c.advance(time.Second)
	r.p.edit("ac-09", func(i *core.TaskIssue) { i.Priority = 4; i.UpdatedAt = r.c.now() })
	r.p.fp = "fp-2"
	r.tick(tasksPollEvery)
	at := r.s.live.changedAt
	for _, c := range []struct {
		after time.Duration
		want  string
	}{
		{0, "● 1 changed just now"}, {9 * time.Second, "● 1 changed just now"}, {10 * time.Second, "● 1 changed 10s ago"},
		{29 * time.Second, "● 1 changed 29s ago"}, {30 * time.Second, "● 1 changed 30s ago"}, {31 * time.Second, ""},
	} {
		r.c.t = at.Add(c.after)
		if got := r.s.changedMarker(); got != c.want {
			t.Errorf("after %v: %q, want %q", c.after, got, c.want)
		}
	}
	// a removal counts in the number
	r.c.t = at
	r.p.snap.Issues = r.p.snap.Issues[:len(r.p.snap.Issues)-1]
	r.p.fp = "fp-3"
	r.tick(tasksPollEvery)
	if m := r.s.changedMarker(); m != "● 1 changed just now" {
		t.Errorf("a removal alone is one change: %q", m)
	}
}

func TestAnOlderReadAfterANewerOneMakesNoPhantomMarker(t *testing.T) {
	r := newLiveRig(t, 100, 32)
	newer := r.p.snap
	newer.ReadAt = r.c.now().Add(time.Minute)
	r.s.Update(taskSnapshotMsg{snap: newer})
	older := fixtureSnapshot()
	older.ReadAt = r.c.now() // read earlier, answers later
	older.Issues = older.Issues[:4]
	r.s.Update(taskSnapshotMsg{snap: older})
	if m := r.s.changedMarker(); m != "" {
		t.Errorf("an older read answering late is not a change: %q", m)
	}
}

// Through Model.Init, not by calling deactivate: a Tasks tab that starts behind another does not run
// at the shown rhythm until it is shown.
func TestModelInitDeactivatesTabsThatStartHidden(t *testing.T) {
	p := &liveProv{clock: &liveClock{t: goldenNow}, snap: fixtureSnapshot(), fp: "x"}
	m := New([]core.Entry{{Module: p, Detection: core.Detection{State: core.Ready}}}, WithTheme(asciiTheme()), WithFullView(noFullView), WithSearchHistory(core.SearchHistory{}))
	tasks := m.tabs[m.tabIndex(core.DomainTasks.Title())].sec.(*tasksSection)
	if tasks.live.poll.Hidden() {
		t.Fatal("a section starts shown until the shell says otherwise")
	}
	_ = m.Init()
	if m.sel == m.tabIndex(core.DomainTasks.Title()) || !tasks.live.poll.Hidden() {
		t.Fatalf("Init must deactivate the Tasks tab while another is shown (sel %d hidden %v)", m.sel, tasks.live.poll.Hidden())
	}
	m.goTo(m.tabIndex(core.DomainTasks.Title()))
	if tasks.live.poll.Hidden() {
		t.Error("showing the tab activates it")
	}
}

// ---- :ctx NAME ----

func TestPromptCtxOpensTheSwitchersConfirmScreen(t *testing.T) {
	r := newSwitchRig(t, 100, 32)
	m := New([]core.Entry{{Module: tasksFake(), Detection: tasksFake().Det}}, WithTheme(asciiTheme()), WithAudit(&memAudit{}), WithFullView(noFullView))
	ti := m.tabIndex(core.DomainTasks.Title())
	m.tabs[ti].sec = r.s // the switcher rig's section stands in for the Tasks tab
	var tm tea.Model = m
	tm = sized(tm, 100, 32)
	tm = press(tm, "2")
	for _, k := range append([]string{":"}, append(typed("ctx beta"), "enter")...) {
		tm = press(tm, k)
	}
	if r.s.sw.mode != switchConfirm || !strings.Contains(tm.View(), "Switch tracker") {
		t.Fatalf(":ctx beta opens the confirm screen (mode %v):\n%s", r.s.sw.mode, tm.View())
	}
	if r.target.dir != "" {
		t.Error("nothing changes before y")
	}
	tm = press(tm, "n")
	r.s.sw.mode = switchNone
	for _, k := range append([]string{":"}, append(typed("ctx nosuch"), "enter")...) {
		tm = press(tm, k)
	}
	if !strings.Contains(tm.View(), "no known tracker called nosuch") || r.s.sw.mode != switchNone {
		t.Errorf("an unknown name says so:\n%s", tm.View())
	}
	_ = errors.New
}
