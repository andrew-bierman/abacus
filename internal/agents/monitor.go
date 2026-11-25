package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The monitor feed runs a built-in live session monitor once per read and takes its JSON snapshot.
// It sees what the files cannot: the real process behind each session, context use, rate limits,
// child processes and open ports, subagents. Its output has no schema version, so the shape read
// here is the one of the pinned release (see docs/agents.md); anything else degrades to a note and
// the native feed carries on.

// monitorTool is the file name of the built-in monitor.
const monitorTool = "abtop"

// monitorArgv is the one argv this module may run. The monitor's other modes open a terminal UI,
// install a hook or update itself; none is reachable.
var monitorArgv = []string{"--json"}

// monitorDeadline bounds one snapshot (a cold start scans every process).
const monitorDeadline = 15 * time.Second

// maxMonitorOutput caps what is read from the monitor: a snapshot carries bounded chat text and
// tool-call tails per session, so a few MiB is generous.
const maxMonitorOutput = 8 << 20

type allowOnly struct{ inner core.Runner }

func (r allowOnly) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	if len(args) != len(monitorArgv) || args[0] != monitorArgv[0] {
		return nil, -1, fmt.Errorf("%s %q is not allowed: this module only reads a snapshot", monitorTool, args)
	}
	return r.inner.Run(ctx, name, args...)
}

// MonitorOptions configure the monitor feed; every process boundary is injectable.
type MonitorOptions struct {
	Bin      string // explicit binary (ABACUS_ABTOP_BIN)
	LookPath func(string) (string, error)
	Runner   core.Runner // wrapped by the allowlist whatever it is
	Enrich   func([]core.Agent)
	Now      func() time.Time // the clock rate limits are judged stale against; nil means time.Now
}

// Monitor is the module for the built-in session monitor.
type Monitor struct {
	core.Locator
	run      allowOnly
	lookup   func(string) (string, error)
	explicit string
	enrich   func([]core.Agent)
	now      func() time.Time
	mu       sync.Mutex
	ran      bool
	lastErr  error
}

