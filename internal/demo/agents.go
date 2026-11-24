package demo

import (
	"context"
	"path"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// agentSource is the demo Agents capability: a few panes in every state, matched to the demo items
// with the same core matcher the real adapter uses. It contacts nothing. It has no Open of its own:
// the plan comes from the demo workspace provider, which is what a real pane would use.
type agentSource struct {
	now func() time.Time // nil means time.Now; a test passes a fixed clock so ages never change
}

// Agents reports the demo panes.
func (a agentSource) Agents(_ context.Context, items []core.WorkspaceItem) (core.AgentReport, error) {
	now := time.Now
	if a.now != nil {
		now = a.now
	}
	return AgentsReport(now(), items), nil
}

// Agents is the demo Agents provider on its own (the demo Leases provider carries the same
// capability, so a demo session has an Agents tab).
type Agents struct {
	module
	agentSource
	WorkspaceProvider
}

// NewAgents returns the demo Agents provider reading the given clock (nil means time.Now).
func NewAgents(now func() time.Time) Agents {
	return Agents{module: module{"rex-demo", "Agents (demo)", "demo"}, agentSource: agentSource{now: now}}
}

func ago(now time.Time, d time.Duration) time.Time { return now.Add(-d) }

func pct(n int) *int { return &n }

// AgentsReport is the demo fleet as of now. The wait times are relative to now. It covers every
// state and kind, a child record count, a stale pane, a pane that runs an agent without having
// reported, a pane that names two items (ambiguous), and one on another machine.
func AgentsReport(now time.Time, items []core.WorkspaceItem) core.AgentReport {
	panes := []core.AgentPane{
		{Session: "demo:s4", SessionLabel: "Lambe · ac-zoj", Block: "demo:b1", Agent: "claude-code", State: core.AgentBlocked, Kind: core.AgentKindPermission,
			Msg: "Approve edit to internal/core/agents.go?", NRecords: 2, UpdatedAt: ago(now, 4*time.Minute), Reported: true,
			Process: "claude", Cwd: "/work/abacus-worktrees/ac-zoj", Repo: "abacus", Branch: "feat/agents-view"},
		{Session: "demo:s5", SessionLabel: "ac-00h", Block: "demo:b2", Agent: "codex", State: core.AgentBlocked, Kind: core.AgentKindQuestion,
			Msg: "Which tracker should the export target?", NRecords: 1, UpdatedAt: ago(now, 9*time.Minute), Reported: true,
			Process: "codex", Cwd: "/work/export", Repo: "export", Branch: "main"},
		{Host: "operators-mac", Session: "demo:s1", SessionLabel: "Lambe · T-1", Block: "demo:b3", Agent: "claude-code", State: core.AgentError,
			Msg: "go test failed: 2 packages", NRecords: 1, UpdatedAt: ago(now, 2*time.Minute), Reported: true,
			Process: "claude", Cwd: "/Users/op/work/tools"},
		{Session: "demo:s3", SessionLabel: "review T-3 notes", Block: "demo:b4", Agent: "claude-code", State: core.AgentDone,
			Msg: "Review notes written to docs/notes.md", NRecords: 1, UpdatedAt: ago(now, 6*time.Minute), Reported: true,
			Process: "claude", Cwd: "/work/docs", Repo: "docs", Branch: "review/t-3"},
		{Session: "demo:s8", SessionLabel: "Lambe · ac-09", Block: "demo:b5", Agent: "claude-code", State: core.AgentWorking, Progress: pct(60),
			Title: "goldens", Msg: "Writing goldens", RecordID: "goldens/ascii", NRecords: 3, UpdatedAt: ago(now, 40*time.Second), Reported: true,
			Process: "claude", Cwd: "/work/abacus", Repo: "abacus", Branch: "feat/queue-digest"},
		{Session: "demo:s9", SessionLabel: "pair: ac-00h + ac-09", Block: "demo:b6", Agent: "codex", State: core.AgentWorking,
			Msg: "Comparing the two exports", NRecords: 1, UpdatedAt: ago(now, 20*time.Second), Reported: true,
			Process: "codex", Cwd: "/work/pair", Repo: "pair", Branch: "main"},
		{Session: "demo:s6", SessionLabel: "Lambe · ac-1k2", Block: "demo:b7", Agent: "claude-code", State: core.AgentWorking, Progress: pct(15),
			Msg: "Indexing the lease history", NRecords: 1, UpdatedAt: ago(now, 25*time.Minute), Reported: true,
			Process: "claude", Cwd: "/work/leases", Repo: "leases", Branch: "main"},
		{Session: "demo:s7", SessionLabel: "retry ac-1k2", Block: "demo:b8", Agent: "claude", State: core.AgentIdle, Process: "claude", Cwd: "/work/leases"},
		{Session: "demo:s10", SessionLabel: "scratch", Block: "demo:b9", Agent: "aider", State: core.AgentIdle, NRecords: 1, UpdatedAt: ago(now, 3*time.Hour), Reported: true,
			Msg: "Ready", Process: "aider", Cwd: "/work/scratch"},
	}
	for i := range panes {
		p := &panes[i]
		texts := []string{p.SessionLabel, p.Msg}
		if p.Cwd != "" {
			texts = append(texts, path.Base(p.Cwd))
		}
		p.Item, p.Candidates, p.Match = core.NamedItems(texts, items, p.Host, "", nil)
	}
	return core.AgentReport{SchemaVersion: core.AgentSchemaVersion, State: core.Connected, Panes: panes, Terminals: 14}
}
