package core

import (
	"context"
	"time"
)

// The Tasks workspace reads one snapshot of the tracker and derives every view from it (list,
// detail, and the views later units add). TaskSnapshot is that snapshot: typed, sanitized and
// provider-neutral. The pure functions that derive blocked and ready, sort, filter and build
// the reports below live in internal/taskview, which imports core and never the reverse.

// TaskDep is one dependency edge as the tracker stores it: this issue depends on DependsOn.
// Type "blocks" holds work back and "parent-child" makes DependsOn the parent; other types are
// carried through unchanged and never block.
type TaskDep struct {
	DependsOn string
	Type      string
}

// TaskComment is one comment on a task.
type TaskComment struct {
	Author    string
	Text      string
	CreatedAt time.Time
}

// TaskIssue is one issue of the snapshot. Every string was sanitized when the snapshot was
// read; Description, Design, Notes, Acceptance and comment text keep their line breaks.
type TaskIssue struct {
	ID          string
	Title       string
	Description string
	Design      string
	Notes       string
	Acceptance  string
	Status      string
	Priority    int
	Type        string
	Assignee    string
	Labels      []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ClosedAt    time.Time
	DeferUntil  time.Time
	CloseReason string
	Deps        []TaskDep
	Comments    []TaskComment
	// What the export itself said it holds, to notice a snapshot that lost records.
	ReportedDeps     int // blocking dependencies
	ReportedComments int
	HasReportedDeps  bool
}

// TaskSnapshot is the whole tracker as read at ReadAt.
type TaskSnapshot struct {
	Issues  []TaskIssue
	ReadAt  time.Time
	Tracker string // display name of the tracker (its directory name), "" when unknown
	// Warnings say why the snapshot may be incomplete (a record that lost its dependencies,
	// an unreadable time); empty for a clean read.
	Warnings []string
	// DepsUnknown marks a snapshot built without dependency data (the summary's recent changes):
	// nothing may be called ready or blocked from it.
	DepsUnknown bool
}

// TaskSource reads the tracker snapshot. It is separate from TaskProvider because it reads a
// different, richer surface than the compact counts do.
type TaskSource interface {
	Snapshot(ctx context.Context) (TaskSnapshot, error)
}

// TaskRow is one task in the list: its stored fields plus what is derived from the whole
// snapshot (KTD4). Blocked is derived (an unfinished blocking dependency) and is not the stored
// status "blocked"; Ready means open, not deferred and nothing unfinished in the way.
type TaskRow struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Priority   int      `json:"priority"`
	Type       string   `json:"type,omitempty"`
	Assignee   string   `json:"assignee,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	DeferUntil string   `json:"defer_until,omitempty"`
	Age        string   `json:"age,omitempty"`
	BlockedBy  int      `json:"blocked_by,omitempty"`
	Blocks     int      `json:"blocks,omitempty"`
	Blocked    bool     `json:"blocked,omitempty"`
	Ready      bool     `json:"ready,omitempty"`
	Parent     string   `json:"parent,omitempty"`
	// HeldBy is the ancestor whose unfinished blocker holds this task (the hold is inherited down
	// parent-child links, as bd ready does); Blocked is then true with BlockedBy 0.
	HeldBy     string `json:"held_by,omitempty"`
	ChildDone  int    `json:"child_done,omitempty"`
	ChildTotal int    `json:"child_total,omitempty"`
	Comments   int    `json:"comments,omitempty"`
}

// TaskListReport is the filtered, sorted task list. Matched of Total is what "N of M" shows.
type TaskListReport struct {
	Header
	Query      string    `json:"query,omitempty"` // the normalized query
	Preset     string    `json:"preset"`
	Sort       string    `json:"sort"`
	Descending bool      `json:"descending,omitempty"`
	Rows       []TaskRow `json:"rows"`
	Matched    int       `json:"matched"`
	Total      int       `json:"total"`
	Ready      int       `json:"ready"` // ready tasks in the whole tracker
	// DepsUnknown is set when the list is the summary's recent changes: dependencies were not read,
	// so no task is ready or blocked and Ready is 0.
	DepsUnknown bool     `json:"deps_unknown,omitempty"`
	Tracker     string   `json:"tracker,omitempty"`
	ReadAt      string   `json:"read_at,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// TaskRef is a task named from another task: a blocker, a dependent, a child or the parent.
// Status "missing" is a dependency on something that is not in this tracker.
type TaskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
}

// TaskCommentView is a comment as the detail shows it.
type TaskCommentView struct {
	Author    string `json:"author,omitempty"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at,omitempty"`
	Age       string `json:"age,omitempty"`
}

// TaskDetailReport is one task in full.
type TaskDetailReport struct {
	Header
	Row         TaskRow           `json:"row"`
	Description string            `json:"description,omitempty"`
	Design      string            `json:"design,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Acceptance  string            `json:"acceptance,omitempty"`
	CloseReason string            `json:"close_reason,omitempty"`
	ClosedAt    string            `json:"closed_at,omitempty"`
	Comments    []TaskCommentView `json:"comments,omitempty"`
	Parent      *TaskRef          `json:"parent,omitempty"`
	BlockedBy   []TaskRef         `json:"blocked_by,omitempty"`
	Blocks      []TaskRef         `json:"blocks,omitempty"`
	Children    []TaskRef         `json:"children,omitempty"`
}
