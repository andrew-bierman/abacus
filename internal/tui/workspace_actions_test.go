package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/rex"
)

// These tests run the real workspace adapter over a scripted runner: no Rex, no session and no
// socket is ever touched, and every command it would run is recorded.

const (
	tSessA = "session:01a20c1b-ae95-745d-ba13-9eaee76983fa"
	tBlk43 = "block:01a20c2b-ed0a-7802-a0b6-b446d8779a01"
	tAnch  = "block:01a20c1b-ae95-76c6-a459-fd28a1f35eda"
)

type wsResp struct {
	out  string
	exit int
	err  error
}

type wsRunner struct {
	mu    sync.Mutex
	resp  map[string]wsResp
	calls []string
}

func (r *wsRunner) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	k := strings.Join(args, " ")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, k)
	if v, ok := r.resp[k]; ok {
		return []byte(v.out), v.exit, v.err
	}
	return nil, -1, fmt.Errorf("no script for %q", k)
}

func (r *wsRunner) writes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if strings.Contains(c, " focus ") || strings.Contains(c, " run ") || strings.Contains(c, " split ") || strings.Contains(c, " new ") {
			out = append(out, c)
		}
	}
	return out
}

func (r *wsRunner) callsWith(s string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if strings.Contains(c, s) {
			n++
		}
	}
	return n
}

func rexFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "rex", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	kLs  = "--autostart=false ls --json"
	kVer = "--autostart=false version --json"
	kBlk = "--autostart=false block ls -s " + tSessA + " --json"
	kCap = "--autostart=false capture -s " + tSessA + " -b " + tBlk43 + " --trim"
	kFoc = "--autostart=false focus -s " + tSessA + " " + tBlk43
)

func newWSRunner(t *testing.T) *wsRunner {
	return &wsRunner{resp: map[string]wsResp{
		kLs:  {out: `{"sessions":[{"session_id":"` + tSessA + `","label":"General"}]}`},
		kVer: {out: rexFixture(t, "version-14.json")},
		kBlk: {out: `{"blocks":[{"block_id":"` + tBlk43 + `","label":"bd-43"},{"block_id":"` + tAnch + `","label":"shell"}]}`},
		kCap: {out: "building...\ntests passed\n"},
		kFoc: {out: ""},
	}}
}

type wsLeaseMod struct {
	*modtest.Fake
	rep core.LeasesReport
}

func (m wsLeaseMod) Leases(context.Context) (core.LeasesReport, error) { return m.rep, nil }

type wsAudit struct {
	mu    sync.Mutex
	lines []core.AuditEntry
	err   error
}

func (a *wsAudit) Record(e core.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.lines = append(a.lines, e)
	return nil
}

func (a *wsAudit) phases() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var p []string
	for _, l := range a.lines {
		p = append(p, l.Phase)
	}
	return strings.Join(p, ",")
}

// wsLive is the shell over a leases provider and the real workspace adapter, with the leases tab
// shown and the workspace read, at w x h.
func wsLive(t *testing.T, r *wsRunner, agent []string, w, h int) (tea.Model, *wsAudit, *rex.Module) {
	t.Helper()
	leases := wsLeaseMod{Fake: &modtest.Fake{IDStr: "beads-lease", Det: core.Detection{State: core.Ready}}, rep: core.LeasesReport{
		Header: core.Header{Source: core.SourceLive},
		Leases: []core.Lease{{Resource: "bd-43", State: core.LeaseHeld, Holder: "alice"}, {Resource: "bd-77", State: core.LeaseFree}},
	}}
	rx := rex.New(rex.Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Runner: r, Hostname: func() string { return "me" }, CacheTTL: -1,
		Getenv: func(k string) string { return map[string]string{"REX_SESSION": tSessA, "REX_BLOCK": tAnch}[k] }})
	entries := []core.Entry{{Module: leases, Detection: leases.Detect(context.Background())}, {Module: rx, Detection: rx.Detect(context.Background())}}
	a := &wsAudit{}
	var m tea.Model = NewWithHandoff(entries, (&wsRecorder{}).handoff, WithTheme(asciiTheme()), WithFullView(noFullView), WithAudit(a),
		WithAgentCommand(func() []string { return agent }), WithWorkDir(func() string { return "/work/proj" }), WithTab("leases"))
	m = sized(m, w, h)
	m.(*Model).hosts[0].(*leasesSection).tick = func() tea.Cmd { return nil } // no timers in a test
	m, cmd := m.Update(leasesMsg{read: readLeases(context.Background(), leases)})
	m = drive(t, m, cmd)
	return m, a, rx
}

func keys(m tea.Model, ks ...string) tea.Model {
	for _, k := range ks {
		m = press(m, k)
	}
	return m
}

