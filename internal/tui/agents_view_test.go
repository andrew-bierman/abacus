package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/demo"
)

// agentsSizes are the extra golden sizes of the Agents screens: a compact terminal and the width
// where list and detail sit side by side.
var agentsSizes = [][2]int{{60, 24}, {100, 32}, {140, 32}}

// fixedAgents is the demo provider on the golden clock, so waits never change a golden.
func fixedAgents() demo.Agents { return demo.NewAgents(func() time.Time { return goldenNow }) }

// agentsTab is the model's Agents section (the tab after Doctor).
func agentsTab(m tea.Model) *agentsSection {
	tabs := m.(*Model).tabs
	return tabs[len(tabs)-1].sec.(*agentsSection)
}

// agentsModel is a demo session on the Agents tab with the given report delivered (nil: none yet).
func agentsModel(t *testing.T, th Theme, rec *wsRecorder, rep *core.AgentReport) tea.Model {
	t.Helper()
	m := demoWorkspaceModel(t, th, rec, 100, 32)
	if rep != nil {
		m, _ = m.Update(agentsMsg{rep: *rep})
	}
	return m
}

// fleet is what the demo provider reports, read through core and matched to the items the other
// tabs show.
func fleet(t *testing.T) core.AgentReport {
	t.Helper()
	m := demoWorkspaceModel(t, asciiTheme(), &wsRecorder{}, 100, 32)
	return core.ReadAgentsFrom(context.Background(), fixedAgents(), agentsTab(m).items())
}

func agentScenarios() []scenario {
	with := func(f func(t *testing.T, rep core.AgentReport) core.AgentReport) func(t *testing.T, th Theme) tea.Model {
		return func(t *testing.T, th Theme) tea.Model {
			rep := f(t, fleet(t))
			return agentsModel(t, th, &wsRecorder{}, &rep)
		}
	}
	std := with(func(_ *testing.T, r core.AgentReport) core.AgentReport { return r })
	return []scenario{
		{name: "agents", build: std, keys: []string{"6"}, tab: "Agents", extra: agentsSizes},
		{name: "agents-detail", build: std, keys: []string{"6", "enter"}, tab: "Agents", extra: agentsSizes},
		{name: "agents-second", build: std, keys: []string{"6", "j", "j", "j"}, tab: "Agents", extra: agentsSizes},
		{name: "agents-ambiguous", build: std, keys: []string{"6", "j", "j", "j", "j", "j", "j"}, tab: "Agents", extra: agentsSizes},
		{name: "agents-notice", build: std, keys: []string{"6", "w"}, tab: "Agents", extra: agentsSizes},
		{name: "agents-loading", tab: "Agents", extra: agentsSizes, keys: []string{"6"}, build: func(t *testing.T, th Theme) tea.Model {
			return agentsModel(t, th, &wsRecorder{}, nil)
		}},
		{name: "agents-empty", tab: "Agents", extra: agentsSizes, keys: []string{"6"}, build: with(func(_ *testing.T, _ core.AgentReport) core.AgentReport {
			return core.AgentReport{State: core.Connected, Terminals: 14}
		})},
		{name: "agents-no-panes", tab: "Agents", extra: agentsSizes, keys: []string{"6"}, build: with(func(_ *testing.T, _ core.AgentReport) core.AgentReport {
			return core.AgentReport{State: core.Connected}
		})},
		{name: "agents-not-connected", tab: "Agents", extra: agentsSizes, keys: []string{"6"}, build: with(func(_ *testing.T, _ core.AgentReport) core.AgentReport {
			return core.AgentReport{State: core.NotConnected, Reason: "No terminal workspace found."}
		})},
		{name: "agents-degraded", tab: "Agents", extra: agentsSizes, keys: []string{"6"}, build: with(func(_ *testing.T, r core.AgentReport) core.AgentReport {
			r.State, r.Reason, r.Stale, r.Truncated = core.Degraded, "mbp: reading sessions timed out; mbp stopped answering, so some panes were not read", true, true
			return r
		})},
		{name: "agents-degraded-empty", tab: "Agents", extra: agentsSizes, keys: []string{"6"}, build: with(func(_ *testing.T, _ core.AgentReport) core.AgentReport {
			return core.AgentReport{State: core.Degraded, Reason: "this machine: 2 panes printed output this version does not recognize", Terminals: 2}
		})},
		{name: "agents-hostile", tab: "Agents", extra: agentsSizes, keys: []string{"6"}, build: with(func(t *testing.T, _ core.AgentReport) core.AgentReport {
			return core.ReadAgentsFrom(context.Background(), hostileAgents{}, nil)
		})},
	}
}

