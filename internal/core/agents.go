package core

import (
	"context"
	"sort"
	"strings"
	"time"
)

// The Agents view answers "what is the fleet doing?" from an optional capability next to the
// workspace one: a terminal workspace server may know which panes run an agent and what status
// each agent last reported (state, kind, progress, message). Core defines the typed report, the
// ordering and the cleaning; an adapter fills it in; a front-end shows it and opens a pane only
// through the existing workspace open plan on an explicit keypress. Nothing here runs a command.
//
// It is called "agents", never "board": Board is the agents' message board (BoardReport).

// AgentSchemaVersion is the schema_version of AgentReport's JSON. Changes are additive.
const AgentSchemaVersion = 1

// AgentState is what an agent last said it was doing. A state this build does not know reads as idle.
type AgentState string

const (
	AgentBlocked AgentState = "blocked" // needs a human (see AgentKind)
	AgentError   AgentState = "error"
	AgentDone    AgentState = "done" // a result is waiting; the server has no "seen" flag, so never "unseen"
	AgentWorking AgentState = "working"
	AgentIdle    AgentState = "idle"
)

// Known reports whether the state is one this build recognizes.
func (s AgentState) Known() bool {
	switch s {
	case AgentBlocked, AgentError, AgentDone, AgentWorking, AgentIdle:
		return true
	}
	return false
}

// AgentKind says why an agent is blocked. A kind this build does not know reads as none.
type AgentKind string

const (
	AgentKindNone       AgentKind = ""
	AgentKindPermission AgentKind = "permission"
	AgentKindAuth       AgentKind = "auth"
	AgentKindQuestion   AgentKind = "question"
)

// AgentStaleAfter is how long a working or blocked report may be the newest before the view marks
// it stale. The protocol has no heartbeat, so "working" only ever means "last report".
const AgentStaleAfter = 10 * time.Minute

// Bounds on what a provider may hand a front-end.
const (
	maxAgentPanes   = 256
	maxAgentRecords = 32
	maxAgentMsg     = 200
	maxAgentName    = 32
	maxAgentPath    = 256
	// agentReadTime is the whole budget for one Agents sweep. It is its own: the sweep makes about
	// two calls per pane (measured about 35 ms each), far more than a workspace read.
	agentReadTime = 4 * time.Second
)

// AgentRecord is one status record a program reported in a pane. Every text field is program
// output and untrusted. Progress is -1 when none was reported.
type AgentRecord struct {
	ID        string
	State     AgentState
	Kind      AgentKind
	App       string
	Title     string
	Msg       string
	Progress  int
	UpdatedAt time.Time
}

// AgentPane is one terminal pane that runs an agent, with the status it last reported.
type AgentPane struct {
	Host         string     `json:"host,omitempty"`
	Session      string     `json:"session"`
	SessionLabel string     `json:"session_label,omitempty"`
	Block        string     `json:"block"`
	Agent        string     `json:"agent,omitempty"`
	State        AgentState `json:"state"`
	Kind         AgentKind  `json:"kind,omitempty"`
	// Progress is 0 to 100 when the agent reported one.
	Progress *int   `json:"progress,omitempty"`
	Msg      string `json:"msg,omitempty"`
	Title    string `json:"title,omitempty"`
	RecordID string `json:"record_id,omitempty"`
	// NRecords is how many status records the pane holds; the row shows the most severe one.
	NRecords  int       `json:"records,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Reported is false for a pane known to run an agent only by its foreground process: it has
	// published no status at all.
	Reported bool `json:"reported"`
	// Process is the foreground process name and its directory; Repo and Branch are read from the
	// directory (local panes only) without running git.
	Process string `json:"process,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
	// Item is the one work item the pane names (task, lease, queue or run); Candidates are the items
	// when it names more than one, in which case Item is nil: core never picks one.
	Item       *WorkspaceItem  `json:"item,omitempty"`
	Candidates []WorkspaceItem `json:"candidates,omitempty"`
	Match      PlacementMatch  `json:"match,omitempty"`
	// Open is how to get to the pane, built from the existing workspace open plan.
	Open *OpenPlan `json:"open,omitempty"`
}

// IsStale reports a working or blocked pane whose newest report is older than AgentStaleAfter.
func (p AgentPane) IsStale(now time.Time) bool {
	if p.State != AgentWorking && p.State != AgentBlocked || p.UpdatedAt.IsZero() {
		return false
	}
	return now.Sub(p.UpdatedAt) > AgentStaleAfter
}

// Waiting is how long the pane has been in its newest report; false when it is unknown or the
// clock says the report is in the future (a remote server's clock may be ahead).
func (p AgentPane) Waiting(now time.Time) (time.Duration, bool) {
	if p.UpdatedAt.IsZero() {
		return 0, false
	}
	d := now.Sub(p.UpdatedAt)
	return d, d >= 0
}

