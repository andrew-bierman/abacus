package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// fakeWS is a scripted workspace provider: it contacts nothing.
type fakeWS struct {
	rep  core.WorkspaceReport
	open func(core.Placement) core.OpenPlan
}

func (f fakeWS) Workspace(context.Context, []core.WorkspaceItem) (core.WorkspaceReport, error) {
	return f.rep, nil
}
func (f fakeWS) Open(p core.Placement) core.OpenPlan { return f.open(p) }

// wsRecorder is the fake runner: it records what the TUI would hand the terminal to.
type wsRecorder struct{ calls [][]string }

func (r *wsRecorder) handoff(argv []string) tea.Cmd {
	r.calls = append(r.calls, argv)
	return nil
}

func leaseIt(id string) core.WorkspaceItem {
	return core.WorkspaceItem{Kind: core.WorkspaceLease, ID: id}
}

func rungPlan(host string) core.OpenPlan {
	argv := core.HandoffArgv("", host)
	return core.OpenPlan{Rungs: []core.OpenRung{{Name: "handoff", Argv: argv, Interactive: true}}, Copy: core.ShellJoin(argv)}
}

func placement(id, host, label string, m core.PlacementMatch) core.Placement {
	it := leaseIt(id)
	return core.Placement{ID: core.PlacementID(it, host, label), Item: it, Host: host, Session: "s-" + id, Label: label, Match: m}
}

// leasesWS is a Leases section over one lease "ac-1", asked of the given provider.
func leasesWS(t *testing.T, prov core.WorkspaceProvider, demoMode bool, rec *wsRecorder, w, h int) (*leasesSection, tea.Model) {
	t.Helper()
	r := leasesReport(core.Lease{Resource: "ac-1", State: core.LeaseHeld, Holder: "alice"}, core.Lease{Resource: "ac-2", State: core.LeaseFree})
	s := leasesWith(&r)
	s.tick = func() tea.Cmd { return nil }
	s.SetTheme(asciiTheme())
	s.setWorkspace(newWsLayer("Leases", prov, demoMode, rec.handoff))
	var m tea.Model = s
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return s, drive(t, m, s.fetch())
}

// loaded runs the workspace read the leases message started.
func loadedLeases(t *testing.T, prov core.WorkspaceProvider, demoMode bool, rec *wsRecorder) (*leasesSection, tea.Model) {
	t.Helper()
	s, m := leasesWS(t, prov, demoMode, rec, 100, 30)
	return s, m
}

func hasKey(bs []key.Binding, k string) bool {
	for _, b := range bs {
		if b.Enabled() {
			for _, kk := range b.Keys() {
				if kk == k {
					return true
				}
			}
		}
	}
	return false
}

func okReport(ps ...core.Placement) core.WorkspaceReport {
	return core.WorkspaceReport{State: core.Connected, Placements: ps}
}

func TestJumpKeyIsAbsentAndInactiveWithoutWorkspaceData(t *testing.T) {
	for name, prov := range map[string]core.WorkspaceProvider{
		"no provider":     nil,
		"nothing placed":  fakeWS{rep: okReport(), open: func(core.Placement) core.OpenPlan { return core.OpenPlan{} }},
		"unplaced others": fakeWS{rep: okReport(placement("zzz", "m", "x", core.MatchExact)), open: func(p core.Placement) core.OpenPlan { return rungPlan("m") }},
	} {
		rec := &wsRecorder{}
		s, m := loadedLeases(t, prov, false, rec)
		before := m.View()
		if hasKey(s.Bindings(), "w") {
			t.Errorf("%s: w offered", name)
		}
		m, cmd := m.Update(runeKey("w"))
		if cmd != nil || len(rec.calls) != 0 || strings.Contains(m.View(), "any key closes") {
			t.Errorf("%s: w did something: %v", name, rec.calls)
		}
		if name != "unplaced others" && m.View() != before {
			t.Errorf("%s: view changed", name)
		}
		if name != "unplaced others" && strings.Contains(m.View(), "not placed") {
			t.Errorf("%s: workspace text with no workspace data", name)
		}
	}
}

