package core

import (
	"context"
	"errors"
	"time"
)

// History of one task: what changed and who changed it, newest first. It is a read of the
// tracker's own event log; the report is typed and sanitized so the TUI and the CLI print the same.

const (
	// HistoryDefaultLimit is how many events one read asks for; HistoryMaxLimit is the most a read may ask for.
	HistoryDefaultLimit = 50
	HistoryMaxLimit     = 200
)

// TaskHistoryEvent is one entry of a task's history. Kind is the tracker's event name (created,
// updated, status_changed, closed, ...) and Detail a short sanitized summary of what changed.
type TaskHistoryEvent struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor,omitempty"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
}

// TaskHistoryReport is the history of one task, schema_version 1 when printed as JSON by the CLI.
type TaskHistoryReport struct {
	ID     string             `json:"id"`
	Events []TaskHistoryEvent `json:"events"`
	// Truncated: the tracker holds more events than were asked for.
	Truncated bool      `json:"truncated,omitempty"`
	ReadAt    time.Time `json:"read_at"`
}

// TaskHistorySource reads the history of one task.
type TaskHistorySource interface {
	History(ctx context.Context, id string, limit int) (TaskHistoryReport, error)
}

// ErrNoFingerprint means the tracker has no cheap change fingerprint (it is not an embedded
// store, or its files are not where they are expected): the caller falls back to a slow poll.
var ErrNoFingerprint = errors.New("no cheap change fingerprint for this tracker")

// TaskFingerprinter says, without running the tracker tool, whether the tracker changed: two
// equal fingerprints mean no write happened in between. A read never changes it.
type TaskFingerprinter interface {
	Fingerprint(ctx context.Context) (string, error)
}

// TrackerSwitcher re-points Tasks at another tracker directory for the running session.
type TrackerSwitcher interface {
	SetTrackerDir(dir string) error
}
