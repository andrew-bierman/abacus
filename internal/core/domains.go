package core

import (
	"context"
	"errors"
	"fmt"
)

// DomainID names what the user manages. Providers (the tools) are invisible behind it.
type DomainID string

const (
	DomainBoard  DomainID = "board"
	DomainTasks  DomainID = "tasks"
	DomainLeases DomainID = "leases"
	DomainQueues DomainID = "queues"
)

// DomainOrder is the navigation order.
var DomainOrder = []DomainID{DomainBoard, DomainTasks, DomainLeases, DomainQueues}

// Title is the user-facing name of a domain.
func (d DomainID) Title() string {
	switch d {
	case DomainBoard:
		return "Board"
	case DomainTasks:
		return "Tasks"
	case DomainLeases:
		return "Leases"
	case DomainQueues:
		return "Queues"
	}
	return string(d)
}

// Connection is whether a domain has a provider that can be read.
type Connection string

const (
	Connected    Connection = "connected"
	NotConnected Connection = "not-connected"
	// Degraded is a read from a reduced source, or a ready provider whose read failed.
	Degraded Connection = "degraded"
)

// Provider-neutral source labels: never a tool name.
const (
	SourceLive     = "live"
	SourceTracker  = "tracker subset"
	SourceSnapshot = "snapshot"
	SourceGraph    = "graph analysis"
)

// Header is common to every domain report.
type Header struct {
	Domain       DomainID   `json:"domain"`
	Title        string     `json:"title"`
	State        Connection `json:"state"`
	Source       string     `json:"source,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	HowToConnect string     `json:"how_to_connect,omitempty"`
	// Setup is set when State is needs-setup: the guided step that fixes it. Additive.
	Setup *Guide `json:"setup,omitempty"`
}

// TaskCounts are the compact Tasks summary (not an issue browser).
type TaskCounts struct {
	Total      int `json:"total"`
	Open       int `json:"open"`
	InProgress int `json:"in_progress"`
	Blocked    int `json:"blocked"`
	Deferred   int `json:"deferred"`
	Closed     int `json:"closed"`
	Ready      int `json:"ready"`
}

// Task is one recently changed issue.
type Task struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Priority  int    `json:"priority"`
	Type      string `json:"type,omitempty"`
	Assignee  string `json:"assignee,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type TasksReport struct {
	Header
	Counts   TaskCounts      `json:"counts"`
	Recent   []Task          `json:"recent,omitempty"`
	Insights *InsightsReport `json:"insights,omitempty"`
}

// Ranked is an issue with a metric value (bottleneck, critical path).
type Ranked struct {
	ID    string  `json:"id"`
	Value float64 `json:"value"`
}

