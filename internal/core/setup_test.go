package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

type recAudit struct{ entries []core.AuditEntry }

func (r *recAudit) Record(e core.AuditEntry) error { r.entries = append(r.entries, e); return nil }

type failAudit struct{}

func (failAudit) Record(core.AuditEntry) error { return errors.New("disk full") }

// fakeBd is a Tasks module that can create the workspace: it counts what actually ran.
type fakeBd struct {
	*modtest.Fake
	initDir string
	ran     [][]string
}

func (f *fakeBd) Domain() core.DomainID { return core.DomainTasks }
func (f *fakeBd) Guide(context.Context) *core.Guide {
	return &core.Guide{Domain: core.DomainTasks, Step: core.StepWorkspace, Key: "i", Message: "No tracker here", Action: "create your fleet workspace (bd init)"}
}
func (f *fakeBd) TrackerDir() string { return "" }
func (f *fakeBd) InitDir() string    { return f.initDir }
func (f *fakeBd) BuildTaskArgv(ch core.TaskChange) ([]string, error) {
	return []string{"bd", "init", "--prefix=x"}, nil
}
func (f *fakeBd) RunTask(_ context.Context, argv []string) (core.TaskResult, error) {
	f.ran = append(f.ran, argv)
	return core.TaskResult{Title: "fleet workspace in " + f.initDir, Status: "initialized"}, nil
}

type fakeLambe struct {
	*modtest.Fake
	needs bool
	ran   []string
}

func (f *fakeLambe) Domain() core.DomainID { return core.DomainQueues }
func (f *fakeLambe) Guide(context.Context) *core.Guide {
	if f.needs {
		return &core.Guide{Domain: core.DomainQueues, Step: core.StepQueues, Key: "s", Message: "Queues need a supervisor config", Action: "set up"}
	}
	return nil
}
func (f *fakeLambe) BuildInitArgv(cfg string) ([]string, error) {
	return []string{"lambe", "init", "-non-interactive", "-config", cfg}, nil
}
func (f *fakeLambe) RunInit(_ context.Context, dir, cfg string) error {
	f.ran = []string{dir, cfg}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cfg, []byte("harness: pi\n"), 0o600)
}

type fakeLeases struct {
	*modtest.Fake
	needs bool
}

func (f *fakeLeases) Domain() core.DomainID { return core.DomainLeases }
func (f *fakeLeases) Guide(context.Context) *core.Guide {
	if f.needs {
		return &core.Guide{Domain: core.DomainLeases, Step: core.StepActor, Key: "s", Message: "Leases need an actor name", Action: "choose one"}
	}
	return nil
}

func ready(m core.Module) core.Entry {
	return core.Entry{Module: m, Detection: core.Detection{State: core.Ready}}
}

type setupEnv struct {
	s      *core.Setup
	bd     *fakeBd
	lambe  *fakeLambe
	audit  *recAudit
	cfg    core.ConfigStore
	ws, lc string
}

func newSetupEnv(t *testing.T) setupEnv {
	t.Helper()
	root := t.TempDir()
	e := setupEnv{
		bd:    &fakeBd{Fake: &modtest.Fake{IDStr: "bd"}, initDir: filepath.Join(root, "ws")},
		lambe: &fakeLambe{Fake: &modtest.Fake{IDStr: "lambe"}, needs: true},
		audit: &recAudit{},
		cfg:   core.ConfigStore{Path: filepath.Join(root, "c", "config.json")},
		ws:    filepath.Join(root, "ws"),
		lc:    filepath.Join(root, "c", "lambe.yaml"),
	}
	leases := &fakeLeases{Fake: &modtest.Fake{IDStr: "beads-lease"}, needs: true}
	e.s = core.NewSetup([]core.Entry{ready(e.bd), ready(e.lambe), ready(leases)}, core.SetupOptions{
		Config: e.cfg, Workspace: e.ws, LambeConfig: e.lc, Audit: e.audit, Suggest: func() string { return "me-mac" }})
	return e
}

func TestSetupStepsAreOfferedInOrderWithTheirExactCommand(t *testing.T) {
	e := newSetupEnv(t)
	steps := e.s.Steps(context.Background())
	if len(steps) != 3 || steps[0].Step != core.StepWorkspace || steps[1].Step != core.StepQueues || steps[2].Step != core.StepActor {
		t.Fatalf("%+v", steps)
	}
	if steps[0].State != "needed" || steps[0].Command != "bd init --prefix=x" {
		t.Fatalf("%+v", steps[0])
	}
	// Queues needs the workspace first, and says so instead of failing later.
	if steps[1].State != "blocked" || !strings.Contains(steps[1].Message, "workspace first") {
		t.Fatalf("%+v", steps[1])
	}
	if steps[2].State != "needed" || steps[2].Command != "abacus config set actor me-mac" {
		t.Fatalf("%+v", steps[2])
	}
}

