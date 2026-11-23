package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Tab indexes of the fixture model.
const (
	tabBoard = iota
	tabTasks
	tabLeases
	tabQueues
	tabDoctor
)

func ctrlK(q string) []string { return append([]string{"ctrl+k"}, typed(q)...) }

func searchScenarios() []scenario {
	std := func(t *testing.T, th Theme) tea.Model { return load(t, th, nil) }
	return []scenario{
		{name: "search-empty", build: std, keys: ctrlK(""), tab: "Board", overlay: true, extra: framedSizes},
		{name: "search-results", build: std, keys: ctrlK("task"), tab: "Board", overlay: true, extra: framedSizes},
		{name: "search-people", build: std, keys: append(ctrlK("who:ada"), "down", "down", "down", "down", "down"), tab: "Board", overlay: true, extra: framedSizes},
		{name: "search-no-match", build: std, keys: ctrlK("zzzzzz"), tab: "Board", overlay: true, extra: framedSizes},
		{name: "search-query-error", build: std, keys: ctrlK("lease nosuch:x"), tab: "Board", overlay: true, extra: framedSizes},
		{name: "search-stale", build: func(t *testing.T, th Theme) tea.Model {
			m := load(t, th, nil)
			secAt(m, tabLeases).(*leasesSection).readAt = goldenNow.Add(-5 * time.Minute)
			return m
		}, keys: ctrlK("ac-"), tab: "Board", overlay: true, extra: framedSizes},
		{name: "search-degraded", build: degradedFixture, keys: ctrlK("ac-"), tab: "Board", overlay: true, extra: framedSizes},
		{name: "search-hostile", build: hostileFixture, keys: ctrlK("e"), tab: "Board", overlay: true, extra: framedSizes},
	}
}

// secAt is the section behind tab i (through a setup guard, when there is one).
func secAt(m tea.Model, i int) section {
	s := m.(*Model).tabs[i].sec
	if g, ok := s.(*guidedSection); ok {
		return g.inner
	}
	return s
}

// degradedFixture has tasks and leases read, the board not set up, queues with no provider and the
// leases hub unreachable, so the overlay must say so in plain words.
func degradedFixture(t *testing.T, th Theme) tea.Model {
	p, b := tasksFake(), boardFake()
	b.err = &core.NotConnectedError{Reason: core.BoardNotSetUpReason, HowToConnect: core.BoardNotSetUp}
	m := New([]core.Entry{{Module: b, Detection: b.Det}, {Module: p, Detection: p.Det}, {Module: leaseFake(), Detection: core.Detection{State: core.Ready}}}, WithTheme(th), WithAudit(&memAudit{}), WithFullView(noFullView), WithSearchHistory(core.SearchHistory{}))
	var tm tea.Model = m
	tm, _ = tm.Update(boardMsg{err: b.err})
	tm, _ = tm.Update(leasesMsg{read: leasesRead{unavailable: "could not reach the hub"}})
	tm, _ = tm.Update(tasksMsg{report: fixtureTasks()})
	tm, _ = tm.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	return tm
}

// hostileFixture feeds the overlay text a hostile tracker or board could hold.
func hostileFixture(t *testing.T, th Theme) tea.Model {
	m := load(t, th, nil)
	evil := "evil \x1b[31mred\x1b[0m \u202e rtl \x07bell " + strings.Repeat("界", 80)
	snap := fixtureSnapshot()
	snap.Issues = append(snap.Issues, core.TaskIssue{ID: "ac-\x1b[2Jx", Title: evil, Description: evil, Status: "open", Labels: []string{evil}, UpdatedAt: goldenNow})
	m, _ = m.Update(taskSnapshotMsg{snap: snap})
	rep := fixtureBoard()
	rep.Threads = append(rep.Threads, core.BoardThread{Root: core.BoardMessage{ID: "9\x1b]0;x\x07", Channel: evil, Author: evil, Text: evil}})
	m, _ = m.Update(boardMsg{report: rep})
	m, _ = m.Update(leasesMsg{read: leasesRead{report: core.LeasesReport{Leases: []core.Lease{{Resource: evil, State: core.LeaseHeld, Holder: evil}}}}})
	return m
}

func open(t *testing.T, w, h int, q string) tea.Model {
	t.Helper()
	m := sized(load(t, asciiTheme(), nil), w, h)
	for _, k := range ctrlK(q) {
		m = press(m, k)
	}
	return m
}

func overlayOpen(m tea.Model) bool { return m.(*Model).search != nil }

func activeTab(m tea.Model) string { x := m.(*Model); return x.tabs[x.sel].name }