// Pick is a recommended next task or a blocker worth clearing.
type Pick struct {
	ID       string   `json:"id"`
	Title    string   `json:"title,omitempty"`
	Score    float64  `json:"score,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
	Unblocks int      `json:"unblocks,omitempty"`
}

// Track is a group of actionable work that can proceed independently.
type Track struct {
	ID    string   `json:"id"`
	Items []string `json:"items,omitempty"`
}

// InsightsReport is provider-neutral graph analysis of the task dependency graph: what to
// work on next, what blocks the most, the critical path and dependency cycles. Any section a
// provider cannot supply is simply empty; Header.Reason says which were unavailable.
type InsightsReport struct {
	Header
	TopPicks        []Pick     `json:"top_picks,omitempty"`
	Blockers        []Pick     `json:"blockers,omitempty"`
	CriticalPath    []Ranked   `json:"critical_path,omitempty"`
	Bottlenecks     []Ranked   `json:"bottlenecks,omitempty"`
	Cycles          [][]string `json:"cycles,omitempty"`
	Tracks          []Track    `json:"tracks,omitempty"`
	TotalActionable int        `json:"total_actionable,omitempty"`
	TotalBlocked    int        `json:"total_blocked,omitempty"`
	// CriticalPathNote says why the critical path is empty when the provider skipped it (for
	// example a dependency cycle), so an empty list is not read as "no critical path".
	CriticalPathNote string `json:"critical_path_note,omitempty"`
	// Empty is set when the tracker has no tasks, so there is nothing to analyze.
	Empty      bool `json:"empty,omitempty"`
	GraphNodes int  `json:"graph_nodes,omitempty"`
	GraphEdges int  `json:"graph_edges,omitempty"`
}

// TaskReader re-reads one task, so a confirm screen can check the issue has not changed since
// the user opened it.
type TaskReader interface {
	ReadTask(ctx context.Context, id string) (Task, error)
}

// InsightsProvider supplies graph analysis for the Tasks and Board domains. It is a separate
// provider because it reads a different surface than the task counts do.
type InsightsProvider interface {
	Insights(context.Context) (InsightsReport, error)
}

// HowToConnectInsights is shown when no graph-analysis provider is available.
const HowToConnectInsights = "Graph insights need bv. It comes with the abacus download (keep the whole folder together), or install it: brew install dicklesworthstone/tap/bv."

// InsightsUnavailable is the plain one-liner for a bv read that failed on a tracker that has
// tasks. The actual reason is kept for Doctor.
const InsightsUnavailable = "Graph insights could not be read just now. Details are in Doctor."

// EmptyInsights is the Insights state of a tracker with no tasks: nothing to analyze, which is
// not a failure and does not run bv at all.
func EmptyInsights() InsightsReport {
	return InsightsReport{Header: Header{Domain: DomainTasks, Title: "Insights", State: Connected, Summary: "no tasks yet"}, Empty: true}
}

// LeaseState is a lease's state; a provider may pass through an unknown one.
type LeaseState string

const (
	LeaseHeld  LeaseState = "held"
	LeaseStale LeaseState = "stale"
	LeaseFree  LeaseState = "free"
)

type Lease struct {
	Resource  string     `json:"resource"`
	State     LeaseState `json:"state"`
	Holder    string     `json:"holder,omitempty"`
	ExpiresAt string     `json:"expires_at,omitempty"`
}

type LeasesReport struct {
	Header
	Leases []Lease `json:"leases"`
}

type QueueRun struct {
	Issue   string `json:"issue"`
	State   string `json:"state,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Machine string `json:"machine,omitempty"`
	Stalled bool   `json:"stalled,omitempty"`
}

type QueueItem struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Blocked bool   `json:"blocked,omitempty"`
}

type Attention struct {
	Issue  string `json:"issue"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type HistoryItem struct {
	Issue     string `json:"issue"`
	Title     string `json:"title,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type QueuesReport struct {
	Header
	Runs      []QueueRun    `json:"runs,omitempty"`
	Queue     []QueueItem   `json:"queue,omitempty"`
	Attention []Attention   `json:"attention,omitempty"`
	History   []HistoryItem `json:"history,omitempty"`
}

// BoardChannel is a place on the board where threads are posted.
type BoardChannel struct {
	Path     string `json:"path"`
	Threads  int    `json:"threads,omitempty"`
	Messages int    `json:"messages,omitempty"`
}

// BoardMessage is one post: who wrote it, when, and what it says.
type BoardMessage struct {
	ID      string `json:"id"`
	Channel string `json:"channel,omitempty"`
	Author  string `json:"author,omitempty"`
	At      string `json:"at,omitempty"`
	Text    string `json:"text,omitempty"`
}

// BoardThread is a root message with its reply count and its latest reply.
type BoardThread struct {
	Root    BoardMessage  `json:"root"`
	Replies int           `json:"replies"`
	Last    *BoardMessage `json:"last,omitempty"`
}

// BoardTask is a thread agents are to do: a state the board keeps, and who holds it.
type BoardTask struct {
	ID       string `json:"id"`
	Channel  string `json:"channel,omitempty"`
	Text     string `json:"text,omitempty"`
	State    string `json:"state"`
	Claimant string `json:"claimant,omitempty"`
	Until    string `json:"until,omitempty"`
}

