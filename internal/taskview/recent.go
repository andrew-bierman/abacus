package taskview

import (
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// FromRecent turns the compact summary's recent changes into a snapshot, for a provider that cannot
// read the whole tracker. Dependencies are unknown, so DepsUnknown is set and Build claims nothing
// is ready or blocked.
func FromRecent(rec []core.Task, at time.Time) core.TaskSnapshot {
	snap := core.TaskSnapshot{ReadAt: at, DepsUnknown: true}
	for _, t := range rec {
		it := core.TaskIssue{ID: t.ID, Title: t.Title, Status: t.Status, Priority: t.Priority, Type: t.Type, Assignee: t.Assignee}
		if ts, err := time.Parse(time.RFC3339, t.UpdatedAt); err == nil {
			it.UpdatedAt, it.CreatedAt = ts, ts
		}
		snap.Issues = append(snap.Issues, it)
	}
	return snap
}
