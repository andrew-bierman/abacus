package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
)

// ---- saved filters and the recent `/` queries ----

func filterSection(t *testing.T, w, h int) (*tasksSection, core.SavedFilters, core.SearchHistory) {
	t.Helper()
	dir := t.TempDir()
	f := core.SavedFilters{Path: filepath.Join(dir, "cfg", "saved-filters.json")}
	hist := core.SearchHistory{Path: filepath.Join(dir, "state", "search-history.json")}
	s := newTasksSection(core.Providers{}, &config{filters: f, history: hist})
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	send(s, "T")
	return s, f, hist
}

func ctrl(s *tasksSection, t tea.KeyType) { s.Update(tea.KeyMsg{Type: t}) }

func TestSaveFilterNameQueryAndApplyRoundTrip(t *testing.T) {
	s, store, _ := filterSection(t, 100, 32)
	all := rowIDs(s)
	ctrl(s, tea.KeyCtrlS) // no query yet
	if s.Capturing() || !strings.Contains(s.notice, "type a query") {
		t.Fatalf("nothing to save: capturing=%v notice %q", s.Capturing(), s.notice)
	}
	send(s, "/")
	typeIn(s, "label:ui")
	ctrl(s, tea.KeyCtrlS) // saved from the middle of typing
	if !s.Capturing() || s.list.typing || s.fil.mode != filterName {
		t.Fatalf("the name prompt is open and owns the keys: %+v typing=%v", s.fil.mode, s.list.typing)
	}
	if out := s.View(); !strings.Contains(out, "Save this filter") || !strings.Contains(out, "label:ui") {
		t.Errorf("prompt:\n%s", out)
	}
	typeIn(s, "ui work")
	send(s, "enter")
	if s.fil.mode != filterNone || !strings.Contains(s.notice, `saved filter "ui work"`) {
		t.Fatalf("mode %v notice %q", s.fil.mode, s.notice)
	}
	if l := store.List(); len(l) != 1 || l[0].Name != "ui work" || l[0].Query != "label:ui" {
		t.Fatalf("stored %+v", l)
	}
	// clear the query, then apply the saved one from the picker
	send(s, "esc")
	if rowIDs(s) != all {
		t.Fatal("esc cleared the query")
	}
	ctrl(s, tea.KeyCtrlO)
	if s.fil.mode != filterPick || !strings.Contains(s.View(), "ui work") {
		t.Fatalf("picker: %v\n%s", s.fil.mode, s.View())
	}
	send(s, "enter")
	if s.list.text != "label:ui" || rowIDs(s) != "ac-15 ac-30 ac-33" || !strings.Contains(s.notice, `applied filter "ui work"`) {
		t.Errorf("applied: text %q rows %q notice %q", s.list.text, rowIDs(s), s.notice)
	}
}

func TestSaveFilterErrorsStayInThePrompt(t *testing.T) {
	s, store, _ := filterSection(t, 100, 32)
	send(s, "/")
	typeIn(s, "label:ui")
	send(s, "enter")
	ctrl(s, tea.KeyCtrlS)
	send(s, "enter") // empty name
	if s.fil.mode != filterName || s.fil.err == "" || len(store.List()) != 0 {
		t.Fatalf("an empty name stays in the prompt with a message: mode %v err %q", s.fil.mode, s.fil.err)
	}
	typeIn(s, "-bad")
	send(s, "enter")
	if s.fil.mode != filterName || !strings.Contains(s.View(), "starts with a letter or digit") {
		t.Errorf("bad name:\n%s", s.View())
	}
	send(s, "esc")
	if s.fil.mode != filterNone || len(store.List()) != 0 || s.list.text != "label:ui" {
		t.Error("esc cancels and keeps the query")
	}
}

func TestSaveFilterRefusedWithoutAStoreOrWithABadQuery(t *testing.T) {
	s := listAt(100, 32) // config{}: a session that keeps nothing
	send(s, "/")
	typeIn(s, "label:ui")
	send(s, "enter")
	ctrl(s, tea.KeyCtrlS)
	if s.fil.mode != filterNone || !strings.Contains(s.notice, "not kept in this session") {
		t.Errorf("notice %q", s.notice)
	}
	s2, _, _ := filterSection(t, 100, 32)
	send(s2, "/")
	typeIn(s2, "nofield:x")
	send(s2, "enter")
	ctrl(s2, tea.KeyCtrlS)
	if s2.fil.mode != filterNone {
		t.Error("a query that does not parse cannot be saved")
	}
	ctrl(s2, tea.KeyCtrlO)
	if s2.fil.mode != filterNone || !strings.Contains(s2.notice, "no saved filters yet") {
		t.Errorf("empty picker: %q", s2.notice)
	}
}