func TestSearchOverlayListsEveryDomainGroupedWithACursor(t *testing.T) {
	m := open(t, 100, 32, "task")
	out := m.View()
	for _, want := range []string{"▸ Search", "search task", "Tasks (", "Queues (", "▸ ", "esc close"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Count(out, "▸ ") < 2 { // the focus marker on the box and on the row
		t.Errorf("no cursor row:\n%s", out)
	}
	// Tab moves between groups, the arrows between rows; neither leaves the overlay or changes tab.
	before := m.(*Model).search.sel
	m = press(m, "tab")
	if m.(*Model).search.sel == before || !overlayOpen(m) || activeTab(m) != "Board" {
		t.Errorf("tab should move to the next group (sel %d -> %d, tab %s)", before, m.(*Model).search.sel, activeTab(m))
	}
}

// Every key goes to the overlay while it is open: q types, digits type, and nothing acts on a section.
func TestSearchOverlayCapturesEveryKey(t *testing.T) {
	m := sized(load(t, asciiTheme(), nil), 100, 32)
	m = press(m, "ctrl+k")
	for _, k := range []string{"q", "2", "?", "n", "x", "y", "r", "o", "/"} {
		m = press(m, k)
		if !overlayOpen(m) || activeTab(m) != "Board" {
			t.Fatalf("key %q leaked out of the overlay (tab %s)", k, activeTab(m))
		}
	}
	if got := string(m.(*Model).search.text); got != "q2?nxyro/" {
		t.Errorf("typed text = %q", got)
	}
	if strings.Contains(m.View(), "Keys") {
		t.Error("? must type, not open help")
	}
	m = press(m, "esc")
	if overlayOpen(m) {
		t.Error("esc closes")
	}
	if m = press(m, "q"); m != nil {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
		if cmd == nil {
			t.Error("with the overlay closed, q quits again")
		}
	}
	// ctrl+c quits even from the overlay
	m = press(sized(load(t, asciiTheme(), nil), 100, 32), "ctrl+k")
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Error("ctrl+c must quit from the overlay")
	}
}

// ctrl+k has one owner. No section binding uses it, and it never opens over a form or confirm
// screen or while a section is capturing text.
func TestSearchKeyCollisions(t *testing.T) {
	m := sized(load(t, asciiTheme(), nil), 100, 32)
	var all []key.Binding
	all = append(all, keyQuit, keyNext, keyPrev, keyJump, keyHelp, keyBack, keyUp, keyDown, keyEnter, keyFilter, keyOpen, keyFull, keyBrowser, keyNew, keyEdit, keyClose, keyYes, keyNo, keyReload, keySort, keyPreset, keyView)
	for i := range m.(*Model).tabs {
		all = append(all, m.(*Model).tabs[i].sec.Bindings()...)
	}
	all = append(all, searchBindings()[1:]...)
	for _, b := range all {
		for _, k := range b.Keys() {
			if k == "ctrl+k" {
				t.Errorf("binding %q also owns ctrl+k", b.Help().Desc)
			}
		}
	}
	if keys := keySearch.Keys(); len(keys) != 1 || keys[0] != "ctrl+k" {
		t.Errorf("keySearch = %v", keys)
	}
	for _, k := range []string{"?", ":"} { // another key is not the search key
		if key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}, keySearch) {
			t.Errorf("%q must not open search", k)
		}
	}
	// a Tasks form, then a confirm screen, then a lambe filter being typed: ctrl+k opens nothing
	type step struct {
		name string
		keys []string
	}
	for _, st := range []step{
		{"form", []string{"2", "n"}},
		{"filter typing", []string{"4", "/"}},
		{"tasks filter typing", []string{"2", "/"}},
	} {
		x := sized(load(t, asciiTheme(), nil), 100, 32)
		for _, k := range st.keys {
			x = press(x, k)
		}
		x = press(x, "ctrl+k")
		if overlayOpen(x) {
			t.Errorf("%s: the overlay opened over a capturing section", st.name)
		}
	}
	x := press(press(sized(load(t, asciiTheme(), nil), 100, 32), "2"), "x")
	x, _ = x.Update(taskVerifiedMsg{id: "ac-12", task: fixtureTasks().Recent[0]})
	if x = press(x, "ctrl+k"); overlayOpen(x) {
		t.Error("the overlay opened over a confirm screen")
	}
	// too small to draw: no overlay either
	small := press(sized(load(t, asciiTheme(), nil), 30, 8), "ctrl+k")
	if overlayOpen(small) {
		t.Error("ctrl+k must not open an overlay that cannot be drawn")
	}
}

