package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

var toolNames = []string{"Lambe", "lambe", "beads-lease", "board-beads", "b9s", "Rex", "bv", "bd"}

// chrome is what the user reads as navigation: the header (tabs and rule) and the footer.
func chrome(m tea.Model) []string {
	ls := lines(m)
	return []string{ls[0], ls[1], ls[len(ls)-1]}
}

func domainNames() []string { return []string{"Board", "Tasks", "Leases", "Queues", "Doctor"} }

// Domain language only: no nav, header or footer text names a tool, on any tab, at any width,
// whether the providers are connected or not.
func TestNavHeaderAndFooterNeverNameATool(t *testing.T) {
	for _, sc := range append(scenarios(), scenario{name: "none", tab: "Board", build: func(*testing.T, Theme) tea.Model { return New(nil, WithTheme(asciiTheme())) }}) {
		for _, w := range []int{40, 60, 80, 120} {
			for _, k := range []string{"1", "2", "3", "4", "5"} {
				m := sized(sc.build(t, asciiTheme()), w, 30)
				m = press(m, k)
				for _, l := range chrome(m) {
					for _, bad := range toolNames[:4] { // names that must never appear in nav
						if strings.Contains(l, bad) {
							t.Errorf("%s tab %s w=%d: chrome line %q names %q", sc.name, k, w, l, bad)
						}
					}
					for _, bad := range []string{"b9s", "Rex"} {
						if strings.Contains(l, bad) {
							t.Errorf("%s tab %s w=%d: chrome line %q names %q", sc.name, k, w, l, bad)
						}
					}
				}
			}
		}
	}
}

func TestNavListsEveryDomainEvenWithNoProvider(t *testing.T) {
	m := sized(New(nil, WithTheme(asciiTheme())), 80, 20)
	head := lines(m)[0]
	for _, d := range domainNames() {
		if !strings.Contains(head, d) {
			t.Errorf("header %q lacks %s", head, d)
		}
	}
}

func TestEveryDomainWithoutAProviderSaysNotConnectedAndHowToConnect(t *testing.T) {
	for i, d := range []string{"Board", "Tasks", "Leases", "Queues"} {
		m := sized(New(nil, WithTheme(asciiTheme())), 100, 30)
		m = press(m, fmt.Sprint(i+1))
		v := m.View()
		if !strings.Contains(v, d) || !strings.Contains(v, "not connected") || !strings.Contains(v, "how to connect") {
			t.Errorf("%s:\n%s", d, v)
		}
	}
}

func TestBrokenProviderExplainsItselfInTheDomain(t *testing.T) {
	var m tea.Model = New([]core.Entry{{Module: brokenTasks(), Detection: core.Detection{State: core.Broken, Detail: "bd exited 2"}}}, WithTheme(asciiTheme()))
	m = press(sized(m, 100, 30), "2")
	if v := m.View(); !strings.Contains(v, "not connected") || !strings.Contains(v, "broken") {
		t.Fatalf("%s", v)
	}
}

func brokenTasks() core.Module {
	f := tasksFake()
	return brokenTaskMod{f}
}

// brokenTaskMod is a Tasks provider whose install is broken, so the domain is not connected.
type brokenTaskMod struct{ taskProv }

// ---- Tasks section ----

type tasksRig struct {
	m     tea.Model
	prov  taskProv
	audit *memAudit
	full  *[]string
}

func newTasksRig(t *testing.T) tasksRig {
	t.Helper()
	p := tasksFake()
	au := &memAudit{}
	var full []string
	en := []core.Entry{{Module: p, Detection: p.Det}}
	var m tea.Model = New(en, WithTheme(asciiTheme()), WithAudit(au), WithFullView(func(dir string) tea.Cmd {
		full = append(full, dir)
		return nil
	}))
	m = sized(m, 100, 40)
	m, _ = m.Update(tasksMsg{report: fixtureTasks()})
	m, _ = m.Update(insightsMsg{report: fixtureInsights()})
	m = press(m, "2")
	return tasksRig{m, p, au, &full}
}

