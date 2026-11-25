// Package agents reads what the AI coding agents on this machine are doing right now:
// which sessions are live, what each one is spending in tokens and context, whether it is
// thinking, executing or waiting for input, and which child processes and ports it owns.
//
// It is logic only. It imports no UI package (see internal/core/arch_test.go), so the TUI
// and any other front-end render the same reads unchanged.
//
// Everything here is read-only and local: process metadata from the kernel, session stores
// the agents already write, and `git status` in a session's directory. Nothing is sent
// anywhere and no agent is ever started.
package agents

import (
	"fmt"
	"time"
)

// Harness names one agent product. It is data, not something the UI should switch on to
// decide layout: a capability a harness lacks is simply empty.
type Harness string

const (
	HarnessClaude   Harness = "claude"
	HarnessCodex    Harness = "codex"
	HarnessGrok     Harness = "grok"
	HarnessOpenCode Harness = "opencode"
	HarnessPi       Harness = "pi"
)

// State is what a live session is doing. It is derived, never read from a single field:
// a session that has not written for a while and owns no busy child is Waiting, not Done.
type State string

const (
	// StateThinking means the model is generating: the transcript's last turn is open.
	StateThinking State = "thinking"
	// StateExecuting means a tool is running: a descendant process is burning CPU.
	StateExecuting State = "executing"
	// StateWaiting means the agent is idle, waiting for the user or a permission prompt.
	StateWaiting State = "waiting"
	// StateUnknown means the session is live but its transcript could not be read yet.
	StateUnknown State = "unknown"
)

// Active reports whether the session is doing work, so a front-end can sort busy first
// without re-deriving the rule.
func (s State) Active() bool { return s == StateThinking || s == StateExecuting }

// Tokens is the session's token accounting. CacheRead is counted apart from Input because
// it is nearly free against a quota and would otherwise swamp a rate.
type Tokens struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cache_read"`
	CacheCreate int64 `json:"cache_create"`
}

// Total is the billable-ish sum used for a session row: cache reads excluded.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheCreate }

// sum is every counter, including cache reads, for a per-turn sparkline point.
func (t Tokens) sum() int64 { return t.Total() + t.CacheRead }

// prompt is what the model saw: input plus both cache counters, because input excludes cached
// content.
func (t Tokens) prompt() int64 { return t.Input + t.CacheRead + t.CacheCreate }

func (t Tokens) add(o Tokens) Tokens {
	return Tokens{Input: t.Input + o.Input, Output: t.Output + o.Output,
		CacheRead: t.CacheRead + o.CacheRead, CacheCreate: t.CacheCreate + o.CacheCreate}
}

func (t Tokens) sub(o Tokens) Tokens {
	return Tokens{Input: t.Input - o.Input, Output: t.Output - o.Output,
		CacheRead: t.CacheRead - o.CacheRead, CacheCreate: t.CacheCreate - o.CacheCreate}
}

// Context is how full the model's window is. Limit is 0 when the harness does not record
// it, so a front-end shows nothing rather than a made-up percentage.
type Context struct {
	Used  int64 `json:"used"`
	Limit int64 `json:"limit"`
}

// Percent returns the window fill 0-100, or 0 when the limit is unknown.
func (c Context) Percent() float64 {
	if c.Limit <= 0 {
		return 0
	}
	return float64(c.Used) / float64(c.Limit) * 100
}

// maxTokenHistory is what a sparkline draws.
const maxTokenHistory = 200

// appendHistoryPoint adds one per-turn point, dropping the oldest once the sparkline's budget
// is full, so a long session cannot grow the series without bound.
func appendHistoryPoint(points []int64, point int64) []int64 {
	points = append(points, point)
	if len(points) > maxTokenHistory {
		points = points[len(points)-maxTokenHistory:]
	}
	return points
}

// StoredSession is one session as its harness recorded it, independent of whether a process
// is running right now. A store is the session list for harnesses that have one; the process
// table only says which of them are live.
type StoredSession struct {
	ID       string
	Title    string
	Model    string
	Dir      string
	ParentID string
	Tokens   Tokens
	// Context is the last turn's window fill. A zero value means the harness does not record
	// it, not that the window is empty.
	Context Context
	// TokenHistory is one point per assistant response, oldest first, bounded to what a
	// sparkline draws. Empty when the harness does not record per-turn usage.
	TokenHistory []int64
	// Quota is the account-level rate limit the store recorded while reading this session.
	// It is nil when the harness does not record one; every session on one account repeats
	// the same value, so a report keeps one entry per source.
	Quota   *Quota
	CostUSD float64
	Updated time.Time
	State   State
}

// String is for logs and doctor output, not for the UI.
func (s StoredSession) String() string {
	return fmt.Sprintf("%s %s %s", s.ID, s.State, s.Title)
}

// Child is one process the session spawned, so an abandoned dev server is visible.
type Child struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	RSSKB   int64  `json:"rss_kb"`
}

// Port is a listening socket owned by a session or left behind by one that ended.
type Port struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Command string `json:"command"`
	// Orphan is set when the listening process outlived the session that started it.
	Orphan bool `json:"orphan"`
	// Project names the session's directory, so an orphan says where it came from.
	Project string `json:"project,omitempty"`
}

// Session is one live agent process and what it is doing.
type Session struct {
	Harness Harness `json:"harness"`
	PID     int     `json:"pid"`
	// CWD is the session's working directory, read from the kernel rather than guessed.
	CWD   string `json:"cwd,omitempty"`
	Title string `json:"title,omitempty"`
	Model string `json:"model,omitempty"`
	State State  `json:"state"`

	Tokens  Tokens    `json:"tokens"`
	Context Context   `json:"context"`
	Git     GitStatus `json:"git"`
	// TokenHistory is one point per assistant response, oldest first. It is what a tab draws
	// as the per-turn sparkline; empty means the harness does not record per-turn usage.
	TokenHistory []int64 `json:"token_history,omitempty"`

	Children []Child `json:"children,omitempty"`
	Ports    []Port  `json:"ports,omitempty"`

	// LastActive is the transcript's last write, which is how a stalled session is spotted.
	LastActive time.Time `json:"last_active,omitempty"`
	// Started is when the agent process began, so a front-end can say how long it has run and
	// the join can ask a store for transcripts written since then.
	Started time.Time `json:"started,omitzero"`
	// ParentPID is the agent process this session reports to, when it is a subagent.
	ParentPID int `json:"parent_pid,omitempty"`
}

// Snapshot is one read of the machine. It is the typed shape a front-end renders and the
// JSON shape `abacus sessions --json` prints, so the tab and the CLI cannot disagree.
type Snapshot struct {
	Taken    time.Time `json:"taken,omitzero"`
	Sessions []Session `json:"sessions"`
	// Orphans are ports whose owning session is gone. They need two reads to detect, so
	// they are empty on the first tick of a process, exactly like abtop's snapshot.
	Orphans []Port `json:"orphans,omitempty"`
	// Unreadable names session stores that exist but could not be read, so a partial read
	// says so instead of looking like "no sessions".
	Unreadable []string `json:"unreadable,omitempty"`
}

// Active returns the sessions doing work.
func (s Snapshot) Active() int {
	n := 0
	for _, x := range s.Sessions {
		if x.State.Active() {
			n++
		}
	}
	return n
}

// TotalTokens sums every session's tokens, cache reads excluded.
func (s Snapshot) TotalTokens() Tokens {
	var t Tokens
	for _, x := range s.Sessions {
		t = t.add(x.Tokens)
	}
	return t
}