func TestNothingRunsWithoutAConfirmedPlanAndEachPlanRunsOnce(t *testing.T) {
	e := newSetupEnv(t)
	p, err := e.s.Plan(core.StepActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Apply(context.Background(), p); !errors.Is(err, core.ErrNotConfirmed) {
		t.Fatalf("an unconfirmed plan must not run: %v", err)
	}
	if c, _ := e.cfg.Load(); c.Actor != "" {
		t.Fatal("an unconfirmed plan changed the config")
	}
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Apply(context.Background(), p.Confirm()); !errors.Is(err, core.ErrAlreadyApplied) {
		t.Fatalf("a plan is single use: %v", err)
	}
	if c, _ := e.cfg.Load(); c.Actor != "me-mac" {
		t.Fatalf("%+v", c)
	}
	if len(e.audit.entries) != 2 || e.audit.entries[0].Phase != "attempt" || e.audit.entries[1].Phase != "ok" || e.audit.entries[0].Verb != "setup-actor" || e.audit.entries[0].Domain != "leases" {
		t.Fatalf("%+v", e.audit.entries)
	}
}

func TestAFailedAuditBlocksTheStep(t *testing.T) {
	e := newSetupEnv(t)
	s := core.NewSetup([]core.Entry{ready(e.bd), ready(e.lambe)}, core.SetupOptions{Config: e.cfg, Workspace: e.ws, LambeConfig: e.lc, Audit: failAudit{}})
	p, _ := s.Plan(core.StepActor)
	if _, err := s.Apply(context.Background(), p.Confirm()); err == nil {
		t.Fatal("must fail closed")
	}
	if c, _ := e.cfg.Load(); c.Actor != "" {
		t.Fatal("the step ran without an audit record")
	}
}

func TestWorkspaceStepGoesThroughTheTasksWriteGate(t *testing.T) {
	e := newSetupEnv(t)
	p, err := e.s.Plan(core.StepWorkspace)
	if err != nil || strings.Join(p.Lines, "|") == "" {
		t.Fatalf("%v", err)
	}
	if _, err := e.s.Apply(context.Background(), p); !errors.Is(err, core.ErrNotConfirmed) || len(e.bd.ran) != 0 {
		t.Fatalf("unconfirmed: %v ran=%v", err, e.bd.ran)
	}
	msg, err := e.s.Apply(context.Background(), p.Confirm())
	if err != nil || len(e.bd.ran) != 1 || !strings.Contains(msg, "fleet workspace") {
		t.Fatalf("%q %v %v", msg, err, e.bd.ran)
	}
	if len(e.audit.entries) < 2 || e.audit.entries[0].Verb != "init" || e.audit.entries[0].Domain != "tasks" {
		t.Fatalf("the audit log should show a tasks init: %+v", e.audit.entries)
	}
}

func TestQueuesStepWritesOnlyTheAbacusOwnedConfigInsideTheWorkspace(t *testing.T) {
	e := newSetupEnv(t)
	if _, err := e.s.Plan(core.StepQueues); err == nil {
		t.Fatal("no workspace yet: the step must refuse")
	}
	if err := os.MkdirAll(filepath.Join(e.ws, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.ws, ".beads", "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := e.s.Plan(core.StepQueues)
	if err != nil || !strings.Contains(strings.Join(p.Lines, "\n"), "nothing is started") {
		t.Fatalf("%v", err)
	}
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err != nil {
		t.Fatal(err)
	}
	if e.lambe.ran[0] != e.ws || e.lambe.ran[1] != e.lc {
		t.Fatalf("lambe init ran in %v", e.lambe.ran)
	}
	if c, _ := e.cfg.Load(); c.LambeConfig != e.lc {
		t.Fatalf("%+v", c)
	}
}

func TestSuggestedActorIsAValidName(t *testing.T) {
	if err := (&core.Config{}).Set("actor", core.SuggestActor()); err != nil {
		t.Fatalf("%q: %v", core.SuggestActor(), err)
	}
}

func TestNoToolMeansTheStepIsUnavailableNotNeeded(t *testing.T) {
	s := core.NewSetup(nil, core.SetupOptions{Audit: &recAudit{}})
	for _, st := range s.Steps(context.Background()) {
		if st.State != "unavailable" {
			t.Errorf("%+v", st)
		}
	}
}
