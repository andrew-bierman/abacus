package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func i64(v int64) *int64 { return &v }

func TestMergeAgentsByPIDAndSession(t *testing.T) {
	monitor := core.Agent{Harness: core.HarnessClaude, PID: 10, SessionID: "s1", State: core.AgentRunning, Source: core.EvidenceMonitor, Tokens: i64(500)}
	transcript := core.Agent{Harness: core.HarnessClaude, SessionID: "s1", State: core.AgentIdle, Source: core.EvidenceTranscript, Task: "do the thing", Repo: "orchard", LastActivity: "2026-01-01T10:00:00Z"}
	process := core.Agent{Harness: core.HarnessClaude, PID: 10, Cwd: "/work/orchard", State: core.AgentUnknown, Source: core.EvidenceProcess, Term: &core.TermRef{Kind: core.TermTmux, Ref: "w:0.0"}}
	other := core.Agent{Harness: core.HarnessCodex, PID: 10, State: core.AgentIdle, Source: core.EvidenceProcess} // same pid, other harness: a different agent
	got := core.MergeAgents([]core.Agent{process, other}, []core.Agent{transcript}, []core.Agent{monitor})
	if len(got) != 2 {
		t.Fatalf("%d agents: %+v", len(got), got)
	}
	var m core.Agent
	for _, a := range got {
		if a.Harness == core.HarnessClaude {
			m = a
		}
	}
	if m.State != core.AgentRunning || m.Source != core.EvidenceMonitor {
		t.Fatalf("the strongest evidence must decide the state: %+v", m)
	}
	if m.Task != "do the thing" || m.Repo != "orchard" || m.Cwd != "/work/orchard" || m.Term == nil || m.Tokens == nil || *m.Tokens != 500 || m.PID != 10 || m.SessionID != "s1" {
		t.Fatalf("fields from every feed are kept: %+v", m)
	}
	if len(m.Sources) != 3 || m.ID != "claude-code:10" {
		t.Fatalf("%+v", m)
	}
}

func TestMergeKeepsUnknownStateWhenNobodyKnows(t *testing.T) {
	got := core.MergeAgents([]core.Agent{{Harness: "x", PID: 1, State: "weird", Source: core.EvidenceProcess}})
	if got[0].State != core.AgentUnknown || got[0].Source != core.EvidenceProcess || !got[0].Guess() {
		t.Fatalf("%+v", got[0])
	}
}

func TestAgentNilIsNotZero(t *testing.T) {
	b, _ := json.Marshal(core.Agent{ID: "x", Harness: "h", State: core.AgentIdle, Source: core.EvidenceProcess})
	for _, k := range []string{"tokens", "cost_usd", "context_pct", "term"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Fatalf("an unknown %s was written as a value: %s", k, b)
		}
	}
	zero := 0.0
	b, _ = json.Marshal(core.Agent{ID: "x", CostUSD: &zero})
	if !strings.Contains(string(b), `"cost_usd":0`) {
		t.Fatalf("a known zero must stay: %s", b)
	}
}

func TestCleanAgentTextAndOutOfRangeContext(t *testing.T) {
	got := core.CleanAgentText("a\x1b[31m red\nline sk-abcdefghijklmnop12 ghp_abcdefghijklmnopqrst Bearer abcdefghijklmnopqrstuv", 0)
	if strings.ContainsAny(got, "\x1b\n") || strings.Contains(got, "sk-abc") || strings.Contains(got, "ghp_") || strings.Contains(got, "Bearer abc") {
		t.Fatalf("%q", got)
	}
	if long := core.CleanAgentText(strings.Repeat("é", 500), 20); len([]rune(long)) != 20 {
		t.Fatal(len([]rune(long)))
	}
	bad := 140.0
	if a := (core.Agent{ContextPct: &bad}).Clean(); a.ContextPct != nil {
		t.Fatal("a context share over 100 must be unknown")
	}
}

