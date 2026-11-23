package bd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

type fakeRun struct {
	resp  map[string]string // joined argv -> stdout
	exit  map[string]int
	errs  map[string]error
	calls []string
}

func (f *fakeRun) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	k := strings.Join(args, " ")
	f.calls = append(f.calls, k)
	if err := f.errs[k]; err != nil {
		return nil, -1, err
	}
	out, ok := f.resp[k]
	if !ok {
		return nil, -1, fmt.Errorf("unexpected argv %q", k)
	}
	return []byte(out), f.exit[k], nil
}

func found(string) (string, error) { return "/fake/bd", nil }

const (
	verJSON    = `{"version":"1.3.4","schema_version":1}`
	statusJSON = `{"schema_version":1,"summary":{"total_issues":5,"open_issues":2,"in_progress_issues":1,"blocked_issues":1,"closed_issues":1,"deferred_issues":0,"ready_issues":2}}`
	listJSON   = `[{"id":"t-2","title":"newer\u001b[2J","status":"in_progress","priority":1,"issue_type":"task","assignee":"bob","updated_at":"2026-11-22T05:00:00Z"},{"id":"t-1","title":"older","status":"open","priority":2,"issue_type":"bug","updated_at":"2026-11-21T05:00:00Z"}]`
)

func newFake() *fakeRun {
	return &fakeRun{resp: map[string]string{
		"--readonly version --json":                                       verJSON,
		"--readonly status --json --no-activity":                          statusJSON,
		"--readonly list --json --brief --all --sort updated --flat -n 8": listJSON,
	}}
}

func mod(f *fakeRun, o Options) *Module {
	o.Runner, o.LookPath = f, found
	return New(o)
}

func TestDetectReadyIncompatibleBrokenAbsent(t *testing.T) {
	m := mod(newFake(), Options{})
	if d := m.Detect(context.Background()); d.State != core.Ready || d.Version != "1.3.4" || d.APIVersion != 1 {
		t.Fatalf("%+v", d)
	}
	f := newFake()
	f.resp["--readonly version --json"] = `{"version":"9","schema_version":2}`
	if d := mod(f, Options{}).Detect(context.Background()); d.State != core.Incompatible {
		t.Fatalf("%+v", d)
	}
	f = newFake()
	f.exit = map[string]int{"--readonly version --json": 3}
	if d := mod(f, Options{}).Detect(context.Background()); d.State != core.Broken {
		t.Fatalf("%+v", d)
	}
	m = New(Options{LookPath: func(string) (string, error) { return "", fmt.Errorf("no") }, Runner: newFake()})
	if d := m.Detect(context.Background()); d.State != core.Absent {
		t.Fatalf("%+v", d)
	}
}

func TestTasksIsACompactSummaryNotABrowser(t *testing.T) {
	f := newFake()
	m := mod(f, Options{})
	m.Detect(context.Background())
	r, err := m.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := r.Counts
	if r.Source != core.SourceLive || c.Total != 5 || c.Open != 2 || c.InProgress != 1 || c.Blocked != 1 || c.Closed != 1 || c.Ready != 2 {
		t.Fatalf("%+v", r)
	}
	if len(r.Recent) != 2 || r.Recent[0].ID != "t-2" || r.Recent[0].Assignee != "bob" || strings.ContainsRune(r.Recent[0].Title, 0x1b) {
		t.Fatalf("recent %+v", r.Recent)
	}
	var _ core.TaskProvider = m
}

func TestReadsAlwaysRunReadonlyAndUseTheConfiguredTrackerDir(t *testing.T) {
	f := &fakeRun{resp: map[string]string{
		"-C /trk --readonly version --json":                                       verJSON,
		"-C /trk --readonly status --json --no-activity":                          statusJSON,
		"-C /trk --readonly list --json --brief --all --sort updated --flat -n 8": listJSON,
	}}
	m := mod(f, Options{Dir: "/trk"})
	m.Detect(context.Background())
	if _, err := m.Tasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if !strings.Contains(c, "--readonly") {
			t.Errorf("read without --readonly: %s", c)
		}
	}
}

