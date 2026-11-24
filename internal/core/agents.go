package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The Agents domain is one dashboard of every coding agent running on this machine, across
// harnesses (Claude Code, Codex, ...). Feeds (modules that implement AgentFeed) each report what
// they can see; MergeAgents joins the answers. Nothing here writes anything.

// AgentState is what an agent is doing. The values are a closed set; a feed that cannot tell says
// AgentUnknown, never a guess dressed as a fact (Agent.Source says how the state was learned).
type AgentState string

const (
	AgentRunning AgentState = "running"
	AgentIdle    AgentState = "idle"
	AgentWaiting AgentState = "waiting" // wants a person (a prompt or a permission)
	AgentError   AgentState = "error"
	AgentStopped AgentState = "stopped"
	AgentUnknown AgentState = "unknown"
)

// AgentStates is the display and filter order.
var AgentStates = []AgentState{AgentWaiting, AgentError, AgentRunning, AgentIdle, AgentUnknown, AgentStopped}

// Valid reports whether s is one of the closed set.
func (s AgentState) Valid() bool {
	for _, v := range AgentStates {
		if v == s {
			return true
		}
	}
	return false
}

// Normalize maps an undeclared state to AgentUnknown, so no front-end sees a value outside the set.
func (s AgentState) Normalize() AgentState {
	if s.Valid() {
		return s
	}
	return AgentUnknown
}

// Tone is the state's severity for every front-end.
func (s AgentState) Tone() Tone {
	switch s {
	case AgentRunning:
		return ToneOK
	case AgentWaiting:
		return ToneWarn
	case AgentError:
		return ToneFail
	}
	return ToneMuted
}

// rank orders states for the default sort: what needs a person first.
func (s AgentState) rank() int {
	for i, v := range AgentStates {
		if v == s {
			return i
		}
	}
	return len(AgentStates)
}

// AgentEvidence says how a fact about an agent was learned, so a guess is never shown as certain.
type AgentEvidence string

const (
	EvidenceMonitor    AgentEvidence = "monitor"    // a live session monitor that watches processes and sessions together
	EvidenceHook       AgentEvidence = "hook"       // the harness itself announced it (reserved, a later step)
	EvidenceTranscript AgentEvidence = "transcript" // inferred from the session file's last records and age
	EvidencePane       AgentEvidence = "pane"       // inferred from a terminal pane
	EvidenceProcess    AgentEvidence = "process"    // a matching process exists; nothing is known about what it is doing
)

// priority is who wins a conflict: an agent's own word, then the monitor, then files, then a bare process.
func (e AgentEvidence) priority() int {
	switch e {
	case EvidenceHook:
		return 5
	case EvidenceMonitor:
		return 4
	case EvidenceTranscript:
		return 3
	case EvidencePane:
		return 2
	case EvidenceProcess:
		return 1
	}
	return 0
}

// Harness ids and their display names.
const (
	HarnessClaude   = "claude-code"
	HarnessCodex    = "codex"
	HarnessGemini   = "gemini-cli"
	HarnessOpenCode = "opencode"
	HarnessAider    = "aider"
)

// HarnessTitle is the display name of a harness id (an unknown id is shown as it is).
func HarnessTitle(id string) string {
	switch id {
	case HarnessClaude:
		return "Claude Code"
	case HarnessCodex:
		return "Codex"
	case HarnessGemini:
		return "Gemini CLI"
	case HarnessOpenCode:
		return "OpenCode"
	case HarnessAider:
		return "Aider"
	}
	return id
}

// TermRef says where an agent's terminal lives. Ref is the machine-readable address; Command is
// the line a person can copy to get there. abacus never runs it.
type TermRef struct {
	Kind    string `json:"kind"` // "tmux pane" | "cmux surface" | "rex session"
	Ref     string `json:"ref"`
	Command string `json:"command,omitempty"`
}

// Terminal kinds.
const (
	TermTmux = "tmux pane"
	TermCmux = "cmux surface"
	TermRex  = "rex session"
)

// AgentPort is a listening port owned by an agent's child process.
type AgentPort struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid,omitempty"`
	Command string `json:"command,omitempty"`
}

// AgentChild is a process an agent started (a build, a test run, a dev server).
type AgentChild struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	MemKB   *int64 `json:"mem_kb,omitempty"`
	Port    int    `json:"port,omitempty"`
}

// Subagent is one helper an agent spawned.
type Subagent struct {
	Name   string `json:"name"`
	State  string `json:"state,omitempty"` // free text from the harness ("working", "done")
	Tokens *int64 `json:"tokens,omitempty"`
}