// hostileAgents is a provider whose every text is an attack.
type hostileAgents struct{}

const hostileText = "\x1b]0;pwned\x07\x1b[2J\x1b[31mred‮\r\nsecond line\ttab " + "wide-wide-wide-wide-wide-wide-wide-wide-wide-wide-wide-wide-wide-wide-wide-wide"

func (hostileAgents) Agents(context.Context, []core.WorkspaceItem) (core.AgentReport, error) {
	three := 300
	return core.AgentReport{State: core.Connected, Panes: []core.AgentPane{{
		Session: hostileText, SessionLabel: hostileText, Block: hostileText, Agent: hostileText, State: core.AgentBlocked, Kind: core.AgentKindPermission,
		Progress: &three, Msg: hostileText, Title: hostileText, RecordID: hostileText, NRecords: 7, UpdatedAt: goldenNow.Add(-time.Hour), Reported: true,
		Process: hostileText, Cwd: hostileText, Repo: hostileText, Branch: hostileText,
	}}}, nil
}
func (hostileAgents) Open(core.Placement) core.OpenPlan { return core.OpenPlan{} }

func TestAgentsTabIsAfterDoctorAndOnlyWithAProvider(t *testing.T) {
	m := demoWorkspaceModel(t, asciiTheme(), &wsRecorder{}, 100, 32)
	var names []string
	for _, tb := range m.(*Model).tabs {
		names = append(names, tb.name)
	}
	if got := strings.Join(names, " "); got != "Board Tasks Leases Queues Doctor Agents" {
		t.Fatalf("digits 1 to 5 must not move: %s", got)
	}
	// with no provider the shell is what it was: five tabs, and the digit 6 goes nowhere
	plain := load(t, asciiTheme(), nil)
	if n := len(plain.(*Model).tabs); n != 5 {
		t.Fatalf("%d tabs without a provider", n)
	}
	before := sized(plain, 100, 30).View()
	if after := press(sized(plain, 100, 30), "6").View(); after != before || strings.Contains(after, "Agents") {
		t.Fatalf("6 did something with no Agents tab:\n%s", after)
	}
	if _, ok := press(sized(plain, 100, 30), ":").(*Model); !ok {
		t.Fatal("the prompt still opens")
	}
}

func TestTheAgentsCommandGoesThereOnlyWhenTheTabExists(t *testing.T) {
	m := demoWorkspaceModel(t, asciiTheme(), &wsRecorder{}, 100, 32)
	m = pressRunKeys(m, ":", "agents", "enter")
	if m.(*Model).sel != len(m.(*Model).tabs)-1 {
		t.Fatalf("selected %d", m.(*Model).sel)
	}
	plain := sized(load(t, asciiTheme(), nil), 100, 30)
	plain = pressRunKeys(plain, ":", "agents", "enter")
	if plain.(*Model).sel != 0 || !strings.Contains(plain.View(), "Agents is not available") {
		t.Fatalf("%s", plain.View())
	}
}

// pressRunKeys types a command: the first key opens the prompt, the rest are text or named keys.
func pressRunKeys(m tea.Model, ks ...string) tea.Model {
	for i, k := range ks {
		if i == 1 {
			m = typeText(m, k)
			continue
		}
		m = press(m, k)
	}
	return m
}