func TestJumpRunsExactlyTheFirstRungOnTheKeyOnly(t *testing.T) {
	plan := rungPlan("mac-one")
	plan.Rungs = append(plan.Rungs, core.OpenRung{Name: "second", Argv: []string{"rex", "-S", "other"}, Interactive: true})
	prov := fakeWS{rep: okReport(placement("ac-1", "mac-one", "Lambe · ac-1", core.MatchExact)), open: func(core.Placement) core.OpenPlan { return plan }}
	rec := &wsRecorder{}
	s, m := loadedLeases(t, prov, false, rec)
	_ = m.View()
	if len(rec.calls) != 0 {
		t.Fatalf("ran before a key: %v", rec.calls)
	}
	if !hasKey(s.Bindings(), "w") {
		t.Fatal("w not offered for a placed lease")
	}
	m.Update(runeKey("w"))
	if len(rec.calls) != 1 || strings.Join(rec.calls[0], " ") != "rex -S mac-one" {
		t.Fatalf("calls = %v, want exactly the first rung", rec.calls)
	}
}

func TestJumpShowsOnlyANoticeWhenThePlanIsNotRunnable(t *testing.T) {
	cases := map[string]struct {
		p    core.Placement
		plan core.OpenPlan
		want string
	}{
		"machine not configured":     {placement("ac-1", "far", "x", core.MatchHostNotConfigured), core.OpenPlan{Copy: "rex -S far", Note: "not in the list"}, "copy: rex -S far"},
		"not configured with a rung": {placement("ac-1", "far", "x", core.MatchHostNotConfigured), rungPlan("far"), "copy: rex -S far"},
		"local session":              {placement("ac-1", "", "x", core.MatchExact), core.OpenPlan{Copy: "rex", Note: "Pick the session"}, "copy: rex"},
		"not interactive":            {placement("ac-1", "m", "x", core.MatchExact), core.OpenPlan{Rungs: []core.OpenRung{{Name: "quiet", Argv: []string{"rex", "-S", "m"}}}, Copy: "rex -S m"}, "copy: rex -S m"},
		"wrong machine":              {placement("ac-1", "m", "x", core.MatchExact), core.OpenPlan{Rungs: []core.OpenRung{{Name: "h", Argv: []string{"rex", "-S", "elsewhere"}, Interactive: true}}, Copy: "rex -S elsewhere"}, "copy: rex -S elsewhere"},
		"other program":              {placement("ac-1", "m", "x", core.MatchExact), core.OpenPlan{Rungs: []core.OpenRung{{Name: "h", Argv: []string{"rm", "-S", "m"}, Interactive: true}}, Copy: "rm -S m"}, "copy: rm -S m"},
		"no plan words":              {placement("ac-1", "m", "x", core.MatchExact), core.OpenPlan{}, "Nothing to open"},
	}
	for name, c := range cases {
		c := c
		rec := &wsRecorder{}
		prov := fakeWS{rep: okReport(c.p), open: func(core.Placement) core.OpenPlan { return c.plan }}
		_, m := loadedLeases(t, prov, false, rec)
		m, _ = m.Update(runeKey("w"))
		v := m.View()
		if len(rec.calls) != 0 || !strings.Contains(v, c.want) || !strings.Contains(v, "Workspace") {
			t.Errorf("%s: calls %v, view:\n%s", name, rec.calls, v)
		}
		m, _ = m.Update(runeKey("x")) // any key closes it
		if strings.Contains(m.View(), "any key closes") {
			t.Errorf("%s: notice stayed", name)
		}
	}
}

