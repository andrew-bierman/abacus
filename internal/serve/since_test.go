package serve_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func since(t *testing.T, c *client, fp string) core.TasksSince {
	t.Helper()
	var out core.TasksSince
	c.call("tasks.since", map[string]any{"fingerprint": fp}).into(t, &out)
	return out
}

func rowIDs(rows []core.TaskRow) []string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// A client that names no fingerprint, or one the server never handed out, gets "full" and a baseline; naming
// the baseline back answers "unchanged" until a task moves.
func TestTasksSinceUnknownFingerprintIsFullThenDelta(t *testing.T) {
	c, f := readyClient(t)
	for _, fp := range []string{"", "nope", "0123456789abcdef0123456789abcdef"} {
		got := since(t, c, fp)
		if got.Kind != core.SinceFull || got.Fingerprint == "" || len(got.Rows) != 0 || len(got.Removed) != 0 || got.Total == 0 {
			t.Fatalf("fingerprint %q: %+v", fp, got)
		}
	}
	base := since(t, c, "")
	same := since(t, c, base.Fingerprint)
	if same.Kind != core.SinceDelta || same.Fingerprint != base.Fingerprint || len(same.Rows) != 0 || len(same.Removed) != 0 {
		t.Fatalf("unchanged: %+v", same)
	}
	// an edit, an addition and a removal
	f.tasks.mutate(func(s *core.TaskSnapshot) {
		s.Issues[0].Title += " (edited)"
		s.Issues = append(s.Issues, core.TaskIssue{ID: "ac-99", Title: "new", Status: "open", Priority: 2, CreatedAt: s.ReadAt, UpdatedAt: s.ReadAt})
		s.Issues = slices.DeleteFunc(s.Issues, func(i core.TaskIssue) bool { return i.ID == "ac-30" })
	})
	d := since(t, c, base.Fingerprint)
	if d.Kind != core.SinceDelta || d.Fingerprint == base.Fingerprint || !slices.Equal(d.Removed, []string{"ac-30"}) || d.Total != base.Total {
		t.Fatalf("delta: %+v", d)
	}
	if ids := rowIDs(d.Rows); len(ids) != 2 || !slices.Contains(ids, "ac-99") || !slices.IsSorted(ids) {
		t.Fatalf("changed rows: %v", ids)
	}
	// the old fingerprint still answers (it is remembered), and the new one is unchanged
	if again := since(t, c, base.Fingerprint); len(again.Rows) != 2 {
		t.Fatalf("again: %+v", again)
	}
	if now := since(t, c, d.Fingerprint); now.Kind != core.SinceDelta || len(now.Rows)+len(now.Removed) != 0 {
		t.Fatalf("settled: %+v", now)
	}
}

// A task that did not change but whose row did (its blocker closed, so it is ready now) is a changed row: the
// client shows the derived state.
func TestTasksSinceReportsRowsWhoseDerivedStateMoved(t *testing.T) {
	c, f := readyClient(t)
	base := since(t, c, "")
	f.tasks.mutate(func(s *core.TaskSnapshot) {
		for i := range s.Issues {
			if s.Issues[i].ID == "ac-15" { // the blocker of ac-16
				s.Issues[i].Status, s.Issues[i].ClosedAt = "closed", s.ReadAt
			}
		}
	})
	d := since(t, c, base.Fingerprint)
	ids := rowIDs(d.Rows)
	if !slices.Equal(ids, []string{"ac-01", "ac-15", "ac-16"}) { // ac-01 is the epic, whose done count moved
		t.Fatalf("rows: %v", ids)
	}
	for _, r := range d.Rows {
		if r.ID == "ac-16" && (!r.Ready || r.Blocked) {
			t.Errorf("ac-16 should be ready now: %+v", r)
		}
	}
}

// The clock alone (an age) is not a change.
func TestTasksSinceIgnoresTheClock(t *testing.T) {
	c, f := readyClient(t)
	base := since(t, c, "")
	f.advance(3 * time.Hour)
	if d := since(t, c, base.Fingerprint); len(d.Rows)+len(d.Removed) != 0 || d.Fingerprint != base.Fingerprint {
		t.Fatalf("%+v", d)
	}
}

// The server keeps the last few states: a fingerprint older than that is "full".
func TestTasksSinceForgetsOldFingerprints(t *testing.T) {
	c, f := readyClient(t)
	first := since(t, c, "")
	for i := 1; i <= 8; i++ {
		f.tasks.mutate(func(s *core.TaskSnapshot) { s.Issues[0].Title += fmt.Sprint(i) })
		since(t, c, "")
	}
	if got := since(t, c, first.Fingerprint); got.Kind != core.SinceFull || len(got.Rows) != 0 {
		t.Fatalf("a forgotten fingerprint: %+v", got)
	}
}

// A tracker past the watcher's bound is never kept: every answer is "full".
func TestTasksSinceOverTheCapIsAlwaysFull(t *testing.T) {
	c, f := readyClient(t)
	f.tasks.mutate(func(s *core.TaskSnapshot) {
		for i := 0; i < 5000; i++ {
			s.Issues = append(s.Issues, core.TaskIssue{ID: fmt.Sprintf("big-%d", i), Title: "x", Status: "open", Priority: 2})
		}
	})
	first := since(t, c, "")
	if first.Kind != core.SinceFull || first.Total <= 5000 {
		t.Fatalf("%+v", first)
	}
	if again := since(t, c, first.Fingerprint); again.Kind != core.SinceFull || len(again.Rows) != 0 {
		t.Fatalf("over the cap the state is not kept: %+v", again)
	}
}

// Exactly at the cap it is still kept.
func TestTasksSinceAtTheCapIsKept(t *testing.T) {
	c, f := readyClient(t)
	f.tasks.mutate(func(s *core.TaskSnapshot) {
		for len(s.Issues) < 5000 {
			s.Issues = append(s.Issues, core.TaskIssue{ID: fmt.Sprintf("big-%d", len(s.Issues)), Title: "x", Status: "open", Priority: 2})
		}
	})
	first := since(t, c, "")
	if again := since(t, c, first.Fingerprint); again.Kind != core.SinceDelta {
		t.Fatalf("at the cap: %+v", again)
	}
}

func TestTasksSinceBadParams(t *testing.T) {
	c, _ := readyClient(t)
	c.call("tasks.since", map[string]any{"fingerprint": 1}).wantErr(t, core.CodeUsage)
	c.call("tasks.since", map[string]any{"since": "x"}).wantErr(t, core.CodeUsage)
}