func TestAgentsListIsSortedForAttentionAndShowsTheStaleMarker(t *testing.T) {
	m := agentsModel(t, asciiTheme(), &wsRecorder{}, ptr(fleet(t)))
	m = press(sized(m, 140, 32), "6")
	var got []string
	for _, p := range agentsTab(m).panes() {
		w := stateWord(p)
		if p.Kind != core.AgentKindNone {
			w += "/" + string(p.Kind)
		}
		got = append(got, w)
	}
	want := "blocked/permission blocked/question error done working working working idle quiet"
	if strings.Join(got, " ") != want {
		t.Fatalf("got  %v\nwant %s", got, want)
	}
	v := m.View()
	if !strings.Contains(v, "25m stale") {
		t.Fatalf("the stale pane is marked:\n%s", v)
	}
	if !strings.Contains(v, "~2m") {
		t.Fatalf("a pane on another machine shows its wait with a tilde:\n%s", v)
	}
	if !strings.Contains(v, "(working)") || !strings.Contains(v, "(blocked)") {
		t.Fatalf("the state badges are whole at this width:\n%s", v)
	}
}

func ptr[T any](v T) *T { return &v }

func TestAgentsBodiesNeverNameTheTool(t *testing.T) {
	for _, sc := range agentScenarios() {
		if sc.name == "agents-notice" { // the notice shows the command to copy, which is the tool's name
			continue
		}
		for _, sz := range [][2]int{{80, 30}, {140, 32}} {
			v := render(t, sc, asciiTheme(), sz[0], sz[1])
			if strings.Contains(strings.ToLower(v), "rex") {
				t.Errorf("%s %v names the tool:\n%s", sc.name, sz, v)
			}
		}
	}
}

func TestHostileAgentTextNeverReachesTheScreenRaw(t *testing.T) {
	var sc scenario
	for _, c := range agentScenarios() {
		if c.name == "agents-hostile" {
			sc = c
		}
	}
	for _, sz := range [][2]int{{60, 24}, {140, 40}} {
		v := render(t, sc, asciiTheme(), sz[0], sz[1])
		for _, bad := range []string{"\x1b", "\x07", "pwned\a", "‮", "\r"} {
			if strings.Contains(v, bad) {
				t.Errorf("%v: %q on screen", sz, bad)
			}
		}
		if e := invariantErrs(sc, v, sz[0], sz[1]); len(e) > 0 {
			t.Errorf("%v: %v", sz, e)
		}
	}
}

func TestAgentsReadsOnlyWhileVisibleOneAtATimeAndBacksOff(t *testing.T) {
	var calls int
	prov := countingAgents{n: &calls, rep: core.AgentReport{State: core.Connected, Panes: []core.AgentPane{{Session: "s", Block: "b", State: core.AgentWorking, Reported: true}}}}
	s := newAgentsSection(prov, nil, nil)
	s.tick = func() tea.Cmd { return nil }
	// hidden: a tick starts nothing
	if _, cmd := s.Update(agentsTickMsg{}); cmd != nil {
		t.Fatal("a hidden view must not sweep")
	}
	// shown: the first sweep starts at once, and only one runs at a time
	cmd := s.activate()
	if cmd == nil {
		t.Fatal("showing the view starts a sweep")
	}
	if s.activate() != nil {
		t.Fatal("a second sweep must not start while one is in flight")
	}
	if _, c := s.Update(runeKey("r")); c != nil {
		t.Fatal("r while a sweep is in flight starts nothing")
	}
	msg := cmd()
	if calls != 1 {
		t.Fatalf("%d reads", calls)
	}
	am := msg.(agentsMsg)
	am.took = 3 * time.Second // slow: the wait doubles
	s.Update(am)
	if s.poll.Wait() != 2*agentsPollEvery {
		t.Fatalf("wait %v after a slow sweep", s.poll.Wait())
	}
	// something is working, so the base rhythm is the quick one; with nothing active it is the idle one
	if s.poll.Base != agentsPollEvery {
		t.Fatalf("base %v", s.poll.Base)
	}
	s.rep.Panes = []core.AgentPane{{State: core.AgentDone, Reported: true}}
	s.retune()
	if s.poll.Base != agentsIdleEvery {
		t.Fatalf("base %v when nothing is blocked or working", s.poll.Base)
	}
	// r asks again at once, however long the wait
	if _, c := s.Update(runeKey("r")); c == nil {
		t.Fatal("r refreshes at once")
	}
	// hidden again: nothing
	s.poll.Abort()
	s.deactivate()
	if _, c := s.Update(agentsTickMsg{}); c != nil {
		t.Fatal("a hidden view must not sweep")
	}
}

