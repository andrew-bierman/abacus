package bd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func exportFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", "export.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSnapshotRunsExactlyTheAllowlistedExportRead(t *testing.T) {
	f := &fakeRun{resp: map[string]string{
		"-C /trk --readonly version --json": verJSON,
		"-C /trk --readonly export":         exportFixture(t),
	}}
	m := mod(f, Options{Dir: "/trk"})
	m.Detect(context.Background())
	snap, err := m.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Issues) != 9 || snap.Tracker != "trk" || snap.ReadAt.IsZero() || len(snap.Warnings) != 0 {
		t.Fatalf("snapshot: %d issues, tracker %q, warnings %v", len(snap.Issues), snap.Tracker, snap.Warnings)
	}
	if got := f.calls[len(f.calls)-1]; got != "-C /trk --readonly export" {
		t.Fatalf("argv = %q", got)
	}
	var _ core.TaskSource = m
}

func TestSnapshotWithoutADirNamesNoTracker(t *testing.T) {
	f := &fakeRun{resp: map[string]string{"--readonly version --json": verJSON, "--readonly export": exportFixture(t)}}
	m := mod(f, Options{})
	m.Detect(context.Background())
	snap, err := m.Snapshot(context.Background())
	if err != nil || snap.Tracker != "" {
		t.Fatalf("tracker %q err %v", snap.Tracker, err)
	}
}

func TestSnapshotFailuresAreDistinguished(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeRun)
		want string
	}{
		{"timeout", func(f *fakeRun) { f.errs = map[string]error{"--readonly export": core.ErrTimedOut} }, "timed out"},
		{"too large", func(f *fakeRun) { f.errs = map[string]error{"--readonly export": core.ErrOutputTooLarge} }, "larger than 64 MiB"},
		{"cannot run", func(f *fakeRun) { f.errs = map[string]error{"--readonly export": errors.New("boom")} }, "could not run"},
		{"no tracker", func(f *fakeRun) { f.exit = map[string]int{"--readonly export": 1}; f.resp["--readonly export"] = "" }, "exited 1"},
		{"garbled", func(f *fakeRun) { f.resp["--readonly export"] = `{"id":"a-1"}` + "\n{oops" }, "unreadable record"},
	}
	for _, c := range cases {
		f := newFake()
		f.resp["--readonly export"] = exportFixture(t)
		c.set(f)
		m := mod(f, Options{})
		m.Detect(context.Background())
		_, err := m.Snapshot(context.Background())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if c.name == "timeout" && !errors.Is(err, core.ErrTimedOut) {
			t.Errorf("a timeout stays typed so the view can back off: %v", err)
		}
	}
}

func TestAWriteArgvIsRefusedOnTheReadPath(t *testing.T) {
	f := newFake()
	m := mod(f, Options{})
	for _, a := range [][]string{{"close", "tt-1", "--json"}, {"update", "tt-1", "--json", "--title=x"}, {"--readonly", "export", "--all"}, {"export"}} {
		before := len(f.calls)
		if _, _, err := m.read(context.Background(), a); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%v: %v", a, err)
		}
		if len(f.calls) != before {
			t.Errorf("%v reached the runner", a)
		}
	}
}

func TestTheDefaultRunnerBoundsTheSnapshotRead(t *testing.T) {
	r, ok := New(Options{}).o.ExportRunner.(core.ExecRunner)
	if !ok || r.MaxOut != ExportMaxOut || r.Timeout != exportTimeout {
		t.Fatalf("the export runner must cap stdout and have the long deadline: %+v", r)
	}
}

// The header names the project the list really reads: the directory that holds the tracker, which
// may be above the configured or working directory.
func TestSnapshotNamesTheDirectoryThatHoldsTheTracker(t *testing.T) {
	fx := exportFixture(t) // before the chdir below: the path is relative
	proj := filepath.Join(t.TempDir(), "fleet-proj")
	deep := filepath.Join(proj, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".beads", "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeRun{resp: map[string]string{"-C " + deep + " --readonly version --json": verJSON, "-C " + deep + " --readonly export": fx}}
	m := mod(f, Options{Dir: deep})
	m.Detect(context.Background())
	snap, err := m.Snapshot(context.Background())
	if err != nil || snap.Tracker != "fleet-proj" {
		t.Fatalf("tracker %q err %v, want fleet-proj", snap.Tracker, err)
	}
	t.Chdir(deep) // no configured directory: bd resolves it from the working directory
	f2 := &fakeRun{resp: map[string]string{"--readonly version --json": verJSON, "--readonly export": fx}}
	m2 := mod(f2, Options{})
	m2.Detect(context.Background())
	if snap, err := m2.Snapshot(context.Background()); err != nil || snap.Tracker != "fleet-proj" {
		t.Fatalf("from the working directory: %q %v", snap.Tracker, err)
	}
}