func TestFocusIsPlannedShownExactlyConfirmedAppliedOnceAndAudited(t *testing.T) {
	r := newWSRunner(t)
	m, a, _ := wsLive(t, r, nil, 240, 40)
	if !strings.Contains(m.View(), "show terminal") {
		t.Fatalf("t is offered when the row has a terminal:\n%s", m.View())
	}
	m = keys(m, "t")
	v := m.View()
	for _, want := range []string{"bring the terminal for bd-43 to the front", "command: rex --autostart=false focus -s " + tSessA + " " + tBlk43, "y runs it", "n cancels"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the confirm screen shows %q:\n%s", want, v)
		}
	}
	if len(r.writes()) != 0 || a.phases() != "" {
		t.Fatalf("planning runs nothing and records nothing: %v %q", r.writes(), a.phases())
	}
	m, cmd := m.Update(runeKey("y"))
	m = drive(t, m, cmd)
	if w := r.writes(); len(w) != 1 || w[0] != kFoc {
		t.Fatalf("%v", w)
	}
	if a.phases() != "attempt,ok" || a.lines[0].Domain != "workspace" || a.lines[0].Verb != "focus" {
		t.Fatalf("%q %+v", a.phases(), a.lines)
	}
	if !strings.Contains(m.View(), "Brought the terminal for bd-43 to the front.") {
		t.Fatalf("%s", m.View())
	}
	m = keys(m, "x")                // any key closes the notice
	m, cmd = m.Update(runeKey("y")) // y with nothing on screen does nothing
	if drive(t, m, cmd); len(r.writes()) != 1 {
		t.Fatalf("a second y must not run again: %v", r.writes())
	}
}

func TestCancelRunsNothingAndSingleUseHoldsAgainstADoublePress(t *testing.T) {
	r := newWSRunner(t)
	m, a, _ := wsLive(t, r, nil, 120, 40)
	m, cmd := keys(m, "t").Update(runeKey("n"))
	m = drive(t, m, cmd)
	if len(r.writes()) != 0 || a.phases() != "" || !strings.Contains(m.View(), "cancelled: nothing was changed") {
		t.Fatalf("%v %q\n%s", r.writes(), a.phases(), m.View())
	}
	m = keys(m, "x", "t")
	m1, c1 := m.Update(runeKey("y"))
	m2, c2 := m1.Update(runeKey("y")) // the second press lands while the first is still running
	drive(t, m2, c2)
	drive(t, m2, c1)
	if len(r.writes()) != 1 {
		t.Fatalf("one plan is applied once: %v", r.writes())
	}
}

func TestAnUnknownOutcomeIsNeverCalledNotApplied(t *testing.T) {
	r := newWSRunner(t)
	r.resp[kFoc] = wsResp{err: core.ErrTimedOut}
	m, a, _ := wsLive(t, r, nil, 120, 40)
	m, cmd := keys(m, "t").Update(runeKey("y"))
	m = drive(t, m, cmd)
	v := m.View()
	if !strings.Contains(v, "result unknown") || strings.Contains(v, "not applied") || a.phases() != "attempt,unknown" {
		t.Fatalf("%q\n%s", a.phases(), v)
	}
	r.resp[kFoc] = wsResp{exit: 4}
	m = keys(m, "x", "t")
	m, cmd = m.Update(runeKey("y"))
	m = drive(t, m, cmd)
	if v := m.View(); !strings.Contains(v, "not applied: no workspace server is running") {
		t.Fatalf("%s", v)
	}
}

func TestAFailedAuditBlocksTheWriteInTheTui(t *testing.T) {
	r := newWSRunner(t)
	m, a, _ := wsLive(t, r, nil, 120, 40)
	a.err = errors.New("disk full")
	m, cmd := keys(m, "t").Update(runeKey("y"))
	m = drive(t, m, cmd)
	if len(r.writes()) != 0 || !strings.Contains(m.View(), "not applied") {
		t.Fatalf("fail closed: %v\n%s", r.writes(), m.View())
	}
}

func TestFocusIsInactiveWithoutATerminalAndSaysWhy(t *testing.T) {
	r := newWSRunner(t)
	m, _, _ := wsLive(t, r, nil, 120, 40)
	m = keys(m, "j") // bd-77 has no terminal
	if strings.Contains(m.View(), "show terminal") {
		t.Fatalf("t must not be offered:\n%s", m.View())
	}
	m = keys(m, "t")
	if v := m.View(); !strings.Contains(v, "No terminal is named for bd-77") || len(r.writes()) != 0 {
		t.Fatalf("%s", v)
	}
}