type countingAgents struct {
	n   *int
	rep core.AgentReport
}

func (c countingAgents) Agents(context.Context, []core.WorkspaceItem) (core.AgentReport, error) {
	*c.n++
	return c.rep, nil
}
func (countingAgents) Open(core.Placement) core.OpenPlan { return core.OpenPlan{} }

func TestAgentsKeysBindNothingThatWritesAndCollideWithNoTab(t *testing.T) {
	s := newAgentsSection(nil, nil, nil)
	s.rep = &core.AgentReport{Panes: []core.AgentPane{{Session: "s", Block: "b", Open: &core.OpenPlan{}}}}
	s.w = 140
	allowed := map[string]bool{"up": true, "k": true, "down": true, "j": true, "enter": true, "esc": true, "r": true, "w": true}
	for _, b := range s.Bindings() {
		for _, k := range b.Keys() {
			if !allowed[k] {
				t.Errorf("unexpected key %q (%s)", k, b.Help().Desc)
			}
		}
	}
	for _, k := range []string{"1", "2", "3", "4", "5", "6", "n", "e", "x", "y", "d", "J", "/", ":", "?", "q", "tab"} {
		if hasKey(s.Bindings(), k) {
			t.Errorf("%q is global or writes elsewhere", k)
		}
	}
}

// openProv is a provider whose panes carry the given open plans.
type openProv struct{ panes []core.AgentPane }

func (o openProv) Agents(context.Context, []core.WorkspaceItem) (core.AgentReport, error) {
	return core.AgentReport{State: core.Connected, Panes: o.panes}, nil
}
func (openProv) Open(p core.Placement) core.OpenPlan {
	if p.Host == "" {
		return core.OpenPlan{Rungs: []core.OpenRung{}, Copy: "rex", Note: "Open the terminal workspace on this machine."}
	}
	argv := core.HandoffArgv("", p.Host)
	return core.OpenPlan{Rungs: []core.OpenRung{{Name: "handoff", Argv: argv, Interactive: true}, {Name: "second", Argv: []string{"rex", "kill"}, Interactive: true}}, Copy: core.ShellJoin(argv)}
}

func agentsOpening(t *testing.T, rec *wsRecorder, panes ...core.AgentPane) *agentsSection {
	t.Helper()
	prov := openProv{panes: panes}
	ws := newWsLayer("Agents", nil, false, rec.handoff)
	s := newAgentsSection(prov, nil, ws)
	s.tick = func() tea.Cmd { return nil }
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	cmd := s.activate()
	s.Update(cmd())
	return s
}

func TestOpeningAPaneRunsExactlyTheFirstHandoffOnTheKeyOnly(t *testing.T) {
	rec := &wsRecorder{ret: opens()}
	s := agentsOpening(t, rec, core.AgentPane{Host: "mbp", Session: "session:1", Block: "block:a", State: core.AgentWorking, Reported: true})
	_ = s.View()
	if len(rec.calls) != 0 {
		t.Fatalf("drawing ran %v", rec.calls)
	}
	_, cmd := s.Update(runeKey("w"))
	if cmd == nil || len(rec.calls) != 1 || strings.Join(rec.calls[0], " ") != "rex -S mbp" {
		t.Fatalf("%v", rec.calls)
	}
	// a second press while it is open does nothing
	if _, cmd := s.Update(runeKey("w")); cmd != nil || len(rec.calls) != 1 {
		t.Fatalf("%v", rec.calls)
	}
}

