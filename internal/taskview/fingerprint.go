package taskview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The live refresh asks two cheap questions before it pays for a full read: has the tracker's own
// fingerprint moved (Changed), and, once a new snapshot is in, which tasks differ from the last one
// (Diff). Both are pure.

// Changed reports whether a fingerprint read now means the tracker changed since the one kept from
// the last full read. An empty cur (no fingerprint could be read) never counts as a change: the
// caller falls back to its slow poll instead of reading on a guess. An empty prev (nothing read yet)
// is a change.
func Changed(prev, cur string) bool { return cur != "" && cur != prev }

// IssueHash is a digest of everything the snapshot holds about one issue, so two reads of an
// unchanged issue hash alike and any edited field (including a comment or a dependency) does not.
func IssueHash(i core.TaskIssue) string {
	deps := slices.Clone(i.Deps)
	slices.SortFunc(deps, func(a, b core.TaskDep) int {
		return strings.Compare(a.Type+"\x00"+a.DependsOn, b.Type+"\x00"+b.DependsOn)
	})
	labels := slices.Clone(i.Labels)
	slices.Sort(labels)
	i.Labels, i.Deps = labels, deps
	b, _ := json.Marshal(i)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// Change is what differs between two snapshots of one tracker.
type Change struct {
	Added, Removed, Changed []string // task ids, sorted
	// Newest is the added or changed task with the latest update time (ties: the smaller id): where
	// `F` takes the cursor. Empty when nothing was added or changed.
	Newest string
}

// Empty is whether the two snapshots held the same tasks.
func (c Change) Empty() bool { return len(c.Added)+len(c.Removed)+len(c.Changed) == 0 }

// Touched is the ids that exist in the new snapshot and differ from the old.
func (c Change) Touched() []string { return append(slices.Clone(c.Added), c.Changed...) }

// Diff compares two snapshots by issue id and content hash.
func Diff(old, cur core.TaskSnapshot) Change {
	was := make(map[string]string, len(old.Issues))
	for _, i := range old.Issues {
		was[i.ID] = IssueHash(i)
	}
	var c Change
	var newest core.TaskIssue
	seen := make(map[string]bool, len(cur.Issues))
	for _, i := range cur.Issues {
		seen[i.ID] = true
		h, existed := was[i.ID]
		switch {
		case !existed:
			c.Added = append(c.Added, i.ID)
		case h != IssueHash(i):
			c.Changed = append(c.Changed, i.ID)
		default:
			continue
		}
		if newest.ID == "" || i.UpdatedAt.After(newest.UpdatedAt) || (i.UpdatedAt.Equal(newest.UpdatedAt) && i.ID < newest.ID) {
			newest = i
		}
	}
	for id := range was {
		if !seen[id] {
			c.Removed = append(c.Removed, id)
		}
	}
	slices.Sort(c.Added)
	slices.Sort(c.Changed)
	slices.Sort(c.Removed)
	c.Newest = newest.ID
	return c
}
