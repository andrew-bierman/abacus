package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// counting counts Doctor runs, to prove how many fetches a keypress storm causes.
type counting struct {
	*modtest.Fake
	doctors atomic.Int32
}

func (c *counting) Doctor(ctx context.Context) []core.Check {
	c.doctors.Add(1)
	return c.Fake.Doctor(ctx)
}

func runCmd(m tea.Model, cmd tea.Cmd) tea.Model {
	if cmd == nil {
		return m
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	return m
}

func manyRuns(n int) lambe.View {
	runs := make([]lambe.Run, n)
	for i := range runs {
		runs[i] = lambe.Run{Issue: fmt.Sprintf("R-%02d", i), State: "running", Phase: "p", Machine: "m", Workflow: "wf"}
	}
	return lambe.View{Snapshot: lambe.Snapshot{Runs: runs,
		NeedsHuman: []lambe.NeedsHuman{{Issue: "R-05", Reason: "gate_failed", Detail: "merge conflict"}}},
		Queue: lambe.Queue{Rows: []lambe.QueueRow{{ID: "Q-1", Title: "task"}}}}
}

func lambeAt(w, h int, v lambe.View) tea.Model {
	s := newLambeSection(lambe.NewDemo(), nil)
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(lambeViewMsg{view: v})
	return sized(m, w, h)
}

func TestNeedsAttentionStaysVisibleWhenRunsFillTheHeight(t *testing.T) {
	for _, w := range []int{40, 80, 120} {
		for _, h := range []int{6, 8, 10, 30} {
			v := lambeAt(w, h, manyRuns(25)).View()
			if !strings.Contains(v, "Needs attention 1") {
				t.Errorf("%dx%d: attention clipped:\n%s", w, h, v)
			}
			if n := strings.Count(strings.TrimRight(v, "\n"), "\n") + 1; n > h {
				t.Errorf("%dx%d: %d lines", w, h, n)
			}
			if !strings.Contains(v, "more") {
				t.Errorf("%dx%d: clipped run list has no +N more marker:\n%s", w, h, v)
			}
		}
	}
}

func TestAttentionDetailRowShowsWhenThereIsRoom(t *testing.T) {
	if v := lambeAt(80, 12, manyRuns(25)).View(); !strings.Contains(v, "gate_failed") {
		t.Fatalf("attention row missing at 80x12:\n%s", v)
	}
}

func TestNoMoreMarkerWhenEverythingFits(t *testing.T) {
	if v := lambeAt(80, 30, manyRuns(3)).View(); strings.Contains(v, "more") {
		t.Fatalf("spurious marker:\n%s", v)
	}
}

func TestDoctorRefreshKeyIsIgnoredWhileARunIsPending(t *testing.T) {
	c := &counting{Fake: fake("a", core.Ready)}
	c.Checks = []core.Check{{Name: "bin", Status: core.StatusOK}}
	var m tea.Model = New(entriesFor2(c), WithTheme(asciiTheme()))
	m = sized(m, 80, 20)
	m, cmd := m.Update(runeKey("6"))
	m = runCmd(m, cmd) // first open: one doctor run
	if c.doctors.Load() != 1 {
		t.Fatalf("first open ran doctor %d times", c.doctors.Load())
	}
	var cmds []tea.Cmd
	for i := 0; i < 3; i++ {
		var cmd tea.Cmd
		m, cmd = m.Update(runeKey("r"))
		cmds = append(cmds, cmd)
	}
	if !strings.Contains(m.View(), "running doctor") {
		t.Fatalf("a pending refresh must say so:\n%s", m.View())
	}
	issued := 0
	for _, cmd := range cmds {
		if cmd != nil {
			issued++
			m = runCmd(m, cmd)
		}
	}
	if issued != 1 || c.doctors.Load() != 2 {
		t.Fatalf("r x3 issued %d fetches, doctor ran %d times, want 1 and 2", issued, c.doctors.Load())
	}
	if strings.Contains(m.View(), "running doctor") {
		t.Fatalf("pending flag must clear when the report arrives:\n%s", m.View())
	}
	m, cmd = m.Update(runeKey("r"))
	if cmd == nil {
		t.Fatal("r must work again after the report arrived")
	}
}

func TestStaleDoctorReportIsDropped(t *testing.T) {
	d := newDoctorSection([]core.Entry{{Module: fake("a", core.Ready), Detection: core.Detection{State: core.Ready}}})
	d.SetTheme(asciiTheme())
	var m tea.Model = d
	m, c1 := m.Update(runeKey("r"))
	_ = c1
	d.pending = false // as if the first run was abandoned
	m, _ = m.Update(runeKey("r"))
	fresh := core.Report{Modules: []core.ModuleReport{{Name: "Fresh", Detection: core.Detection{State: core.Ready}}}}
	stale := core.Report{Modules: []core.ModuleReport{{Name: "Stale", Detection: core.Detection{State: core.Ready}}}}
	m, _ = m.Update(doctorMsg{report: fresh, seq: d.seq})
	m, _ = m.Update(doctorMsg{report: stale, seq: d.seq - 1})
	if v := m.View(); !strings.Contains(v, "Fresh") || strings.Contains(v, "Stale") {
		t.Fatalf("out-of-order report applied:\n%s", v)
	}
}

func entriesFor2(mods ...*counting) []core.Entry {
	var out []core.Entry
	for _, c := range mods {
		out = append(out, core.Entry{Module: c, Detection: c.Det})
	}
	return out
}

func TestRunProbesTheBackgroundBeforeBubbleTeOwnsTheTerminal(t *testing.T) {
	var order []string
	oldProbe, oldStart := probeBackground, startProgram
	defer func() { probeBackground, startProgram = oldProbe, oldStart }()
	probeBackground = func() bool { order = append(order, "probe"); return true }
	startProgram = func(tea.Model, ...tea.ProgramOption) error { order = append(order, "start"); return nil }
	if err := Run(nil, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "probe,start" {
		t.Fatalf("order = %v", order)
	}
}

func TestTypedLeasesDriveBadgesNotTextParsing(t *testing.T) {
	r := leasesReport(
		core.Lease{Resource: "r:1", State: core.LeaseStale, Holder: "bob"},
		core.Lease{Resource: "r:2", State: core.LeaseHeld, Holder: "alice", ExpiresAt: "5m"},
		core.Lease{Resource: "r:3", State: "from-a-newer-provider"})
	s := leasesWith(&r)
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(leasesMsg{read: r})
	v := m.View()
	rowWith(t, v, "(stale)", "r:1", "bob")
	rowWith(t, v, "(held)", "r:2", "alice", "5m")
	rowWith(t, v, "(from-", "r:3") // an unknown state still gets a row; the table may cut its text
	m = press(press(press(m, "down"), "down"), "enter")
	if !strings.Contains(m.View(), "(from-a-newer-provider)") {
		t.Errorf("the whole unknown state must be readable in the detail:\n%s", m.View())
	}
	th := NewTheme(rendererFor(termenv.TrueColor, true))
	for tone, kind := range map[core.Tone]Kind{core.ToneOK: KindOK, core.ToneWarn: KindWarn, core.ToneFail: KindFail, core.ToneInfo: KindAccent, core.ToneMuted: KindMuted} {
		if toneKind(tone) != kind {
			t.Errorf("toneKind(%s) = %v, want %v", tone, toneKind(tone), kind)
		}
	}
	_ = th
}

func TestEnterOnlyDrillsInWhenStacked(t *testing.T) {
	s := lambeAt(120, 30, manyRuns(3))
	s = press(s, "enter")
	s = sized(s, 80, 30)
	if v := s.View(); strings.Contains(v, "machine") {
		t.Fatalf("enter in the wide layout must not leave the narrow layout drilled in:\n%s", v)
	}
}

func TestEscFromDetailKeepsTheFilterUntilASecondEsc(t *testing.T) {
	var s tea.Model = lambeAt(80, 30, manyRuns(30))
	s = press(s, "/")
	for _, r := range "r-0" {
		s = press(s, string(r))
	}
	s = press(s, "enter")
	s = press(s, "enter") // drill in
	s = press(s, "esc")
	if v := s.View(); !strings.Contains(v, "10/30") {
		t.Fatalf("first esc must only close the detail:\n%s", v)
	}
	s = press(s, "esc")
	if v := s.View(); strings.Contains(v, "10/30") {
		t.Fatalf("second esc must clear the filter:\n%s", v)
	}
}

func TestFilterThatHidesTheSelectionDoesNotMoveItPermanently(t *testing.T) {
	calls, h := recorder()
	s := newLambeSection(lambe.NewDemo(), h)
	var m tea.Model = s
	m, _ = m.Update(viewOf("A-1", "B-2", "C-3"))
	m = press(m, "j") // select B-2
	m = press(m, "/")
	for _, r := range "c-3" { // matches only C-3, so B-2 is hidden
		m = press(m, string(r))
	}
	if strings.Contains(m.View(), "B-2") {
		t.Fatal("test setup: the filter must hide B-2")
	}
	m = press(m, "esc")
	_ = press(m, "o")
	if len(*calls) != 1 || (*calls)[0][2] != "mac-B-2" {
		t.Fatalf("after clearing the filter the original run must be selected, got %v", *calls)
	}
}

func TestFilterMatchesEachHaystackField(t *testing.T) {
	v := lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{
		{Issue: "I-1", State: "alpha", Phase: "p1", Machine: "m1", Workflow: "w1"},
		{Issue: "I-2", State: "s2", Phase: "bravo", Machine: "m2", Workflow: "w2"},
		{Issue: "I-3", State: "s3", Phase: "p3", Machine: "charlie", Workflow: "w3"},
		{Issue: "I-4", State: "s4", Phase: "p4", Machine: "m4", Workflow: "delta"},
	}}}
	for needle, want := range map[string]string{"ALPHA": "I-1", "bravo": "I-2", "Charlie": "I-3", "delta": "I-4", "i-2": "I-2"} {
		var m tea.Model = lambeAt(120, 30, v)
		m = press(m, "/")
		for _, r := range needle {
			m = press(m, string(r))
		}
		m = press(m, "enter")
		out := m.View()
		var left []string
		for _, l := range strings.Split(out, "\n") {
			// The list is the first framed panel: the text between its left border and the next.
			if parts := strings.Split(l, "│"); len(parts) > 1 {
				left = append(left, parts[1])
			}
		}
		list := strings.Join(left, "\n")
		for _, id := range []string{"I-1", "I-2", "I-3", "I-4"} {
			if got := strings.Contains(list, id); got != (id == want) {
				t.Errorf("filter %q: %s visible=%v, want only %s\n%s", needle, id, got, want, out)
			}
		}
	}
}