// Agent is one coding agent. Pointer fields are nil when unknown (nil is never zero).
type Agent struct {
	ID           string     `json:"id"`
	Harness      string     `json:"harness"`
	PID          int        `json:"pid,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	Repo         string     `json:"repo,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	Worktree     string     `json:"worktree,omitempty"` // set only for a linked worktree
	Cwd          string     `json:"cwd,omitempty"`
	Model        string     `json:"model,omitempty"`
	State        AgentState `json:"state"`
	Task         string     `json:"task,omitempty"`
	LastActivity string     `json:"last_activity,omitempty"` // RFC 3339, UTC
	Tokens       *int64     `json:"tokens,omitempty"`
	CostUSD      *float64   `json:"cost_usd,omitempty"`
	ContextPct   *float64   `json:"context_pct,omitempty"`
	Term         *TermRef   `json:"term,omitempty"`
	// Source says how State was learned; Sources is every feed that saw this agent.
	Source   AgentEvidence   `json:"source"`
	Sources  []AgentEvidence `json:"sources,omitempty"`
	Ports    []AgentPort     `json:"ports,omitempty"`
	Children []AgentChild    `json:"children,omitempty"`
	Subs     []Subagent      `json:"subagents,omitempty"`
}

// Title is "Harness" for display.
func (a Agent) Title() string { return HarnessTitle(a.Harness) }

// Last parses LastActivity (zero when unknown).
func (a Agent) Last() time.Time {
	t, _ := time.Parse(time.RFC3339, a.LastActivity)
	return t
}

