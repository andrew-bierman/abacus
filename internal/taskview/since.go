package taskview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// RowState is what a client was last told about the tracker, kept small: one digest per task of everything it
// shows (the stored issue and the row derived from it, so a task freed by a closed blocker counts as changed,
// not only a task that was edited). It lets a later read say which rows differ without keeping the snapshot.
type RowState struct {
	hashes map[string]string
	ident  string // a digest of which tracker this is (its project directory): a state of one is never compared with another's
	dup    bool   // the snapshot held an id twice: by-id digests would hide a change, so no delta is made from it
}

// Ident is the tracker identity of the state; equal only for states of the same tracker.
func (s RowState) Ident() string { return s.ident }

// Duplicates reports a snapshot with a repeated task id (a state that cannot be compared by id).
func (s RowState) Duplicates() bool { return s.dup }

// State is the state of this view.
func (v *View) State() RowState {
	h := make(map[string]string, len(v.rows))
	dup := false
	for i, r := range v.rows {
		if _, seen := h[r.ID]; seen {
			dup = true
		}
		row := r.TaskRow
		row.Age = "" // the age moves with the clock, not with the task
		b, _ := json.Marshal(row)
		sum := sha256.Sum256(b)
		h[r.ID] = IssueHash(v.snap.Issues[i]) + hex.EncodeToString(sum[:6])
	}
	who := v.snap.Root
	if who == "" {
		who = v.snap.Tracker
	}
	id := sha256.Sum256([]byte(who))
	return RowState{h, hex.EncodeToString(id[:6]), dup}
}

// Fingerprint is an opaque token of the state: equal tokens mean nothing shown differs.
func (s RowState) Fingerprint() string {
	lines := make([]string, 0, len(s.hashes)+1)
	lines = append(lines, "tracker\x00"+s.ident)
	for id, h := range s.hashes {
		lines = append(lines, id+"\x00"+h)
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:16])
}

// Since is the rows of this view that are new or differ from prev, sorted by id, and the ids in prev that are
// no longer here. now is v.State(), passed in so a caller that has it does not compute it twice.
func (v *View) Since(prev, now RowState) (changed []core.TaskRow, removed []string) {
	changed, removed = []core.TaskRow{}, []string{}
	for _, r := range v.rows {
		if h, had := prev.hashes[r.ID]; !had || h != now.hashes[r.ID] {
			changed = append(changed, r.TaskRow)
		}
	}
	for id := range prev.hashes {
		if _, still := now.hashes[id]; !still {
			removed = append(removed, id)
		}
	}
	slices.SortFunc(changed, func(a, b core.TaskRow) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(removed)
	return changed, removed
}
