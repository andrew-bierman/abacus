package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestAgentsAreSearchable(t *testing.T) {
	rep := core.AgentsReport{Agents: core.MergeAgents([]core.Agent{
		{Harness: core.HarnessCodex, PID: 2, State: core.AgentWaiting, Source: core.EvidenceMonitor, Repo: "mill", Branch: "main", Task: "Package the wheat module", LastActivity: "2026-11-23T11:30:00Z"},
		{Harness: core.HarnessClaude, PID: 3, State: core.AgentRunning, Source: core.EvidenceMonitor, Repo: "orchard", Branch: "feat/pears", Cwd: "/work/orchard"},
	})}
	ix := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainAgents, Docs: core.AgentsSearchDocs(rep), ReadAt: searchNow})
	for q, want := range map[string]string{"wheat": "codex:2", "status:running": "claude-code:3", "who:codex": "codex:2", "pears": "claude-code:3", "orchard": "claude-code:3", "in:agents status:waiting": "codex:2"} {
		r, err := ix.Search(q, core.SearchOptions{Now: searchNow})
		if err != nil || r.Total != 1 || r.Groups[0].Results[0].ID != want {
			t.Errorf("%q: %v %+v", q, err, r.Groups)
		}
	}
	if r, _ := ix.Search("status:running wheat", core.SearchOptions{Now: searchNow}); r.Total != 0 {
		t.Error("predicates and words both apply")
	}
}

// hostile is text that would move the cursor, retitle the window, hide or reorder what follows.
const hostile = "a\x1b[31mred\x1b]0;pwned\x07b‮evil⁦x​z\x9b2J"

// walkStrings calls f on every string in v (decoded JSON).
func walkStrings(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case []any:
		for _, e := range x {
			walkStrings(e, f)
		}
	case map[string]any:
		for k, e := range x {
			f(k)
			walkStrings(e, f)
		}
	}
}

// NoUnsafeText fails when any string of a report holds a control or invisible/reordering character.
func NoUnsafeText(t *testing.T, report any) {
	t.Helper()
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	_ = json.Unmarshal(b, &v)
	walkStrings(v, func(s string) {
		for _, r := range s {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x202e || r == 0x2066 || r == 0x200b {
				t.Errorf("unsafe %U in %q", r, s)
				return
			}
		}
	})
}

func TestEveryStringFromOutsideIsSanitizedAtTheBoundary(t *testing.T) {
	pct := 50.0
	f := &feed{name: "f", state: core.Ready,
		list: []core.Agent{{Harness: hostile, PID: 1, SessionID: hostile, Repo: hostile, Branch: hostile, Worktree: hostile, Cwd: hostile, Model: hostile, Task: hostile,
			State: core.AgentRunning, Source: core.EvidenceMonitor, LastActivity: hostile,
			Term:     &core.TermRef{Kind: hostile, Ref: hostile, Command: hostile},
			Ports:    []core.AgentPort{{Port: 1, Command: hostile}},
			Children: []core.AgentChild{{PID: 2, Command: hostile}},
			Subs:     []core.Subagent{{Name: hostile, State: hostile}}}},
		extra: core.AgentsExtra{
			RateLimits:  []core.RateLimit{{Harness: hostile, FiveHourPct: &pct, FiveHourResets: hostile, SevenDayResets: hostile, UpdatedAt: hostile}},
			OrphanPorts: []core.AgentPort{{Port: 3, Command: hostile}}}}
	r := core.ReadAgents(context.Background(), []core.Entry{{Module: f, Detection: core.Detection{State: core.Ready}}})
	NoUnsafeText(t, r)
	if len(r.RateLimits) != 1 || len(r.OrphanPorts) != 1 || len(r.Agents) != 1 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.OrphanPorts[0].Command, "red") {
		t.Fatalf("text must be cleaned, not dropped: %q", r.OrphanPorts[0].Command)
	}
	// the feed's own slices are not edited in place
	if f.list[0].Ports[0].Command != hostile {
		t.Fatal("Clean wrote through to the feed's slices")
	}
}

func TestSecretsAreMaskedInEveryTextField(t *testing.T) {
	for in, leak := range map[string]string{
		"run --api-key sk_live_9f8e7d6c5b4a":                 "sk_live_9f8e7d6c5b4a",
		"curl --token=abcDEF123456":                          "abcDEF123456",
		"mysql --password hunter2xyz":                        "hunter2xyz",
		"deploy -token s3cr3tvalue":                          "s3cr3tvalue",
		"MY_API_KEY=abc123def456 node x.js":                  "abc123def456",
		"DB_PASSWORD=p@ss node":                              "p@ss",
		"GITHUB_TOKEN=ghx_verylongvaluevalue run":            "ghx_verylongvaluevalue",
		"git clone https://user:pa55w0rd@host/repo":          "pa55w0rd",
		"client_secret=zzz999 --client-secret yyy888":        "zzz999",
		"export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG": "wJalrXUtnFEMI",
	} {
		a := core.Agent{Harness: "h", PID: 1, Task: in, Cwd: "/x/" + in, Children: []core.AgentChild{{PID: 2, Command: in}},
			Ports: []core.AgentPort{{Port: 1, Command: in}}, Source: core.EvidenceProcess}.Clean()
		b, _ := json.Marshal(a)
		if strings.Contains(string(b), leak) {
			t.Errorf("%q leaked %q: %s", in, leak, b)
		}
		x := core.CleanExtra(core.AgentsExtra{OrphanPorts: []core.AgentPort{{Port: 1, Command: in}}})
		if strings.Contains(x.OrphanPorts[0].Command, leak) {
			t.Errorf("%q leaked in an orphan port", in)
		}
	}
	// ordinary text is left alone
	if got := core.CleanAgentText("go test -run TestKeyboard ./... and open the token docs", 0); got != "go test -run TestKeyboard ./... and open the token docs" {
		t.Fatalf("%q", got)
	}
}

