package bd

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

type memAudit struct{ n int }

func (m *memAudit) Record(core.AuditEntry) error { m.n++; return nil }

// The only test that runs the real bd, and only against a tracker it creates in a temp dir
// with an isolated HOME and XDG dirs. It skips when bd is not installed.
func TestRealBdWritePathAgainstAnIsolatedTemporaryTracker(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd is not installed")
	}
	home, proj := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", home+"/s")
	t.Setenv("XDG_CONFIG_HOME", home+"/c")
	t.Setenv("BEADS_ACTOR", "abacus-test")
	t.Setenv("BEADS_DIR", "")
	for _, c := range [][]string{{"git", "init", "-q", "."}, {"bd", "init", "--non-interactive", "--prefix", "tt", "--quiet"}} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = proj
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot set up an isolated tracker (%v): %s", err, out)
		}
	}
	ctx := context.Background()
	m := New(Options{Dir: proj})
	if d := m.Detect(ctx); d.State != core.Ready {
		t.Fatalf("detect %+v", d)
	}
	au := &memAudit{}
	ap := core.NewApplier(m, au)
	do := func(ch core.TaskChange) core.TaskResult {
		t.Helper()
		p, err := core.PlanTaskChange(m, ch)
		if err != nil {
			t.Fatal(err)
		}
		res, err := ap.Apply(ctx, p.Confirm())
		if err != nil {
			t.Fatalf("%v: %v", ch.Op, err)
		}
		return res
	}
	p1 := 1
	created := do(core.TaskChange{Op: core.TaskCreate, Title: "e2e task", Description: "multi\nline", Priority: &p1})
	if created.ID == "" || created.Title != "e2e task" {
		t.Fatalf("created %+v", created)
	}
	r, err := m.Tasks(ctx)
	if err != nil || r.Counts.Open != 1 || len(r.Recent) != 1 {
		t.Fatalf("after create: %+v %v", r, err)
	}
	if u := do(core.TaskChange{Op: core.TaskUpdate, ID: created.ID, Title: "renamed", Status: "in_progress", IfStatus: "open"}); u.Title != "renamed" || u.Status != "in_progress" {
		t.Fatalf("update %+v", u)
	}
	if c := do(core.TaskChange{Op: core.TaskClose, ID: created.ID, Reason: "done"}); c.Status != "closed" {
		t.Fatalf("close %+v", c)
	}
	if r, _ = m.Tasks(ctx); r.Counts.Closed != 1 || r.Counts.Open != 0 {
		t.Fatalf("after close: %+v", r.Counts)
	}
	// A failing write surfaces bd's own error.
	p, _ := core.PlanTaskChange(m, core.TaskChange{Op: core.TaskClose, ID: "tt-nope"})
	if _, err := ap.Apply(ctx, p.Confirm()); err == nil {
		t.Fatal("closing a missing issue must fail")
	}
	if au.n != 8 {
		t.Fatalf("audit lines = %d, want 8 (attempt+result per write)", au.n)
	}
}

// The real `bd init` through the write gate, in an isolated temp dir and HOME. Skips without bd.
func TestRealBdInitCreatesTheManagedWorkspaceInAnIsolatedDir(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", home+"/s")
	t.Setenv("XDG_CONFIG_HOME", home+"/c")
	t.Setenv("BEADS_ACTOR", "abacus-test")
	t.Setenv("BEADS_DIR", "")
	t.Chdir(t.TempDir()) // a cwd with no tracker
	ws := home + "/d/abacus/workspace"
	ref := core.NewTrackerRef("")
	var saved string
	m := New(Options{InitDir: ws, Ref: ref, OnInit: func(d string) error { saved = d; return nil }})
	ctx := context.Background()
	if d := m.Detect(ctx); d.State != core.Ready {
		t.Fatalf("detect %+v", d)
	}
	if _, err := m.Tasks(ctx); err == nil {
		t.Fatal("no tracker yet: Tasks must ask for setup")
	}
	plan, err := core.PlanTaskChange(m, core.TaskChange{Op: core.TaskInit, Dir: ws})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.NewApplier(m, &memAudit{}).Apply(ctx, plan.Confirm()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if saved != ws || ref.Dir() != ws {
		t.Fatalf("saved=%q ref=%q", saved, ref.Dir())
	}
	r, err := m.Tasks(ctx)
	if err != nil || r.Counts.Total != 0 {
		t.Fatalf("a fresh workspace reads as empty: %+v %v", r, err)
	}
}

// A decoy BEADS_DIR pointing at another (isolated) tracker must not redirect init or reads.
func TestRealBdIgnoresADecoyBEADS_DIR(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", home+"/s")
	t.Setenv("XDG_CONFIG_HOME", home+"/c")
	t.Setenv("BEADS_ACTOR", "abacus-test")
	decoy := t.TempDir()
	for _, c := range [][]string{{"bd", "init", "--non-interactive", "--quiet", "--skip-agents", "--skip-hooks", "--prefix=decoy"},
		{"bd", "create", "decoy issue", "--json"}} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = decoy
		cmd.Env = append(os.Environ(), "BEADS_DIR=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot set up the decoy tracker (%v): %s", err, out)
		}
	}
	t.Setenv("BEADS_DIR", decoy+"/.beads")
	t.Setenv("BD_SOCKET", decoy+"/sock")
	t.Chdir(t.TempDir())
	ws := home + "/d/abacus/workspace"
	ref := core.NewTrackerRef("")
	m := New(Options{InitDir: ws, Ref: ref})
	ctx := context.Background()
	if d := m.Detect(ctx); d.State != core.Ready {
		t.Fatalf("detect %+v", d)
	}
	plan, err := core.PlanTaskChange(m, core.TaskChange{Op: core.TaskInit, Dir: ws})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.NewApplier(m, &memAudit{}).Apply(ctx, plan.Confirm()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !core.HasOwnTracker(ws) {
		t.Fatal("init must create the tracker in the workspace, not wherever BEADS_DIR points")
	}
	r, err := m.Tasks(ctx)
	if err != nil || r.Counts.Total != 0 {
		t.Fatalf("reads must see the workspace (empty), not the decoy with its issue: %+v %v", r, err)
	}
}
