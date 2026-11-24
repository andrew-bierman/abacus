package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return t0.Add(-d) }

func pane(block string, st AgentState, kind AgentKind, age time.Duration) AgentPane {
	return AgentPane{Session: "session:a", Block: block, State: st, Kind: kind, Reported: true, UpdatedAt: ago(age)}
}

func blocks(ps []AgentPane) string {
	var b []string
	for _, p := range ps {
		b = append(b, p.Block)
	}
	return strings.Join(b, " ")
}

func TestAttentionOrderPutsWhatNeedsAHumanFirst(t *testing.T) {
	ps := []AgentPane{
		pane("idle", AgentIdle, "", time.Minute),
		pane("work", AgentWorking, "", time.Minute),
		pane("done", AgentDone, "", time.Minute),
		pane("err", AgentError, "", time.Minute),
		pane("question", AgentBlocked, AgentKindQuestion, time.Minute),
		pane("auth", AgentBlocked, AgentKindAuth, time.Minute),
		pane("plain-blocked", AgentBlocked, "", time.Minute),
		pane("perm", AgentBlocked, AgentKindPermission, time.Minute),
		{Session: "session:a", Block: "nostatus", State: AgentIdle, UpdatedAt: time.Time{}},
	}
	SortAgents(ps)
	if got, want := blocks(ps), "perm auth question plain-blocked err done work idle nostatus"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestWithinAGroupTheLongestWaitingIsFirstAndTiesAreDeterministic(t *testing.T) {
	a := pane("b", AgentBlocked, AgentKindPermission, time.Minute)
	b := pane("a", AgentBlocked, AgentKindPermission, time.Hour)
	c := pane("c", AgentBlocked, AgentKindPermission, time.Hour)
	d := pane("z", AgentBlocked, AgentKindPermission, 0)
	d.UpdatedAt = time.Time{} // no time: after the ones that have one
	for _, order := range [][]AgentPane{{a, b, c, d}, {d, c, b, a}, {c, a, d, b}} {
		ps := append([]AgentPane(nil), order...)
		SortAgents(ps)
		if got := blocks(ps); got != "a c b z" {
			t.Fatalf("%q: oldest first, then block id, then untimed", got)
		}
	}
}

func TestCollapseRecordsKeepsTheMostSevereAndCountsThem(t *testing.T) {
	rs := []AgentRecord{
		{ID: "build", State: AgentWorking, Progress: 40, UpdatedAt: ago(time.Minute)},
		{State: AgentBlocked, Kind: AgentKindQuestion, Msg: "which one?", UpdatedAt: ago(time.Hour)},
		{ID: "lint", State: AgentError, Msg: "failed", UpdatedAt: ago(time.Second)},
		{ID: "weird", State: "melting", UpdatedAt: ago(time.Second)},
	}
	top, n, ok := CollapseRecords(rs)
	if !ok || n != 4 || top.State != AgentBlocked || top.Msg != "which one?" {
		t.Fatalf("%+v n=%d ok=%v", top, n, ok)
	}
	if rs[0].State != AgentWorking {
		t.Fatal("the caller's slice must not be reordered")
	}
	// an equal rank is decided by the newer report
	top, _, _ = CollapseRecords([]AgentRecord{{ID: "old", State: AgentWorking, UpdatedAt: ago(time.Hour)}, {ID: "new", State: AgentWorking, UpdatedAt: ago(time.Second)}})
	if top.ID != "new" {
		t.Fatalf("%+v", top)
	}
	// an unknown state reads as idle, below working
	top, _, _ = CollapseRecords([]AgentRecord{{ID: "weird", State: "melting"}, {ID: "w", State: AgentWorking}})
	if top.ID != "w" {
		t.Fatalf("%+v", top)
	}
	if _, n, ok := CollapseRecords(nil); ok || n != 0 {
		t.Fatal("no records, no row")
	}
}

func TestCollapseRecordsCapsHowManyAreLookedAt(t *testing.T) {
	var rs []AgentRecord
	for i := 0; i < 300; i++ {
		rs = append(rs, AgentRecord{State: AgentWorking})
	}
	if _, n, _ := CollapseRecords(rs); n != maxAgentRecords {
		t.Fatalf("n=%d", n)
	}
}

func TestStaleIsOnlyAWorkingOrBlockedPaneWithAnOldReport(t *testing.T) {
	for _, tc := range []struct {
		st    AgentState
		age   time.Duration
		stale bool
	}{
		{AgentWorking, AgentStaleAfter + time.Second, true},
		{AgentWorking, AgentStaleAfter, false},
		{AgentBlocked, time.Hour, true},
		{AgentDone, time.Hour * 24, false},
		{AgentError, time.Hour * 24, false},
		{AgentIdle, time.Hour * 24, false},
		{AgentWorking, -time.Hour, false}, // a report from the future is not stale
	} {
		if got := pane("x", tc.st, "", tc.age).IsStale(t0); got != tc.stale {
			t.Errorf("%s %v: stale=%v", tc.st, tc.age, got)
		}
	}
	if (AgentPane{State: AgentWorking}).IsStale(t0) {
		t.Error("no time, never stale")
	}
}

func TestWaitingHidesAnUnknownOrNegativeDuration(t *testing.T) {
	if d, ok := pane("x", AgentWorking, "", 90*time.Second).Waiting(t0); !ok || d != 90*time.Second {
		t.Fatal(d, ok)
	}
	if _, ok := pane("x", AgentWorking, "", -time.Minute).Waiting(t0); ok {
		t.Fatal("a future report has no wait")
	}
	if _, ok := (AgentPane{}).Waiting(t0); ok {
		t.Fatal("no time")
	}
}

type fakeAgents struct {
	rep  AgentReport
	err  error
	wait bool
}

func (f fakeAgents) Agents(ctx context.Context, _ []WorkspaceItem) (AgentReport, error) {
	if f.wait {
		<-ctx.Done()
		return AgentReport{}, ctx.Err()
	}
	return f.rep, f.err
}
func (fakeAgents) Open(p Placement) OpenPlan {
	return OpenPlan{Rungs: []OpenRung{{Name: "handoff", Argv: HandoffArgv("", p.Host), Interactive: true}}, Copy: "rex -S " + p.Host}
}

func TestCleaningMakesProviderTextSafeAndBounded(t *testing.T) {
	hostile := "\x1b]0;pwned\x07\x1b[2Jboom‮\nline2\t" + strings.Repeat("é", 5000)
	three := 150
	rep := ReadAgentsFrom(context.Background(), fakeAgents{rep: AgentReport{State: Connected, Reason: hostile, HowToConnect: hostile, Panes: []AgentPane{{
		Host: "mbp", Session: hostile, SessionLabel: hostile, Block: hostile, Agent: hostile, State: "melting", Kind: "sudo", Progress: &three,
		Msg: hostile, Title: hostile, RecordID: hostile, Process: hostile, Cwd: hostile, Repo: hostile, Branch: hostile, Match: PlacementMatch(hostile),
		NRecords: 9999, Reported: true,
	}}}}, nil)
	if len(rep.Panes) != 1 || rep.SchemaVersion != AgentSchemaVersion {
		t.Fatalf("%+v", rep)
	}
	p := rep.Panes[0]
	if p.State != AgentIdle || p.Kind != AgentKindNone {
		t.Fatalf("unknown state is idle, unknown kind is none: %s %q", p.State, p.Kind)
	}
	if *p.Progress != 100 || p.NRecords != 256 {
		t.Fatalf("progress and record count are clamped: %d %d", *p.Progress, p.NRecords)
	}
	bounds := map[string]struct {
		got string
		max int
	}{"msg": {p.Msg, maxAgentMsg}, "title": {p.Title, MaxLabelRunes}, "label": {p.SessionLabel, MaxLabelRunes}, "agent": {p.Agent, maxAgentName},
		"cwd": {p.Cwd, maxAgentPath}, "branch": {p.Branch, MaxLabelRunes}, "repo": {p.Repo, MaxLabelRunes}, "id": {p.RecordID, MaxLabelRunes},
		"session": {p.Session, MaxIDRunes}, "block": {p.Block, MaxIDRunes}, "process": {p.Process, maxAgentName}, "reason": {rep.Reason, maxReasonRunes}, "how": {rep.HowToConnect, maxHowRunes}}
	for name, b := range bounds {
		if n := len([]rune(b.got)); n > b.max {
			t.Errorf("%s is %d runes, max %d", name, n, b.max)
		}
		if Sanitize(b.got) != b.got {
			t.Errorf("%s is not sanitized: %q", name, b.got)
		}
	}
}

func TestAKindOnlyMeansSomethingOnABlockedPane(t *testing.T) {
	rep := cleanAgents(AgentReport{Panes: []AgentPane{
		{Block: "a", State: AgentWorking, Kind: AgentKindPermission, Reported: true},
		{Block: "b", State: AgentBlocked, Kind: AgentKindPermission, Reported: true},
	}}, nil)
	for _, p := range rep.Panes {
		if (p.Block == "a") != (p.Kind == AgentKindNone) {
			t.Fatalf("%+v", p)
		}
	}
}

func TestAPaneWithAnUnsafeHostIsDroppedAndTruncatedIsSet(t *testing.T) {
	rep := cleanAgents(AgentReport{Panes: []AgentPane{{Block: "a", Host: "bad\nhost"}, {Block: "b", Host: "ok"}}}, nil)
	if len(rep.Panes) != 1 || rep.Panes[0].Block != "b" || !rep.Truncated {
		t.Fatalf("%+v", rep)
	}
}

func TestPanesAreCappedAndTheCutIsMarked(t *testing.T) {
	var ps []AgentPane
	for i := 0; i < maxAgentPanes+5; i++ {
		ps = append(ps, AgentPane{Block: "b"})
	}
	rep := cleanAgents(AgentReport{Panes: ps}, nil)
	if len(rep.Panes) != maxAgentPanes || !rep.Truncated {
		t.Fatalf("%d %v", len(rep.Panes), rep.Truncated)
	}
}

func TestCandidatesMeanNoItemAndAreBounded(t *testing.T) {
	one := WorkspaceItem{Kind: WorkspaceTask, ID: "ac-1"}
	var cands []WorkspaceItem
	for i := 0; i < 20; i++ {
		cands = append(cands, WorkspaceItem{Kind: WorkspaceTask, ID: "ac-" + string(rune('a'+i))})
	}
	rep := cleanAgents(AgentReport{Panes: []AgentPane{{Block: "a", Item: &one, Candidates: cands}, {Block: "b", Item: &one}}}, nil)
	byBlock := map[string]AgentPane{}
	for _, p := range rep.Panes {
		byBlock[p.Block] = p
	}
	if a := byBlock["a"]; a.Item != nil || len(a.Candidates) != maxCandidates {
		t.Fatalf("an ambiguous pane is never given one item: %+v", a)
	}
	if b := byBlock["b"]; b.Item == nil || *b.Item != one {
		t.Fatalf("%+v", b)
	}
}

func TestEveryPaneGetsTheWorkspaceOpenPlanBuiltByCore(t *testing.T) {
	rep := ReadAgentsFrom(context.Background(), fakeAgents{rep: AgentReport{State: Connected, Panes: []AgentPane{{Host: "mbp", Session: "session:a", Block: "block:b", SessionLabel: "x"}}}}, nil)
	p := rep.Panes[0]
	if p.Open == nil || len(p.Open.Rungs) != 1 || !reflect.DeepEqual(p.Open.Rungs[0].Argv, []string{"rex", "-S", "mbp"}) {
		t.Fatalf("%+v", p.Open)
	}
}

func TestAProviderFailureIsADegradedReportNotAnError(t *testing.T) {
	rep := ReadAgentsFrom(context.Background(), fakeAgents{err: errors.New("boom\x1b[2J")}, nil)
	if rep.State != Degraded || rep.Reason != "boom[2J" || len(rep.Panes) != 0 {
		t.Fatalf("%+v", rep)
	}
	if rep := ReadAgentsFrom(context.Background(), nil, nil); !rep.Empty() || rep.State != "" {
		t.Fatalf("no provider is empty: %+v", rep)
	}
	if rep := ReadAgents(context.Background(), nil, nil); !rep.Empty() {
		t.Fatalf("no entries is empty: %+v", rep)
	}
}

func TestAHungProviderIsBoundedByTheAgentBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	rep := ReadAgentsFrom(ctx, fakeAgents{wait: true}, nil)
	if rep.State != Degraded || time.Since(start) > time.Second {
		t.Fatalf("%+v after %v", rep, time.Since(start))
	}
	if agentReadTime != 4*time.Second {
		t.Fatalf("the sweep budget is pinned at 4s, got %v", agentReadTime)
	}
}

