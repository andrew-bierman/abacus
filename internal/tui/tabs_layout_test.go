package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

func leasesAt(w, h int, ls ...core.Lease) tea.Model {
	r := leasesReport(ls...)
	s := leasesWith(&r)
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(leasesMsg{read: r})
	return sized(m, w, h)
}

var sampleLeases = []core.Lease{
	{Resource: "ac-zoj", State: core.LeaseHeld, Holder: "glm-scheduler-01", ExpiresAt: "12m"},
	{Resource: "ac-00h", State: core.LeaseHeld, Holder: "codex-macbookpro", ExpiresAt: "3m"},
	{Resource: "ac-1k2", State: core.LeaseStale, Holder: "claude-mac"},
	{Resource: "ac-9x1", State: core.LeaseFree},
}

// ---- Leases ----

func TestLeasesDetailPanelAppearsExactlyAtOneHundredColumns(t *testing.T) {
	if v := leasesAt(99, 30, sampleLeases...).View(); strings.Contains(v, "Lease ─") || strings.Contains(v, "holder ") {
		t.Fatalf("99 columns shows the list only:\n%s", v)
	}
	v := leasesAt(100, 30, sampleLeases...).View()
	if !strings.Contains(v, "─ Lease ─") {
		t.Fatalf("100 columns shows the selected lease beside the list:\n%s", v)
	}
	rowWith(t, v, "holder", "glm-scheduler-01")
}

func TestLeasesColumnsDropByPriorityAndNeverClipTheIdOrState(t *testing.T) {
	long := core.Lease{Resource: "ac-" + strings.Repeat("z", 21), State: core.LeaseStale, Holder: "someone-with-a-long-name", ExpiresAt: "12m"}
	for _, tc := range []struct {
		w            int
		holder, expr bool
	}{{140, true, true}, {80, true, true}, {54, true, true}, {53, true, false}, {50, true, false}, {40, false, false}} {
		v := leasesAt(tc.w, 30, long).View()
		if !strings.Contains(v, "(stale)") || !strings.Contains(v, long.Resource) {
			t.Errorf("w=%d: state and id must stay whole:\n%s", tc.w, v)
		}
		if got := strings.Contains(v, "someone"); got != tc.holder {
			t.Errorf("w=%d: holder visible=%v, want %v:\n%s", tc.w, got, tc.holder, v)
		}
		if got := strings.Contains(v, "12m"); got != tc.expr {
			t.Errorf("w=%d: expiry visible=%v, want %v:\n%s", tc.w, got, tc.expr, v)
		}
	}
}

func TestLeasesEmptyStatesAreWordsInsideAPanel(t *testing.T) {
	v := leasesAt(120, 30).View()
	for _, want := range []string{"no leases held", "select a row", "╭", "0 held, 0 stale, 0 free"} {
		if !strings.Contains(v, want) {
			t.Errorf("empty leases missing %q:\n%s", want, v)
		}
	}
}

func TestLeasesFocusMarkerIsOnTheListUntilYouDrillIn(t *testing.T) {
	wide := leasesAt(120, 30, sampleLeases...).View()
	if !strings.Contains(wide, "╭─ ▸ Leases") || strings.Contains(wide, "▸ Lease ─") {
		t.Fatalf("the list is the focused panel and the detail is not:\n%s", wide)
	}
	m := leasesAt(80, 30, sampleLeases...)
	m = press(m, "enter")
	if v := m.View(); !strings.Contains(v, "╭─ ▸ Lease ") || strings.Contains(v, "Leases ·") {
		t.Fatalf("drilled in, the detail alone is shown and focused:\n%s", v)
	}
}

func TestLeasesCursorDrillAndBack(t *testing.T) {
	m := leasesAt(80, 30, sampleLeases...)
	if v := m.View(); strings.Contains(v, "─ Lease ─") {
		t.Fatalf("stacked shows the list first:\n%s", v)
	}
	m = press(press(m, "j"), "j")
	m = press(m, "enter")
	if v := m.View(); !strings.Contains(v, "ac-1k2") || !strings.Contains(v, "claude-mac") || strings.Contains(v, "Leases ·") {
		t.Fatalf("enter drills into the lease under the cursor:\n%s", v)
	}
	if v := press(m, "esc").View(); !strings.Contains(v, "Leases ·") {
		t.Fatalf("esc returns to the list:\n%s", v)
	}
	up := press(press(press(m, "esc"), "k"), "k")
	rowWith(t, up.View(), "▸", "(held)", "ac-zoj") // the cursor moves up and clamps at the first row
	// Enter on a wide screen never leaves the section drilled in: shrinking afterwards shows the
	// list, not a detail the user never asked for.
	w := press(leasesAt(120, 30, sampleLeases...), "enter")
	if v := sized(w, 80, 30).View(); !strings.Contains(v, "Leases ·") || strings.Contains(v, "─ Lease ") {
		t.Fatalf("enter does nothing when the detail is already beside the list:\n%s", v)
	}
}

