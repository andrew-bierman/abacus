package demo

import (
	"context"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

var at = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

// The demo summary, the demo list and the demo insights describe one tracker.
func TestDemoSnapshotAgreesWithTheSummary(t *testing.T) {
	snap := TaskSnapshot(at)
	rep := TasksReport()
	v := taskview.Build(snap, at)
	if v.Total() != rep.Counts.Total || v.Ready() != rep.Counts.Ready {
		t.Errorf("snapshot has %d tasks, %d ready; the summary says %d, %d", v.Total(), v.Ready(), rep.Counts.Total, rep.Counts.Ready)
	}
	got := map[string]int{}
	for _, i := range snap.Issues {
		got[i.Status]++
	}
	c := rep.Counts
	if got["open"] != c.Open || got["in_progress"] != c.InProgress || got["blocked"] != c.Blocked || got["deferred"] != c.Deferred || got["closed"] != c.Closed {
		t.Errorf("status counts %v vs %+v", got, c)
	}
	for _, r := range rep.Recent {
		row, ok := v.Row(r.ID)
		if !ok || row.Status != r.Status || row.Priority != r.Priority || row.Title != r.Title {
			t.Errorf("%s: summary %+v, list %+v", r.ID, r, row)
		}
	}
	if w := snap.Warnings; len(w) != 0 {
		t.Errorf("warnings: %v", w)
	}
	ins := InsightsReport()
	blocked := 0
	for _, r := range v.List(taskview.ListOptions{Preset: taskview.PresetAll}).Rows {
		if r.Blocked {
			blocked++
		}
	}
	if ins.TotalActionable != v.Ready() || ins.TotalBlocked != blocked {
		t.Errorf("insights say %d actionable, %d blocked; the list has %d, %d", ins.TotalActionable, ins.TotalBlocked, v.Ready(), blocked)
	}
	for _, id := range []string{ins.TopPicks[0].ID, ins.CriticalPath[0].ID, ins.Cycles[0][0]} {
		if _, ok := v.Row(id); !ok {
			t.Errorf("insights name %s, which is not in the list", id)
		}
	}
}

func TestDemoTasksProvidesTheSnapshot(t *testing.T) {
	var _ core.TaskSource = NewTasks()
	s, err := NewTasks().Snapshot(context.Background())
	if err != nil || len(s.Issues) != 14 {
		t.Fatalf("%v %d", err, len(s.Issues))
	}
}