// AgentReport is what a provider found. State says whether a workspace was reachable; a workspace
// that is simply not running is NotConnected with how to start it, never an error.
type AgentReport struct {
	SchemaVersion int         `json:"schema_version"`
	State         Connection  `json:"state,omitempty"`
	Reason        string      `json:"reason,omitempty"`
	HowToConnect  string      `json:"how_to_connect,omitempty"`
	Panes         []AgentPane `json:"panes,omitempty"`
	// Terminals is how many terminal panes were looked at, agents or not, so "no agents" can say
	// how many terminals there were.
	Terminals int `json:"terminals,omitempty"`
	// Stale says part of this came from a recent cached read. Truncated says something was cut:
	// more sessions, panes or records than are looked at, or a pane dropped as unsafe.
	Stale     bool `json:"stale,omitempty"`
	Truncated bool `json:"truncated,omitempty"`
}

// Empty reports whether there is nothing to show: no panes, no degraded read and nothing cut.
func (r AgentReport) Empty() bool {
	return len(r.Panes) == 0 && r.State != Degraded && !r.Truncated
}

// AgentProvider is the optional capability a module implements. Agents reads the panes that run an
// agent, matching what they name against the given work items, and Open builds the plan for one
// pane's placement without running anything (the same plan the workspace layer uses).
type AgentProvider interface {
	Agents(ctx context.Context, items []WorkspaceItem) (AgentReport, error)
	Open(p Placement) OpenPlan
}

// agentRank orders what needs a human first: blocked (permission, then auth, then question, then
// any other), then error, done, working, idle, and last a pane that runs an agent but reported
// nothing.
func agentRank(p AgentPane) int {
	switch p.State {
	case AgentBlocked:
		switch p.Kind {
		case AgentKindPermission:
			return 0
		case AgentKindAuth:
			return 1
		case AgentKindQuestion:
			return 2
		}
		return 3
	case AgentError:
		return 4
	case AgentDone:
		return 5
	case AgentWorking:
		return 6
	}
	if !p.Reported {
		return 8
	}
	return 7
}

// SortAgents orders panes for attention: by rank, then the longest waiting first (the oldest
// report), then a fixed tie-break, so the order never depends on read order.
func SortAgents(panes []AgentPane) {
	sort.SliceStable(panes, func(i, j int) bool {
		a, b := panes[i], panes[j]
		if ra, rb := agentRank(a), agentRank(b); ra != rb {
			return ra < rb
		}
		switch az, bz := a.UpdatedAt.IsZero(), b.UpdatedAt.IsZero(); {
		case az != bz:
			return bz // a pane with a time sorts before one without
		case !a.UpdatedAt.Equal(b.UpdatedAt):
			return a.UpdatedAt.Before(b.UpdatedAt)
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Block < b.Block
	})
}

// recordRank is agentRank for a record.
func recordRank(r AgentRecord) int {
	return agentRank(AgentPane{State: r.State, Kind: r.Kind, Reported: true})
}

// CollapseRecords reduces a pane's records to the one that matters: the most severe, the most
// recently updated on a tie, with the count of records looked at (at most maxAgentRecords, most
// severe first; the rest are not counted). ok is false with no records.
func CollapseRecords(rs []AgentRecord) (top AgentRecord, n int, ok bool) {
	if len(rs) == 0 {
		return AgentRecord{}, 0, false
	}
	cp := make([]AgentRecord, len(rs))
	for i, r := range rs {
		if !r.State.Known() {
			r.State = AgentIdle
		}
		cp[i] = r
	}
	sort.SliceStable(cp, func(i, j int) bool {
		if a, b := recordRank(cp[i]), recordRank(cp[j]); a != b {
			return a < b
		}
		return cp[i].UpdatedAt.After(cp[j].UpdatedAt)
	})
	if len(cp) > maxAgentRecords {
		cp = cp[:maxAgentRecords]
	}
	return cp[0], len(cp), true
}

// NamedItems says which work item a pane is about, from the texts it carries (session label, pane
// label, status title and message, working directory name). Each id is looked at once, and only if
// the item lives on the pane's machine (an item with no machine matches any). One item is a match;
// two or more are candidates, in item order, and no pick is made; none is neither. The match is
// exact when a text is exactly a label convention for the item, else by label.
func NamedItems(texts []string, items []WorkspaceItem, paneHost, localHost string, conventions []string) (item *WorkspaceItem, candidates []WorkspaceItem, match PlacementMatch) {
	if paneHost == "" {
		paneHost = localHost
	}
	seen := map[string]bool{}
	var found []WorkspaceItem
	best := PlacementMatch("")
	for _, it := range items {
		if seen[it.ID] || it.ID == "" || it.Host != "" && !strings.EqualFold(it.Host, paneHost) {
			continue
		}
		for _, t := range texts {
			m, ok := MatchSessionLabel(t, it.ID, conventions)
			if !ok {
				continue
			}
			seen[it.ID] = true
			found = append(found, it)
			if best != MatchExact {
				best = m
			}
			break
		}
	}
	switch len(found) {
	case 0:
		return nil, nil, ""
	case 1:
		return &found[0], nil, best
	}
	return nil, found, MatchLabel
}

func cleanAgentPane(p AgentPane) (AgentPane, bool) {
	host, ok := cleanHost(p.Host)
	if !ok {
		return AgentPane{}, false
	}
	out := AgentPane{
		Host:         host,
		Session:      ClipRunes(Sanitize(p.Session), MaxIDRunes),
		SessionLabel: ClipRunes(Sanitize(p.SessionLabel), MaxLabelRunes),
		Block:        ClipRunes(Sanitize(p.Block), MaxIDRunes),
		Agent:        ClipRunes(Sanitize(p.Agent), maxAgentName),
		State:        p.State,
		Kind:         p.Kind,
		Msg:          ClipRunes(Sanitize(p.Msg), maxAgentMsg),
		Title:        ClipRunes(Sanitize(p.Title), MaxLabelRunes),
		RecordID:     ClipRunes(Sanitize(p.RecordID), MaxLabelRunes),
		NRecords:     min(max(p.NRecords, 0), 256),
		Reported:     p.Reported,
		Process:      ClipRunes(Sanitize(p.Process), maxAgentName),
		Cwd:          ClipRunes(Sanitize(p.Cwd), maxAgentPath),
		Repo:         ClipRunes(Sanitize(p.Repo), MaxLabelRunes),
		Branch:       ClipRunes(Sanitize(p.Branch), MaxLabelRunes),
		Match:        PlacementMatch(ClipRunes(Sanitize(string(p.Match)), MaxIDRunes)),
	}
	if !out.State.Known() {
		out.State = AgentIdle
	}
	switch p.Kind {
	case AgentKindPermission, AgentKindAuth, AgentKindQuestion:
	default:
		out.Kind = AgentKindNone
	}
	if out.State != AgentBlocked {
		out.Kind = AgentKindNone // a kind only means something on a blocked report
	}
	if p.Progress != nil {
		v := min(max(*p.Progress, 0), 100)
		out.Progress = &v
	}
	// The report time is the server's clock; it is kept as given (the view hides a negative wait).
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = p.UpdatedAt.UTC()
	}
	if p.Item != nil {
		if it, ok := cleanItem(*p.Item); ok {
			out.Item = &it
		}
	}
	for _, it := range p.Candidates {
		if len(out.Candidates) >= maxCandidates {
			break
		}
		if c, ok := cleanItem(it); ok {
			out.Candidates = append(out.Candidates, c)
		}
	}
	if len(out.Candidates) > 0 {
		out.Item = nil
	}
	return out, true
}

