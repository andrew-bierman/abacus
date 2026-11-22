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
		m := press(r.m, "x") // close the selected task
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
	m = press(m, "enter") // review with nothing changed
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
	m = press(m, "enter")
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
	m := press(r.m, "x")
	m, _ = m.Update(taskAppliedMsg{err: errors.New("bd: boom\x1b[2J"), op: core.TaskClose})
	v := m.View()
	if !strings.Contains(v, "not applied: bd: boom") || strings.ContainsRune(v, 0x1b) {
		t.Fatalf("%s", v)
	}
}

// ---- full-view handoff ----

func TestFullViewHandoffOnlyOnOAndOnlyForTasksAndBoard(t *testing.T) {
	r := newTasksRig(t)
	if len(*r.full) != 0 {
		t.Fatal("nothing may launch at startup")
	}
	m := press(r.m, "o")
	if len(*r.full) != 1 {
		t.Fatalf("o on Tasks calls the handoff once, got %v", *r.full)
	}
	m = press(m, "1") // Board is not connected but also offers the handoff
	_ = press(m, "o")
	if len(*r.full) != 2 {
		t.Fatalf("o on Board calls the handoff, got %v", *r.full)
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