func TestPickerDeleteNeedsConfirmation(t *testing.T) {
	s, store, _ := filterSection(t, 100, 32)
	for _, c := range [][2]string{{"a one", "label:ui"}, {"b two", "status:blocked"}} {
		if err := store.Save(c[0], c[1]); err != nil {
			t.Fatal(err)
		}
	}
	ctrl(s, tea.KeyCtrlO)
	send(s, "d")
	if s.fil.mode != filterDelete || !strings.Contains(s.View(), "a one") {
		t.Fatalf("delete asks first:\n%s", s.View())
	}
	send(s, "n")
	if len(store.List()) != 2 || s.fil.mode != filterPick {
		t.Fatal("n keeps it")
	}
	send(s, "d", "y")
	if l := store.List(); len(l) != 1 || l[0].Name != "b two" || !strings.Contains(s.notice, `deleted filter "a one"`) {
		t.Errorf("after y: %+v %q", l, s.notice)
	}
	send(s, "esc")
	if s.Capturing() {
		t.Error("esc closes the picker")
	}
}

func TestRecentQueriesRecallWithCtrlPAndCtrlN(t *testing.T) {
	s, _, hist := filterSection(t, 100, 32)
	for _, q := range []string{"label:ui", "status:blocked", "priority:0"} {
		send(s, "/")
		typeIn(s, q)
		send(s, "enter")
		send(s, "esc")
	}
	if got := strings.Join(hist.Load(core.HistoryTasksFilter), "|"); got != "label:ui|status:blocked|priority:0" {
		t.Fatalf("history = %q (kept in the state file, one per enter)", got)
	}
	send(s, "/")
	typeIn(s, "dra")
	ctrl(s, tea.KeyCtrlP)
	if s.list.text != "priority:0" {
		t.Errorf("ctrl+p = %q", s.list.text)
	}
	ctrl(s, tea.KeyCtrlP)
	ctrl(s, tea.KeyCtrlP)
	ctrl(s, tea.KeyCtrlP) // stops at the oldest
	if s.list.text != "label:ui" {
		t.Errorf("oldest = %q", s.list.text)
	}
	ctrl(s, tea.KeyCtrlN)
	if s.list.text != "status:blocked" {
		t.Errorf("ctrl+n = %q", s.list.text)
	}
	ctrl(s, tea.KeyCtrlN)
	ctrl(s, tea.KeyCtrlN) // back to the draft
	if s.list.text != "dra" {
		t.Errorf("draft = %q", s.list.text)
	}
	send(s, "esc")
	// a query that does not parse is never recorded
	send(s, "/")
	typeIn(s, "nofield:x")
	send(s, "enter")
	if n := len(hist.Load(core.HistoryTasksFilter)); n != 3 {
		t.Errorf("a bad query was recorded (%d entries)", n)
	}
}

// ---- keys ----