func TestWorkOnThisNeedsTheAgentCommandAndShowsHowToSetIt(t *testing.T) {
	r := newWSRunner(t)
	m, _, _ := wsLive(t, r, nil, 120, 40)
	m = keys(m, "W")
	if v := m.View(); !strings.Contains(v, "Set the agent command first") || !strings.Contains(v, "abacus config set agent_command") || len(r.writes()) != 0 {
		t.Fatalf("%s", v)
	}
}

func TestWorkOnThisShowsTheExactArgvAndSplitIsOneKeyAway(t *testing.T) {
	r := newWSRunner(t)
	agent := []string{"claude", "-p", "Work on {task_id}: {title}"}
	m, a, _ := wsLive(t, r, agent, 240, 40)
	m = keys(m, "j", "W") // bd-77
	v := m.View()
	if !strings.Contains(v, "open a new tab named bd-77 in session "+tSessA) || !strings.Contains(v, "nothing is typed into any existing terminal") ||
		!strings.Contains(v, "--label bd-77") || !strings.Contains(v, `-c /work/proj --keep-open --json -- claude -p "Work on bd-77: bd-77"`) {
		t.Fatalf("%s", v)
	}
	m = keys(m, "s")
	if v := m.View(); !strings.Contains(v, "a split beside this terminal") || !strings.Contains(v, " split -s "+tSessA+" -b "+tAnch+" --split=right --label bd-77") {
		t.Fatalf("%s", v)
	}
	k := "--autostart=false split -s " + tSessA + " -b " + tAnch + " --split=right --label bd-77 -c /work/proj --keep-open --json -- claude -p Work on bd-77: bd-77"
	r.resp[k] = wsResp{out: rexFixture(t, "split-json.json")}
	m, cmd := m.Update(runeKey("y"))
	m = drive(t, m, cmd)
	if w := r.writes(); len(w) != 1 || w[0] != k {
		t.Fatalf("each template word is one argument, and nothing else runs: %q", w)
	}
	if a.phases() != "attempt,ok" || a.lines[0].Verb != "work" || !strings.Contains(m.View(), "Opened a terminal for bd-77.") {
		t.Fatalf("%q\n%s", a.phases(), m.View())
	}
	if r.callsWith(" send") != 0 {
		t.Fatal("nothing is ever typed into a terminal")
	}
}

func TestAClippedConfirmScreenCannotBeApplied(t *testing.T) {
	r := newWSRunner(t)
	agent := []string{"claude", "-p", "Work on {task_id}: {title}"}
	m, a, _ := wsLive(t, r, agent, 60, 24)
	m = keys(m, "W")
	m = sized(m, 60, 12) // too short to show the whole plan
	m.View()             // a render always comes before the next key
	m, cmd := m.Update(runeKey("y"))
	drive(t, m, cmd)
	if len(r.writes()) != 0 || a.phases() != "" {
		t.Fatalf("a plan that is not shown whole is not applied: %v", r.writes())
	}
	if !strings.Contains(m.View(), "enlarge the terminal to review") {
		t.Fatalf("%s", m.View())
	}
}

func TestWhileAPlanIsOnScreenOnlyItsKeysAct(t *testing.T) {
	r := newWSRunner(t)
	m, _, _ := wsLive(t, r, nil, 120, 40)
	m = keys(m, "t")
	for _, k := range []string{"q", "j", "2", "W", "t", "tab"} {
		var cmd tea.Cmd
		m, cmd = m.Update(runeKey(k))
		if cmd != nil && k == "q" {
			if msg := cmd(); msg == tea.Quit() {
				t.Fatalf("q must not quit while a plan waits")
			}
		}
	}
	if !strings.Contains(m.View(), "y runs it") {
		t.Fatalf("the plan stays up:\n%s", m.View())
	}
	if hasKey(m.(*Model).ShortHelp(), "q") {
		t.Fatal("the footer offers only the plan's keys")
	}
}

func TestDemoAndNoProviderOfferNoActions(t *testing.T) {
	rec := &wsRecorder{}
	m := demoWorkspaceModel(t, asciiTheme(), rec, 100, 32)
	m = keys(m, "3")
	for _, k := range []string{"t", "W"} {
		var cmd tea.Cmd
		m, cmd = m.Update(runeKey(k))
		if cmd != nil || strings.Contains(m.View(), "y runs it") {
			t.Fatalf("%s acts in a demo", k)
		}
	}
	if strings.Contains(m.View(), "work on this") {
		t.Fatal("a demo does not offer actions")
	}
}

