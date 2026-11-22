package core

import (
	"context"
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
	Counts TaskCounts `json:"counts"`
	Recent []Task     `json:"recent,omitempty"`
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

type BoardCard struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

type BoardColumn struct {
	Name  string      `json:"name"`
	Cards []BoardCard `json:"cards,omitempty"`
}

type BoardReport struct {
	Header
	Columns []BoardColumn `json:"columns,omitempty"`
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
	DomainBoard:  "No Board provider is available yet: the Board contract is an open decision (board-beads issue 15). For epic lanes today, open the board in b9s with o.",
	DomainTasks:  "Install the Beads CLI (bd) and run abacus inside a tracker directory, or set ABACUS_BD_DIR to one.",
	DomainLeases: "Install beads-lease and set BEADS_ACTOR; the hub it reads comes from its own config.",
	DomainQueues: "Install and start the Lambe daemon with its state API on, or set ABACUS_LAMBE_STATE_DIR or pass --lambe-config.",
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

// finish turns a provider read into a header: reduced sources and failed reads are degraded.
func finish(d DomainID, h Header, err error) Header {
	if err != nil {
		out := header(d)
		out.State, out.Reason, out.HowToConnect = Degraded, Sanitize(err.Error()), HowToConnect[d]
		return out
	}
	h.Domain, h.Title, h.State = d, d.Title(), Connected
	h.Source, h.Reason = Sanitize(h.Source), Sanitize(h.Reason)
	if h.Source != "" && h.Source != SourceLive && h.Source != SourceSnapshot {
		h.State = Degraded
	}
	return h
}

// ReadDomains reads every domain from the detected modules. A domain nobody provides is
// returned as not-connected with how to connect; it never disappears.
func ReadDomains(ctx context.Context, entries []Entry) DomainsReport {
	var r DomainsReport
	if p, why, ok := pick[TaskProvider](entries); ok {
		body, err := p.Tasks(ctx)
		r.Tasks = body
		r.Tasks.Header = finish(DomainTasks, body.Header, err)
		if err == nil {
			c := body.Counts
			r.Tasks.Summary = fmt.Sprintf("%d tasks: %d open, %d in progress, %d blocked, %d ready", c.Total, c.Open, c.InProgress, c.Blocked, c.Ready)
		} else {
			r.Tasks = TasksReport{Header: r.Tasks.Header}
		}
	} else {
		r.Tasks.Header = notConnected(DomainTasks, why)
	}
	if p, why, ok := pick[LeaseProvider](entries); ok {
		body, err := p.Leases(ctx)
		r.Leases = body
		r.Leases.Header = finish(DomainLeases, body.Header, err)
		if err == nil {
			held, stale, free := 0, 0, 0
			for _, l := range body.Leases {
				switch l.State {
				case LeaseHeld:
					held++
				case LeaseStale:
					stale++
				case LeaseFree:
					free++
				}
			}
			r.Leases.Summary = fmt.Sprintf("%d held, %d stale, %d free", held, stale, free)
		} else {
			r.Leases = LeasesReport{Header: r.Leases.Header}
		}
	} else {
		r.Leases.Header = notConnected(DomainLeases, why)
	}
	if p, why, ok := pick[QueueProvider](entries); ok {
		body, err := p.Queues(ctx)
		r.Queues = body
		r.Queues.Header = finish(DomainQueues, body.Header, err)
		if err == nil {
			r.Queues.Summary = fmt.Sprintf("%d runs, %d queued, %d need attention", len(body.Runs), len(body.Queue), len(body.Attention))
		} else {
			r.Queues = QueuesReport{Header: r.Queues.Header}
		}
	} else {
		r.Queues.Header = notConnected(DomainQueues, why)
	}
	if p, why, ok := pick[BoardProvider](entries); ok {
		body, err := p.Board(ctx)
		r.Board = body
		r.Board.Header = finish(DomainBoard, body.Header, err)
		if err == nil {
			r.Board.Summary = fmt.Sprintf("%d columns", len(body.Columns))
		} else {
			r.Board = BoardReport{Header: r.Board.Header}
		}
	} else {
		r.Board.Header = notConnected(DomainBoard, why)
	}
	return r
}

// Providers is what a front-end needs to drive each domain lazily: the provider for each
// domain (nil when none is usable) and, for the others, the not-connected header to show.
type Providers struct {
	Tasks   TaskProvider
	Leases  LeaseProvider
	Queues  QueueProvider
	Board   BoardProvider
	Writer  TaskWriter // the module that can write tasks, if any
	Missing map[DomainID]Header
}

// ResolveProviders maps detected modules to domains exactly as ReadDomains does.
func ResolveProviders(entries []Entry) Providers {
	p := Providers{Missing: map[DomainID]Header{}}
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
	return p
}