func TestAmbiguousPlacementShowsCandidatesAndRunsNothing(t *testing.T) {
	it := leaseIt("ac-1")
	rep := core.WorkspaceReport{State: core.Connected, Ambiguous: []core.Ambiguity{{Item: it, Truncated: true, Candidates: []core.Placement{
		placement("ac-1", "mac-a", "first", core.MatchLabel), placement("ac-1", "mac-b", "second", core.MatchLabel)}}}}
	rec := &wsRecorder{}
	s, m := loadedLeases(t, fakeWS{rep: rep, open: func(p core.Placement) core.OpenPlan { return rungPlan(p.Host) }}, false, rec)
	if !hasKey(s.Bindings(), "w") {
		t.Fatal("w should be offered for an ambiguity (it shows the candidates)")
	}
	if v := m.View(); !strings.Contains(v, "ambiguous: 2 claim it") {
		t.Fatalf("detail lacks the ambiguity:\n%s", v)
	}
	m, _ = m.Update(runeKey("w"))
	v := m.View()
	for _, want := range []string{"2 places claim ac-1", "first @ mac-a", "second @ mac-b", "copy: rex -S mac-a", "not all are listed"} {
		if !strings.Contains(v, want) {
			t.Errorf("notice lacks %q:\n%s", want, v)
		}
	}
	if len(rec.calls) != 0 {
		t.Fatalf("an ambiguous placement ran %v", rec.calls)
	}
}

func TestDemoNeverRunsEvenAPlanWithARung(t *testing.T) {
	prov := fakeWS{rep: okReport(placement("ac-1", "mac-one", "x", core.MatchExact)), open: func(core.Placement) core.OpenPlan { return rungPlan("mac-one") }}
	rec := &wsRecorder{}
	_, m := loadedLeases(t, prov, true, rec)
	m, _ = m.Update(runeKey("w"))
	if v := m.View(); len(rec.calls) != 0 || !strings.Contains(v, "Demo data: nothing is opened.") || !strings.Contains(v, "copy: rex -S mac-one") {
		t.Fatalf("calls %v:\n%s", rec.calls, v)
	}
}

// A demo session through the real shell: no key runs anything, on any of the three screens.
func TestDemoSessionJumpKeysOnlyShowTheCommand(t *testing.T) {
	rec := &wsRecorder{}
	m := demoWorkspaceModel(t, asciiTheme(), rec, 100, 32)
	for _, step := range []struct{ tab, pre, key, want string }{
		{"4", "", "o", "copy: rex -S operators-mac"}, // T-1 is placed
		{"3", "", "w", "Demo data: nothing is opened."},
		{"2", "j", "w", "Demo data: nothing is opened."}, // ac-09 is placed
	} {
		m = press(m, step.tab)
		if step.pre != "" {
			m = press(m, step.pre)
		}
		m = press(m, step.key)
		if v := m.View(); !strings.Contains(v, step.want) {
			t.Fatalf("tab %s key %s: want %q:\n%s", step.tab, step.key, step.want, v)
		}
		m = press(m, "esc")
	}
	// An unplaced run (T-2) used to hand the terminal over in a demo; with the layer it shows the command.
	m = press(press(press(m, "4"), "j"), "o")
	if v := m.View(); !strings.Contains(v, "copy: rex -S operators-mac") {
		t.Fatalf("o on an unplaced demo run:\n%s", v)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("the demo ran %v", rec.calls)
	}
}

func TestWorkspaceKeyIsUnboundElsewhere(t *testing.T) {
	rec := &wsRecorder{}
	m := demoWorkspaceModel(t, asciiTheme(), rec, 100, 32)
	for _, tab := range []string{"1", "4", "5"} { // Board, Queues (o, not w), Doctor
		m = press(m, tab)
		before := m.View()
		if after := press(m, "w").View(); after != before {
			t.Errorf("tab %s: w changed the screen", tab)
		}
	}
	m = press(m, "?")
	before := m.View()
	if press(m, "w").View() != before {
		t.Error("w acted in the help overlay")
	}
	m = press(m, "esc")
	m = press(press(press(m, "2"), "n"), "w") // the new-task form takes it as text
	if v := m.View(); !strings.Contains(v, "New task") || strings.Contains(v, "any key closes") {
		t.Errorf("w in the form:\n%s", v)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("ran %v", rec.calls)
	}
}

