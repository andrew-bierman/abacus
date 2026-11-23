package bd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

// A manifest that is a FIFO would block an open for ever: it is refused on its type, before any open,
// and promptly. Symlinks, directories and oversize files are refused too; a plain I/O failure is a
// different error, so the caller backs off instead of giving up on cheap checks.
func TestFingerprintRefusesNonRegularManifests(t *testing.T) {
	for name, make := range map[string]func(path string, t *testing.T){
		"fifo": func(p string, t *testing.T) {
			_ = os.Remove(p)
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Skip("no mkfifo here")
			}
		},
		"symlink": func(p string, t *testing.T) {
			real := p + ".real"
			_ = os.Rename(p, real)
			if err := os.Symlink(real, p); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(p string, t *testing.T) { _ = os.Remove(p); _ = os.Mkdir(p, 0o700) },
		"oversize":  func(p string, t *testing.T) { _ = os.WriteFile(p, make8k(), 0o600) },
	} {
		root, mp := trackerWithManifest(t, "manifest-before.txt", embeddedMeta)
		make(mp, t)
		done := make1(New(Options{Dir: root}))
		select {
		case err := <-done:
			if !errors.Is(err, core.ErrNoFingerprint) {
				t.Errorf("%s: %v, want ErrNoFingerprint", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: Fingerprint did not return: a non-regular manifest blocked it", name)
		}
	}
}

func make8k() []byte { return make([]byte, 9<<10) }

func make1(m *Module) chan error {
	ch := make(chan error, 1)
	go func() { _, err := m.Fingerprint(context.Background()); ch <- err }()
	return ch
}

func TestFingerprintRefusesASymlinkedMetadataAndReportsIOFailureApart(t *testing.T) {
	root, _ := trackerWithManifest(t, "manifest-before.txt", embeddedMeta)
	meta := filepath.Join(root, ".beads", "metadata.json")
	real := meta + ".real"
	_ = os.Rename(meta, real)
	if err := os.Symlink(real, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Dir: root}).Fingerprint(context.Background()); !errors.Is(err, core.ErrNoFingerprint) {
		t.Errorf("a symlinked metadata.json: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	root, mp := trackerWithManifest(t, "manifest-before.txt", embeddedMeta)
	if err := os.Chmod(mp, 0); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{Dir: root}).Fingerprint(context.Background())
	if !errors.Is(err, core.ErrFingerprintFailed) || errors.Is(err, core.ErrNoFingerprint) {
		t.Errorf("an unreadable manifest is a failed read, not 'no fingerprint': %v", err)
	}
}

func TestFingerprintHonorsAnExpiredContext(t *testing.T) {
	root, _ := trackerWithManifest(t, "manifest-before.txt", embeddedMeta)
	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	if _, err := New(Options{Dir: root}).Fingerprint(ctx); !errors.Is(err, core.ErrFingerprintFailed) {
		t.Errorf("%v", err)
	}
}

// A limit above the maximum is clamped to the default, never passed on to bd.
func TestHistoryClampsAnOversizeLimit(t *testing.T) {
	events, _ := os.ReadFile(filepath.Join("..", "taskview", "testdata", "history-events.json"))
	f := &fakeRun{resp: map[string]string{
		"-C /trk --readonly version --json":                            verJSON,
		"-C /trk --readonly history hs-yqf --events --json --limit=51": string(events),
	}}
	m := mod(f, Options{Dir: "/trk"})
	m.Detect(context.Background())
	for _, lim := range []int{core.HistoryMaxLimit + 1, 999, -5} {
		if _, err := m.History(context.Background(), "hs-yqf", lim); err != nil {
			t.Fatalf("limit %d: %v", lim, err)
		}
		if got := f.calls[len(f.calls)-1]; got != "-C /trk --readonly history hs-yqf --events --json --limit=51" {
			t.Errorf("limit %d went to bd as %q", lim, got)
		}
	}
	if _, err := m.History(context.Background(), "hs-yqf", core.HistoryMaxLimit); err == nil {
		// limit 200 asks bd for 201: the fake has no such response, so this must be an error from the runner
		t.Log("limit 200 reached bd as limit=201")
	}
	if got := f.calls[len(f.calls)-1]; got != "-C /trk --readonly history hs-yqf --events --json --limit=201" {
		t.Errorf("the maximum limit is allowed: %q", got)
	}
}