// With the overlay open no write can happen and no section sees a key.
func TestSearchOverlayNeverReachesAConfirm(t *testing.T) {
	p := tasksFake()
	ran := p.ran
	var m tea.Model = New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(asciiTheme()), WithAudit(&memAudit{}), WithFullView(noFullView), WithSearchHistory(core.SearchHistory{}))
	m = sized(m, 100, 32)
	m, _ = m.Update(tasksMsg{report: fixtureTasks()})
	m, _ = m.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	m = press(m, "ctrl+k")
	for _, k := range []string{"x", "y", "n", "e", "enter"} {
		m = press(m, k)
	}
	if len(*ran) != 0 {
		t.Errorf("a write ran from the overlay: %v", *ran)
	}
	if ts := secAt(m, tabTasks).(*tasksSection); ts.mode != modeList {
		t.Errorf("the Tasks section left list mode (%v)", ts.mode)
	}
}

// ---- jumps, one per owning section

func jump(t *testing.T, q string, prep ...string) tea.Model {
	t.Helper()
	m := sized(load(t, asciiTheme(), nil), 100, 32)
	for _, k := range prep {
		m = press(m, k)
	}
	for _, k := range ctrlK(q) {
		m = press(m, k)
	}
	m.View() // the overlay refreshes when drawn, as it does after every update
	if !strings.Contains(m.View(), "▸ ") {
		t.Fatalf("no result for %q:\n%s", q, m.View())
	}
	m = press(m, "enter")
	if overlayOpen(m) {
		t.Fatalf("enter did not close the overlay for %q", q)
	}
	return m
}

func TestJumpToATask(t *testing.T) {
	m := jump(t, "ac-16")
	ts := secAt(m, tabTasks).(*tasksSection)
	if activeTab(m) != "Tasks" || ts.selected != "ac-16" || ts.rows()[ts.cursor].ID != "ac-16" {
		t.Errorf("tab %s selected %q", activeTab(m), ts.selected)
	}
}

// A task the list's own filter hides: the filter is cleared and the footer says so.
func TestJumpClearsTheListFilterThatHidesTheTask(t *testing.T) {
	m := jump(t, "ac-03", "2", "/", "z", "z", "z", "enter") // ac-03 is closed, and the query matches nothing
	ts := secAt(m, tabTasks).(*tasksSection)
	if ts.selected != "ac-03" || ts.list.text != "" || ts.list.preset != "all" {
		t.Errorf("selected %q text %q preset %q", ts.selected, ts.list.text, ts.list.preset)
	}
	if last := lines(m)[len(lines(m))-1]; !strings.Contains(last, "Cleared the filter in Tasks") {
		t.Errorf("footer %q", last)
	}
	// and the note goes at the next key
	m = press(m, "j")
	if last := lines(m)[len(lines(m))-1]; strings.Contains(last, "Cleared") {
		t.Errorf("the note outstayed a key: %q", last)
	}
}

func TestJumpToBoardThreadReplyAndTask(t *testing.T) {
	// the cursor starts on the other thread, so a jump has to move it; the reply (m:17) is shown by its thread
	for q, want := range map[string]string{"checkout": "t:15", "rolled back": "t:15", "CVE-123": "k:16", "deploys": "t:2"} {
		for _, from := range []string{"t:15", "t:2"} {
			m := jump(t, q, "1", map[string]string{"t:15": "up", "t:2": "down"}[from])
			bs := secAt(m, tabBoard).(*boardSection)
			if activeTab(m) != "Board" || bs.selected != want || bs.ids()[bs.cursor] != want {
				t.Errorf("%q from %s: tab %s selected %q, want %q", q, from, activeTab(m), bs.selected, want)
			}
		}
	}
}

func TestJumpToALease(t *testing.T) {
	m := jump(t, "ac-1k2", "2")
	ls := secAt(m, tabLeases).(*leasesSection)
	if activeTab(m) != "Leases" || ls.selected != "ac-1k2" || ls.ids()[ls.cursor] != "ac-1k2" {
		t.Errorf("tab %s selected %q", activeTab(m), ls.selected)
	}
}

func TestJumpToARunAndItsAttention(t *testing.T) {
	for _, q := range []string{"T-3", "gate_failed"} {
		m := jump(t, q)
		qs := secAt(m, tabQueues).(*lambeSection)
		want := "T-3"
		if q == "gate_failed" {
			want = "T-2"
		}
		if activeTab(m) != "Queues" || !strings.HasPrefix(qs.selected, want+"\x00") || qs.runs()[qs.cursor].Issue != want {
			t.Errorf("%q: tab %s selected %q, want run %s", q, activeTab(m), qs.selected, want)
		}
	}
}