func TestTheTerminalOutputIsShownBoundedAndCleanAndRefreshedByThePoll(t *testing.T) {
	r := newWSRunner(t)
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	r.resp[kCap] = wsResp{out: strings.Join(lines, "\n") + "\n\x1b]0;pwn\x07\x1b[2Jevil \u202e text\n"}
	m, _, _ := wsLive(t, r, nil, 140, 40)
	v := m.View()
	if !strings.Contains(v, "the end of what the terminal shows") || !strings.Contains(v, "line 39") || strings.Contains(v, "line 20") {
		t.Fatalf("only the end is shown:\n%s", v)
	}
	for _, bad := range []string{"\x1b", "\u202e", "pwn\x07"} {
		if strings.Contains(v, bad) {
			t.Fatalf("control text reached the screen: %q", bad)
		}
	}
	if n := r.callsWith("capture"); n != 1 {
		t.Fatalf("one read so far: %d", n)
	}
	// The next workspace read (the poll) refreshes the output.
	r.resp[kCap] = wsResp{out: "fresh output\n"}
	m, cmd := m.Update(leasesTickMsg{})
	m = drive(t, m, cmd)
	if v := m.View(); !strings.Contains(v, "fresh output") || strings.Contains(v, "line 39") {
		t.Fatalf("%s", v)
	}
	// Moving to a row with no terminal clears it; no read is made for it.
	n := r.callsWith("capture")
	m = keys(m, "j")
	if v := m.View(); strings.Contains(v, "fresh output") || r.callsWith("capture") != n {
		t.Fatalf("%s", v)
	}
	// A failed read says so instead of showing stale text.
	r.resp[kCap] = wsResp{exit: 1}
	m, cmd = m.Update(runeKey("k"))
	m = drive(t, m, cmd)
	m, cmd = m.Update(leasesTickMsg{})
	m = drive(t, m, cmd)
	if v := m.View(); !strings.Contains(v, "not available") || strings.Contains(v, "fresh output") {
		t.Fatalf("%s", v)
	}
}

func TestTerminalOutputIsNotReadWhenTheDetailIsNotShown(t *testing.T) {
	r := newWSRunner(t)
	m, _, _ := wsLive(t, r, nil, 60, 24) // stacked: the list alone, no detail pane
	n := r.callsWith("capture")
	m.View()
	for i := 0; i < 3; i++ {
		var cmd tea.Cmd
		m, cmd = m.Update(leasesTickMsg{})
		m = drive(t, m, cmd)
		m.View()
	}
	if got := r.callsWith("capture"); got != n {
		t.Fatalf("polling must not read terminals nobody sees: %d -> %d", n, got)
	}
}

// ---- the change feed ----

type testFeed struct {
	started chan struct{}
	stopped chan struct{}
	push    func()
}

func (f *testFeed) Watch(ctx context.Context, changed func()) {
	f.push = changed
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
}

type feedProv struct {
	core.WorkspaceProvider
	*testFeed
}

func (p feedProv) Workspace(ctx context.Context, items []core.WorkspaceItem) (core.WorkspaceReport, error) {
	return p.WorkspaceProvider.Workspace(ctx, items)
}

func TestAChangeFeedStartsOnceRefreshesTheWorkspaceAndIsStoppedOnClose(t *testing.T) {
	f := &testFeed{started: make(chan struct{}), stopped: make(chan struct{})}
	w := newWsWatch(f)
	if cmd := w.start(); cmd == nil {
		t.Fatal("start returns the command that waits for a change")
	}
	select {
	case <-f.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the feed did not start")
	}
	if cmd := w.start(); cmd != nil {
		t.Fatal("it starts once")
	}
	f.push()
	f.push() // two changes before anyone reads: one message
	msg := w.wait()()
	if _, ok := msg.(wsChangedMsg); !ok {
		t.Fatalf("%T", msg)
	}
	w.stop()
	select {
	case <-f.stopped:
	default:
		t.Fatal("Close must wait for the feed to finish")
	}
	w.stop() // safe twice
	var nilWatch *wsWatch
	nilWatch.stop()
	if nilWatch.start() != nil || nilWatch.wait() != nil {
		t.Fatal("no feed, no commands")
	}
}

func TestAChangeMessageReadsTheWorkspaceAgainForTheVisibleSection(t *testing.T) {
	r := newWSRunner(t)
	m, _, _ := wsLive(t, r, nil, 120, 40)
	before := r.callsWith("ls --json")
	m, cmd := m.Update(wsChangedMsg{})
	drive(t, m, cmd)
	if r.callsWith("ls --json") <= before {
		t.Fatal("a change reads where things live again, now")
	}
}

func TestWithTabShowsTheNamedTabFirst(t *testing.T) {
	for name, want := range map[string]string{"queues": "Queues", "Doctor": "Doctor", "tasks": "Tasks", "nonsense": "Board"} {
		m := New(nil, WithTheme(asciiTheme()), WithFullView(noFullView), WithTab(name))
		if got := m.tabs[m.sel].name; got != want {
			t.Errorf("%s: %s", name, got)
		}
	}
}
