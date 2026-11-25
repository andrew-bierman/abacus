package bd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
)

var initArgv = []string{"init", "--non-interactive", "--quiet", "--skip-agents", "--skip-hooks", "--prefix=" + brand.Name}

func TestInitIsAnAllowedWriteOnlyWithExactlyTheFixedFlags(t *testing.T) {
	if !writeAllowed(initArgv, "") {
		t.Fatalf("the fixed init argv must be allowed: %v", initArgv)
	}
	bad := map[string][]string{
		"no flags":            {"init"},
		"missing a flag":      {"init", "--non-interactive", "--quiet", "--skip-agents", "--prefix=abacus"},
		"force":               append(append([]string{}, initArgv...), "--force"),
		"reinit":              append(append([]string{}, initArgv...), "--reinit-local"),
		"destroy token":       append(append([]string{}, initArgv...), "--destroy-token=DESTROY-abacus"),
		"discard remote":      append(append([]string{}, initArgv...), "--discard-remote"),
		"remote":              append(append([]string{}, initArgv...), "--remote=https://evil"),
		"server":              append(append([]string{}, initArgv...), "--server"),
		"duplicate":           append(append([]string{}, initArgv...), "--quiet"),
		"positional":          append(append([]string{}, initArgv...), "extra"),
		"bad prefix":          {"init", "--non-interactive", "--quiet", "--skip-agents", "--skip-hooks", "--prefix=a b"},
		"flag-like prefix":    {"init", "--non-interactive", "--quiet", "--skip-agents", "--skip-hooks", "--prefix=--force"},
		"with -C":             append([]string{"-C", "/x"}, initArgv...),
		"as a read-only init": append([]string{"--readonly"}, initArgv...),
	}
	for name, a := range bad {
		if writeAllowed(a, "") || writeAllowed(a, "/x") {
			t.Errorf("%s must be refused: %v", name, a)
		}
	}
}

func TestBuildInitArgvNeedsTheConfiguredWorkspace(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	m := New(Options{InitDir: ws})
	argv, err := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws})
	if err != nil || strings.Join(argv, " ") != "bd "+strings.Join(initArgv, " ") {
		t.Fatalf("%v %v", argv, err)
	}
	if _, err := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: "/elsewhere"}); err == nil {
		t.Fatal("a different directory must be refused")
	}
	if _, err := New(Options{}).BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws}); err == nil {
		t.Fatal("no workspace configured must be refused")
	}
}

type dirRunner struct {
	dir  string
	args []string
	exit int
	// makeTracker simulates bd leaving a tracker behind.
	makeTracker bool
}

func (d *dirRunner) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	d.args = args
	if d.makeTracker && d.exit == 0 {
		_ = os.MkdirAll(filepath.Join(d.dir, ".beads"), 0o755)
		_ = os.WriteFile(filepath.Join(d.dir, ".beads", "metadata.json"), []byte("{}"), 0o600)
	}
	return nil, d.exit, nil
}

func initModule(t *testing.T, dr *dirRunner, onInit func(string) error) (*Module, *core.TrackerRef, string) {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "data", "workspace")
	ref := core.NewTrackerRef("")
	dr.dir = ws
	m := New(Options{InitDir: ws, Ref: ref, OnInit: onInit, LookPath: found,
		InitRunner: func(dir string) core.Runner {
			if dir != ws {
				t.Errorf("init must run inside the workspace, got %q", dir)
			}
			return dr
		}})
	m.SetFound(core.Found{Bin: "/fake/bd"})
	return m, ref, ws
}

func TestRunInitCreatesTheFolderRunsBdThereAndPointsTasksAtIt(t *testing.T) {
	dr := &dirRunner{makeTracker: true}
	var saved string
	m, ref, ws := initModule(t, dr, func(d string) error { saved = d; return nil })
	argv, err := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.RunTask(context.Background(), argv)
	if err != nil || res.Status != "initialized" {
		t.Fatalf("%+v %v", res, err)
	}
	if fi, _ := os.Stat(ws); fi == nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("workspace folder: %v", fi)
	}
	if strings.Join(dr.args, " ") != strings.Join(initArgv, " ") || saved != ws || ref.Dir() != ws || m.TrackerDir() != ws {
		t.Fatalf("args=%v saved=%q ref=%q", dr.args, saved, ref.Dir())
	}
}