func TestLongRunListFollowsTheCursorAtSmallHeight(t *testing.T) {
	var m tea.Model = lambeAt(80, 6, manyRuns(20))
	for i := 0; i < 19; i++ {
		m = press(m, "j")
	}
	v := m.View()
	if !strings.Contains(v, "▸ R-19") {
		t.Fatalf("cursor row scrolled out of view:\n%s", v)
	}
	if n := strings.Count(strings.TrimRight(v, "\n"), "\n") + 1; n > 6 {
		t.Fatalf("%d lines at h=6", n)
	}
	for i := 0; i < 19; i++ {
		m = press(m, "k")
	}
	if !strings.Contains(m.View(), "▸ R-00") {
		t.Fatalf("scrolling back up lost the cursor:\n%s", m.View())
	}
}

func TestNoticeRowFitsTheHeight(t *testing.T) {
	var m tea.Model = lambeAt(80, 5, manyRuns(10))
	m, _ = m.Update(handoffDoneMsg{err: fmt.Errorf("boom")})
	v := m.View()
	if n := strings.Count(strings.TrimRight(v, "\n"), "\n") + 1; n > 5 || !strings.Contains(v, "rex: boom") {
		t.Fatalf("notice must fit the height (%d lines):\n%s", n, v)
	}
}