func TestLeasesCursorStaysOnARowAfterARefreshShrinksTheList(t *testing.T) {
	m := leasesAt(120, 30, sampleLeases...)
	for i := 0; i < 3; i++ {
		m = press(m, "j")
	}
	m, _ = m.Update(leasesMsg{read: leasesReport(sampleLeases[0])})
	if v := m.View(); strings.Contains(v, "select a row") {
		t.Fatalf("the cursor must clamp to the last remaining row:\n%s", v)
	}
	rowWith(t, m.View(), "▸", "(held)", "ac-zoj")
}

func TestLongLeaseListScrollsWithTheCursorAndKeepsItsMoreMarker(t *testing.T) {
	var ls []core.Lease
	for i := 0; i < 40; i++ {
		ls = append(ls, core.Lease{Resource: fmt.Sprintf("L%02d", i), State: core.LeaseFree})
	}
	m := leasesAt(80, 14, ls...)
	if v := m.View(); strings.Contains(v, "L39") || !strings.Contains(v, "more") {
		t.Fatalf("a long list is clipped with a marker:\n%s", v)
	}
	for i := 0; i < 39; i++ {
		m = press(m, "j")
	}
	rowWith(t, m.View(), "▸", "(free)", "L39") // the window follows the cursor to the end
	for _, l := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(l) > 80 {
			t.Fatalf("line too wide: %q", l)
		}
	}
}

// ---- Queues ----

func TestQueuesStalledFlagSurvivesNarrowWidthsBeforePhaseAndMachine(t *testing.T) {
	for _, w := range []int{40, 60, 100, 140} {
		v := demoSection(t, w, 30).View()
		if !strings.Contains(v, "(stalled)") {
			t.Errorf("w=%d: the stalled marker must stay visible:\n%s", w, v)
		}
		if w == 40 && strings.Contains(v, "operators-mac  ") {
			t.Errorf("w=%d: the machine column is the first to go:\n%s", w, v)
		}
	}
	if v := demoSection(t, 140, 30).View(); !strings.Contains(v, "gating") || !strings.Contains(v, "operators-mac") {
		t.Fatalf("a wide list shows phase and machine columns:\n%s", v)
	}
}

func TestQueuesRowsAlignIntoColumns(t *testing.T) {
	v := demoSection(t, 120, 30).View()
	col := -1
	for _, l := range strings.Split(v, "\n") {
		for _, id := range []string{"T-1 ", "T-2 ", "T-3 "} {
			if i := strings.Index(l, id); i >= 0 && strings.Contains(l, "│") {
				at := lipgloss.Width(l[:i])
				if col >= 0 && at != col {
					t.Fatalf("run rows must start in the same column (%d vs %d):\n%s", at, col, v)
				}
				col = at
			}
		}
	}
	if col < 0 {
		t.Fatalf("no run rows found:\n%s", v)
	}
}

func TestQueuesPanelsAreCompactInATallTerminal(t *testing.T) {
	v := strings.Split(strings.TrimRight(demoSection(t, 120, 50).View(), "\n"), "\n")
	if len(v) > duoMinRows+panelChromeH+1 {
		t.Fatalf("a short run list in a 50-row body is a compact box, got %d rows", len(v))
	}
}

// ---- Doctor ----

func doctorAt(w, h int, mods ...core.ModuleReport) tea.Model {
	d := newDoctorSection(nil)
	d.SetTheme(asciiTheme())
	var m tea.Model = d
	ov := &core.Overview{
		Ready: false,
		Domains: []core.DomainLine{
			{Domain: core.DomainTasks, Title: "Tasks", State: core.Connected, Line: "14 tasks"},
			{Domain: core.DomainLeases, Title: "Leases", State: core.NotConnected, Line: "no hub on this machine"},
		},
		Tools: []core.ToolLine{{ID: "bd", Name: "Beads", State: core.Ready, Where: "bundled", Version: "1.3.4"}},
	}
	m, _ = m.Update(doctorMsg{report: core.Report{Overview: ov, Modules: mods}})
	return sized(m, w, h)
}