// The merged key map: every Tasks mode gives each key one meaning. This runs over the modes of the
// earlier units plus this one's overlays, and adds that no key of this unit shadows a global one.
func TestKeyMapWithLiveKeysHasNoCollisions(t *testing.T) {
	global := map[string]string{}
	for name, b := range map[string]key.Binding{"quit": keyQuit, "next": keyNext, "prev": keyPrev, "jump": keyJump, "help": keyHelp} {
		for _, k := range b.Keys() {
			global[k] = name
		}
	}
	// the shell's keys from the command prompt unit: this unit's keys must never be among them
	for _, k := range []string{":", "ctrl+k", "d", "y"} {
		for _, m := range []string{"F", "H", "P", "ctrl+s", "ctrl+o"} {
			if k == m {
				t.Errorf("%q is the shell's key", k)
			}
		}
	}
	mine := map[string]bool{"F": true, "H": true, "P": true, "ctrl+s": true, "ctrl+o": true, "ctrl+p": true, "ctrl+n": true}
	rig := func(s *tasksSection) { s.live.newest = "ac-09" } // so F is offered
	modes := map[string]func(s *tasksSection){
		"list":     func(s *tasksSection) { rig(s) },
		"typing":   func(s *tasksSection) { send(s, "/") },
		"drilled":  func(s *tasksSection) { rig(s); send(s, "enter") },
		"insights": func(s *tasksSection) { send(s, "V") },
		"picker": func(s *tasksSection) {
			_ = s.fil.store.Save("a", "label:ui")
			ctrl(s, tea.KeyCtrlO)
		},
		"name": func(s *tasksSection) { send(s, "/"); typeIn(s, "label:ui"); ctrl(s, tea.KeyCtrlS) },
	}
	for name, setup := range modes {
		for _, w := range []int{60, 100, 120} {
			s, _, _ := filterSection(t, w, 30)
			setup(s)
			seen := map[string]bool{}
			for _, k := range keyStrings(s.Bindings()) {
				if seen[k] {
					t.Errorf("%s %d: key %q is bound twice", name, w, k)
				}
				seen[k] = true
				if g, clash := global[k]; clash && k != "esc" {
					t.Errorf("%s %d: key %q clashes with the global %s key", name, w, k, g)
				}
				if k == ":" || k == "ctrl+k" || k == "w" && name != "list" {
					t.Errorf("%s: %q is reserved", name, k)
				}
			}
		}
	}
	// within one mode: the list view's own keys never reuse one of ours
	s, _, _ := filterSection(t, 100, 30)
	s.live.newest = "ac-09"
	own := map[string]bool{}
	for _, k := range keyStrings(s.baseBindings()) {
		own[k] = true
	}
	for k := range mine {
		if own[k] {
			t.Errorf("the section already binds %q", k)
		}
	}
}

// Every key of this unit works in List and nowhere it should not.
func TestLiveKeysOnlyActInTheViewsTheyBelongTo(t *testing.T) {
	s, store, _ := filterSection(t, 100, 32)
	_ = store.Save("a", "label:ui")
	send(s, "V") // Insights view
	for _, k := range []string{"F", "H"} {
		send(s, k)
		if s.hist.on || s.fil.mode != filterNone {
			t.Errorf("%s acted in the Insights view", k)
		}
	}
	ctrl(s, tea.KeyCtrlO)
	if s.fil.mode != filterNone {
		t.Error("ctrl+o acted in the Insights view")
	}
}

// ---- the tracker switcher ----

type switchRig struct {
	s       *tasksSection
	a, b, c string
	cfg     core.ConfigStore
	audit   *recAudit
	target  *recTarget
	cur     *string
	prov    *liveProv
}

// recAudit keeps the audit lines so a test can read them.
type recAudit struct{ lines []core.AuditEntry }

func (a *recAudit) Record(e core.AuditEntry) error { a.lines = append(a.lines, e); return nil }

type recTarget struct{ dir string }

func (r *recTarget) SetTrackerDir(d string) error { r.dir = d; return nil }