func TestSortFilterAgents(t *testing.T) {
	list := core.MergeAgents([]core.Agent{
		{Harness: core.HarnessClaude, PID: 1, State: core.AgentIdle, Source: core.EvidenceMonitor, LastActivity: "2026-01-01T10:00:00Z", Repo: "a", Task: "alpha"},
		{Harness: core.HarnessCodex, PID: 2, State: core.AgentWaiting, Source: core.EvidenceMonitor, LastActivity: "2026-01-01T09:00:00Z", Repo: "b", Task: "beta"},
		{Harness: core.HarnessClaude, PID: 3, State: core.AgentRunning, Source: core.EvidenceMonitor, LastActivity: "2026-01-01T11:00:00Z", Repo: "b", Task: "gamma"},
		{Harness: core.HarnessClaude, PID: 4, State: core.AgentRunning, Source: core.EvidenceMonitor, LastActivity: "2026-01-01T11:30:00Z", Repo: "a", Task: "delta"},
	})
	var ids []int
	for _, a := range list {
		ids = append(ids, a.PID)
	}
	if ids[0] != 2 || ids[1] != 4 || ids[2] != 3 || ids[3] != 1 { // waiting, then running newest first, then idle
		t.Fatalf("%v", ids)
	}
	if n := len(core.FilterAgents(list, core.AgentFilter{Harness: "claude-code", Repo: "b"})); n != 1 {
		t.Fatal(n)
	}
	if n := len(core.FilterAgents(list, core.AgentFilter{Harness: "Codex"})); n != 1 {
		t.Fatal("harness display name")
	}
	if n := len(core.FilterAgents(list, core.AgentFilter{State: core.AgentRunning, Query: "GAM"})); n != 1 {
		t.Fatal(n)
	}
	if n := len(core.FilterAgents(list, core.AgentFilter{})); n != 4 {
		t.Fatal(n)
	}
}

type feed struct {
	list  []core.Agent
	extra core.AgentsExtra
	err   error
	state core.State
	name  string
}

func (f *feed) ID() string                            { return f.name }
func (f *feed) Name() string                          { return f.name }
func (f *feed) Detect(context.Context) core.Detection { return core.Detection{State: f.state} }
func (f *feed) Doctor(context.Context) []core.Check   { return nil }
func (f *feed) Status(context.Context) core.Status    { return core.Status{} }
func (f *feed) Agents(context.Context) ([]core.Agent, core.AgentsExtra, error) {
	return f.list, f.extra, f.err
}

func TestReadAgentsPartialFeedsAndNotConnected(t *testing.T) {
	rate := 3.0
	good := &feed{name: "good", state: core.Ready, list: []core.Agent{{Harness: core.HarnessCodex, PID: 5, State: core.AgentIdle, Source: core.EvidenceProcess}},
		extra: core.AgentsExtra{TokenRate: &rate}}
	bad := &feed{name: "bad", state: core.Ready, err: errors.New("monitor\x1b[31m broke")}
	absent := &feed{name: "absent", state: core.Absent}
	r := core.ReadAgents(context.Background(), []core.Entry{{Module: good, Detection: core.Detection{State: core.Ready}}, {Module: bad, Detection: core.Detection{State: core.Ready}}, {Module: absent, Detection: core.Detection{State: core.Absent}}})
	if r.State != core.Connected || len(r.Agents) != 1 || len(r.Notes) != 1 || strings.Contains(r.Notes[0], "\x1b") || r.TokenRate == nil || r.Domain != core.DomainAgents {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Summary, "1 agents") {
		t.Fatal(r.Summary)
	}
	none := core.ReadAgents(context.Background(), nil)
	if none.State != core.NotConnected || none.Agents == nil {
		t.Fatalf("%+v", none)
	}
	b, _ := core.ReportJSON(r)
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m["schema_version"] != float64(1) || m["agents"] == nil {
		t.Fatalf("%s", b)
	}
}

func TestAgentStateClosedSet(t *testing.T) {
	for _, s := range core.AgentStates {
		if !s.Valid() || s.Normalize() != s {
			t.Fatal(s)
		}
	}
	if core.AgentState("zzz").Normalize() != core.AgentUnknown {
		t.Fatal("unknown values normalize")
	}
}