func TestDoctorAreasAndToolsSitSideBySideFromOneHundredColumns(t *testing.T) {
	both := func(v string) bool {
		for _, l := range strings.Split(v, "\n") {
			if strings.Contains(l, "─ Areas ") && strings.Contains(l, "─ Tools ") {
				return true
			}
		}
		return false
	}
	if both(doctorAt(99, 60).View()) {
		t.Fatal("99 columns stacks Areas over Tools")
	}
	if v := doctorAt(100, 60).View(); !both(v) {
		t.Fatalf("100 columns puts Areas beside Tools:\n%s", v)
	}
}

func TestDoctorWrapsLongDetailsInsteadOfCuttingThem(t *testing.T) {
	detail := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda"
	m := doctorAt(40, 80, core.ModuleReport{Name: "Tool", Detection: core.Detection{State: core.Ready},
		Checks: []core.Check{{Name: "path", Status: core.StatusWarn, Detail: detail, Fix: "reinstall the tool from the release archive"}}})
	v := m.View()
	for _, word := range strings.Fields(detail + " reinstall archive") {
		if !strings.Contains(v, word) {
			t.Errorf("word %q was cut off at 40 columns:\n%s", word, v)
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if lipgloss.Width(l) > 40 {
			t.Fatalf("line too wide: %q", l)
		}
	}
}

func TestDoctorModulePanelsAreTitledWithTheToolName(t *testing.T) {
	v := doctorAt(80, 80, core.ModuleReport{Name: "Lambe", Detection: core.Detection{State: core.Ready, Version: "0.10.1"}}).View()
	if !strings.Contains(v, "╭─ Lambe ") || !strings.Contains(v, "(ready) 0.10.1") {
		t.Fatalf("each tool gets its own titled panel:\n%s", v)
	}
}

// The stalled flag has a column only when a visible run is stalled, so a calm run list has no
// hole where the flag would be.
func TestQueuesStalledColumnExistsOnlyWhenARunIsStalled(t *testing.T) {
	calm := lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{{Issue: "A-1", State: "running", Phase: "gating", Machine: "mac-1"}}}}
	if v := lambeAt(120, 30, calm).View(); !strings.Contains(v, "running  gating") {
		t.Fatalf("no stalled run, no flag column:\n%s", v)
	}
	calm.Snapshot.Runs[0].Stalled = true
	if v := lambeAt(120, 30, calm).View(); !strings.Contains(v, "running  (stalled)  gating") {
		t.Fatalf("a stalled run gets the flag between state and phase:\n%s", v)
	}
}

func TestQueuesWorkflowAndLastEventAreWideOnlyColumns(t *testing.T) {
	row := func(w int) string {
		for _, l := range strings.Split(demoSection(t, w, 30).View(), "\n") {
			if strings.Contains(l, "▸ T-1") {
				return l
			}
		}
		t.Fatalf("no selected row at %d", w)
		return ""
	}
	if l := row(100); strings.Contains(l, "general") {
		t.Errorf("100 columns has no room for the workflow column: %q", l)
	}
	if l := row(160); !strings.Contains(l, "general") || !strings.Contains(l, "30s") {
		t.Errorf("160 columns shows workflow and last event: %q", l)
	}
}

// The selection follows the lease, not the row number: a refresh that reorders or removes rows
// above the cursor must not move it onto a different lease.
func TestLeasesSelectionFollowsTheLeaseAcrossARefresh(t *testing.T) {
	m := leasesAt(120, 30, sampleLeases...)
	m = press(m, "j") // ac-00h
	reordered := []core.Lease{sampleLeases[3], sampleLeases[2], sampleLeases[1], sampleLeases[0]}
	m, _ = m.Update(leasesMsg{read: leasesReport(reordered...)})
	rowWith(t, m.View(), "▸", "ac-00h")
	rowWith(t, m.View(), "holder", "codex-macbookpro")
	m, _ = m.Update(leasesMsg{read: leasesReport(sampleLeases[0], sampleLeases[3])}) // ac-00h is gone
	if v := m.View(); strings.Contains(v, "select a row") {
		t.Fatalf("a vanished lease falls back to a row, not to nothing:\n%s", v)
	}
	// A refresh that drops the selection does not move it: when the lease comes back it is
	// selected again.
	m, _ = m.Update(leasesMsg{read: leasesReport(sampleLeases...)})
	rowWith(t, m.View(), "▸", "ac-00h")
}