func TestEmptyIsNothingToShow(t *testing.T) {
	for r, want := range map[string]bool{"zero": true, "panes": false, "degraded": false, "truncated": false} {
		var rep AgentReport
		switch r {
		case "panes":
			rep.Panes = []AgentPane{{}}
		case "degraded":
			rep.State = Degraded
		case "truncated":
			rep.Truncated = true
		}
		if rep.Empty() != want {
			t.Errorf("%s: %v", r, rep.Empty())
		}
	}
}

// The JSON is a contract (schema_version 1, additive): the keys are pinned, not derived.
func TestAgentReportJSONIsPinnedAtSchemaVersionOne(t *testing.T) {
	prog := 40
	it := WorkspaceItem{Kind: WorkspaceTask, ID: "ac-12"}
	rep := AgentReport{SchemaVersion: 1, State: Connected, Terminals: 3, Panes: []AgentPane{{
		Host: "mbp", Session: "session:a", SessionLabel: "ac-12", Block: "block:b", Agent: "claude-code", State: AgentBlocked, Kind: AgentKindPermission,
		Progress: &prog, Msg: "Approve edit?", Title: "t", RecordID: "build/test", NRecords: 2, UpdatedAt: t0, Reported: true,
		Process: "node", Cwd: "/w", Repo: "abacus", Branch: "main", Item: &it, Match: MatchLabel, Open: &OpenPlan{Copy: "rex -S mbp"},
	}}}
	got, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"state":"connected","panes":[{"host":"mbp","session":"session:a","session_label":"ac-12","block":"block:b",` +
		`"agent":"claude-code","state":"blocked","kind":"permission","progress":40,"msg":"Approve edit?","title":"t","record_id":"build/test",` +
		`"records":2,"updated_at":"2026-11-24T12:00:00Z","reported":true,"process":"node","cwd":"/w","repo":"abacus","branch":"main",` +
		`"item":{"kind":"task","id":"ac-12"},"match":"label","open":{"rungs":[],"copy":"rex -S mbp"}}],"terminals":3}`
	if string(got) != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestPickFindsTheAgentProviderOnlyWhenReady(t *testing.T) {
	m := fakeAgentModule{}
	p := ResolveProviders([]Entry{{Module: m, Detection: Detection{State: Ready}}})
	if p.Agents == nil {
		t.Fatal("a ready module that provides agents is picked")
	}
	for _, st := range []State{Absent, Incompatible} {
		if ResolveProviders([]Entry{{Module: m, Detection: Detection{State: st}}}).Agents != nil {
			t.Errorf("%v: picked", st)
		}
	}
}

type fakeAgentModule struct{ fakeAgents }

func (fakeAgentModule) ID() string                       { return "fake-agents" }
func (fakeAgentModule) Name() string                     { return "Fake agents" }
func (fakeAgentModule) Detect(context.Context) Detection { return Detection{State: Ready} }
func (fakeAgentModule) Doctor(context.Context) []Check   { return nil }
func (fakeAgentModule) Status(context.Context) Status    { return Status{} }

func TestNamedItemsMatchesOnceByIDAndNeverPicksBetweenTwo(t *testing.T) {
	task := WorkspaceItem{Kind: WorkspaceTask, ID: "ac-1"}
	lease := WorkspaceItem{Kind: WorkspaceLease, ID: "ac-1"}
	other := WorkspaceItem{Kind: WorkspaceTask, ID: "ac-2"}
	remote := WorkspaceItem{Kind: WorkspaceRun, ID: "T-9", Host: "mbp"}
	items := []WorkspaceItem{task, lease, other, remote}
	it, cands, m := NamedItems([]string{"work", "Lambe · ac-1"}, items, "", "me", nil)
	if it == nil || *it != task || cands != nil || m != MatchExact {
		t.Fatalf("one id, first item of that id, exact: %v %v %v", it, cands, m)
	}
	it, cands, m = NamedItems([]string{"ac-1 and ac-2 pair"}, items, "", "me", nil)
	if it != nil || len(cands) != 2 || cands[0].ID != "ac-1" || cands[1].ID != "ac-2" || m != MatchLabel {
		t.Fatalf("two ids: candidates, no pick: %v %v %v", it, cands, m)
	}
	if it, cands, _ := NamedItems([]string{"nothing here"}, items, "", "me", nil); it != nil || cands != nil {
		t.Fatal("none")
	}
	// an item that lives on another machine only matches a pane there
	if it, _, _ := NamedItems([]string{"T-9"}, items, "", "me", nil); it != nil {
		t.Fatalf("a local pane must not claim a run on mbp: %v", it)
	}
	if it, _, _ := NamedItems([]string{"T-9"}, items, "MBP", "me", nil); it == nil || it.ID != "T-9" {
		t.Fatal("the pane on mbp names it")
	}
}