func TestScrollKeysMoveAndClampLeasesAndDoctorAtSmallHeight(t *testing.T) {
	var ls []core.Lease
	for i := 0; i < 30; i++ {
		ls = append(ls, core.Lease{Resource: fmt.Sprintf("L%02d", i), State: core.LeaseFree})
	}
	lr := leasesReport(ls...)
	g := leasesWith(&lr)
	g.SetTheme(asciiTheme())
	d := newDoctorSection(nil)
	d.SetTheme(asciiTheme())
	var checks []core.Check
	for i := 0; i < 30; i++ {
		checks = append(checks, core.Check{Name: fmt.Sprintf("L%02d", i), Status: core.StatusOK})
	}
	for name, tc := range map[string]struct {
		s    tea.Model
		last string
	}{"leases": {g, "L29"}, "doctor": {d, "L29"}} {
		var m tea.Model = tc.s
		m, _ = m.Update(leasesMsg{read: lr})
		m, _ = m.Update(doctorMsg{report: core.Report{Modules: []core.ModuleReport{{Name: "mod", Detection: core.Detection{State: core.Ready}, Checks: checks}}}})
		m = sized(m, 60, 6)
		first := m.View()
		if strings.Contains(first, "L29") || !strings.Contains(first, "more") {
			t.Fatalf("%s: initial window wrong:\n%s", name, first)
		}
		m = press(m, "j")
		if m.View() == first {
			t.Fatalf("%s: j did not scroll", name)
		}
		for i := 0; i < 100; i++ {
			m = press(m, "j")
		}
		m = press(m, "k") // before any View: an unclamped offset would still be far past the end
		oneUp := m.View()
		m = press(m, "j")
		end := m.View()
		if oneUp == end {
			t.Fatalf("%s: one k after hammering j must scroll back (offset left unclamped)", name)
		}
		if !strings.Contains(end, tc.last) {
			t.Fatalf("%s: end of list not reachable:\n%s", name, end)
		}
		m = press(m, "j")
		if m.View() != end {
			t.Fatalf("%s: scrolling past the end must clamp", name)
		}
		for i := 0; i < 100; i++ {
			m = press(m, "k")
		}
		if m.View() != first {
			t.Fatalf("%s: k must return to the top", name)
		}
		if n := strings.Count(strings.TrimRight(end, "\n"), "\n") + 1; n > 6 {
			t.Fatalf("%s: %d lines at h=6", name, n)
		}
	}
}