func TestResizingWideNeverLeavesADrilledDetailBehind(t *testing.T) {
	l := press(leasesAt(80, 30, sampleLeases...), "enter")
	l = sized(sized(l, 120, 30), 80, 30)
	if v := l.View(); !strings.Contains(v, "Leases ·") || strings.Contains(v, "─ ▸ Lease ") {
		t.Fatalf("leases: widening ends the drill:\n%s", v)
	}
	q := press(demoSection(t, 80, 30), "enter")
	q = sized(sized(q, 120, 30), 80, 30)
	if v := q.View(); !strings.Contains(v, "Queue") || strings.Contains(v, "─ ▸ Run ") {
		t.Fatalf("queues: widening ends the drill:\n%s", v)
	}
}

func TestStaleLeaseDetailSaysLastHolderAndExpiryMatchesTheTable(t *testing.T) {
	v := leasesAt(120, 30, core.Lease{Resource: "ac-1", State: core.LeaseStale, Holder: "bob"}).View()
	rowWith(t, v, "last holder", "bob")
	held := leasesAt(120, 30, core.Lease{Resource: "ac-2", State: core.LeaseHeld, Holder: "amy"}).View()
	rowWith(t, held, "expires", "?")
	if strings.Contains(held, "expires  -") {
		t.Fatalf("a held lease with no expiry reads ? in the table and the detail alike:\n%s", held)
	}
}

// ---- coverage the first pass missed: hostile text, the pinned attention cap, wrapping, tones ----