// pressRun presses a key and runs the command it returns, feeding the results back (a Batch is
// flattened), so an async step such as the re-read before a confirm completes.
func pressRun(t *testing.T, m tea.Model, k string) tea.Model {
	t.Helper()
	m, cmd := m.Update(runeOrKey(k))
	return run(t, m, cmd)
}

func typeText(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m = press(m, string(r))
	}
	return m
}

func TestTasksShowsCompactSummaryAndInsights(t *testing.T) {
	v := newTasksRig(t).m.View()
	for _, want := range []string{"14 tasks", "ready 4", "Recent changes", "ac-12", "Insights (graph analysis)", "next", "critical path", "cycle"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
}

func TestInsightsNotConnectedShowsHowToInstall(t *testing.T) {
	r := newTasksRig(t)
	m, _ := r.m.Update(insightsMsg{report: core.InsightsReport{Header: core.Header{State: core.NotConnected, HowToConnect: core.HowToConnectInsights}}})
	if v := m.View(); !strings.Contains(v, "brew install dicklesworthstone/tap/bv") || !strings.Contains(v, "not connected") {
		t.Fatalf("%s", v)
	}
}

// The confirm gate: no keystroke path writes until y on the confirm screen.
func TestCreateNeedsAConfirmScreenAndOnlyYWrites(t *testing.T) {
	r := newTasksRig(t)
	m := press(r.m, "n")
	m = typeText(m, "ship it")
	m = press(m, "enter") // description
	m = press(m, "enter") // priority
	m = press(m, "enter") // review
	v := m.View()
	for _, want := range []string{"Confirm this change", `title: "ship it"`, "exact command:", `bd create "--title=ship it"`, "y apply"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm screen missing %q:\n%s", want, v)
		}
	}
	if len(*r.prov.ran) != 0 || r.audit.n != 0 {
		t.Fatal("nothing may run before y")
	}
	for _, k := range []string{"enter", "x", "e", "o", "q", "1", "tab"} {
		var cmd tea.Cmd
		m, cmd = m.Update(runeOrKey(k))
		if cmd != nil {
			if _, quit := cmd().(tea.QuitMsg); quit {
				t.Fatalf("%q quit from the confirm screen", k)
			}
		}
	}
	if len(*r.prov.ran) != 0 {
		t.Fatalf("a stray key wrote: %v", *r.prov.ran)
	}
	m, cmd := m.Update(runeOrKey("y"))
	m = run(t, m, cmd)
	if len(*r.prov.ran) != 1 || strings.Join((*r.prov.ran)[0], " ") != "bd create --title=ship it" {
		t.Fatalf("ran %v", *r.prov.ran)
	}
	if r.audit.n != 2 {
		t.Fatalf("audit lines %d", r.audit.n)
	}
	if v := m.View(); !strings.Contains(v, "created ac-99") {
		t.Fatalf("no result notice:\n%s", v)
	}
}

