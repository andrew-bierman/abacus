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

func TestHistoryReadAllowlist(t *testing.T) {
	for _, a := range [][]string{
		{"--readonly", "history", "tt-1", "--events", "--json", "--limit=51"},
		{"-C", "/trk", "--readonly", "history", "tt-1.2", "--events", "--json", "--limit=201"},
	} {
		if !readAllowed(a, "/trk") {
			t.Errorf("should be allowed: %v", a)
		}
	}
	for _, a := range [][]string{
		{"history", "tt-1", "--events", "--json", "--limit=5"},                       // not readonly
		{"--readonly", "history", "tt-1", "--events", "--json"},                      // unbounded
		{"--readonly", "history", "tt-1", "--events", "--json", "--limit=0"},         // 0 means everything
		{"--readonly", "history", "tt-1", "--events", "--json", "--limit=1000"},      // over three digits
		{"--readonly", "history", "tt-1", "--events", "--json", "--limit=-1"},        //
		{"--readonly", "history", "tt-1", "--events", "--json", "--limit=5 --all"},   //
		{"--readonly", "history", "tt-1", "--json", "--limit=5"},                     // not the event log
		{"--readonly", "history", "--all", "--events", "--json", "--limit=5"},        // not an id
		{"--readonly", "history", "tt-1", "tt-2", "--events", "--json", "--limit=5"}, // two ids
		{"-C", "/other", "--readonly", "history", "tt-1", "--events", "--json", "--limit=5"},
	} {
		if readAllowed(a, "/trk") {
			t.Errorf("should be refused: %v", a)
		}
	}
	if writeAllowed([]string{"history", "tt-1"}, "") {
		t.Error("history is a read, never a write")
	}
}

