package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// guidedLeases is a Leases provider that needs an actor name until one is configured.
type guidedLeases struct{ *modtest.Fake }

func (guidedLeases) Domain() core.DomainID { return core.DomainLeases }
func (guidedLeases) Guide(context.Context) *core.Guide {
	return &core.Guide{Domain: core.DomainLeases, Step: core.StepActor, Message: "Leases need an actor name", Action: "choose one"}
}

type setupRun struct {
	code     int
	out, err string
	cfg      core.ConfigStore
	audit    *memAudit
}

func runSetupCmd(t *testing.T, in string, args ...string) setupRun {
	t.Helper()
	root := t.TempDir()
	r := setupRun{cfg: core.ConfigStore{Path: filepath.Join(root, "config.json")}, audit: &memAudit{}}
	mods := []core.Module{guidedLeases{ready("beads-lease")}}
	var o, e bytes.Buffer
	d := cli.Deps{Out: &o, Err: &e, Version: "test", Config: r.cfg, Audit: r.audit,
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(mods...) },
		Setup: func(entries []core.Entry) *core.Setup {
			return core.NewSetup(entries, core.SetupOptions{Config: r.cfg, Audit: r.audit, Suggest: func() string { return "me-mac" }})
		}}
	if in != "" {
		d.In = strings.NewReader(in)
	}
	r.code = cli.Run(args, d)
	r.out, r.err = o.String(), e.String()
	return r
}

func TestSetupReportsWithoutChangingAnythingByDefault(t *testing.T) {
	r := runSetupCmd(t, "", "setup")
	c, _ := r.cfg.Load()
	if r.code != 0 || c.Actor != "" || r.audit.n != 0 {
		t.Fatalf("a bare `setup` with no input must change nothing: %+v audit=%d", c, r.audit.n)
	}
	for _, want := range []string{"needed", "Leases need an actor name", "abacus config set actor me-mac", "setup --yes"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("output missing %q:\n%s", want, r.out)
		}
	}
}

func TestSetupJSONIsOneDocumentAndNeverPromptsOrApplies(t *testing.T) {
	r := runSetupCmd(t, "y\n", "setup", "--json")
	var d struct {
		SchemaVersion int             `json:"schema_version"`
		Steps         []core.StepInfo `json:"steps"`
	}
	if err := json.Unmarshal([]byte(r.out), &d); err != nil || d.SchemaVersion != 1 || len(d.Steps) != 3 {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if c, _ := r.cfg.Load(); c.Actor != "" {
		t.Fatal("--json without --yes must not apply")
	}
}

func TestSetupYesAppliesThroughTheGateAndAudits(t *testing.T) {
	r := runSetupCmd(t, "", "setup", "--yes")
	if c, _ := r.cfg.Load(); r.code != 0 || c.Actor != "me-mac" || r.audit.n != 2 {
		t.Fatalf("code=%d cfg=%+v audit=%d\n%s%s", r.code, c, r.audit.n, r.out, r.err)
	}
	if !strings.Contains(r.out, "done:") {
		t.Fatalf("%s", r.out)
	}
}

func TestSetupDryRunChangesNothingEvenWithYes(t *testing.T) {
	r := runSetupCmd(t, "", "setup", "--yes", "--dry-run")
	if c, _ := r.cfg.Load(); c.Actor != "" || r.audit.n != 0 {
		t.Fatalf("%+v %d", c, r.audit.n)
	}
}

func TestSetupPromptsAndHonoursNo(t *testing.T) {
	if r := runSetupCmd(t, "n\n", "setup"); func() bool { c, _ := r.cfg.Load(); return c.Actor != "" }() {
		t.Fatal("n must not apply")
	}
	if r := runSetupCmd(t, "y\n", "setup"); func() bool { c, _ := r.cfg.Load(); return c.Actor != "me-mac" }() {
		t.Fatalf("y should apply\n%s", r.out)
	}
}

func TestConfigSetGetShow(t *testing.T) {
	root := t.TempDir()
	st := core.ConfigStore{Path: filepath.Join(root, "c.json")}
	do := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := cli.Run(append([]string{"config"}, args...), cli.Deps{Out: &o, Err: &e, Config: st})
		return code, o.String(), e.String()
	}
	if code, _, e := do("set", "actor", "fleet-a1"); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	if code, out, _ := do("get", "actor"); code != 0 || strings.TrimSpace(out) != "fleet-a1" {
		t.Fatalf("%d %q", code, out)
	}
	if code, out, _ := do("show", "--json"); code != 0 || !strings.Contains(out, `"actor": "fleet-a1"`) {
		t.Fatalf("%d %s", code, out)
	}
	for _, bad := range [][]string{{"set", "actor", "has space"}, {"set", "nope", "x"}, {"get", "nope"}, {"set", "actor"}, {}} {
		if code, _, _ := do(bad...); code != core.ExitUsage {
			t.Errorf("%v should be a usage error, got %d", bad, code)
		}
	}
}