func TestNoTrackerIsAnErrorWithAHintNotAFailure(t *testing.T) {
	f := newFake()
	f.exit = map[string]int{"--readonly status --json --no-activity": 1}
	f.resp["--readonly status --json --no-activity"] = ""
	m := mod(f, Options{})
	m.Detect(context.Background())
	if _, err := m.Tasks(context.Background()); err == nil || !strings.Contains(err.Error(), "ABACUS_BD_DIR") {
		t.Fatalf("%v", err)
	}
	if cs := m.Doctor(context.Background()); len(cs) == 0 || cs[0].Status != core.StatusWarn {
		t.Fatalf("doctor %+v", cs)
	}
}

func TestStatusMirrorsTheSummary(t *testing.T) {
	m := mod(newFake(), Options{})
	m.Detect(context.Background())
	if st := m.Status(context.Background()); !strings.Contains(st.Summary, "5 tasks") {
		t.Fatalf("%+v", st)
	}
}

func TestTasksFailuresAreDistinguishedAndOnlyANonZeroExitIsNoTracker(t *testing.T) {
	cases := map[string]struct {
		body string
		exit int
		err  error
		want string
	}{
		"timeout":    {"", 0, fmt.Errorf("%w after 10s", core.ErrTimedOut), "timed out"},
		"cannot run": {"", 0, fmt.Errorf("fork/exec: nope"), "could not run"},
		"bad json":   {"warning: x", 0, nil, "unreadable"},
		"schema":     {`{"schema_version":2,"summary":{}}`, 0, nil, "schema_version 2"},
		"no tracker": {"", 1, nil, "ABACUS_BD_DIR"},
	}
	for name, c := range cases {
		f := newFake()
		k := "--readonly status --json --no-activity"
		f.resp[k], f.exit = c.body, map[string]int{k: c.exit}
		f.errs = map[string]error{}
		if c.err != nil {
			f.errs[k] = c.err
		}
		m := mod(f, Options{})
		m.Detect(context.Background())
		_, err := m.Tasks(context.Background())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
		if name != "no tracker" && strings.Contains(err.Error(), "ABACUS_BD_DIR") {
			t.Errorf("%s: must not claim there is no tracker: %v", name, err)
		}
	}
}

func TestReadTaskReadsOneIssueReadonlyAndChecksTheID(t *testing.T) {
	f := newFake()
	f.resp["--readonly show tt-1 --json"] = `[{"id":"tt-1","title":"t\u001b","status":"open","priority":2,"issue_type":"task"}]`
	m := mod(f, Options{})
	m.Detect(context.Background())
	got, err := m.ReadTask(context.Background(), "tt-1")
	if err != nil || got.ID != "tt-1" || got.Status != "open" || got.Priority != 2 || strings.ContainsRune(got.Title, 0x1b) {
		t.Fatalf("%+v %v", got, err)
	}
	f.resp["--readonly show tt-1 --json"] = `[{"id":"tt-9","title":"other"}]`
	if _, err := m.ReadTask(context.Background(), "tt-1"); err == nil {
		t.Fatal("a different issue is an error")
	}
	f.resp["--readonly show tt-1 --json"] = ``
	f.exit = map[string]int{"--readonly show tt-1 --json": 1}
	if _, err := m.ReadTask(context.Background(), "tt-1"); err == nil {
		t.Fatal("not found is an error")
	}
	if _, err := m.ReadTask(context.Background(), "--force"); err == nil {
		t.Fatal("a flag-like id is refused before any process")
	}
	var _ core.TaskReader = m
}