func TestRunInitFailureLeavesTasksUntouched(t *testing.T) {
	for name, dr := range map[string]*dirRunner{
		"bd exits non-zero":              {exit: 1},
		"exit 0 but no tracker appeared": {exit: 0, makeTracker: false},
	} {
		m, ref, ws := initModule(t, dr, func(string) error { t.Error("config must not be saved"); return nil })
		argv, _ := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws})
		if _, err := m.RunTask(context.Background(), argv); err == nil || ref.Dir() != "" {
			t.Errorf("%s: err=%v ref=%q", name, err, ref.Dir())
		}
	}
}

func TestRunInitReportsAConfigSaveFailureAfterTheWorkspaceExists(t *testing.T) {
	m, ref, ws := initModule(t, &dirRunner{makeTracker: true}, func(string) error { return errors.New("disk full") })
	argv, _ := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws})
	_, err := m.RunTask(context.Background(), argv)
	if err == nil || !strings.Contains(err.Error(), "disk full") || !strings.Contains(err.Error(), ws) {
		t.Fatalf("%v", err)
	}
	if ref.Dir() != ws { // the workspace is real, so this session uses it
		t.Fatalf("ref %q", ref.Dir())
	}
}

func TestTasksWithNoTrackerAsksForSetupInsteadOfFailing(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	ws := filepath.Join(t.TempDir(), "workspace")
	f := newFake()
	m := mod(f, Options{InitDir: ws})
	m.Detect(context.Background())
	_, err := m.Tasks(context.Background())
	g, ok := core.AsNeedsSetup(err)
	if !ok || g.Step != core.StepWorkspace || g.Message != "No Beads tracker found" || g.Action != "create a workspace (bd init)" {
		t.Fatalf("%+v %v", g, err)
	}
	if got := m.Guide(context.Background()); got == nil || got.Step != core.StepWorkspace || !strings.Contains(got.Detail, ws) {
		t.Fatalf("guide %+v", got)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "status") {
			t.Fatalf("bd must not be asked to read a tracker that is not there: %v", f.calls)
		}
	}
	// With a tracker the same module reads normally and offers nothing.
	if err := os.MkdirAll(filepath.Join(cwd, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".beads", "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := m.Tasks(context.Background()); err != nil || r.Counts.Total != 5 {
		t.Fatalf("%+v %v", r, err)
	}
	if m.Guide(context.Background()) != nil {
		t.Fatal("a tracker is there: nothing to offer")
	}
}

