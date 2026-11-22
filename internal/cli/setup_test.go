package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	return &core.Guide{Domain: core.DomainLeases, Step: core.StepActor, Key: "s", Message: "Leases need an actor name", Action: "choose one"}
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