func TestJumpClearsTheQueuesFilter(t *testing.T) {
	m := jump(t, "T-3", "4", "/", "z", "z", "enter")
	qs := secAt(m, tabQueues).(*lambeSection)
	if qs.filter != "" || !strings.HasPrefix(qs.selected, "T-3\x00") {
		t.Errorf("filter %q selected %q", qs.filter, qs.selected)
	}
}

// An item that left its section between the index and the keypress says so and still opens the tab.
func TestJumpToAnItemThatIsGone(t *testing.T) {
	m := sized(load(t, asciiTheme(), nil), 100, 32)
	for _, k := range ctrlK("ac-16") {
		m = press(m, k)
	}
	m.View()
	gone := fixtureSnapshot()
	var keep []core.TaskIssue
	for _, it := range gone.Issues {
		if it.ID != "ac-16" {
			keep = append(keep, it)
		}
	}
	gone.Issues = keep
	m, _ = m.Update(taskSnapshotMsg{snap: gone}) // the section changes, the overlay has not redrawn yet
	m = press(m, "enter")
	if activeTab(m) != "Tasks" {
		t.Errorf("the tab still opens, got %s", activeTab(m))
	}
	if last := lines(m)[len(lines(m))-1]; !strings.Contains(last, "no longer in Tasks") {
		t.Errorf("footer %q", last)
	}
}

func TestJumpToADomainThatIsNotConnected(t *testing.T) {
	m := sized(degradedFixture(t, asciiTheme()), 100, 32)
	for _, k := range ctrlK("ac-") {
		m = press(m, k)
	}
	if out := m.View(); !strings.Contains(out, "Board and Queues are not connected.") || !strings.Contains(out, "Leases could not be read just now: could not reach the hub.") {
		t.Errorf("degraded states:\n%s", out)
	}
}

// ---- states in plain words

func TestSearchStatesNeverGiveAnInstallInstruction(t *testing.T) {
	m := sized(degradedFixture(t, asciiTheme()), 100, 32)
	for _, k := range ctrlK("ac-") {
		m = press(m, k)
	}
	out := strings.ToLower(m.View())
	for _, bad := range []string{"install", "npx", "brew", "go install", "how to connect", "bd ", "lambe", "beads"} {
		if strings.Contains(out, bad) {
			t.Errorf("tool or install text %q in the overlay:\n%s", bad, out)
		}
	}
}

func TestSearchMarksAStaleDomain(t *testing.T) {
	m := sized(load(t, asciiTheme(), nil), 100, 32)
	secAt(m, tabLeases).(*leasesSection).readAt = goldenNow.Add(-5 * time.Minute)
	for _, k := range ctrlK("ac-1k2") {
		m = press(m, k)
	}
	if out := m.View(); !strings.Contains(out, "Leases (1) · read 5m ago, may be out of date") {
		t.Errorf("stale group:\n%s", out)
	}
	m = sized(load(t, asciiTheme(), nil), 100, 32)
	for _, k := range ctrlK("ac-1k2") {
		m = press(m, k)
	}
	if out := m.View(); strings.Contains(out, "out of date") {
		t.Errorf("a fresh read is not marked:\n%s", out)
	}
}

// A domain that never loaded gets exactly one read, the same call its tab makes, and shows loading.
func TestSearchLoadsAnUnloadedDomainOnce(t *testing.T) {
	b := boardFake()
	reads := 0
	counting := countingBoard{boardProv: b, n: &reads}
	var m tea.Model = New([]core.Entry{{Module: counting, Detection: b.Det}}, WithTheme(asciiTheme()), WithAudit(&memAudit{}), WithSearchHistory(core.SearchHistory{}))
	m = sized(m, 100, 32)
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	for _, k := range typed("checkout") {
		m = press(m, k)
	}
	if out := m.View(); !strings.Contains(out, "Board is still loading") {
		t.Errorf("loading state:\n%s", out)
	}
	for _, msg := range runBoardCmd(cmd) {
		m, _ = m.Update(msg)
	}
	m, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK}) // close and reopen: nothing more to read
	m, cmd2 = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if reads != 1 || len(runBoardCmd(cmd2)) != 0 {
		t.Errorf("the board was read %d times", reads)
	}
	for _, k := range typed("checkout") {
		m = press(m, k)
	}
	if out := m.View(); !strings.Contains(out, "Board (1)") {
		t.Errorf("after the read:\n%s", out)
	}
}