// A fake bd that records the environment of every call it receives.
func envRecordingBd(t *testing.T) (bin, out string) {
	t.Helper()
	dir := t.TempDir()
	out = filepath.Join(dir, "env.txt")
	bin = filepath.Join(dir, "bd")
	script := `#!/bin/sh
{ echo "--- $*"; env; } >> ` + out + `
case "$*" in
  *version*) echo '{"version":"1","schema_version":1}' ;;
  *status*) echo '{"schema_version":1,"summary":{}}' ;;
  *list*) echo '[]' ;;
  init*) mkdir -p .beads && echo '{}' > .beads/metadata.json ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, out
}

func TestEveryBdCallRunsWithTheBeadsEnvironmentScrubbed(t *testing.T) {
	t.Setenv("BEADS_DIR", "/real/tracker/.beads")
	t.Setenv("BEADS_DB", "/real/tracker/db")
	t.Setenv("BD_SOCKET", "/real/sock")
	t.Setenv("BEADS_ACTOR", "me")
	bin, out := envRecordingBd(t)
	ws := filepath.Join(t.TempDir(), "ws")
	ref := core.NewTrackerRef("")
	m := New(Options{Bin: bin, InitDir: ws, Ref: ref, LookPath: found})
	ctx := context.Background()
	m.Detect(ctx) // version
	argv, err := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.RunTask(ctx, argv); err != nil { // init
		t.Fatal(err)
	}
	if _, err := m.Tasks(ctx); err != nil { // reads against the new tracker
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	s := string(b)
	if n := strings.Count(s, "--- "); n < 4 {
		t.Fatalf("expected version, init, status and list calls, got %d:\n%s", n, s)
	}
	for _, bad := range []string{"BEADS_DIR=", "BEADS_DB=", "BD_SOCKET="} {
		if strings.Contains(s, bad) {
			t.Errorf("%s reached a bd process:\n%s", bad, s)
		}
	}
	if strings.Count(s, "BEADS_ACTOR=me") != strings.Count(s, "--- ") {
		t.Errorf("BEADS_ACTOR must still reach every call:\n%s", s)
	}
}

func TestAHalfCreatedWorkspaceGuidesToTheRepairNotAnotherInit(t *testing.T) {
	t.Chdir(t.TempDir())
	ws := filepath.Join(t.TempDir(), "workspace")
	m := mod(newFake(), Options{InitDir: ws})
	m.Detect(context.Background())
	if err := os.MkdirAll(filepath.Join(ws, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if g := m.Guide(context.Background()); g == nil || g.Step != core.StepRepair || !strings.Contains(g.Detail, ws) {
		t.Fatalf("%+v", g)
	}
	// A folder with someone's files in it is not offered a repair: the normal step stays, and its
	// apply refuses to run over a half-created .beads.
	if err := os.WriteFile(filepath.Join(ws, "mine.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if g := m.Guide(context.Background()); g == nil || g.Step != core.StepWorkspace {
		t.Fatalf("%+v", g)
	}
}

// errRunner stands in for bd failing to run, or running out of time.
type errRunner struct {
	err    error
	called bool
}

func (r *errRunner) Run(context.Context, string, ...string) ([]byte, int, error) {
	r.called = true
	return nil, 0, r.err
}

// An init that timed out or was canceled may still have created files, so it is "result unknown", never
// "not applied"; an init that could not start at all is a plain failure. Neither points Tasks at the folder.
func TestRunInitTimeoutIsUnknownAndACrashIsAFailure(t *testing.T) {
	for name, c := range map[string]struct {
		err     error
		unknown bool
	}{
		"timed out": {core.ErrTimedOut, true},
		"canceled":  {core.ErrCanceled, true},
		"not found": {errors.New("fork/exec /fake/bd: no such file"), false},
	} {
		ws := filepath.Join(t.TempDir(), "workspace")
		ref := core.NewTrackerRef("")
		er := &errRunner{err: c.err}
		m := New(Options{InitDir: ws, Ref: ref, LookPath: found, InitRunner: func(string) core.Runner { return er }})
		m.SetFound(core.Found{Bin: "/fake/bd"})
		argv, _ := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws})
		_, err := m.RunTask(context.Background(), argv)
		if err == nil || errors.Is(err, core.ErrOutcomeUnknown) != c.unknown || ref.Dir() != "" {
			t.Errorf("%s: err=%v unknown=%v ref=%q", name, err, errors.Is(err, core.ErrOutcomeUnknown), ref.Dir())
		}
		if c.unknown && !strings.Contains(err.Error(), ws) {
			t.Errorf("%s: the unknown outcome should say where to look: %v", name, err)
		}
	}
}

// A folder with an unfinished .beads is never initialised again over the top: the user is sent to the
// repair, and bd is not run.
func TestRunInitRefusesAHalfCreatedWorkspaceWithoutRunningBd(t *testing.T) {
	er := &errRunner{}
	m, _, ws := initModule(t, &dirRunner{}, nil)
	m.o.InitRunner = func(string) core.Runner { return er }
	if err := os.MkdirAll(filepath.Join(ws, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	argv, _ := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws})
	_, err := m.RunTask(context.Background(), argv)
	if err == nil || !strings.Contains(err.Error(), "setup --repair") || er.called {
		t.Fatalf("err=%v called=%v", err, er.called)
	}
}

// A write never silently falls back to a PATH copy of bd when the bundled one was rejected: the preflight a
// repair makes before moving any file says so, and so does the init itself.
func TestInitPreflightAndInitRefuseAWriteThroughAFallbackBd(t *testing.T) {
	er := &errRunner{}
	m, _, ws := initModule(t, &dirRunner{}, nil)
	m.o.InitRunner = func(string) core.Runner { return er }
	m.SetFound(core.Found{Bin: "/usr/bin/bd", Src: core.SourcePATH, Ignored: "the built-in bd is not trusted"})
	if err := m.InitPreflight(); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("preflight: %v", err)
	}
	argv, _ := m.BuildTaskArgv(core.TaskChange{Op: core.TaskInit, Dir: ws})
	if _, err := m.RunTask(context.Background(), argv); err == nil || er.called {
		t.Fatalf("init ran through a fallback bd: err=%v called=%v", err, er.called)
	}
	// with nothing wrong, and no bd to be found, the preflight reports what is missing
	m.SetFound(core.Found{})
	m.o.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	if err := m.InitPreflight(); err == nil {
		t.Fatal("preflight passed with no bd")
	}
}