func TestHistoryRunsTheAllowlistedReadAndParsesIt(t *testing.T) {
	events, err := os.ReadFile(filepath.Join("..", "taskview", "testdata", "history-events.json"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRun{resp: map[string]string{
		"-C /trk --readonly version --json":                             verJSON,
		"-C /trk --readonly history hs-yqf --events --json --limit=4":   string(events),
		"-C /trk --readonly history hs-yqf --events --json --limit=51":  string(events),
		"-C /trk --readonly history hs-yqf --events --json --limit=201": string(events),
	}}
	m := mod(f, Options{Dir: "/trk"})
	m.Detect(context.Background())
	rep, err := m.History(context.Background(), "hs-yqf", 3)
	if err != nil || len(rep.Events) != 3 || !rep.Truncated || rep.Events[0].Kind != "reopened" {
		t.Fatalf("%v %+v", err, rep)
	}
	if got := f.calls[len(f.calls)-1]; got != "-C /trk --readonly history hs-yqf --events --json --limit=4" {
		t.Errorf("argv = %q (one more than asked, to know there is more)", got)
	}
	if _, err := m.History(context.Background(), "--all", 3); err == nil {
		t.Error("an id that is not an id must never reach bd")
	}
	if rep, err := m.History(context.Background(), "hs-yqf", 0); err != nil || len(rep.Events) != 8 {
		t.Errorf("a missing limit uses the default: %v %d", err, len(rep.Events))
	}
	f.exit = map[string]int{"-C /trk --readonly history hs-yqf --events --json --limit=51": 1}
	if _, err := m.History(context.Background(), "hs-yqf", 50); err == nil || !strings.Contains(err.Error(), "exited 1") {
		t.Errorf("a failing bd is an error: %v", err)
	}
	var _ core.TaskHistorySource = m
}

// trackerWithManifest builds the files the fingerprint reads, around a real captured manifest.
func trackerWithManifest(t *testing.T, manifest, meta string) (root string, manifestPath string) {
	t.Helper()
	root = t.TempDir()
	noms := filepath.Join(root, ".beads", "embeddeddolt", "hs", ".dolt", "noms")
	if err := os.MkdirAll(noms, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", manifest))
	if err != nil {
		t.Fatal(err)
	}
	manifestPath = filepath.Join(noms, "manifest")
	if err := os.WriteFile(manifestPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".beads", "metadata.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, manifestPath
}

const embeddedMeta = `{"database":"dolt","backend":"dolt","dolt_mode":"embedded","dolt_database":"hs","project_id":"x"}`

func TestFingerprintMovesOnAWriteAndNotOnARead(t *testing.T) {
	root, mp := trackerWithManifest(t, "manifest-before.txt", embeddedMeta)
	m := New(Options{Dir: root})
	a, err := m.Fingerprint(context.Background())
	if err != nil || a == "" {
		t.Fatalf("%q %v", a, err)
	}
	// A read leaves the manifest alone (only the mtime of other files moves): the same fingerprint.
	if b, _ := m.Fingerprint(context.Background()); b != a {
		t.Errorf("a read changed the fingerprint: %q != %q", b, a)
	}
	after, _ := os.ReadFile(filepath.Join("testdata", "manifest-after.txt"))
	if err := os.WriteFile(mp, after, 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _ := m.Fingerprint(context.Background()); c == a || c == "" {
		t.Errorf("a write (the real manifest after an update) must change the fingerprint: %q vs %q", c, a)
	}
}

func TestFingerprintRefusesWhatItCannotTrust(t *testing.T) {
	for name, meta := range map[string]string{
		"server mode":  `{"dolt_mode":"server","dolt_database":"hs"}`,
		"no mode":      `{"dolt_database":"hs"}`,
		"path in name": `{"dolt_mode":"embedded","dolt_database":"../../etc"}`,
		"empty name":   `{"dolt_mode":"embedded","dolt_database":""}`,
		"not json":     `nope`,
	} {
		root, _ := trackerWithManifest(t, "manifest-before.txt", meta)
		if _, err := New(Options{Dir: root}).Fingerprint(context.Background()); !errors.Is(err, core.ErrNoFingerprint) {
			t.Errorf("%s: %v, want ErrNoFingerprint", name, err)
		}
	}
	if _, err := New(Options{Dir: t.TempDir()}).Fingerprint(context.Background()); !errors.Is(err, core.ErrNoFingerprint) {
		t.Errorf("no tracker: %v", err)
	}
	root, mp := trackerWithManifest(t, "manifest-before.txt", embeddedMeta)
	_ = os.WriteFile(mp, nil, 0o600)
	if _, err := New(Options{Dir: root}).Fingerprint(context.Background()); !errors.Is(err, core.ErrNoFingerprint) {
		t.Errorf("empty manifest: %v", err)
	}
	_ = os.WriteFile(mp, make([]byte, 9<<10), 0o600)
	if _, err := New(Options{Dir: root}).Fingerprint(context.Background()); !errors.Is(err, core.ErrNoFingerprint) {
		t.Errorf("oversized manifest: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Options{Dir: root}).Fingerprint(ctx); err == nil {
		t.Error("a cancelled context stops the read")
	}
	var _ core.TaskFingerprinter = New(Options{})
}

func TestSetTrackerDirRepointsTheSharedRefOnlyToATracker(t *testing.T) {
	a, _ := trackerWithManifest(t, "manifest-before.txt", embeddedMeta)
	b, _ := trackerWithManifest(t, "manifest-before.txt", embeddedMeta)
	ref := core.NewTrackerRef(a)
	m := New(Options{Ref: ref})
	if err := m.SetTrackerDir(b); err != nil || ref.Dir() != b || m.TrackerDir() != b {
		t.Fatalf("%v %q", err, ref.Dir())
	}
	if m.SetTrackerDir(t.TempDir()) == nil || ref.Dir() != b {
		t.Error("a directory that is not a tracker must be refused and change nothing")
	}
	if New(Options{Dir: a}).SetTrackerDir(b) == nil {
		t.Error("a module without a shared reference cannot switch")
	}
	var _ core.TrackerSwitcher = m
}