// Age is how long ago the agent was last active as a short word ("4s", "3m", "2h", "5d"), "" when unknown.
func (a Agent) Age(now time.Time) string {
	t := a.Last()
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Guess reports whether the state was inferred rather than reported by the monitor or the harness.
func (a Agent) Guess() bool {
	return a.Source != EvidenceMonitor && a.Source != EvidenceHook
}

// RateLimit is one harness account's usage windows, nil when a window is unknown.
type RateLimit struct {
	Harness        string   `json:"harness"`
	FiveHourPct    *float64 `json:"five_hour_pct,omitempty"`
	FiveHourResets string   `json:"five_hour_resets_at,omitempty"`
	SevenDayPct    *float64 `json:"seven_day_pct,omitempty"`
	SevenDayResets string   `json:"seven_day_resets_at,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

// AgentsReport is the typed view of the Agents domain.
type AgentsReport struct {
	Header
	Agents      []Agent     `json:"agents"`
	RateLimits  []RateLimit `json:"rate_limits,omitempty"`
	OrphanPorts []AgentPort `json:"orphan_ports,omitempty"`
	TokenRate   *float64    `json:"token_rate,omitempty"` // tokens per second across agents, nil when unknown
	// Notes say which feeds could not be read or whose answer was not understood. A note is not a
	// failure: the other feeds still count.
	Notes []string `json:"notes,omitempty"`
}

// AgentsExtra is what a feed may add beyond agents.
type AgentsExtra struct {
	RateLimits  []RateLimit
	OrphanPorts []AgentPort
	TokenRate   *float64
}

// AgentFeed is a module that can see agents. Several feeds are merged; any of them may be
// partial. The error of a feed becomes a note, never the failure of the domain.
type AgentFeed interface {
	Agents(ctx context.Context) ([]Agent, AgentsExtra, error)
}

const (
	maxAgentTask   = 160
	maxAgentFields = 200
)

var secretRE = regexp.MustCompile(`(?i)\b(sk-[a-z0-9_-]{12,}|gh[pousr]_[a-z0-9]{16,}|xox[abp]-[a-z0-9-]{10,}|bearer\s+[a-z0-9._-]{16,}|AKIA[0-9A-Z]{16})`)

// CleanAgentText makes text from a transcript, a process list or a monitor safe to show: control
// characters removed, one line, obvious credentials masked, at most max runes.
func CleanAgentText(s string, max int) string {
	s = Sanitize(s)
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	s = secretRE.ReplaceAllString(s, "[redacted]")
	if max > 0 && utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max-1]) + "…"
	}
	return s
}

// Clean returns a copy with every text field sanitized and every enum normalized.
func (a Agent) Clean() Agent {
	c := func(s string) string { return CleanAgentText(s, maxAgentFields) }
	a.Harness, a.Repo, a.Branch, a.Worktree, a.Cwd, a.Model = c(a.Harness), c(a.Repo), c(a.Branch), c(a.Worktree), c(a.Cwd), c(a.Model)
	a.SessionID, a.LastActivity = c(a.SessionID), c(a.LastActivity)
	a.Task = CleanAgentText(a.Task, maxAgentTask)
	a.State = a.State.Normalize()
	if a.Term != nil {
		t := *a.Term
		t.Kind, t.Ref, t.Command = c(t.Kind), c(t.Ref), c(t.Command)
		a.Term = &t
	}
	for i := range a.Children {
		a.Children[i].Command = c(a.Children[i].Command)
	}
	for i := range a.Ports {
		a.Ports[i].Command = c(a.Ports[i].Command)
	}
	for i := range a.Subs {
		a.Subs[i].Name, a.Subs[i].State = c(a.Subs[i].Name), c(a.Subs[i].State)
	}
	if a.ContextPct != nil {
		v := *a.ContextPct
		if v < 0 || v > 100 || v != v {
			a.ContextPct = nil
		}
	}
	return a
}

// AgentID is the stable id of an agent: its harness plus the pid, else the session.
func AgentID(a Agent) string {
	switch {
	case a.PID > 0:
		return fmt.Sprintf("%s:%d", a.Harness, a.PID)
	case a.SessionID != "":
		return a.Harness + ":" + a.SessionID
	}
	return a.Harness + ":" + a.Cwd
}

// MergeAgents joins the agents of several feeds. Two records are one agent when they share a pid
// or a session id (the same harness only). Fields are filled from the strongest evidence first;
// State and Source come from the strongest feed that knew the state. The result is sorted.
func MergeAgents(lists ...[]Agent) []Agent {
	var all []Agent
	for _, l := range lists {
		for _, a := range l {
			a = a.Clean()
			if a.Source == "" {
				a.Source = EvidenceProcess
			}
			all = append(all, a)
		}
	}
	// union-find over records that share a pid or session
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	seen := map[string]int{}
	join := func(key string, i int) {
		if j, ok := seen[key]; ok {
			parent[find(i)] = find(j)
			return
		}
		seen[key] = i
	}
	for i, a := range all {
		if a.PID > 0 {
			join(fmt.Sprintf("%s|pid|%d", a.Harness, a.PID), i)
		}
		if a.SessionID != "" {
			join(a.Harness+"|sess|"+a.SessionID, i)
		}
	}
	groups := map[int][]Agent{}
	var order []int
	for i, a := range all {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], a)
	}
	out := make([]Agent, 0, len(groups))
	for _, r := range order {
		out = append(out, mergeOne(groups[r]))
	}
	SortAgents(out)
	return out
}

func mergeOne(g []Agent) Agent {
	sort.SliceStable(g, func(i, j int) bool { return g[i].Source.priority() > g[j].Source.priority() })
	var m Agent
	m.State = AgentUnknown
	stateSet := false
	for _, a := range g {
		if m.Harness == "" {
			m.Harness = a.Harness
		}
		if m.PID == 0 {
			m.PID = a.PID
		}
		for _, p := range []struct {
			dst *string
			src string
		}{
			{&m.SessionID, a.SessionID}, {&m.Repo, a.Repo}, {&m.Branch, a.Branch}, {&m.Worktree, a.Worktree},
			{&m.Cwd, a.Cwd}, {&m.Model, a.Model}, {&m.Task, a.Task},
		} {
			if *p.dst == "" {
				*p.dst = p.src
			}
		}
		if !stateSet && a.State != AgentUnknown {
			m.State, m.Source, stateSet = a.State, a.Source, true
		}
		if a.Last().After(m.Last()) {
			m.LastActivity = a.LastActivity
		}
		if m.Tokens == nil {
			m.Tokens = a.Tokens
		}
		if m.CostUSD == nil {
			m.CostUSD = a.CostUSD
		}
		if m.ContextPct == nil {
			m.ContextPct = a.ContextPct
		}
		if m.Term == nil {
			m.Term = a.Term
		}
		if len(m.Ports) == 0 {
			m.Ports = a.Ports
		}
		if len(m.Children) == 0 {
			m.Children = a.Children
		}
		if len(m.Subs) == 0 {
			m.Subs = a.Subs
		}
		m.Sources = appendEvidence(m.Sources, a.Source)
	}
	if !stateSet {
		m.Source = g[0].Source
	}
	m.ID = AgentID(m)
	return m
}

func appendEvidence(l []AgentEvidence, e AgentEvidence) []AgentEvidence {
	for _, x := range l {
		if x == e {
			return l
		}
	}
	return append(l, e)
}

// SortAgents orders by what needs a person first, then the most recent, then id.
func SortAgents(l []Agent) {
	sort.SliceStable(l, func(i, j int) bool {
		a, b := l[i], l[j]
		if ra, rb := a.State.rank(), b.State.rank(); ra != rb {
			return ra < rb
		}
		if ta, tb := a.Last(), b.Last(); !ta.Equal(tb) {
			return ta.After(tb)
		}
		return a.ID < b.ID
	})
}

// AgentFilter narrows the list. Empty fields match everything; Query matches the task, repo,
// branch, harness name and id (every word, case-insensitive).
type AgentFilter struct {
	Harness string
	State   AgentState
	Repo    string
	Query   string
}

// Match reports whether a passes the filter.
func (f AgentFilter) Match(a Agent) bool {
	if f.Harness != "" && !strings.EqualFold(f.Harness, a.Harness) && !strings.EqualFold(f.Harness, a.Title()) {
		return false
	}
	if f.State != "" && f.State != a.State {
		return false
	}
	if f.Repo != "" && !strings.EqualFold(f.Repo, a.Repo) {
		return false
	}
	hay := strings.ToLower(strings.Join([]string{a.ID, a.Title(), a.Harness, a.Repo, a.Branch, a.Task, a.Model, a.Cwd}, " "))
	for _, w := range strings.Fields(strings.ToLower(f.Query)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// FilterAgents returns the agents that pass f, in order.
func FilterAgents(l []Agent, f AgentFilter) []Agent {
	out := make([]Agent, 0, len(l))
	for _, a := range l {
		if f.Match(a) {
			out = append(out, a)
		}
	}
	return out
}

// Counts is the number of agents per state.
func (r AgentsReport) Counts() map[AgentState]int {
	m := map[AgentState]int{}
	for _, a := range r.Agents {
		m[a.State]++
	}
	return m
}

// Summarize is the one-line summary shown in the overview.
func (r AgentsReport) Summarize() string {
	c := r.Counts()
	return fmt.Sprintf("%d agents: %d running, %d waiting, %d idle", len(r.Agents), c[AgentRunning], c[AgentWaiting], c[AgentIdle])
}

// Line is the one-line description every front-end shows.
func (a Agent) Line() string {
	s := fmt.Sprintf("%s  %s  %s", a.Title(), a.State, orDash(a.Repo))
	if a.Branch != "" {
		s += "@" + a.Branch
	}
	if a.Task != "" {
		s += "  " + a.Task
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// AgentFeedDeadline bounds each feed of one read, so a slow one costs its own answer and no more.
var AgentFeedDeadline = 10 * time.Second

// ReadAgents merges every ready feed into one report. Feeds run side by side. A feed that fails
// or answers something unreadable becomes a note (what it did return still counts). With no feed
// at all the domain is not connected.
func ReadAgents(ctx context.Context, entries []Entry) AgentsReport {
	type answer struct {
		name  string
		list  []Agent
		extra AgentsExtra
		err   error
	}
	var chans []chan answer
	var names []string
	for _, e := range entries {
		f, ok := e.Module.(AgentFeed)
		if !ok || e.Detection.State != Ready {
			continue
		}
		ch := make(chan answer, 1)
		chans, names = append(chans, ch), append(names, e.Module.Name())
		go func() {
			fctx, cancel := context.WithTimeout(ctx, AgentFeedDeadline)
			defer cancel()
			l, x, err := f.Agents(fctx)
			ch <- answer{e.Module.Name(), l, x, err}
		}()
	}
	var r AgentsReport
	if len(chans) == 0 {
		r.Header = notConnected(DomainAgents, "")
		r.Agents = []Agent{}
		return r
	}
	var lists [][]Agent
	for i, ch := range chans {
		var a answer
		select {
		case a = <-ch:
		case <-time.After(AgentFeedDeadline + 2*time.Second): // a feed that ignores its deadline
			a = answer{name: names[i], err: errors.New("timed out")}
		}
		if a.err != nil {
			r.Notes = append(r.Notes, CleanAgentText(a.err.Error(), 200))
		}
		lists = append(lists, a.list)
		r.RateLimits = append(r.RateLimits, a.extra.RateLimits...)
		r.OrphanPorts = append(r.OrphanPorts, a.extra.OrphanPorts...)
		if r.TokenRate == nil {
			r.TokenRate = a.extra.TokenRate
		}
	}
	r.Agents = MergeAgents(lists...)
	r.Header = finish(DomainAgents, Header{Source: SourceLive}, nil)
	r.Header.Summary = r.Summarize()
	return r
}
