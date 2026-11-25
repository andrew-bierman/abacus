package demo

import (
	"context"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func ptr[T any](v T) *T { return &v }

// AgentsFeed is the demo Agents provider: invented agents on invented repositories, covering every
// state, both kinds of evidence and the live detail (context, ports, subagents, rate limits).
type AgentsFeed struct{ module }

// NewAgents returns the demo Agents feed.
func NewAgents() AgentsFeed { return AgentsFeed{module{"agents", "Agents (demo)", "demo"}} }

// Agents returns the demo agents with times relative to now.
func (AgentsFeed) Agents(context.Context) ([]core.Agent, core.AgentsExtra, error) {
	return AgentsAt(time.Now())
}

// AgentsAt is the demo agent list at a given moment.
func AgentsAt(now time.Time) ([]core.Agent, core.AgentsExtra, error) {
	ago := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }
	list := []core.Agent{
		{Harness: core.HarnessClaude, PID: 41022, SessionID: "demo-s1", Repo: "orchard", Branch: "feat/pear-counter", Cwd: "/work/orchard", Model: "claude-demo-1",
			State: core.AgentRunning, Source: core.EvidenceMonitor, Task: "Add a pear counter to the orchard report", LastActivity: ago(4 * time.Second),
			Tokens: ptr(int64(152_300)), ContextPct: ptr(72.0), ContextTokens: ptr(int64(144_000)), ContextWindow: ptr(int64(200_000)),
			TurnTokens: []int64{4200, 6100, 5300, 9800, 7400, 12100, 8800, 15200, 11000, 6900, 13400, 18700, 9500, 21000, 14200, 8100, 16800, 12500}, Git: &core.AgentGit{Added: 2, Modified: 5},
			Term:     &core.TermRef{Kind: core.TermTmux, Ref: "work:1.0", Command: "tmux switch-client -t %3"},
			Ports:    []core.AgentPort{{Port: 5173, PID: 41188, Command: "node dev-server.js"}},
			Children: []core.AgentChild{{PID: 41187, Command: "go build ./...", MemKB: ptr(int64(120_000))}, {PID: 41188, Command: "node dev-server.js", MemKB: ptr(int64(80_000)), Port: 5173}},
			Subs:     []core.Subagent{{Name: "Survey the orchard", State: "done", Tokens: ptr(int64(12_400))}, {Name: "Check the harvest tests", State: "working", Tokens: ptr(int64(8_200))}}},
		{Harness: core.HarnessCodex, PID: 41310, SessionID: "demo-s2", Repo: "mill", Branch: "main", Cwd: "/work/mill", Model: "codex-demo-1",
			State: core.AgentWaiting, Source: core.EvidenceMonitor, Task: "Package the wheat module", LastActivity: ago(95 * time.Second), Tokens: ptr(int64(4_321)), ContextPct: ptr(18.0), ContextTokens: ptr(int64(36_000)), ContextWindow: ptr(int64(200_000)),
			TurnTokens: []int64{900, 1500, 1921}, Git: &core.AgentGit{},
			Term: &core.TermRef{Kind: core.TermTmux, Ref: "work:2.1", Command: "tmux switch-client -t %5"}},
		{Harness: core.HarnessClaude, PID: 41555, SessionID: "demo-s3", Repo: "cellar", Branch: "fix/barrel-rename", Worktree: "cellar-barrels", Cwd: "/work/wt/cellar-barrels", Model: "claude-demo-1",
			State: core.AgentIdle, Source: core.EvidenceTranscript, Task: "Rename the barrel type everywhere", LastActivity: ago(7 * time.Minute)},
		{Harness: core.HarnessOpenCode, PID: 41701, Repo: "barn", Branch: "main", Cwd: "/work/barn", State: core.AgentUnknown, Source: core.EvidenceProcess},
		{Harness: core.HarnessClaude, SessionID: "demo-s4", Repo: "attic", Branch: "chore/deps", Cwd: "/work/attic", Model: "claude-demo-1",
			State: core.AgentError, Source: core.EvidenceTranscript, Task: "Bump the lockfile", LastActivity: ago(3 * time.Minute)},
		{Harness: core.HarnessCodex, SessionID: "demo-s5", Repo: "orchard", Branch: "docs", Cwd: "/work/orchard", Model: "codex-demo-1",
			State: core.AgentStopped, Source: core.EvidenceTranscript, Task: "Write the pruning guide", LastActivity: ago(12 * time.Minute)},
	}
	limits := []core.RateLimit{
		{Harness: core.HarnessClaude, FiveHourPct: ptr(35.0), FiveHourResets: ago(-90 * time.Minute), SevenDayPct: ptr(12.0), SevenDayResets: ago(-3 * 24 * time.Hour), UpdatedAt: ago(time.Minute)},
		// older than ten minutes: marked stale, drawn dim with no countdown; its far window is 30 days
		{Harness: core.HarnessCodex, FiveHourPct: ptr(88.0), FiveHourResets: ago(-25 * time.Minute), SevenDayPct: ptr(41.0), LongWindowMinutes: 43200, UpdatedAt: ago(40 * time.Minute)},
	}
	for i := range limits {
		limits[i].Stale = core.RateLimitStale(limits[i].UpdatedAt, now)
	}
	return list, core.AgentsExtra{
		TokenRate:   ptr(310.0),
		RateLimits:  limits,
		OrphanPorts: []core.AgentPort{{Port: 4000, PID: 40990, Command: "node old-server.js"}},
	}, nil
}