func TestFooterOffersWOnlyWithWorkspaceData(t *testing.T) {
	m := demoWorkspaceModel(t, asciiTheme(), &wsRecorder{}, 140, 32)
	if last := lines(press(m, "3"))[31]; !strings.Contains(last, "w workspace") { // ac-zoj is placed
		t.Errorf("footer lacks w: %q", last)
	}
	m = press(press(press(press(m, "3"), "j"), "j"), "j") // ac-9x1: not placed
	if last := lines(m)[31]; strings.Contains(last, "w workspace") {
		t.Errorf("footer offers w for an unplaced lease: %q", last)
	}
	plain := sized(load(t, asciiTheme(), nil), 140, 32) // fixture providers, no workspace
	if last := lines(press(plain, "3"))[31]; strings.Contains(last, "workspace") {
		t.Errorf("footer mentions the workspace without one: %q", last)
	}
}

func TestPlacementShowsInDetailAndWhereColumnOnlyWhenUltraWide(t *testing.T) {
	th := asciiTheme()
	wide := demoWorkspaceModel(t, th, &wsRecorder{}, 200, 32)
	var row string
	for _, l := range lines(press(wide, "4")) {
		if strings.Contains(l, "T-1") && strings.Contains(l, "running") {
			row = l
		}
	}
	if !strings.Contains(row, "Lambe · T-1") {
		t.Errorf("no where column at 200 wide: %q", row)
	}
	for _, w := range []int{60, 80, 100, 120} {
		m := demoWorkspaceModel(t, th, &wsRecorder{}, w, 32)
		for _, l := range lines(press(m, "4")) {
			if strings.Contains(l, "T-1") && strings.Contains(l, "running") && strings.Contains(l, "Lambe") {
				t.Errorf("%d wide: where column below ultra: %q", w, l)
			}
		}
	}
	d := strings.Join(lines(press(demoWorkspaceModel(t, th, &wsRecorder{}, 120, 40), "4")), "\n")
	for _, want := range []string{"where", "Lambe · T-1", "host", "match", "exact"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail lacks %q:\n%s", want, d)
		}
	}
	// T-2 is not placed, T-3 is found by a word in a label.
	m := press(press(press(demoWorkspaceModel(t, th, &wsRecorder{}, 120, 40), "4"), "j"), "j")
	if v := m.View(); !strings.Contains(v, "by label") {
		t.Errorf("T-3:\n%s", v)
	}
	m = press(demoWorkspaceModel(t, th, &wsRecorder{}, 120, 40), "4")
	if v := press(m, "j").View(); !strings.Contains(v, "not placed") {
		t.Errorf("T-2:\n%s", v)
	}
}

func TestHostilePlacementTextNeverReachesTheScreenRaw(t *testing.T) {
	evil := "\x1b[31mred\x1b[0m\x1b]0;title\x07\u202e\x00evil" + strings.Repeat("é", 400)
	p := placement("ac-1", "mac-one", evil, core.MatchExact)
	p.Session, p.Tab = evil, evil
	unknown := placement("ac-2", "mac-one", "ok", core.PlacementMatch(evil))
	amb := core.Ambiguity{Item: leaseIt("ac-2"), Candidates: []core.Placement{p, p}}
	for _, rep := range []core.WorkspaceReport{
		{State: core.Connected, Placements: []core.Placement{p, unknown}, Reason: evil},
		{State: core.Degraded, Reason: evil, Ambiguous: []core.Ambiguity{amb}, Placements: []core.Placement{p}},
	} {
		prov := fakeWS{rep: rep, open: func(core.Placement) core.OpenPlan {
			return core.OpenPlan{Note: evil, Copy: evil, Rungs: []core.OpenRung{{Argv: []string{"rex", "-S", evil}, Interactive: true}}}
		}}
		for _, w := range []int{40, 100, 200} {
			rec := &wsRecorder{}
			_, m := leasesWS(t, prov, false, rec, w, 30)
			for _, k := range []string{"", "w", "x", "j", "w"} {
				if k != "" {
					m, _ = m.Update(runeKey(k))
				}
				v := m.View()
				for _, bad := range []string{"\x1b", "\x00", "\u202e", "\x07"} {
					if strings.Contains(v, bad) {
						t.Fatalf("%d wide key %q: raw %q on screen:\n%s", w, k, bad, v)
					}
				}
				for _, l := range strings.Split(v, "\n") {
					if lipgloss.Width(l) > w {
						t.Fatalf("line wider than %d: %q", w, l)
					}
				}
			}
			if len(rec.calls) != 0 {
				t.Fatalf("hostile plan ran: %v", rec.calls)
			}
		}
	}
}