// NewMonitor returns the monitor feed.
func NewMonitor(o MonitorOptions) *Monitor {
	if o.Runner == nil {
		o.Runner = core.ExecRunner{Timeout: monitorDeadline, MaxOut: maxMonitorOutput, Scrub: keepEnv}
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Monitor{run: allowOnly{o.Runner}, lookup: o.LookPath, explicit: o.Bin, enrich: o.Enrich, now: o.Now}
}

// keepEnv drops every inherited variable but the few the monitor needs to find its files.
func keepEnv(env []string) []string {
	var out []string
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		for _, w := range envKeep {
			if k == w {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func (m *Monitor) ID() string   { return monitorTool }
func (m *Monitor) Name() string { return monitorTool }

// Detect finds the binary without running it (the only argv is the snapshot itself).
func (m *Monitor) Detect(context.Context) core.Detection {
	_, absent := m.Find(monitorTool, m.explicit, m.lookup)
	if absent != "" {
		return core.Detection{State: core.Absent, Detail: absent}
	}
	return core.Detection{State: core.Ready}
}

func (m *Monitor) Doctor(ctx context.Context) []core.Check {
	m.mu.Lock()
	ran, err := m.ran, m.lastErr
	m.mu.Unlock()
	if !ran { // nothing has read it yet: one snapshot
		_, _, err = m.Agents(ctx)
	}
	if err != nil {
		return []core.Check{{Name: "live session monitor", Status: core.StatusWarn, Detail: core.CleanAgentText(err.Error(), 160)}}
	}
	return []core.Check{{Name: "live session monitor", Status: core.StatusOK, Detail: "snapshot read"}}
}

func (m *Monitor) Status(context.Context) core.Status { return core.Status{} }

// Agents runs one snapshot and maps it, remembering whether it worked for Doctor.
func (m *Monitor) Agents(ctx context.Context) ([]core.Agent, core.AgentsExtra, error) {
	a, x, err := m.snapshot(ctx)
	m.mu.Lock()
	m.ran, m.lastErr = true, err
	m.mu.Unlock()
	return a, x, err
}

func (m *Monitor) snapshot(ctx context.Context) ([]core.Agent, core.AgentsExtra, error) {
	bin := m.Bin()
	if bin == "" {
		return nil, core.AgentsExtra{}, errors.New("not found")
	}
	ctx, cancel := context.WithTimeout(ctx, monitorDeadline)
	defer cancel()
	out, exit, err := m.run.Run(ctx, bin, monitorArgv...)
	switch {
	case errors.Is(err, core.ErrTimedOut), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, core.AgentsExtra{}, errors.New("the live monitor did not answer in time")
	case err != nil:
		return nil, core.AgentsExtra{}, fmt.Errorf("the live monitor could not run: %w", err)
	case exit != 0:
		return nil, core.AgentsExtra{}, fmt.Errorf("the live monitor exited %d", exit)
	}
	agents, extra, err := ParseSnapshot(out)
	if err == nil && m.enrich != nil {
		m.enrich(agents)
	}
	for i := range extra.RateLimits {
		extra.RateLimits[i].Stale = core.RateLimitStale(extra.RateLimits[i].UpdatedAt, m.now())
	}
	return agents, extra, err
}

// ---- the snapshot ----

type snapSession struct {
	AgentCLI      *string  `json:"agent_cli"`
	PID           *int     `json:"pid"`
	SessionID     string   `json:"session_id"`
	Cwd           string   `json:"cwd"`
	Status        string   `json:"status"`
	Model         string   `json:"model"`
	ContextPct    *float64 `json:"context_percent"`
	ContextWindow *int64   `json:"context_window"`
	TotalTokens   *int64   `json:"total_tokens"`
	GitBranch     string   `json:"git_branch"`
	Summary       string   `json:"summary"`
	CurrentTask   *string  `json:"current_task"`
	Children      []struct {
		PID     int    `json:"pid"`
		Command string `json:"command"`
		MemKB   *int64 `json:"mem_kb"`
		Port    *int   `json:"port"`
	} `json:"children"`
	Subagents []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Tokens *int64 `json:"tokens"`
	} `json:"subagents"`
}

type snapshot struct {
	Sessions   *[]snapSession `json:"sessions"`
	TokenRate  *float64       `json:"token_rate"`
	IntervalMS *float64       `json:"interval_ms"`
	RateLimits []struct {
		Source      string   `json:"source"`
		FiveHourPct *float64 `json:"five_hour_pct"`
		FiveHourEnd *int64   `json:"five_hour_resets_at"`
		SevenDayPct *float64 `json:"seven_day_pct"`
		SevenDayEnd *int64   `json:"seven_day_resets_at"`
		UpdatedAt   *int64   `json:"updated_at"`
	} `json:"rate_limits"`
	OrphanPorts []struct {
		Port    int    `json:"port"`
		PID     int    `json:"pid"`
		Command string `json:"command"`
	} `json:"orphan_ports"`
}

// harnessID maps the monitor's name for a harness to ours; one it has that we have not heard of
// passes through.
func harnessID(cli string) string {
	switch strings.ToLower(cli) {
	case "claude":
		return core.HarnessClaude
	case "codex":
		return core.HarnessCodex
	case "gemini":
		return core.HarnessGemini
	}
	return strings.ToLower(cli)
}

// monitorState maps the monitor's activity word to ours. A word it has that we have not heard of
// is unknown, never invented.
func monitorState(s string) core.AgentState {
	switch s {
	case "Thinking", "Executing":
		return core.AgentRunning
	case "Waiting", "RateLimited":
		return core.AgentWaiting
	case "Done":
		return core.AgentStopped
	}
	return core.AgentUnknown
}

func epoch(sec *int64) string {
	if sec == nil || *sec <= 0 {
		return ""
	}
	return time.Unix(*sec, 0).UTC().Format(time.RFC3339)
}

// ParseSnapshot reads the monitor's `--json` output. Unknown fields are ignored and missing ones
// are unknown (nil); output with no session list at all is rejected as an unrecognized shape.
func ParseSnapshot(b []byte) ([]core.Agent, core.AgentsExtra, error) {
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, core.AgentsExtra{}, errors.New("the live monitor's answer was not understood")
	}
	if s.Sessions == nil {
		return nil, core.AgentsExtra{}, errors.New("the live monitor's answer has a different shape than this version of abacus reads")
	}
	var out []core.Agent
	for _, x := range *s.Sessions {
		if x.AgentCLI == nil || x.PID == nil || *x.PID <= 0 {
			continue // not a session this build can name; the native feed still sees the process
		}
		a := core.Agent{
			Harness: harnessID(*x.AgentCLI), PID: *x.PID, SessionID: x.SessionID, Cwd: x.Cwd, Model: x.Model,
			Branch: x.GitBranch, State: monitorState(x.Status), Source: core.EvidenceMonitor, Task: x.Summary, Tokens: x.TotalTokens,
		}
		if a.Task == "" && x.CurrentTask != nil {
			a.Task = *x.CurrentTask
		}
		if x.ContextPct != nil && x.ContextWindow != nil && *x.ContextWindow > 0 {
			a.ContextPct = x.ContextPct
		}
		for _, c := range x.Children {
			ch := core.AgentChild{PID: c.PID, Command: c.Command, MemKB: c.MemKB}
			if c.Port != nil && *c.Port > 0 {
				ch.Port = *c.Port
				a.Ports = append(a.Ports, core.AgentPort{Port: *c.Port, PID: c.PID, Command: c.Command})
			}
			a.Children = append(a.Children, ch)
		}
		for _, sa := range x.Subagents {
			a.Subs = append(a.Subs, core.Subagent{Name: sa.Name, State: sa.Status, Tokens: sa.Tokens})
		}
		out = append(out, a)
	}
	var extra core.AgentsExtra
	for _, r := range s.RateLimits {
		extra.RateLimits = append(extra.RateLimits, core.RateLimit{
			Harness: harnessID(r.Source), FiveHourPct: r.FiveHourPct, FiveHourResets: epoch(r.FiveHourEnd),
			SevenDayPct: r.SevenDayPct, SevenDayResets: epoch(r.SevenDayEnd), UpdatedAt: epoch(r.UpdatedAt),
		})
	}
	for _, p := range s.OrphanPorts {
		extra.OrphanPorts = append(extra.OrphanPorts, core.AgentPort{Port: p.Port, PID: p.PID, Command: p.Command})
	}
	if s.TokenRate != nil && s.IntervalMS != nil && *s.IntervalMS > 0 {
		// token_rate is tokens per tick; the interval turns it into per second
		r := *s.TokenRate / (*s.IntervalMS / 1000)
		extra.TokenRate = &r
	}
	return out, extra, nil
}