func TestMergeKeepsTwoLiveProcessesApartEvenWithOneSessionID(t *testing.T) {
	a := core.Agent{Harness: core.HarnessClaude, PID: 10, SessionID: "same", State: core.AgentRunning, Source: core.EvidenceMonitor}
	b := core.Agent{Harness: core.HarnessClaude, PID: 11, SessionID: "same", State: core.AgentIdle, Source: core.EvidenceMonitor}
	tr := core.Agent{Harness: core.HarnessClaude, SessionID: "other", Source: core.EvidenceTranscript, State: core.AgentIdle}
	if got := core.MergeAgents([]core.Agent{a, b, tr}); len(got) != 3 {
		t.Fatalf("two pids with one session id are two agents: %+v", got)
	}
	// a pid-less record of that session still joins one of them
	t2 := core.Agent{Harness: core.HarnessClaude, SessionID: "same", Source: core.EvidenceTranscript, State: core.AgentIdle, Task: "x"}
	if got := core.MergeAgents([]core.Agent{a, b, t2}); len(got) != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestRateLimitsKeepOneEntryPerHarnessTheNewestWins(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := func(v float64) *float64 { return &v }
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	a := &feed{name: "a", state: core.Ready, extra: core.AgentsExtra{RateLimits: []core.RateLimit{
		{Harness: core.HarnessCodex, FiveHourPct: p(10), UpdatedAt: ts(30 * time.Minute)},
		{Harness: core.HarnessClaude, FiveHourPct: p(5)},
	}}}
	b := &feed{name: "b", state: core.Ready, extra: core.AgentsExtra{RateLimits: []core.RateLimit{
		{Harness: core.HarnessCodex, FiveHourPct: p(40), UpdatedAt: ts(time.Minute), Stale: false},
		{Harness: core.HarnessClaude, FiveHourPct: p(6), UpdatedAt: ts(time.Hour), Stale: true},
	}}}
	r := core.ReadAgents(context.Background(), []core.Entry{{Module: a, Detection: core.Detection{State: core.Ready}}, {Module: b, Detection: core.Detection{State: core.Ready}}})
	got := map[string]core.RateLimit{}
	for _, l := range r.RateLimits {
		got[l.Harness] = l
	}
	if len(r.RateLimits) != 2 || *got[core.HarnessCodex].FiveHourPct != 40 {
		t.Fatalf("one entry per harness, the most recently updated: %+v", r.RateLimits)
	}
	if c := got[core.HarnessClaude]; *c.FiveHourPct != 6 || !c.Stale {
		t.Fatalf("a known time beats none, and the entry keeps its own stale mark: %+v", c)
	}
	for _, c := range []struct {
		at   string
		want bool
	}{{ts(time.Minute), false}, {ts(10 * time.Minute), false}, {ts(10*time.Minute + time.Second), true}, {"", true}, {"junk", true}} {
		if got := core.RateLimitStale(c.at, now); got != c.want {
			t.Errorf("%q: stale %v, want %v", c.at, got, c.want)
		}
	}
	if core.WindowLabel(0, "5h") != "5h" || core.WindowLabel(300, "") != "5h" || core.WindowLabel(10080, "") != "7d" || core.WindowLabel(43200, "") != "30d" || core.WindowLabel(90, "") != "90m" {
		t.Fatal("window labels")
	}
}

func TestCleanDerivesContextAndBoundsSeries(t *testing.T) {
	used, win := int64(50), int64(200)
	a := core.Agent{Harness: "h", ContextTokens: &used, ContextWindow: &win}.Clean()
	if a.ContextPct == nil || *a.ContextPct != 25 {
		t.Fatalf("share derived from tokens and window: %+v", a.ContextPct)
	}
	over := int64(900)
	if b := (core.Agent{ContextTokens: &over, ContextWindow: &win}).Clean(); b.ContextPct != nil {
		t.Fatal("a window overrun is unknown, not a share above 100")
	}
	zero := int64(0)
	if b := (core.Agent{ContextTokens: &used, ContextWindow: &zero}).Clean(); b.ContextPct != nil || b.ContextWindow != nil {
		t.Fatal("a zero window is unknown")
	}
	long := make([]int64, 500)
	for i := range long {
		long[i] = int64(i)
	}
	if c := (core.Agent{TurnTokens: long}).Clean(); len(c.TurnTokens) != 120 || c.TurnTokens[119] != 499 {
		t.Fatalf("the newest points are kept: %d", len(c.TurnTokens))
	}
	if c := (core.Agent{TurnTokens: []int64{1, -2}}).Clean(); c.TurnTokens != nil {
		t.Fatal("a negative point drops the series")
	}
	if c := (core.Agent{Git: &core.AgentGit{Added: -1}}).Clean(); c.Git != nil {
		t.Fatal("a negative count is unknown")
	}
	m := core.MergeAgents([]core.Agent{{Harness: "h", PID: 1, Source: core.EvidenceProcess, Git: &core.AgentGit{Modified: 2}}}, []core.Agent{{Harness: "h", PID: 1, Source: core.EvidenceMonitor, TurnTokens: []int64{3}, ContextTokens: &used, ContextWindow: &win}})
	if len(m) != 1 || m[0].Git == nil || m[0].Git.Modified != 2 || len(m[0].TurnTokens) != 1 || m[0].ContextPct == nil {
		t.Fatalf("every feed's new fields survive the merge: %+v", m)
	}
}