func TestAPaneOnThisMachineHasOnlyTheCommandToCopyAndRunsNothing(t *testing.T) {
	rec := &wsRecorder{ret: opens()}
	s := agentsOpening(t, rec, core.AgentPane{Session: "session:1", Block: "block:a", SessionLabel: "work", State: core.AgentWorking, Reported: true})
	_, cmd := s.Update(runeKey("w"))
	v := s.View()
	if cmd != nil || len(rec.calls) != 0 || !strings.Contains(v, "copy: rex") || !strings.Contains(v, "any key closes") {
		t.Fatalf("%v\n%s", rec.calls, v)
	}
	if _, cmd := s.Update(runeKey("j")); cmd != nil || strings.Contains(s.View(), "any key closes") {
		t.Fatal("the key that closes the notice does nothing else")
	}
}

func TestOnlyTheWorkspaceHandoffShapeIsEverRunFromAPane(t *testing.T) {
	// a plan whose first step is anything but `<tool> -S <this pane's machine>` is shown, not run
	rec := &wsRecorder{ret: opens()}
	bad := openBadProv{}
	ws := newWsLayer("Agents", nil, false, rec.handoff)
	s := newAgentsSection(bad, nil, ws)
	s.tick = func() tea.Cmd { return nil }
	s.SetTheme(asciiTheme())
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.Update(s.activate()())
	_, cmd := s.Update(runeKey("w"))
	if cmd != nil || len(rec.calls) != 0 {
		t.Fatalf("ran %v", rec.calls)
	}
	if !strings.Contains(s.View(), "cannot be run from here") {
		t.Fatalf("%s", s.View())
	}
}

type openBadProv struct{}

func (openBadProv) Agents(context.Context, []core.WorkspaceItem) (core.AgentReport, error) {
	return core.AgentReport{State: core.Connected, Panes: []core.AgentPane{{Host: "mbp", Session: "session:1", Block: "block:a", Reported: true, State: core.AgentIdle}}}, nil
}
func (openBadProv) Open(core.Placement) core.OpenPlan {
	return core.OpenPlan{Rungs: []core.OpenRung{{Name: "handoff", Argv: []string{"rex", "kill", "mbp"}, Interactive: true}}}
}

func TestADemoSessionOpensNothing(t *testing.T) {
	rec := &wsRecorder{ret: opens()}
	rep := fleet(t)
	m := agentsModel(t, asciiTheme(), rec, &rep)
	m = press(press(sized(m, 140, 32), "6"), "j") // the second pane is on another machine
	m = press(m, "w")
	if len(rec.calls) != 0 || !strings.Contains(m.View(), "Demo data: nothing is opened.") {
		t.Fatalf("%v\n%s", rec.calls, m.View())
	}
}

func TestSelectionStaysOnThePaneWhenARefreshReordersTheRows(t *testing.T) {
	rep := fleet(t)
	m := press(sized(agentsModel(t, asciiTheme(), &wsRecorder{}, &rep), 140, 32), "6")
	m = press(press(m, "j"), "j")
	s := agentsTab(m)
	want := paneID(*s.selected())
	rev := rep
	rev.Panes = append([]core.AgentPane(nil), rep.Panes...)
	for i, j := 0, len(rev.Panes)-1; i < j; i, j = i+1, j-1 {
		rev.Panes[i], rev.Panes[j] = rev.Panes[j], rev.Panes[i]
	}
	m, _ = m.Update(agentsMsg{rep: rev})
	if got := paneID(*agentsTab(m).selected()); got != want {
		t.Fatalf("the selection moved from %q to %q", want, got)
	}
}

func TestAgentsKeyBindingsAreInTheFooterOnlyOnTheAgentsTab(t *testing.T) {
	rep := fleet(t)
	m := sized(agentsModel(t, asciiTheme(), &wsRecorder{}, &rep), 140, 32)
	if strings.Contains(m.View(), "open workspace") {
		t.Fatal("not on the tab")
	}
	if v := press(m, "6").View(); !strings.Contains(v, "r refresh") || !strings.Contains(v, "w open workspace") {
		t.Fatalf("%s", v)
	}
}