func TestWorkspaceItemsRefuseAnUnsafeMachineName(t *testing.T) {
	if _, ok := itemOf(core.WorkspaceRun, "T-1", "mac\x1b[2J"); ok {
		t.Error("an unsafe machine name was kept")
	}
	if it, ok := itemOf(core.WorkspaceRun, "T-1\x1b[2J", "mac"); !ok || strings.ContainsRune(it.ID, 0x1b) {
		t.Errorf("id not cleaned: %+v", it)
	}
	if _, ok := itemOf(core.WorkspaceRun, "", "mac"); ok {
		t.Error("an empty id was kept")
	}
}

func TestQueuesOKeepsItsBehaviorWithoutWorkspaceDataAndUsesThePlanWithIt(t *testing.T) {
	// No layer data: the legacy handoff for the run's machine.
	rec := &wsRecorder{}
	var s tea.Model = newLambeSection(lambe.NewDemo(), rec.handoff)
	s.(*lambeSection).setWorkspace(newWsLayer("Queues", nil, false, rec.handoff))
	s, _ = s.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{{Issue: "A-1", Machine: "mac-one"}}}}})
	s.Update(runeKey("o"))
	if len(rec.calls) != 1 || strings.Join(rec.calls[0], " ") != "rex -S mac-one" {
		t.Fatalf("legacy o: %v", rec.calls)
	}
	// Placed with a rung: the plan's first rung runs once.
	rec = &wsRecorder{}
	it := core.WorkspaceItem{Kind: core.WorkspaceRun, ID: "A-1", Host: "mac-one"}
	pl := core.Placement{ID: "p", Item: it, Host: "mac-one", Session: "s", Label: "Lambe · A-1", Match: core.MatchExact}
	prov := fakeWS{rep: okReport(pl), open: func(core.Placement) core.OpenPlan { return rungPlan("mac-one") }}
	ls := newLambeSection(lambe.NewDemo(), rec.handoff)
	ls.setWorkspace(newWsLayer("Queues", prov, false, rec.handoff))
	s = ls
	s, cmd := s.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{{Issue: "A-1", Machine: "mac-one"}}}}})
	s = drive(t, s, cmd)
	s.Update(runeKey("o"))
	if len(rec.calls) != 1 || strings.Join(rec.calls[0], " ") != "rex -S mac-one" {
		t.Fatalf("placed o: %v", rec.calls)
	}
}

func TestHandoffErrorOfAJumpIsShownOnlyToTheSectionThatStartedIt(t *testing.T) {
	prov := fakeWS{rep: okReport(placement("ac-1", "mac-one", "x", core.MatchExact)), open: func(core.Placement) core.OpenPlan { return rungPlan("mac-one") }}
	rec := &wsRecorder{}
	_, m := loadedLeases(t, prov, false, rec)
	m, _ = m.Update(handoffDoneMsg{err: context.Canceled}) // not ours: no jump was started
	if strings.Contains(m.View(), "did not open") {
		t.Fatal("a foreign handoff error was shown")
	}
	m, _ = m.Update(runeKey("w"))
	m, _ = m.Update(handoffDoneMsg{err: context.Canceled})
	if !strings.Contains(m.View(), "did not open") {
		t.Fatalf("own handoff error missing:\n%s", m.View())
	}
}