// guidedQueues needs setup but there is no Lambe initializer, so its step cannot be planned.
type guidedQueues struct{ *modtest.Fake }

func (guidedQueues) Domain() core.DomainID { return core.DomainQueues }
func (guidedQueues) Guide(context.Context) *core.Guide {
	return &core.Guide{Domain: core.DomainQueues, Step: core.StepQueues, Message: "Queues need a supervisor config", Action: "set up"}
}

func TestSetupYesExitsOneWhenANeededStepCannotBePlanned(t *testing.T) {
	root := t.TempDir()
	cfg := core.ConfigStore{Path: filepath.Join(root, "config.json")}
	for _, c := range []struct {
		args []string
		want int
	}{{[]string{"setup", "--yes"}, core.ExitFail}, {[]string{"setup"}, core.ExitOK}} {
		var o, e bytes.Buffer
		code := cli.Run(c.args, cli.Deps{Out: &o, Err: &e, Version: "test", Config: cfg, Audit: &memAudit{},
			Build: func(cli.Options) *core.Registry { return core.NewRegistry(guidedQueues{ready("lambe")}) },
			Setup: func(en []core.Entry) *core.Setup {
				return core.NewSetup(en, core.SetupOptions{Config: cfg, Audit: &memAudit{}})
			}})
		if code != c.want {
			t.Errorf("%v: exit %d, want %d\n%s%s", c.args, code, c.want, o.String(), e.String())
		}
	}
}

