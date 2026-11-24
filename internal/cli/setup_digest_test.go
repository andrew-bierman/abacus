package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// cascadeBd creates the workspace (which is what unblocks Queues); cascadeLambe is the Queues step.
type cascadeBd struct {
	*modtest.Fake
	ws  string
	ran int
}

func (b *cascadeBd) Domain() core.DomainID { return core.DomainTasks }
func (b *cascadeBd) Guide(context.Context) *core.Guide {
	if core.HasOwnTracker(b.ws) {
		return nil
	}
	return &core.Guide{Domain: core.DomainTasks, Step: core.StepWorkspace, Message: "No tracker here", Action: "create it"}
}
func (b *cascadeBd) TrackerDir() string { return "" }
func (b *cascadeBd) InitDir() string    { return b.ws }
func (b *cascadeBd) BuildTaskArgv(core.TaskChange) ([]string, error) {
	return []string{"bd", "init"}, nil
}
func (b *cascadeBd) RunTask(context.Context, []string) (core.TaskResult, error) {
	b.ran++
	if err := os.MkdirAll(filepath.Join(b.ws, ".beads"), 0o755); err != nil {
		return core.TaskResult{}, err
	}
	return core.TaskResult{Status: "initialized"}, os.WriteFile(filepath.Join(b.ws, ".beads", "metadata.json"), []byte("{}"), 0o600)
}

type cascadeLambe struct {
	*modtest.Fake
	ran int
}

func (*cascadeLambe) Domain() core.DomainID { return core.DomainQueues }
func (l *cascadeLambe) Guide(context.Context) *core.Guide {
	if l.ran > 0 {
		return nil
	}
	return &core.Guide{Domain: core.DomainQueues, Step: core.StepQueues, Message: "Queues need a supervisor config", Action: "set up"}
}
func (*cascadeLambe) BuildInitArgv(cfg string) ([]string, error) {
	return []string{"lambe", "init", "-config", cfg}, nil
}
func (l *cascadeLambe) RunInit(_ context.Context, _, cfg string) error {
	l.ran++
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cfg, []byte("x"), 0o600)
}

func cascadeSetup(t *testing.T, args ...string) (code int, out string, b *cascadeBd, l *cascadeLambe) {
	t.Helper()
	root := t.TempDir()
	t.Chdir(t.TempDir())
	b = &cascadeBd{Fake: ready("bd"), ws: filepath.Join(root, "ws")}
	l = &cascadeLambe{Fake: ready("lambe")}
	au := &memAudit{}
	var o, e bytes.Buffer
	cfg := core.ConfigStore{Path: filepath.Join(root, "c", "config.json")}
	code = cli.Run(append([]string{"setup", "--json"}, args...), cli.Deps{Out: &o, Err: &e, Version: "t", Config: cfg, Audit: au,
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(b, l) },
		Setup: func(entries []core.Entry) *core.Setup {
			return core.NewSetup(entries, core.SetupOptions{Config: cfg, Workspace: b.ws, LambeConfig: filepath.Join(root, "c", "lambe.yaml"), Audit: au})
		}})
	return code, o.String(), b, l
}

// Queues is blocked behind the workspace when the plan is reviewed. Creating the workspace unblocks it, but
// that step was never reviewed, so with --expect-digest it is not run; the workspace (reviewed) was.
func TestSetupWithADigestDoesNotRunAStepThatBecameRunnable(t *testing.T) {
	_, out, _, _ := cascadeSetup(t, "--dry-run")
	if !strings.Contains(out, `"blocked"`) {
		t.Fatalf("queues should be blocked at review: %s", out)
	}
	d := digestOf(t, out)
	code, out2, b, l := cascadeSetup(t, "--yes", "--expect-digest", d)
	if code != 1 || b.ran != 1 || l.ran != 0 || !strings.Contains(out2, `"code": "changed"`) || !strings.Contains(out2, "not run") {
		t.Fatalf("code=%d bd=%d lambe=%d\n%s", code, b.ran, l.ran, out2)
	}
	// without a digest the cascade still works as before
	code, o3, b, l := cascadeSetup(t, "--yes")
	if code != 0 || b.ran != 1 || l.ran != 1 {
		t.Fatalf("no digest: code=%d bd=%d lambe=%d\n%s", code, b.ran, l.ran, o3)
	}
}

// Blocked steps are in the digest with their reason.
func TestSetupDigestIncludesBlockedSteps(t *testing.T) {
	_, a, _, _ := cascadeSetup(t, "--dry-run")
	_, b, _, _ := cascadeSetup(t, "--dry-run")
	if digestOf(t, a) != digestOf(t, b) {
		t.Fatal("deterministic across runs")
	}
}