// BoardReport is the Board domain: a message board for agents. It is bounded by the provider
// (a few channels, a few recent threads, one page of open tasks), not a full mirror.
type BoardReport struct {
	Header
	Channels []BoardChannel `json:"channels,omitempty"`
	Threads  []BoardThread  `json:"threads,omitempty"`
	Tasks    []BoardTask    `json:"tasks,omitempty"`
}

// DomainsReport is the additive `domains` section of `status --json`.
type DomainsReport struct {
	Board  BoardReport  `json:"board"`
	Tasks  TasksReport  `json:"tasks"`
	Leases LeasesReport `json:"leases"`
	Queues QueuesReport `json:"queues"`
}

// Providers. A module opts into a domain by implementing the interface; the provider fills
// the body and Header.Source, core fills the rest of the Header. A future provider (for
// example a graph-insights one) plugs in the same way.
type (
	TaskProvider interface {
		Tasks(context.Context) (TasksReport, error)
	}
	LeaseProvider interface {
		Leases(context.Context) (LeasesReport, error)
	}
	QueueProvider interface {
		Queues(context.Context) (QueuesReport, error)
	}
	BoardProvider interface {
		Board(context.Context) (BoardReport, error)
	}
)

// HowToConnect is the fix message shown for a domain with no usable provider. It may name
// the provider command because it helps the user fix a provider.
var HowToConnect = map[DomainID]string{
	DomainBoard:  HowToConnectBoard,
	DomainTasks:  "The Beads CLI (bd) was not found. It comes with the abacus download (keep the whole folder together), or install it and put it on your PATH.",
	DomainLeases: "Leases need beads-lease, which comes with the abacus download (keep the whole folder together), or install it and put it on your PATH.",
	DomainQueues: "Queues need Lambe, which comes with the abacus download (keep the whole folder together), or install it and put it on your PATH.",
}

func header(d DomainID) Header { return Header{Domain: d, Title: d.Title()} }

// notConnected builds the header for a domain with no usable provider.
func notConnected(d DomainID, reason string) Header {
	h := header(d)
	h.State, h.Reason, h.HowToConnect = NotConnected, Sanitize(reason), HowToConnect[d]
	return h
}

// pick finds the provider for a domain. It returns the first ready module that implements
// it; otherwise the reason the best candidate is unusable ("" when there is none at all).
func pick[P any](entries []Entry) (p P, reason string, ok bool) {
	for _, e := range entries {
		prov, is := e.Module.(P)
		if !is {
			continue
		}
		switch e.Detection.State {
		case Ready:
			return prov, "", true
		case Absent:
		default:
			if reason == "" {
				reason = fmt.Sprintf("provider %s: %s", e.Detection.State, e.Detection.Detail)
			}
		}
	}
	return p, reason, false
}

// finish turns a provider read into a header. A reduced source, a failed read or a Reason
// (for example demo data, or a missing history) make the domain degraded.
func finish(d DomainID, h Header, err error) Header {
	if g, ok := AsNeedsSetup(err); ok {
		out := header(d)
		out.State, out.Reason, out.Setup = NeedsSetup, Sanitize(g.CLILine()), &g
		return out
	}
	var nc *NotConnectedError
	if errors.As(err, &nc) {
		out := header(d)
		out.State, out.Reason, out.HowToConnect = NotConnected, Sanitize(nc.Reason), Sanitize(nc.HowToConnect)
		return out
	}
	if err != nil {
		out := header(d)
		out.State, out.Reason, out.HowToConnect = Degraded, Sanitize(err.Error()), HowToConnect[d]
		return out
	}
	h.Domain, h.Title, h.State = d, d.Title(), Connected
	h.Source, h.Reason = Sanitize(h.Source), Sanitize(h.Reason)
	if (h.Source != "" && h.Source != SourceLive && h.Source != SourceSnapshot) || h.Reason != "" {
		h.State = Degraded
	}
	return h
}