func mkTrk(t *testing.T, root, name string) string {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(d, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, ".beads", "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func newSwitchRig(t *testing.T, w, h int) *switchRig {
	t.Helper()
	root := t.TempDir()
	r := &switchRig{a: mkTrk(t, root, "alpha"), b: mkTrk(t, root, "beta"), c: mkTrk(t, root, "gamma"), audit: &recAudit{}, target: &recTarget{}}
	r.cfg = core.ConfigStore{Path: filepath.Join(root, "cfg", "config.json")}
	recents := core.RecentTrackers{Path: filepath.Join(root, "state", "recent.json")}
	_ = recents.Touch(r.a, time.Now().Add(-2*time.Hour))
	_ = recents.Touch(r.b, time.Now().Add(-time.Hour))
	cur := r.a
	r.cur = &cur
	sw := core.NewSwitcher(r.cfg, recents, r.target, func() string { return *r.cur }, r.audit)
	s := newTasksSection(core.Providers{}, &config{switcher: sw, recents: recents})
	s.SetTheme(asciiTheme())
	s.tick = func() tea.Cmd { return nil }
	s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.Update(tasksMsg{report: demo.TasksReport()})
	s.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
	send(s, "T")
	r.s = s
	return r
}

func TestSwitcherPicksConfirmsAndReloads(t *testing.T) {
	r := newSwitchRig(t, 100, 32)
	send(r.s, "P")
	out := r.s.View()
	if r.s.sw.mode != switchPick || !strings.Contains(out, "alpha (current)") || !strings.Contains(out, "beta") || strings.Contains(out, "gamma") {
		t.Fatalf("the picker lists the one in use and the remembered ones, never an unopened folder:\n%s", out)
	}
	if !r.s.Capturing() {
		t.Error("the picker owns the keys")
	}
	send(r.s, "enter") // starts on the first tracker that is not the current one
	out = r.s.View()
	if r.s.sw.mode != switchConfirm || !strings.Contains(out, "beta") || !strings.Contains(out, "touches neither tracker") || !strings.Contains(out, "abacus config set tracker_dir") {
		t.Fatalf("confirm:\n%s", out)
	}
	if r.target.dir != "" || len(r.audit.lines) != 0 {
		t.Fatal("nothing happens before y")
	}
	_, cmd := r.s.Update(keyRunes("y"))
	if cmd == nil || r.s.sw.mode != switchBusy {
		t.Fatal("y applies")
	}
	msg := cmd()
	if r.target.dir != r.b || len(r.audit.lines) != 2 {
		t.Fatalf("target %q audit %v", r.target.dir, r.audit.lines)
	}
	*r.cur = r.b
	_, next := r.s.Update(msg)
	if r.s.sw.mode != switchNone || !strings.Contains(r.s.notice, "switched to beta") || next == nil {
		t.Errorf("after the switch: mode %v notice %q cmd %v", r.s.sw.mode, r.s.notice, next)
	}
	if r.s.loaded || r.s.list.view != nil {
		t.Error("everything of the old tracker is dropped until the new one is read")
	}
	if c, _ := r.cfg.Load(); c.TrackerDir != r.b {
		t.Errorf("tracker_dir = %q", c.TrackerDir)
	}
}

func TestSwitcherCancelChangesNothing(t *testing.T) {
	r := newSwitchRig(t, 100, 32)
	send(r.s, "P", "enter", "n")
	if r.s.sw.mode != switchNone || r.target.dir != "" || len(r.audit.lines) != 0 || !strings.Contains(r.s.notice, "nothing was changed") {
		t.Errorf("mode %v notice %q", r.s.sw.mode, r.s.notice)
	}
	send(r.s, "P", "esc")
	if r.s.sw.mode != switchNone || r.s.Capturing() {
		t.Error("esc closes the picker")
	}
}

func TestSwitcherRefusesWhatIsNotATrackerAtApplyTime(t *testing.T) {
	r := newSwitchRig(t, 100, 32)
	send(r.s, "P", "enter")
	_ = os.RemoveAll(r.b) // the folder goes away while the confirm screen is open
	_, cmd := r.s.Update(keyRunes("y"))
	msg := cmd()
	r.s.Update(msg)
	if r.target.dir != "" || !strings.Contains(strings.ToLower(r.s.notice), "not a tracker") && !strings.Contains(r.s.notice, "no .beads") {
		t.Errorf("target %q notice %q", r.target.dir, r.s.notice)
	}
	if last := r.audit.lines[len(r.audit.lines)-1]; last.Phase != "error" {
		t.Errorf("the refusal is audited: %+v", last)
	}
}

func TestSwitcherYIsIgnoredWhileTheConfirmScreenIsClipped(t *testing.T) {
	r := newSwitchRig(t, 60, 12)
	send(r.s, "P", "enter")
	if r.s.switchWhole() {
		t.Skip("the confirm screen fits at this size")
	}
	if _, cmd := r.s.Update(keyRunes("y")); cmd != nil || r.s.sw.mode != switchConfirm {
		t.Error("a clipped confirm screen cannot be answered with y")
	}
	if bs := keyStrings(r.s.Bindings()); len(bs) != 2 || bs[0] != "n" {
		t.Errorf("only cancel is offered: %v", bs)
	}
}

func TestSwitcherNotAvailableAndNothingToSwitchTo(t *testing.T) {
	s := listAt(100, 32)
	send(s, "P")
	if s.sw.mode != switchNone || !strings.Contains(s.notice, "not available") {
		t.Errorf("no switcher: %q", s.notice)
	}
	r := newSwitchRig(t, 100, 32)
	_ = os.RemoveAll(r.b)
	send(r.s, "P")
	if r.s.sw.mode != switchNone || !strings.Contains(r.s.notice, "no other tracker") {
		t.Errorf("only the current one: %q", r.s.notice)
	}
}

// Switching asks the new tracker for its own tasks and keeps the user's view choices.
func TestSwitcherKeepsViewChoicesAndRereads(t *testing.T) {
	r := newSwitchRig(t, 100, 32)
	send(r.s, "s") // a different sort
	sortKey := r.s.list.sort
	r.s.resetForTracker()
	if r.s.list.sort != sortKey || r.s.selected != "" || r.s.cursor != 0 {
		t.Errorf("sort %v cursor %d selected %q", r.s.list.sort, r.s.cursor, r.s.selected)
	}
}

// histTask is the golden Tasks provider that can also read a task's history.
type histTask struct {
	taskProv
	hist map[string]core.TaskHistoryReport
}

func (h histTask) History(_ context.Context, id string, _ int) (core.TaskHistoryReport, error) {
	return h.hist[id], nil
}

// ---- goldens, width invariants, hostile text ----

func liveScenarios() []scenario {
	std := func(t *testing.T, th Theme) tea.Model { return load(t, th, nil) }
	savedModel := func(t *testing.T, th Theme) tea.Model {
		dir := t.TempDir()
		p := tasksFake()
		f := core.SavedFilters{Path: filepath.Join(dir, "saved-filters.json")}
		_ = f.Save("ui work", "label:ui !status:closed")
		_ = f.Save("blocked soon", "status:blocked priority:1")
		var m tea.Model = New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(th), WithAudit(&memAudit{}), WithFullView(noFullView), WithSavedFilters(f))
		m, _ = m.Update(tasksMsg{report: fixtureTasks()})
		m, _ = m.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
		return m
	}
	switchModel := func(t *testing.T, th Theme) tea.Model {
		root := t.TempDir()
		a, b, c := mkTrk(t, root, "fleet"), mkTrk(t, root, "side-project"), mkTrk(t, root, "docs")
		rec := core.RecentTrackers{Path: filepath.Join(root, "recent.json")}
		now := time.Now()
		_ = rec.Touch(c, now.Add(-3*time.Hour))
		_ = rec.Touch(b, now.Add(-time.Hour))
		_ = rec.Touch(a, now)
		old := scrubPaths
		// The picker shortens paths under HOME before scrubbing them.
		scrubPaths = strings.NewReplacer(root, "/work", shortHome(root), "/work").Replace
		t.Cleanup(func() { scrubPaths = old })
		sw := core.NewSwitcher(core.ConfigStore{Path: filepath.Join(root, "config.json")}, rec, &recTarget{}, func() string { return a }, &memAudit{})
		p := tasksFake()
		var m tea.Model = New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(th), WithAudit(&memAudit{}), WithFullView(noFullView), WithTrackerSwitcher(sw), WithRecentTrackers(rec))
		m, _ = m.Update(tasksMsg{report: fixtureTasks()})
		m, _ = m.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
		return m
	}
	// a snapshot read a minute later with two edits: the live marker, and the list after a change
	changed := func(t *testing.T, th Theme) tea.Model {
		m := std(t, th)
		snap := fixtureSnapshot()
		snap.ReadAt = goldenNow.Add(time.Second)
		snap.Issues = append([]core.TaskIssue(nil), snap.Issues...)
		for i := range snap.Issues {
			if snap.Issues[i].ID == "ac-09" || snap.Issues[i].ID == "ac-30" {
				snap.Issues[i].Title += " (edited)"
				snap.Issues[i].UpdatedAt = goldenNow
			}
		}
		m, _ = m.Update(taskSnapshotMsg{snap: snap})
		return m
	}
	withHistory := func(hostile bool) func(*testing.T, Theme) tea.Model {
		return func(t *testing.T, th Theme) tea.Model {
			p := histTask{tasksFake(), histFixture(t)}
			var m tea.Model = New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(th), WithAudit(&memAudit{}), WithFullView(noFullView))
			m, _ = m.Update(tasksMsg{report: fixtureTasks()})
			m, _ = m.Update(taskSnapshotMsg{snap: fixtureSnapshot()})
			m = press(m, "2")
			id := "ac-12"
			if hostile {
				id = "ac-09"
				ts := m.(*Model).tabs[1].sec.(*tasksSection)
				ts.selected = id
				ts.reanchor(ts.ids(), true)
			}
			m = press(m, "H")
			m, _ = m.Update(histMsg{id: id, key: fixtureSnapshot().ReadAt, rep: p.hist[id]})
			return m
		}
	}
	return []scenario{
		{name: "tasks-live-changed", build: changed, keys: []string{"2", "T"}, tab: "Tasks", extra: framedSizes},
		{name: "tasks-history", build: withHistory(false), keys: nil, tab: "Tasks", extra: framedSizes},
		{name: "tasks-history-hostile", build: withHistory(true), keys: nil, tab: "Tasks", extra: framedSizes},
		{name: "tasks-filter-save", lite: true, build: savedModel, keys: append(append([]string{"2", "/"}, typed("label:ui")...), "ctrl+s", "u", "i", " "), tab: "Tasks", extra: framedSizes},
		{name: "tasks-filter-pick", lite: true, build: savedModel, keys: []string{"2", "ctrl+o"}, tab: "Tasks", extra: framedSizes},
		{name: "tasks-switch-pick", lite: true, build: switchModel, keys: []string{"2", "P"}, tab: "Tasks", extra: framedSizes},
		{name: "tasks-switch-confirm", lite: true, build: switchModel, keys: []string{"2", "P", "enter"}, tab: "Tasks", extra: framedSizes},
	}
}

