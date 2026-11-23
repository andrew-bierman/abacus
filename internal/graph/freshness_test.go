package graph_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/graph"
)

// fakeTracker is a tracker directory with an export and a database at controlled mtimes.
func fakeTracker(t *testing.T, now time.Time, exportAge, dataAge time.Duration) *core.TrackerRef {
	t.Helper()
	dir := t.TempDir()
	touch(t, writeExport(t, dir), now.Add(-exportAge))
	if dataAge >= 0 {
		db := filepath.Join(dir, ".beads", "beads.db")
		if err := os.WriteFile(db, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, db, now.Add(-dataAge))
	}
	return core.NewTrackerRef(dir)
}

func freshModule(ref *core.TrackerRef, now time.Time) *graph.Module {
	return graph.NewModule(graph.Options{Source: graph.FileSource{Ref: ref}, Analyzer: graph.NewBVAnalyzer(), Now: func() time.Time { return now }})
}

func TestInsightsShowTheExportAgeOrSayItIsStale(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	// No tracker database to compare with: the export's age rides in the header summary.
	r, err := freshModule(fakeTracker(t, now, 90*time.Minute, -1), now).Insights(context.Background())
	if err != nil || r.Header.Summary != "export 1h30m old" || r.Header.Reason != "" {
		t.Fatalf("age only: %+v %v", r.Header, err)
	}

	// Data newer than the export: a reason (so the header is degraded), not just a note.
	r, err = freshModule(fakeTracker(t, now, 10*time.Minute, 4*time.Minute), now).Insights(context.Background())
	if err != nil || !strings.Contains(r.Header.Reason, "6m older than the tracker data") {
		t.Fatalf("stale: %+v %v", r.Header, err)
	}
	// The graph itself is still returned.
	if r.GraphNodes != 3 {
		t.Fatalf("stale export still analysed: %+v", r)
	}

	// A current export with a database: just its age.
	r, _ = freshModule(fakeTracker(t, now, 10*time.Minute, 30*time.Minute), now).Insights(context.Background())
	if r.Header.Reason != "" || r.Header.Summary != "export 10m old" {
		t.Fatalf("current: %+v", r.Header)
	}

	// A source with no freshness (a fixed list) adds nothing.
	r, _ = graph.NewModule(graph.Options{Source: graph.StaticSource{is("a", graph.StatusOpen)}, Analyzer: graph.NewBVAnalyzer()}).Insights(context.Background())
	if r.Header.Summary != "" || r.Header.Reason != "" {
		t.Fatalf("static: %+v", r.Header)
	}
}

func TestReadInsightsKeepsTheExportNoteAndDegradesWhenStale(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	rep := core.ReadInsights(context.Background(), freshModule(fakeTracker(t, now, 90*time.Minute, -1), now), "")
	if rep.Header.State != core.Connected || !strings.HasPrefix(rep.Header.Summary, rep.Summarize()) || !strings.Contains(rep.Header.Summary, "export 1h30m old") {
		t.Fatalf("%+v", rep.Header)
	}
	rep = core.ReadInsights(context.Background(), freshModule(fakeTracker(t, now, 10*time.Minute, 0), now), "")
	if rep.Header.State != core.Degraded || !strings.Contains(rep.Header.Reason, "older than the tracker data") {
		t.Fatalf("%+v", rep.Header)
	}
}

func TestDoctorWarnsWhenTheExportIsOlderThanTheTrackerData(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	find := func(m *graph.Module) *core.Check {
		for _, c := range m.Doctor(context.Background()) {
			if c.Name == "export" {
				return &c
			}
		}
		return nil
	}
	c := find(freshModule(fakeTracker(t, now, 10*time.Minute, 4*time.Minute), now))
	if c == nil || c.Status != core.StatusWarn || !strings.Contains(c.Detail, "6m") || c.Fix == "" {
		t.Fatalf("stale export: %+v", c)
	}
	c = find(freshModule(fakeTracker(t, now, 10*time.Minute, 30*time.Minute), now))
	if c == nil || c.Status != core.StatusOK || !strings.Contains(c.Detail, "10m") {
		t.Fatalf("current export: %+v", c)
	}
	if c = find(graph.NewModule(graph.Options{Source: graph.StaticSource(nil), Analyzer: graph.NewBVAnalyzer()})); c != nil {
		t.Fatalf("no export line without a file export: %+v", c)
	}
}

func TestExportAgeText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Minute: "0s", 0: "0s", 20 * time.Second: "20s", 59 * time.Second: "59s", time.Minute: "1m",
		90 * time.Second: "1m", 61 * time.Minute: "1h1m", 3 * time.Hour: "3h", 49 * time.Hour: "2d1h", 72 * time.Hour: "3d",
	} {
		if got := graph.AgeText(d); got != want {
			t.Errorf("AgeText(%v) = %q, want %q", d, got, want)
		}
	}
}