func TestHostileFieldsInLeasesAndDoctorSections(t *testing.T) {
	bad := "x\x1b[2J\u202etxt\u200b\u200d\ufeff\x07y"
	hostile := leasesRead{report: core.LeasesReport{Header: core.Header{Source: bad},
		Leases: []core.Lease{
			{Resource: bad, State: core.LeaseHeld, Holder: bad, ExpiresAt: bad},
			{Resource: bad, State: core.LeaseStale, Holder: bad},
			{Resource: bad, State: core.LeaseState(bad)}}}}
	g := leasesWith(&hostile)
	g.SetTheme(asciiTheme())
	var m tea.Model = g
	m, _ = m.Update(leasesMsg{read: hostile})
	m = sized(m, 40, 20)
	check := func(name, v string) {
		for _, r := range v {
			if r < 0x20 && r != '\n' || r == 0x7f || r >= 0x200b && r <= 0x200f || r >= 0x202a && r <= 0x202e || r == 0xfeff {
				t.Errorf("%s: forbidden rune %U in %q", name, r, v)
			}
		}
		for _, l := range strings.Split(v, "\n") {
			if lipgloss.Width(l) > 40 {
				t.Errorf("%s: %q too wide", name, l)
			}
		}
	}
	check("leases", m.View())
	m, _ = m.Update(leasesMsg{read: leasesRead{unavailable: bad}})
	check("leases unavailable", m.View())
	m, _ = m.Update(leasesMsg{read: leasesRead{unavailable: bad, howToConnect: bad}})
	check("leases not connected", m.View())

	d := newDoctorSection(nil)
	d.SetTheme(asciiTheme())
	m = d
	m, _ = m.Update(doctorMsg{report: core.Report{Modules: []core.ModuleReport{{Name: bad,
		Detection: core.Detection{State: core.Ready, Version: bad},
		Checks:    []core.Check{{Name: bad, Status: core.CheckStatus(bad), Detail: bad, Fix: bad}}}}}})
	m = sized(m, 40, 20)
	check("doctor", m.View())
}