func init() { switchInline = true } // the key-pressing tests do not run the commands they get back

// The disk work runs off the UI goroutine: P opens a loading state that owns the keys, the answer
// opens the picker, and esc while waiting drops the answer.
func TestSwitcherReadsTheDiskOffTheUIGoroutine(t *testing.T) {
	switchInline = false
	t.Cleanup(func() { switchInline = true })
	r := newSwitchRig(t, 100, 32)
	_, cmd := r.s.Update(keyRunes("P"))
	if cmd == nil || r.s.sw.mode != switchLoading || !r.s.Capturing() || !strings.Contains(r.s.View(), "reading the known trackers") {
		t.Fatalf("P opens the loading state: mode %v", r.s.sw.mode)
	}
	_, next := r.s.Update(cmd())
	if r.s.sw.mode != switchPick || next != nil {
		t.Fatalf("the answer opens the picker: %v", r.s.sw.mode)
	}
	send(r.s, "esc")
	_, cmd = r.s.Update(keyRunes("P"))
	send(r.s, "esc") // cancelled while waiting
	r.s.Update(cmd())
	if r.s.sw.mode != switchNone {
		t.Errorf("an answer after esc must be dropped: %v", r.s.sw.mode)
	}
	// Enter in the picker plans off the goroutine too
	_, cmd = r.s.Update(keyRunes("P"))
	r.s.Update(cmd())
	_, cmd = r.s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if r.s.sw.mode != switchLoading {
		t.Fatalf("enter plans in the background: %v", r.s.sw.mode)
	}
	r.s.Update(cmd())
	if r.s.sw.mode != switchConfirm {
		t.Errorf("then the confirm screen: %v", r.s.sw.mode)
	}
}