func hostileOK(t *testing.T, name, v string, w int) {
	t.Helper()
	for _, r := range v {
		if r < 0x20 && r != '\n' || r == 0x7f || r >= 0x200b && r <= 0x200f || r >= 0x202a && r <= 0x202e || r == 0xfeff {
			t.Errorf("%s: forbidden rune %U reached the screen", name, r)
			return
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if lipgloss.Width(l) > w {
			t.Errorf("%s: %q is %d wide, over %d", name, l, lipgloss.Width(l), w)
			return
		}
	}
}

func TestHostileTextNeverReachesTheLeasesDetailOrDoctorOverviewPanels(t *testing.T) {
	bad := "x\x1b[2J\u202etxt\u200b\u200d\ufeff\x07y\x00日本語"
	ls := core.Lease{Resource: bad, State: core.LeaseHeld, Holder: bad, ExpiresAt: bad}
	for _, w := range []int{40, 120} {
		m := leasesAt(w, 30, ls)
		hostileOK(t, fmt.Sprintf("leases %d", w), m.View(), w)
		hostileOK(t, fmt.Sprintf("leases drilled %d", w), press(m, "enter").View(), w)

		ov := &core.Overview{
			Domains: []core.DomainLine{{Domain: core.DomainTasks, Title: bad, State: core.Connected, Line: bad}},
			Tools:   []core.ToolLine{{ID: bad, Name: bad, State: core.Ready, Where: bad, Version: bad, Note: bad}},
		}
		d := newDoctorSection(nil)
		d.SetTheme(asciiTheme())
		var dm tea.Model = d
		dm, _ = dm.Update(doctorMsg{report: core.Report{Overview: ov, Modules: []core.ModuleReport{{Name: bad,
			Detection: core.Detection{State: core.Ready, Version: bad}, Checks: []core.Check{{Name: bad, Status: core.StatusWarn, Detail: bad, Fix: bad}}}}}})
		hostileOK(t, fmt.Sprintf("doctor %d", w), sized(dm, w, 60).View(), w)
	}
}

// With more attention entries than a third of the panel, the block is cut to that third and says
// how many entries it hides; the run list keeps its own marker.
func TestQueuesPinnedAttentionIsCappedAtAThirdAndSaysWhatItHides(t *testing.T) {
	v := manyRuns(25)
	for i := 0; i < 4; i++ {
		v.Snapshot.NeedsHuman = append(v.Snapshot.NeedsHuman, lambe.NeedsHuman{Issue: fmt.Sprintf("R-%02d", 10+i), Reason: "gate_failed", Detail: "d"})
	}
	for _, h := range []int{12, 14} {
		out := lambeAt(80, h, v).View()
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		inner := h - panelChromeH
		start := -1
		for i, l := range lines {
			if strings.Contains(l, "Needs attention 5") {
				start = i
			}
		}
		if start < 0 {
			t.Fatalf("h=%d: the attention header must stay visible:\n%s", h, out)
		}
		block := len(lines) - 1 - start // rows from the header to just above the closing border
		if want := max(1, inner/3); block != want {
			t.Errorf("h=%d: attention block is %d rows, want %d:\n%s", h, block, want, out)
		}
		if !strings.Contains(out, "more") || !strings.Contains(out, "+") {
			t.Errorf("h=%d: a cut block says how many it hides:\n%s", h, out)
		}
		if !strings.Contains(strings.Join(lines[:start], "\n"), "↕") {
			t.Errorf("h=%d: the clipped run list keeps its own marker:\n%s", h, out)
		}
	}
}

func TestDoctorToolNoteWrapsUnderItsLine(t *testing.T) {
	note := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu"
	ov := &core.Overview{Tools: []core.ToolLine{{ID: "bd", Name: "Beads", State: core.Ready, Where: "bundled", Version: "1", Note: note}}}
	d := newDoctorSection(nil)
	d.SetTheme(asciiTheme())
	var m tea.Model = d
	m, _ = m.Update(doctorMsg{report: core.Report{Overview: ov}})
	v := sized(m, 40, 60).View()
	for _, word := range strings.Fields(note) {
		if !strings.Contains(v, word) {
			t.Errorf("note word %q was cut at 40 columns:\n%s", word, v)
		}
	}
}

func TestLeasesDetailBadgeUsesTheLeaseTone(t *testing.T) {
	th := NewTheme(rendererFor(termenv.TrueColor, true))
	r := leasesReport(sampleLeases[0])
	s := leasesWith(&r)
	s.SetTheme(th)
	var m tea.Model = s
	m, _ = m.Update(leasesMsg{read: r})
	v := sized(m, 120, 30).View()
	want := th.Badge(KindAccent, "held") // held leases are the info tone
	if strings.Count(v, want) != 2 {
		t.Fatalf("the list row and the detail header both carry the held badge (%d found):\n%s", strings.Count(v, want), v)
	}
}

func TestLeasesFooterKeysFollowTheLayout(t *testing.T) {
	enabled := func(s *leasesSection, h string) bool {
		for _, b := range s.Bindings() {
			if b.Help().Key == h || b.Help().Desc == h {
				return b.Enabled()
			}
		}
		return false
	}
	r := leasesReport(sampleLeases...)
	s := leasesWith(&r)
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(leasesMsg{read: r})
	m = sized(m, 80, 30)
	if !enabled(s, "details") || enabled(s, "back") {
		t.Fatal("stacked and not drilled: enter is offered, esc is not")
	}
	m = press(m, "enter")
	if enabled(s, "details") || !enabled(s, "back") {
		t.Fatal("drilled: esc is offered, enter is not")
	}
	m = sized(m, 120, 30)
	if enabled(s, "details") || enabled(s, "back") {
		t.Fatal("wide: the detail is already visible, so neither key is offered")
	}
}

func TestLeasesDrilledAndLongListNeverOverflowAnySize(t *testing.T) {
	var ls []core.Lease
	for i := 0; i < 40; i++ {
		ls = append(ls, core.Lease{Resource: fmt.Sprintf("L%02d-%s", i, strings.Repeat("x", i%30)), State: core.LeaseHeld, Holder: strings.Repeat("h", i%40), ExpiresAt: "1m"})
	}
	for _, w := range gridWidths {
		for _, h := range gridHeights {
			m := leasesAt(w, h, ls...)
			for _, v := range []string{m.View(), press(press(m, "enter"), "j").View()} {
				if h > 0 && len(strings.Split(strings.TrimRight(v, "\n"), "\n")) > h {
					t.Fatalf("%dx%d: taller than the body", w, h)
				}
				for _, l := range strings.Split(v, "\n") {
					if lipgloss.Width(l) > w {
						t.Fatalf("%dx%d: %q is %d wide", w, h, l, lipgloss.Width(l))
					}
				}
			}
		}
	}
}

// In a demo nothing is missing: every tool Doctor lists says it is demo data.
func TestDoctorShowsDemoToolsAsDemoNotMissing(t *testing.T) {
	d := newDoctorSection(demoEntries())
	d.SetTheme(asciiTheme())
	var m tea.Model = d
	m, _ = m.Update(doctorMsg{report: core.DoctorReport(context.Background(), "", demoEntries()), seq: 0})
	v := sized(m, 120, 60).View()
	if strings.Contains(v, "missing") {
		t.Fatalf("a demo has no missing tools:\n%s", v)
	}
	if strings.Count(v, "(demo)") < 4 {
		t.Fatalf("every demo tool is labelled demo:\n%s", v)
	}
	if strings.Contains(v, "(demo) demo") {
		t.Fatalf("the demo badge is not repeated as a version:\n%s", v)
	}
}
