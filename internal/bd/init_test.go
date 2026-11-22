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
	m.bin = "/fake/bd"
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
	if !ok || g.Step != core.StepWorkspace || g.Key != "i" || g.Line() != "No tracker here — press i to create your fleet workspace (bd init)" {
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