// cleanAgents makes a provider's report safe and bounded, attaches each pane's open plan (from
// open, which may be nil) and orders the panes for attention. Whatever is cut or dropped sets
// Truncated.
func cleanAgents(r AgentReport, open func(Placement) OpenPlan) AgentReport {
	out := AgentReport{SchemaVersion: AgentSchemaVersion, State: r.State, Reason: ClipRunes(Sanitize(r.Reason), maxReasonRunes),
		HowToConnect: ClipRunes(Sanitize(r.HowToConnect), maxHowRunes), Terminals: max(r.Terminals, 0), Stale: r.Stale, Truncated: r.Truncated}
	for _, p := range r.Panes {
		if len(out.Panes) >= maxAgentPanes {
			out.Truncated = true
			break
		}
		c, ok := cleanAgentPane(p)
		if !ok {
			out.Truncated = true
			continue
		}
		if open != nil {
			pl := Placement{Host: c.Host, Session: c.Session, Label: c.SessionLabel, Match: c.Match}
			if c.Item != nil {
				pl.Item = *c.Item
			}
			plan := cleanPlan(open(pl))
			c.Open = &plan
		}
		out.Panes = append(out.Panes, c)
	}
	SortAgents(out.Panes)
	return out
}

// ReadAgents asks the first ready agent provider what the panes are doing. With no provider the
// result is empty and there is no error; a provider that fails is a Degraded report with the
// reason, never an error, so the cockpit never breaks over an optional layer.
func ReadAgents(ctx context.Context, entries []Entry, items []WorkspaceItem) AgentReport {
	p, _, ok := pick[AgentProvider](entries)
	if !ok {
		return AgentReport{}
	}
	return ReadAgentsFrom(ctx, p, items)
}

// ReadAgentsFrom is ReadAgents for a provider already chosen (a nil one reads as empty). The read
// has its own budget, agentReadTime.
func ReadAgentsFrom(ctx context.Context, p AgentProvider, items []WorkspaceItem) AgentReport {
	if p == nil {
		return AgentReport{}
	}
	if len(items) > maxWorkspaceItems {
		items = items[:maxWorkspaceItems]
	}
	ctx, cancel := context.WithTimeout(ctx, agentReadTime)
	defer cancel()
	rep, err := p.Agents(ctx, items)
	if err != nil {
		return AgentReport{SchemaVersion: AgentSchemaVersion, State: Degraded, Reason: ClipRunes(Sanitize(err.Error()), 200)}
	}
	return cleanAgents(rep, p.Open)
}