// readDomain is the one read for every domain: find the provider, call it, shape the header.
// With no usable provider the body is empty and the header says not-connected.
func readDomain[P, B any](ctx context.Context, entries []Entry, d DomainID, call func(P, context.Context) (B, error), hdr func(*B) *Header, sum func(B) string) B {
	var body B
	p, why, ok := pick[P](entries)
	if !ok {
		*hdr(&body) = notConnected(d, why)
		return body
	}
	body, err := call(p, ctx)
	if err != nil {
		var empty B
		*hdr(&empty) = finish(d, Header{}, err)
		return empty
	}
	h := hdr(&body)
	*h = finish(d, *h, nil)
	h.Summary = sum(body)
	return body
}

func (r TasksReport) Summarize() string {
	c := r.Counts
	return fmt.Sprintf("%d tasks: %d open, %d in progress, %d blocked, %d ready", c.Total, c.Open, c.InProgress, c.Blocked, c.Ready)
}

func (r LeasesReport) Summarize() string {
	held, stale, free := r.counts()
	return fmt.Sprintf("%d held, %d stale, %d free", held, stale, free)
}

func (r QueuesReport) Summarize() string {
	if r.Source == SourceTracker { // the tracker subset has no queue to count
		return fmt.Sprintf("%d runs, %d need attention (%s, no live daemon)", len(r.Runs), len(r.Attention), SourceTracker)
	}
	return fmt.Sprintf("%d runs, %d queued, %d need attention", len(r.Runs), len(r.Queue), len(r.Attention))
}

func (r BoardReport) Summarize() string {
	return fmt.Sprintf("%d channels, %d recent threads, %d open tasks", len(r.Channels), len(r.Threads), len(r.Tasks))
}

func (r InsightsReport) Summarize() string {
	return fmt.Sprintf("%d actionable, %d blocked, %d cycles", r.TotalActionable, r.TotalBlocked, len(r.Cycles))
}

// Line is the one-line run summary every front-end shows.
func (r QueueRun) Line() string {
	orQ := func(s string) string {
		if s == "" {
			return "?"
		}
		return s
	}
	s := fmt.Sprintf("%s  %s  %s", r.Issue, orQ(r.State), orQ(r.Phase))
	if r.Stalled {
		s += "  (stalled)"
	}
	return s
}

func (r LeasesReport) counts() (held, stale, free int) {
	for _, l := range r.Leases {
		switch l.State {
		case LeaseHeld:
			held++
		case LeaseStale:
			stale++
		case LeaseFree:
			free++
		}
	}
	return
}

// Detail is the lease's one-line description, shared by every front-end.
func (l Lease) Detail() string {
	switch l.State {
	case LeaseHeld:
		exp := l.ExpiresAt
		if exp == "" {
			exp = "?"
		}
		return "by " + l.Holder + " (expires " + exp + ")"
	case LeaseStale:
		return "(last holder " + l.Holder + ")"
	}
	return ""
}

// Tone is the lease's severity.
func (l Lease) Tone() Tone {
	switch l.State {
	case LeaseHeld:
		return ToneInfo
	case LeaseStale:
		return ToneWarn
	case LeaseFree:
		return ToneOK
	}
	return ToneMuted
}

// Status shapes the report as a module Status: summary, typed rows and the plain lines. A live
// source is not a badge, so Source is set only for a reduced one.
func (r LeasesReport) Status() Status {
	st := Status{Summary: r.Summarize()}
	if r.Source != "" && r.Source != SourceLive {
		st.Source = r.Source
	}
	for _, l := range r.Leases {
		row := Row{Resource: l.Resource, State: string(l.State), Tone: l.Tone(), Detail: l.Detail()}
		line := row.Resource + "  " + row.State
		if row.Detail != "" {
			line += " " + row.Detail
		}
		st.Lines, st.Rows = append(st.Lines, line), append(st.Rows, row)
	}
	return st
}

func tasksHdr(b *TasksReport) *Header   { return &b.Header }
func leasesHdr(b *LeasesReport) *Header { return &b.Header }
func queuesHdr(b *QueuesReport) *Header { return &b.Header }
func boardHdr(b *BoardReport) *Header   { return &b.Header }

func (r *DomainsReport) readTasks(ctx context.Context, entries []Entry) {
	r.Tasks = readDomain(ctx, entries, DomainTasks, func(p TaskProvider, c context.Context) (TasksReport, error) { return p.Tasks(c) }, tasksHdr, TasksReport.Summarize)
}