// demoWorkspaceModel is the shell over the demo providers with the demo workspace loaded, at w x h.
func demoWorkspaceModel(t *testing.T, th Theme, rec *wsRecorder, w, h int) tea.Model {
	t.Helper()
	var m tea.Model = NewWithHandoff(demoEntries(), rec.handoff, WithTheme(th), WithFullView(noFullView), WithAudit(&memAudit{}))
	m = sized(m, w, h)
	l := lambe.NewDemo()
	v, err := l.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m, cmd := m.Update(lambeViewMsg{view: v})
	m = drive(t, m, cmd)
	m, cmd = m.Update(leasesMsg{read: readLeases(context.Background(), demo.NewLeases())})
	m = drive(t, m, cmd)
	m, _ = m.Update(insightsMsg{report: demo.InsightsReport()})
	m, cmd = m.Update(tasksMsg{report: demo.TasksReport()})
	return drive(t, m, cmd)
}

// drive runs a command and feeds each answer back, following the commands those return, until
// nothing is left (a Batch is flattened). The commands here read only fixtures and fakes.
func drive(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = drive(t, m, c)
		}
	case nil:
	default:
		var next tea.Cmd
		m, next = m.Update(msg)
		m = drive(t, m, next)
	}
	return m
}

// The Tasks screen has the same key: one rung on the key, nothing without a placement.
func TestTasksJumpKeyRunsTheFirstRungOnce(t *testing.T) {
	p := tasksFake()
	rec := &wsRecorder{}
	var m tea.Model = NewWithHandoff([]core.Entry{{Module: p, Detection: p.Det}}, rec.handoff, WithTheme(asciiTheme()), WithAudit(&memAudit{}), WithFullView(noFullView))
	it := core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "ac-09"}
	pl := core.Placement{ID: "p", Item: it, Host: "mac-one", Session: "s", Label: "Lambe · ac-09", Match: core.MatchExact}
	prov := fakeWS{rep: okReport(pl), open: func(core.Placement) core.OpenPlan { return rungPlan("mac-one") }}
	ts := m.(*Model).tabs[1].sec.(*tasksSection)
	ts.setWorkspace(newWsLayer("Tasks", prov, false, rec.handoff))
	m = sized(m, 100, 32)
	m, cmd := m.Update(tasksMsg{report: fixtureTasks()})
	m = drive(t, m, cmd)
	m = press(m, "2")
	m = press(m, "w") // ac-12 is not placed: nothing happens
	if len(rec.calls) != 0 || hasKey(ts.Bindings(), "w") {
		t.Fatalf("w acted on an unplaced task: %v", rec.calls)
	}
	m = press(m, "j")
	if !hasKey(ts.Bindings(), "w") {
		t.Fatal("w not offered on the placed task")
	}
	press(m, "w")
	if len(rec.calls) != 1 || strings.Join(rec.calls[0], " ") != "rex -S mac-one" {
		t.Fatalf("calls = %v", rec.calls)
	}
}

// A run the workspace is unsure about is never handed over by the legacy `o`: the notice decides.
func TestQueuesOOnAnAmbiguousRunShowsCandidatesNotTheLegacyHandoff(t *testing.T) {
	it := core.WorkspaceItem{Kind: core.WorkspaceRun, ID: "A-1", Host: "mac-one"}
	mk := func(label string) core.Placement {
		return core.Placement{ID: "p" + label, Item: it, Host: "mac-one", Session: label, Label: label, Match: core.MatchLabel}
	}
	rep := core.WorkspaceReport{State: core.Connected, Ambiguous: []core.Ambiguity{{Item: it, Candidates: []core.Placement{mk("one"), mk("two")}}}}
	rec := &wsRecorder{}
	ls := newLambeSection(lambe.NewDemo(), rec.handoff)
	ls.setWorkspace(newWsLayer("Queues", fakeWS{rep: rep, open: func(core.Placement) core.OpenPlan { return rungPlan("mac-one") }}, false, rec.handoff))
	var s tea.Model = ls
	s, cmd := s.Update(lambeViewMsg{view: lambe.View{Snapshot: lambe.Snapshot{Runs: []lambe.Run{{Issue: "A-1", Machine: "mac-one"}}}}})
	s = drive(t, s, cmd)
	s, _ = s.Update(runeKey("o"))
	if v := s.View(); len(rec.calls) != 0 || !strings.Contains(v, "2 places claim A-1") {
		t.Fatalf("calls %v:\n%s", rec.calls, v)
	}
}