func runeOrKey(k string) tea.KeyMsg {
	if k == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	if k == "tab" {
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	if k == "esc" {
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func TestCancelAtConfirmWritesNothing(t *testing.T) {
	for _, k := range []string{"n", "esc"} {
		r := newTasksRig(t)
		m := pressRun(t, r.m, "x") // close the selected task (re-reads it first)
		if !strings.Contains(m.View(), "close task ac-12") {
			t.Fatalf("no confirm screen:\n%s", m.View())
		}
		var cmd tea.Cmd
		m, cmd = m.Update(runeOrKey(k))
		m = run(t, m, cmd) // a returned command must not write either
		if len(*r.prov.ran) != 0 || r.audit.n != 0 || !strings.Contains(m.View(), "cancelled") {
			t.Fatalf("%s: ran=%v audit=%d\n%s", k, *r.prov.ran, r.audit.n, m.View())
		}
	}
}

func TestEditPrefillsAndOnlySendsWhatChanged(t *testing.T) {
	r := newTasksRig(t)
	m := press(r.m, "e")
	if !strings.Contains(m.View(), "wire the lease heartbeat") {
		t.Fatalf("title not prefilled:\n%s", m.View())
	}
	m = press(m, "enter")
	m = press(m, "enter")
	m = press(m, "enter")
	m = pressRun(t, m, "enter") // review with nothing changed
	if v := m.View(); !strings.Contains(v, "nothing to change") {
		t.Fatalf("an edit with no change must not reach the confirm screen:\n%s", v)
	}
	m = press(m, "backspace") // status field: nothing; go back to the title and edit it
	m = press(m, "shift+tab")
	m = press(m, "shift+tab")
	m = press(m, "shift+tab")
	m = typeText(m, "!")
	m = press(m, "enter")
	m = press(m, "enter")
	m = press(m, "enter")
	m = pressRun(t, m, "enter")
	v := m.View()
	if !strings.Contains(v, `title: "wire the lease heartbeat" -> "wire the lease heartbeat!"`) || !strings.Contains(v, `bd update ac-12 "--title=wire`) {
		t.Fatalf("%s", v)
	}
}

func TestFormEscCancelsAndInvalidPriorityStaysInTheForm(t *testing.T) {
	r := newTasksRig(t)
	m := press(r.m, "n")
	m = typeText(m, "x")
	m = press(m, "tab")
	m = press(m, "tab")
	m = typeText(m, "9x")
	m = press(m, "enter")
	if v := m.View(); !strings.Contains(v, "priority must be a number 0-4") || strings.Contains(v, "Confirm") {
		t.Fatalf("%s", v)
	}
	m = press(m, "esc")
	if v := m.View(); !strings.Contains(v, "cancelled") || len(*r.prov.ran) != 0 {
		t.Fatalf("%s", v)
	}
}

func TestFormCapturesGlobalKeys(t *testing.T) {
	r := newTasksRig(t)
	m := press(r.m, "n")
	m, cmd := m.Update(runeOrKey("q"))
	if cmd != nil {
		t.Fatal("q in the form must type, not quit")
	}
	m = press(m, "3")
	if !strings.Contains(lines(m)[0], "[Tasks]") || !strings.Contains(m.View(), "q3") {
		t.Fatalf("digits go to the form:\n%s", m.View())
	}
}

func TestWritesUnavailableWithoutAWritableProvider(t *testing.T) {
	m := sized(New(nil, WithTheme(asciiTheme())), 100, 30)
	m = press(m, "2")
	m = press(m, "n")
	if strings.Contains(m.View(), "New task") {
		t.Fatal("no form without a provider")
	}
}

func TestFailedWriteIsShownAndNothingIsClaimedDone(t *testing.T) {
	r := newTasksRig(t)
	m := pressRun(t, r.m, "x")
	m, _ = m.Update(taskAppliedMsg{err: errors.New("bd: boom\x1b[2J"), op: core.TaskClose})
	v := m.View()
	if !strings.Contains(v, "not applied: bd: boom") || strings.ContainsRune(v, 0x1b) {
		t.Fatalf("%s", v)
	}
}

// ---- full-view handoff ----

func TestFullViewHandoffOnlyOnOAndOnlyForTasks(t *testing.T) {
	r := newTasksRig(t)
	if len(*r.full) != 0 {
		t.Fatal("nothing may launch at startup")
	}
	m := press(r.m, "o")
	if len(*r.full) != 1 {
		t.Fatalf("o on Tasks calls the handoff once, got %v", *r.full)
	}
	m = press(m, "1") // Board is not connected here and has no b9s handoff: only Tasks has one
	_ = press(m, "o")
	if len(*r.full) != 1 {
		t.Fatalf("o on Board must not call the b9s handoff, got %v", *r.full)
	}
	m = press(sized(New(nil, WithTheme(asciiTheme()), WithFullView(func(string) tea.Cmd { t.Fatal("no handoff on Leases"); return nil })), 100, 30), "3")
	press(m, "o")
}

func TestFullViewMissingIsAnErrorNoticeWithAnInstallHint(t *testing.T) {
	r := newTasksRig(t)
	m, _ := r.m.Update(fullViewDoneMsg{err: errors.New(core.FullViewInstallHint)})
	if v := m.View(); !strings.Contains(v, "b9s is not installed") || !strings.Contains(v, "github.com/vanderheijden86/b9s") {
		t.Fatalf("%s", v)
	}
	m = press(m, "j") // any key clears the notice
	if strings.Contains(m.View(), "not installed") {
		t.Fatal("notice must clear")
	}
}

func TestDefaultFullViewProbesOnlyWhenRunAndReportsAMissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // b9s cannot be found
	msg := defaultFullView("/tmp")()
	d, ok := msg.(fullViewDoneMsg)
	if !ok || d.err == nil || !strings.Contains(d.err.Error(), "not installed") {
		t.Fatalf("%#v", msg)
	}
}

func TestHostileTextInTasksNeverReachesTheScreen(t *testing.T) {
	r := newTasksRig(t)
	bad := "x\x1b[2J\u202etxt\u200b\x07y"
	rep := fixtureTasks()
	rep.Recent[0].Title, rep.Recent[0].ID = bad, bad
	in := fixtureInsights()
	in.TopPicks[0].Title, in.Reason = bad, bad
	m, _ := r.m.Update(tasksMsg{report: rep})
	m, _ = m.Update(insightsMsg{report: in})
	for _, w := range []int{20, 40, 100} {
		v := sized(m, w, 30).View()
		for _, c := range v {
			if c < 0x20 && c != '\n' || c == 0x7f || c >= 0x200b && c <= 0x200f || c >= 0x202a && c <= 0x202e {
				t.Fatalf("w=%d: forbidden rune %U in %q", w, c, v)
			}
		}
	}
}

// ---- stale confirm, edited fields, refresh guard, unknown outcome ----

func TestConfirmShowsAChangedSinceBannerAndTheFreshOldValues(t *testing.T) {
	r := newTasksRig(t)
	(*r.prov.fresh)["ac-12"] = core.Task{ID: "ac-12", Title: "wire the lease heartbeat v2", Status: "blocked", Priority: 1}
	m := pressRun(t, r.m, "x")
	v := m.View()
	for _, want := range []string{"changed since you opened it", "title", "status", "Confirm this change", `"wire the lease heartbeat v2"`, "y apply"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	if len(*r.prov.ran) != 0 {
		t.Fatal("verification must not write")
	}
	m = pressRun(t, m, "y")
	if len(*r.prov.ran) != 1 {
		t.Fatalf("y on the refreshed plan applies it: %v", *r.prov.ran)
	}
}

func TestConfirmWithoutChangeHasNoBanner(t *testing.T) {
	r := newTasksRig(t)
	if v := pressRun(t, r.m, "x").View(); strings.Contains(v, "changed since") || !strings.Contains(v, "Confirm this change") {
		t.Fatalf("%s", v)
	}
}

func TestUnverifiableIssueIsFlaggedNotSilentlyConfirmed(t *testing.T) {
	r := newTasksRig(t)
	*r.prov.readErr = errors.New("bd show exited 1: issue not found?")
	v := pressRun(t, r.m, "x").View()
	if !strings.Contains(v, "could not re-read") || !strings.Contains(v, "Confirm this change") {
		t.Fatalf("%s", v)
	}
}

func TestEscWhileCheckingCancelsAndALateAnswerIsIgnored(t *testing.T) {
	r := newTasksRig(t)
	m, cmd := r.m.Update(runeOrKey("x")) // now verifying
	if !strings.Contains(m.View(), "Checking") {
		t.Fatalf("%s", m.View())
	}
	m = press(m, "esc")
	m = run(t, m, cmd) // the late answer
	if strings.Contains(m.View(), "Confirm this change") {
		t.Fatalf("a late answer must not open the confirm screen:\n%s", m.View())
	}
}

func TestPriorityIsSentWheneverTheUserEditedIt(t *testing.T) {
	r := newTasksRig(t)
	m := press(r.m, "e")
	m = press(m, "tab")
	m = press(m, "tab") // priority field, prefilled with 1
	m = press(m, "backspace")
	m = typeText(m, "1")  // the same value again
	m = press(m, "enter") // on to the status field
	m = pressRun(t, m, "enter")
	v := m.View()
	if !strings.Contains(v, "--priority=1") {
		t.Fatalf("an edited priority must be sent even if it equals the old one:\n%s", v)
	}
}

func TestRefreshIsIgnoredWhileOneIsInFlight(t *testing.T) {
	r := newTasksRig(t)
	_, c1 := r.m.Update(runeOrKey("r"))
	_, c2 := r.m.Update(runeOrKey("r"))
	if c1 == nil || c2 != nil {
		t.Fatalf("a second r while the first is in flight must be ignored (c1=%v c2=%v)", c1 != nil, c2 != nil)
	}
	m := run(t, r.m, c1) // the answers arrive
	if _, c3 := m.Update(runeOrKey("r")); c3 == nil {
		t.Fatal("r works again once the refresh finished")
	}
}

func TestUnknownOutcomeIsShownAsUnknownNeverNotApplied(t *testing.T) {
	r := newTasksRig(t)
	m, _ := r.m.Update(taskAppliedMsg{err: core.UnknownOutcome("bd timed out; refresh before retrying"), op: core.TaskClose})
	v := m.View()
	if !strings.Contains(v, "result unknown: bd timed out; refresh before retrying") || strings.Contains(v, "not applied") {
		t.Fatalf("%s", v)
	}
}

func TestQueuesLabelsDemoDataAndTheTrackerSubset(t *testing.T) {
	m := sized(load(t, asciiTheme(), nil), 100, 30)
	m = press(m, "4")
	if v := m.View(); !strings.Contains(v, "(demo data)") {
		t.Fatalf("demo data must be labelled:\n%s", v)
	}
}

// outsideAllowlist drops the regions where a provider may be named: a "how to connect" fix
// message (including the wrapped lines under it), and the exact command on a confirm screen.
// Everything else on a domain tab is domain language.
func outsideAllowlist(view string) []string {
	var out []string
	skip := false
	for _, l := range strings.Split(view, "\n") {
		switch {
		case strings.Contains(l, "how to connect:"), strings.Contains(l, "Insights") && strings.Contains(l, "not connected"), strings.Contains(l, "Insights") && strings.Contains(l, "unavailable"):
			skip = true
		case strings.Contains(l, "exact command:"):
			skip = true
		case strings.TrimSpace(l) == "":
			skip = false
		}
		// The daemon's own actor identity (for example lambe@host) is data, not a label.
		if !skip && !strings.HasPrefix(l, "? help") && !strings.Contains(l, "actor ") {
			out = append(out, l)
		}
	}
	return out
}

func TestDomainBodiesNameNoToolOutsideFixMessages(t *testing.T) {
	banned := []string{"Lambe", "lambe", "beads-lease", "board-beads", "b9s", "Rex", "bv", "bd", "Vercel", "vb", "@vercel/", "board CLI", "npm"}
	build := map[string]func() tea.Model{
		"data": func() tea.Model { return load(t, asciiTheme(), nil) },
		"none": func() tea.Model { return New(nil, WithTheme(asciiTheme()), WithFullView(noFullView)) },
	}
	for name, mk := range build {
		for _, w := range []int{60, 80, 120} {
			for _, k := range []string{"1", "2", "3", "4"} { // the four domains; Doctor is the one place providers are named
				m := press(sized(mk(), w, 40), k)
				for _, l := range outsideAllowlist(m.View()) {
					for _, bad := range banned {
						if strings.Contains(l, bad) {
							t.Errorf("%s tab %s w=%d: %q names %q", name, k, w, l, bad)
						}
					}
				}
			}
		}
	}
}

func TestInsightsFollowAnEmptyTrackerOnceItHasATask(t *testing.T) {
	r := newTasksRig(t)
	sec := r.m.(*Model).tabs[1].sec.(*tasksSection)
	empty := fixtureTasks()
	empty.Counts = core.TaskCounts{}
	empty.Recent = nil
	_, cmd := sec.Update(tasksMsg{report: empty})
	if cmd != nil || sec.insights == nil || !sec.insights.Empty {
		t.Fatalf("an empty tracker says no tasks yet and runs nothing: cmd=%v ins=%+v", cmd != nil, sec.insights)
	}
	_, cmd = sec.Update(tasksMsg{report: fixtureTasks()})
	if cmd == nil {
		t.Fatal("the first task must trigger an insights read, not leave 'no tasks yet' on screen")
	}
}

// While the terminal is too small the confirm screen is hidden, so no key may reach it: a change
// must never be applied on a screen the user cannot read. Growing the window back restores it.
func TestTooSmallTerminalCannotApplyAHiddenConfirm(t *testing.T) {
	r := newTasksRig(t)
	m := press(r.m, "n")
	m = typeText(m, "ship it")
	m = press(m, "enter")
	m = press(m, "enter")
	m = press(m, "enter")
	if !strings.Contains(m.View(), "Confirm this change") {
		t.Fatalf("no confirm screen to hide:\n%s", m.View())
	}
	for _, size := range [][2]int{{39, 30}, {100, 11}} {
		m = sized(m, size[0], size[1])
		if !strings.Contains(m.View(), "Terminal too small") {
			t.Fatalf("%v should be too small:\n%s", size, m.View())
		}
		for _, k := range []string{"y", "enter", "n", "esc"} {
			var cmd tea.Cmd
			m, cmd = m.Update(runeOrKey(k))
			m = run(t, m, cmd)
		}
		if len(*r.prov.ran) != 0 || r.audit.n != 0 {
			t.Fatalf("%v: a key reached the hidden confirm and wrote: %v", size, *r.prov.ran)
		}
	}
	// ctrl+c still gets the user out.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Fatal("ctrl+c must quit even when the terminal is too small")
	}
	m = sized(m, 100, 30)
	if !strings.Contains(m.View(), "Confirm this change") {
		t.Fatalf("the pending change must survive the resize:\n%s", m.View())
	}
	m, cmd := m.Update(runeOrKey("y"))
	m = run(t, m, cmd)
	if len(*r.prov.ran) != 1 {
		t.Fatalf("y on the restored confirm screen must apply once, ran %v", *r.prov.ran)
	}
}

// The same holds for a hidden list: no key may launch the handoff or open a form on a screen the
// user cannot see, and the keys work again once the window is big enough.
func TestTooSmallTerminalGivesAHiddenListNoKeys(t *testing.T) {
	r := newTasksRig(t)
	m := sized(r.m, 39, 30)
	for _, k := range []string{"o", "n", "e", "x"} {
		m = press(m, k)
	}
	if len(*r.full) != 0 {
		t.Fatalf("o launched a handoff from a hidden list: %v", *r.full)
	}
	m = sized(m, 100, 40)
	if strings.Contains(m.View(), "New task") || strings.Contains(m.View(), "Confirm this change") {
		t.Fatalf("a hidden key opened a form:\n%s", m.View())
	}
	m = press(m, "o")
	if len(*r.full) != 1 {
		t.Fatalf("o works again at a usable size, ran %v", *r.full)
	}
}