// A drive that does not answer: the switcher gives up at its deadline and says so.
func TestSwitcherGivesUpOnAStuckDrive(t *testing.T) {
	switchInline = false
	old := switchDeadline
	switchDeadline = 50 * time.Millisecond
	t.Cleanup(func() { switchInline, switchDeadline = true, old })
	r := newSwitchRig(t, 100, 32)
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	sw := core.NewSwitcher(r.cfg, core.RecentTrackers{}, r.target, func() string { <-stuck; return "" }, r.audit)
	r.s.sw.sw = sw
	_, cmd := r.s.Update(keyRunes("P"))
	start := time.Now()
	msg := cmd()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %v", d)
	}
	r.s.Update(msg)
	if r.s.sw.mode != switchNone || !strings.Contains(r.s.notice, "took too long") {
		t.Errorf("mode %v notice %q", r.s.sw.mode, r.s.notice)
	}
}

func TestSwitcherRefusesATrackerOthersCanWrite(t *testing.T) {
	r := newSwitchRig(t, 100, 32)
	if err := os.Chmod(r.b, 0o777); err != nil {
		t.Fatal(err)
	}
	send(r.s, "P", "enter")
	if r.s.sw.mode != switchNone || !strings.Contains(r.s.notice, "writable by other users") {
		t.Errorf("mode %v notice %q", r.s.sw.mode, r.s.notice)
	}
	if r.target.dir != "" {
		t.Error("nothing was repointed")
	}
}