func (r *DomainsReport) readLeases(ctx context.Context, entries []Entry) {
	r.Leases = readDomain(ctx, entries, DomainLeases, func(p LeaseProvider, c context.Context) (LeasesReport, error) { return p.Leases(c) }, leasesHdr, LeasesReport.Summarize)
	if r.Leases.Leases == nil { // [] not null, whatever the state
		r.Leases.Leases = []Lease{}
	}
}

func (r *DomainsReport) readQueues(ctx context.Context, entries []Entry) {
	r.Queues = readDomain(ctx, entries, DomainQueues, func(p QueueProvider, c context.Context) (QueuesReport, error) { return p.Queues(c) }, queuesHdr, QueuesReport.Summarize)
}

func (r *DomainsReport) readBoard(ctx context.Context, entries []Entry) {
	r.Board = readDomain(ctx, entries, DomainBoard, func(p BoardProvider, c context.Context) (BoardReport, error) { return p.Board(c) }, boardHdr, BoardReport.Summarize)
}

// ReadDomain reads one domain only (and its insights, for Tasks): a single-domain
// command must not run the other providers. The other domains are left empty.
func ReadDomain(ctx context.Context, entries []Entry, id DomainID) DomainsReport {
	var r DomainsReport
	r.Leases.Leases = []Lease{}
	switch id {
	case DomainTasks:
		r.readTasks(ctx, entries)
		ins := readInsightsFor(ctx, entries, r.Tasks)
		r.Tasks.Insights = &ins
	case DomainBoard:
		r.readBoard(ctx, entries)
	case DomainLeases:
		r.readLeases(ctx, entries)
	case DomainQueues:
		r.readQueues(ctx, entries)
	}
	return r
}

// ReadDomains reads every domain from the detected modules. A domain nobody provides is
// returned as not-connected with how to connect; it never disappears.
func ReadDomains(ctx context.Context, entries []Entry) DomainsReport {
	var r DomainsReport
	r.readTasks(ctx, entries)
	r.readLeases(ctx, entries)
	r.readQueues(ctx, entries)
	r.readBoard(ctx, entries)
	ins := readInsightsFor(ctx, entries, r.Tasks)
	r.Tasks.Insights = &ins
	return r
}

// readInsightsFor reads insights for a tracker already read as tasks: with no tasks it says
// "no tasks yet" and does not run bv.
func readInsightsFor(ctx context.Context, entries []Entry, tasks TasksReport) InsightsReport {
	if tasks.State == Connected && tasks.Counts.Total == 0 {
		return EmptyInsights()
	}
	p, why, _ := pick[InsightsProvider](entries)
	return ReadInsights(ctx, p, why)
}

// ReadInsights reads graph insights from p. A nil p is "not connected" (whyNot says why the
// provider is unusable, if it was found). A failed read is degraded; a partial one (Reason set)
// is degraded too; the header is always the Insights one so every front-end shows it the same.
func ReadInsights(ctx context.Context, p InsightsProvider, whyNot string) InsightsReport {
	h := Header{Domain: DomainTasks, Title: "Insights"}
	if p == nil {
		h.State, h.Reason, h.HowToConnect = NotConnected, Sanitize(whyNot), HowToConnectInsights
		return InsightsReport{Header: h}
	}
	body, err := p.Insights(ctx)
	if err != nil {
		h.State, h.Reason = Degraded, InsightsUnavailable
		return InsightsReport{Header: h}
	}
	body.Header.Domain, body.Header.Title = DomainTasks, "Insights"
	body.Header.Source, body.Header.Reason = Sanitize(body.Header.Source), Sanitize(body.Header.Reason)
	body.Header.State = Connected
	if body.Header.Reason != "" {
		body.Header.State = Degraded
	}
	body.Header.Summary = body.Summarize()
	return body
}