func TestConfigSetExitCodesSeparateUsageFromWriteFailures(t *testing.T) {
	root := t.TempDir()
	do := func(path string, args ...string) int {
		var o, e bytes.Buffer
		return cli.Run(append([]string{"config"}, args...), cli.Deps{Out: &o, Err: &e, Config: core.ConfigStore{Path: path}})
	}
	good := filepath.Join(root, "c.json")
	for _, bad := range [][]string{{"set", "nope", "x"}, {"set", "actor", "has space"}, {"set", "tracker_dir", "relative"}} {
		if got := do(good, bad...); got != core.ExitUsage {
			t.Errorf("%v: exit %d, want usage (2)", bad, got)
		}
	}
	// A valid change that cannot be written (the parent is a file) is a failure, not a usage error.
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := do(filepath.Join(blocker, "sub", "c.json"), "set", "actor", "fine-1"); got != core.ExitFail {
		t.Errorf("an unwritable config: exit %d, want failure (1)", got)
	}
	// A corrupt file is kept aside and the change goes through.
	corrupt := filepath.Join(root, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := do(corrupt, "set", "actor", "fine-1"); got != core.ExitOK {
		t.Errorf("corrupt file: exit %d", got)
	}
	if _, err := os.Stat(corrupt + ".corrupt"); err != nil {
		t.Errorf("the corrupt file should be kept: %v", err)
	}
}

// repairBd is a Tasks provider that can create the workspace; it only counts the init.
type repairBd struct {
	*modtest.Fake
	dir  string
	runs int
}

func (b *repairBd) Domain() core.DomainID { return core.DomainTasks }
func (b *repairBd) Guide(context.Context) *core.Guide {
	return &core.Guide{Domain: core.DomainTasks, Step: core.StepRepair, Message: "Your fleet workspace was only partly created", Action: "repair it"}
}
func (b *repairBd) TrackerDir() string { return "" }
func (b *repairBd) InitDir() string    { return b.dir }
func (b *repairBd) BuildTaskArgv(core.TaskChange) ([]string, error) {
	return []string{"bd", "init"}, nil
}
func (b *repairBd) RunTask(context.Context, []string) (core.TaskResult, error) {
	b.runs++
	return core.TaskResult{Status: "initialized"}, nil
}

func runRepairCmd(t *testing.T, ws string, extra ...string) (int, string, string, *repairBd, *memAudit) {
	t.Helper()
	t.Chdir(t.TempDir())
	bd := &repairBd{Fake: ready("bd"), dir: ws}
	au := &memAudit{}
	var o, e bytes.Buffer
	d := cli.Deps{Out: &o, Err: &e, Version: "test", Audit: au,
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(bd) },
		Setup: func(entries []core.Entry) *core.Setup {
			return core.NewSetup(entries, core.SetupOptions{Workspace: ws, Audit: au})
		}}
	code := cli.Run(append([]string{"setup", "--repair"}, extra...), d)
	return code, o.String(), e.String(), bd, au
}

func halfWorkspace(t *testing.T) string {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(filepath.Join(ws, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".beads", ".gitignore"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestSetupRepairShowsThePlanAndChangesNothingWithoutAYes(t *testing.T) {
	for _, args := range [][]string{{"--dry-run"}, {"--dry-run", "--yes"}, {}, {"--json"}} {
		ws := halfWorkspace(t)
		code, out, _, bd, au := runRepairCmd(t, ws, args...)
		if _, err := os.Stat(filepath.Join(ws, ".beads", ".gitignore")); err != nil || bd.runs != 0 || au.n != 0 {
			t.Fatalf("%v changed something", args)
		}
		wantCode := core.ExitFail // no yes, no terminal: asked for confirmation
		if len(args) > 0 && args[0] == "--dry-run" {
			wantCode = core.ExitOK
		}
		if code != wantCode || (len(args) != 1 || args[0] != "--json") && !strings.Contains(out, ".beads.incomplete-") {
			t.Fatalf("%v: code=%d\n%s", args, code, out)
		}
	}
}

func TestSetupRepairYesSetsItAsideAndRefusesAFolderWithUserFiles(t *testing.T) {
	ws := halfWorkspace(t)
	code, out, errs, bd, au := runRepairCmd(t, ws, "--yes")
	if code != 0 || bd.runs != 1 || au.n < 2 || !strings.Contains(out, "done:") {
		t.Fatalf("code=%d runs=%d audit=%d\n%s%s", code, bd.runs, au.n, out, errs)
	}
	if m, _ := filepath.Glob(filepath.Join(ws, ".beads.incomplete-*", ".gitignore")); len(m) != 1 {
		t.Fatal("the unfinished files must be kept")
	}

	ws = halfWorkspace(t)
	os.WriteFile(filepath.Join(ws, "mine.txt"), []byte("x"), 0o600)
	code, _, errs, bd, au = runRepairCmd(t, ws, "--yes")
	if code == 0 || bd.runs != 0 || au.n != 0 || !strings.Contains(errs, "refusing") {
		t.Fatalf("code=%d runs=%d audit=%d %s", code, bd.runs, au.n, errs)
	}
	if _, err := os.Stat(filepath.Join(ws, ".beads", ".gitignore")); err != nil {
		t.Fatal("a refused repair must leave everything where it was")
	}
}