func TestExportReadsTheWholeTrackerReadOnly(t *testing.T) {
	f := newFake()
	f.resp["-C /trk --readonly export"] = "{\"id\":\"a\"}\n"
	out, err := mod(f, Options{Dir: "/trk"}).Export(context.Background())
	if err != nil || string(out) != "{\"id\":\"a\"}\n" {
		t.Fatalf("%q %v", out, err)
	}
	f.exit = map[string]int{"-C /trk --readonly export": 1}
	if _, err := mod(f, Options{Dir: "/trk"}).Export(context.Background()); err == nil || !strings.Contains(err.Error(), "exited 1") {
		t.Fatalf("a failing export must be an error: %v", err)
	}
	f.errs = map[string]error{"-C /trk --readonly export": fmt.Errorf("boom\x1b[2J")}
	if _, err := mod(f, Options{Dir: "/trk"}).Export(context.Background()); err == nil || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("the error must be sanitized: %q", err)
	}
}

// splitFake is a Runner that also reports stderr, like the real ExecRunner.
type splitFake struct {
	out, errOut []byte
	exit        int
	err         error
}

func (s splitFake) Run(context.Context, string, ...string) ([]byte, int, error) {
	return s.out, s.exit, s.err
}

func (s splitFake) RunSplit(context.Context, string, ...string) ([]byte, []byte, int, error) {
	return s.out, s.errOut, s.exit, s.err
}

func exportMod(r core.Runner) *Module {
	return New(Options{Dir: "/trk", Runner: r, LookPath: found})
}

func TestExportFailureKeepsASanitizedShortStderrTail(t *testing.T) {
	long := strings.Repeat("x", 500)
	_, err := exportMod(splitFake{exit: 1, errOut: []byte("first line\n\x1b[2Jdatabase is locked " + long)}).Export(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "bd export exited 1: ") || strings.Contains(err.Error(), "\x1b") || len(err.Error()) > 260 {
		t.Fatalf("%q", err)
	}
	if _, err := exportMod(splitFake{exit: 2, errOut: []byte("database is locked\n")}).Export(context.Background()); err == nil || !strings.Contains(err.Error(), "exited 2: database is locked") {
		t.Fatalf("%v", err)
	}
	if _, err := exportMod(splitFake{exit: 3}).Export(context.Background()); err == nil || err.Error() != "bd export exited 3" {
		t.Fatalf("no stderr, no suffix: %v", err)
	}
}

func TestExportOverTheCapIsTheBoundedError(t *testing.T) {
	// The capped runner reports ErrOutputTooLarge with whatever it kept; a runner that hands back
	// more than the cap is treated the same.
	if _, err := exportMod(splitFake{out: []byte("{}"), err: core.ErrOutputTooLarge}).Export(context.Background()); !errors.Is(err, core.ErrOutputTooLarge) {
		t.Fatalf("%v", err)
	}
	if _, err := exportMod(splitFake{out: make([]byte, ExportMaxOut)}).Export(context.Background()); !errors.Is(err, core.ErrOutputTooLarge) {
		t.Fatalf("%v", err)
	}
	if out, err := exportMod(splitFake{}).Export(context.Background()); err != nil || len(out) != 0 {
		t.Fatalf("empty stdout is an empty export: %q %v", out, err)
	}
}

// The real process runner discards what a tool prints past the cap instead of buffering it, and
// stops with ErrOutputTooLarge: a bd that prints 768 MiB keeps at most the cap (the allocation bound is loose because the kept
// slice grows by appending).
func TestExportStreamsThroughACappedRunnerAndDoesNotBufferPastTheCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	script := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nhead -c 805306368 /dev/zero\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Dir: "/trk", LookPath: found}) // no injected runner: the production capped one
	m.SetFound(core.Found{Bin: script})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := m.Export(context.Background())
	runtime.ReadMemStats(&after)
	if !errors.Is(err, core.ErrOutputTooLarge) {
		t.Fatalf("%v", err)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 600<<20 {
		t.Fatalf("allocated %d MiB for a 768 MiB output: it was buffered", grown>>20)
	}
}