func TestHostileUnicodeInLambeFields(t *testing.T) {
	bad := "e\u0301\u0301\u0301\u0301\u0301 \u202e\u200b \U0001F468\u200d\U0001F469\u200d\U0001F467 日本語"
	v := manyRuns(2)
	v.Snapshot.Runs[0] = lambe.Run{Issue: bad, State: bad, Phase: bad, Machine: bad, Actor: bad, Workflow: bad, Session: bad}
	for _, w := range []int{20, 40, 120} {
		m := lambeAt(w, 20, v)
		for _, view := range []string{m.View(), press(m, "enter").View()} {
			for _, r := range view {
				if r >= 0x200b && r <= 0x200f || r >= 0x202a && r <= 0x202e {
					t.Fatalf("w=%d: %U reached the screen", w, r)
				}
			}
			for _, l := range strings.Split(view, "\n") {
				if lipgloss.Width(l) > w {
					t.Fatalf("w=%d: %q is %d wide", w, l, lipgloss.Width(l))
				}
			}
		}
	}
}

func TestPaneBreakpointIsExactlyOneHundred(t *testing.T) {
	th := asciiTheme()
	list, detail := []string{"LIST"}, []string{"DETAIL"}
	if g := duoLayout(99, 9); g.wide || strings.Contains(strings.Join(g.draw(th, "L", list, "D", detail, false), "\n"), "DETAIL") {
		t.Fatal("99 columns must be stacked")
	}
	g := duoLayout(100, 9)
	if out := strings.Join(g.draw(th, "L", list, "D", detail, false), "\n"); !g.wide || !strings.Contains(out, "DETAIL") || !strings.Contains(out, "LIST") {
		t.Fatalf("100 columns must be side by side: %q", out)
	}
}

func TestHeaderCompactionThresholds(t *testing.T) {
	var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
	for w, brand := range map[int]bool{59: false, 60: true} {
		mm := sized(m, w, 20)
		if got := strings.Contains(lines(mm)[0], "abacus"); got != brand {
			t.Errorf("width %d: brand present=%v, want %v: %q", w, got, brand, lines(mm)[0])
		}
	}
	for h, rule := range map[int]bool{15: false, 16: true} {
		mm := sized(m, 80, h)
		// the rule is a row of nothing but "─"; a framed panel's top edge contains one too
		row := strings.TrimSpace(lines(mm)[1])
		if got := row != "" && strings.Trim(row, "─") == ""; got != rule {
			t.Errorf("height %d: rule row present=%v, want %v: %q", h, got, rule, lines(mm)[1])
		}
	}
}

func TestFooterSurvivesHeightTwoAndOne(t *testing.T) {
	var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
	for _, h := range []int{2, 3} {
		mm := sized(m, 80, h)
		ls := lines(mm)
		if len(ls) != h || !strings.Contains(ls[len(ls)-1], "? help") || !strings.Contains(ls[0], "[Board]") {
			t.Fatalf("h=%d: %q", h, ls)
		}
	}
}

func TestResizeAfterDrillInKeepsInvariants(t *testing.T) {
	var m tea.Model = lambeAt(80, 30, manyRuns(3))
	m = press(m, "enter")
	for _, w := range []int{120, 99, 40, 100} {
		m = sized(m, w, 30)
		for _, l := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(l) > w {
				t.Fatalf("w=%d: %q", w, l)
			}
		}
	}
}