// Providers is what a front-end needs to drive each domain lazily: the provider for each
// domain (nil when none is usable) and, for the others, the not-connected header to show.
type Providers struct {
	Tasks  TaskProvider
	Leases LeaseProvider
	Queues QueueProvider
	Board  BoardProvider
	Writer TaskWriter // the module that can write tasks, if any
	// BoardWriter is the module that can post and reply to the board, if any.
	BoardWriter BoardWriter
	Reader      TaskReader // re-reads one task before a write is confirmed
	// Insights is the graph-analysis provider; nil means insights are not connected.
	Insights    InsightsProvider
	InsightsWhy string // why a found insights provider is unusable ("" when none was found)
	// Guiders are the ready providers that can say their domain needs setup.
	Guiders map[DomainID]Guider
	Missing map[DomainID]Header
}

// ResolveProviders maps detected modules to domains exactly as ReadDomains does.
func ResolveProviders(entries []Entry) Providers {
	p := Providers{Missing: map[DomainID]Header{}, Guiders: map[DomainID]Guider{}}
	for _, e := range ReadyOnly(entries) {
		if g, ok := e.Module.(Guider); ok {
			p.Guiders[g.Domain()] = g
		}
	}
	var why string
	var ok bool
	if p.Tasks, why, ok = pick[TaskProvider](entries); !ok {
		p.Missing[DomainTasks] = notConnected(DomainTasks, why)
	}
	if p.Leases, why, ok = pick[LeaseProvider](entries); !ok {
		p.Missing[DomainLeases] = notConnected(DomainLeases, why)
	}
	if p.Queues, why, ok = pick[QueueProvider](entries); !ok {
		p.Missing[DomainQueues] = notConnected(DomainQueues, why)
	}
	if p.Board, why, ok = pick[BoardProvider](entries); !ok {
		p.Missing[DomainBoard] = notConnected(DomainBoard, why)
	}
	p.Writer, _, _ = pick[TaskWriter](entries)
	p.BoardWriter, _, _ = pick[BoardWriter](entries)
	p.Reader, _, _ = pick[TaskReader](entries)
	p.Insights, p.InsightsWhy, _ = pick[InsightsProvider](entries)
	return p
}

// readFailed reports a header from a failed provider read (empty body, how-to-connect set), as
// opposed to a degraded read that still has data.
func (h Header) readFailed() bool { return h.State == Degraded && h.HowToConnect != "" }

// statusFromDomains is a provider module's Status taken from an already-read domain, so one
// `status` run reads each provider once. ok is false for a module that provides no domain.
func statusFromDomains(m Module, d DomainsReport) (Status, bool) {
	var h Header
	var st Status
	switch m.(type) {
	case TaskProvider:
		h, st = d.Tasks.Header, Status{Summary: d.Tasks.Summary}
	case LeaseProvider:
		h, st = d.Leases.Header, d.Leases.Status()
	case QueueProvider:
		h, st = d.Queues.Header, Status{Summary: d.Queues.Summary}
		for _, x := range d.Queues.Runs {
			st.Lines = append(st.Lines, x.Line())
		}
		if d.Queues.Source == SourceTracker {
			st.Source = SourceTracker
		}
	case InsightsProvider:
		if d.Tasks.Insights == nil {
			return Status{}, false
		}
		h, st = d.Tasks.Insights.Header, Status{Summary: d.Tasks.Insights.Summary, Source: SourceGraph}
	default:
		return Status{}, false
	}
	if h.State == NotConnected || h.State == NeedsSetup || h.readFailed() {
		return Status{Unavailable: h.Reason}, true
	}
	return st, true
}

// FleetStatusReport is the `status` report: the fleet domains, read once, plus each ready
// module's status taken from them (a module that provides no domain keeps its own Status).
func FleetStatusReport(ctx context.Context, version string, entries []Entry) Report {
	r := newReport(version)
	dom := ReadDomains(ctx, entries)
	r.Domains = &dom
	for _, e := range present(entries) {
		m := base(e)
		if e.Detection.State == Ready {
			st, ok := statusFromDomains(e.Module, dom)
			if !ok {
				st = e.Module.Status(ctx)
			}
			m.Status = &st
		}
		r.Modules = append(r.Modules, m)
	}
	return r
}