type countingBoard struct {
	boardProv
	n *int
}

func (c countingBoard) Board(ctx context.Context) (core.BoardReport, error) {
	*c.n++
	return c.boardProv.Board(ctx)
}

// runCmd runs a command (and the commands of a batch) and returns the messages, never waiting on a timer.
func runBoardCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan []tea.Msg, 1)
	go func() {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range b {
				if c == nil {
					continue
				}
				ch := make(chan tea.Msg, 1)
				go func() { ch <- c() }()
				select {
				case x := <-ch:
					out = append(out, x)
				case <-time.After(200 * time.Millisecond): // a tick: not a read
				}
			}
			done <- out
			return
		}
		done <- []tea.Msg{msg}
	}()
	select {
	case out := <-done:
		var real []tea.Msg
		for _, x := range out {
			if _, ok := x.(boardMsg); ok {
				real = append(real, x)
			}
		}
		return real
	case <-time.After(2 * time.Second):
		return nil
	}
}

// ---- history

func historyModel(t *testing.T, path string) tea.Model {
	t.Helper()
	m := load(t, asciiTheme(), nil)
	m.(*Model).searchHist = core.SearchHistory{Path: path} // load builds a demo session, which keeps none
	return sized(m, 100, 32)
}

func TestSearchHistoryIsKeptOnJumpAndRecalled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abacus", "search-history.json")
	m := historyModel(t, path)
	for _, k := range ctrlK("ac-16") {
		m = press(m, k)
	}
	m.View()
	m = press(m, "enter")
	if got := (core.SearchHistory{Path: path}).Load(core.HistoryGlobal); len(got) != 1 || got[0] != "ac-16" {
		t.Fatalf("history = %v", got)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	// reopened with an empty query: recent searches, selectable
	m = press(m, "ctrl+k")
	if out := m.View(); !strings.Contains(out, "Recent searches") || !strings.Contains(out, "▸ ac-16") {
		t.Errorf("recent searches:\n%s", out)
	}
	m = press(m, "enter") // loads the query
	if got := string(m.(*Model).search.text); got != "ac-16" {
		t.Errorf("text %q", got)
	}
	if out := m.View(); !strings.Contains(out, "Tasks (1)") {
		t.Errorf("results after recalling:\n%s", out)
	}
	// ctrl+p recalls while typing, ctrl+n goes back to the draft
	m = press(m, "esc")
	m = press(m, "ctrl+k")
	m = press(m, "x")
	m = press(m, "ctrl+p")
	if got := string(m.(*Model).search.text); got != "ac-16" {
		t.Errorf("ctrl+p text %q", got)
	}
	m = press(m, "ctrl+n")
	if got := string(m.(*Model).search.text); got != "x" {
		t.Errorf("ctrl+n text %q", got)
	}
}

// A demo session keeps nothing, and nothing is ever written into a tracker directory.
func TestDemoSessionKeepsNoSearchHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	m := sized(load(t, asciiTheme(), nil), 100, 32) // load includes the demo module
	for _, k := range ctrlK("ac-16") {
		m = press(m, k)
	}
	m.View()
	press(m, "enter")
	if _, err := os.Stat(filepath.Join(home, "abacus", "search-history.json")); err == nil {
		t.Error("a demo session wrote search history")
	}
}

// ---- text safety and size

func TestSearchOverlayTextIsSanitizedAndWithinWidth(t *testing.T) {
	m := sized(hostileFixture(t, asciiTheme()), 100, 32)
	for _, k := range ctrlK("evil") {
		m = press(m, k)
	}
	out := m.View()
	for _, l := range strings.Split(out, "\n") {
		if lipgloss.Width(l) > 100 {
			t.Errorf("line %d cells wide: %q", lipgloss.Width(l), l)
		}
		for _, r := range l {
			if unicode.IsControl(r) || r == '\u202e' {
				t.Errorf("control or bidi rune %q in %q", r, l)
			}
		}
	}
	if !strings.Contains(out, "evil") {
		t.Errorf("the hostile hit should still be found:\n%s", out)
	}
	// hostile typing never gets into the query
	m = press(m, "esc")
	m = press(m, "ctrl+k")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\x1b[2Jb\x07")})
	if got := string(m.(*Model).search.text); strings.ContainsFunc(got, unicode.IsControl) {
		t.Errorf("control characters reached the query: %q", got)
	}
	// the query is capped
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("a", 400))})
	if n := len(m.(*Model).search.text); n != 256 {
		t.Errorf("query length %d, want the 256 cap", n)
	}
}
