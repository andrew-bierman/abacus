package lambe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

const doctorNoConfig = `[{"name":"config","status":"fail","detail":"no config at /x","fix":"run lambe init"}]`
const doctorWithConfig = `[{"name":"config","status":"ok","detail":"/x"}]`

func TestNoConfigAnywhereOffersTheQueuesSetupStep(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"doctor -json": {out: doctorNoConfig, exit: 1}, "status -json": {exit: 2}}}
	m := degradedModule(t, f, Options{})
	g := m.Guide(context.Background())
	if g == nil || g.Step != core.StepQueues || g.Line() != "Queues need a supervisor config — press s to set up" {
		t.Fatalf("%+v", g)
	}
	_, err := m.Queues(context.Background())
	if got, ok := core.AsNeedsSetup(err); !ok || got.Step != core.StepQueues {
		t.Fatalf("%v", err)
	}
	f.resp["doctor -json"] = result{out: doctorWithConfig}
	m.mu.Lock()
	m.guideAt = m.guideAt.Add(-guideTTL * 2) // the cached answer is stale
	m.mu.Unlock()
	if m.Guide(context.Background()) != nil {
		t.Fatal("a config exists: nothing to offer")
	}
	_, err = m.Queues(context.Background())
	var nc *core.NotConnectedError
	if !errors.As(err, &nc) || nc.Reason != NoDaemonReason || !strings.Contains(nc.HowToConnect, "lambe run") {
		t.Fatalf("a configured but stopped supervisor is a plain not-connected: %v", err)
	}
}

func TestAnExplicitConfigMeansNothingToSetUpAndNoDoctorCall(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"status -json -config /tmp/l.yml": {exit: 2}}}
	m := degradedModule(t, f, Options{ConfigPath: "/tmp/l.yml"})
	if m.Guide(context.Background()) != nil {
		t.Fatal("explicit config")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "doctor") {
			t.Fatalf("doctor must not run: %v", f.calls)
		}
	}
}

type dirRun struct {
	dir  string
	args []string
	exit int
	cfg  string // the file the fake init writes
}

func (d *dirRun) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	d.args = args
	if d.exit == 0 && d.cfg != "" {
		_ = os.WriteFile(d.cfg, []byte("harness: pi\n"), 0o600)
	}
	return nil, d.exit, nil
}

func TestRunInitRunsOnlyTheFixedArgvInTheWorkspaceAndAdoptsTheConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "lambe.yaml")
	ws := t.TempDir()
	dr := &dirRun{cfg: cfg}
	f := &fakeRunner{resp: map[string]result{}}
	m := degradedModule(t, f, Options{InitRunner: func(dir string) core.Runner {
		if dir != ws {
			t.Errorf("dir %q", dir)
		}
		return dr
	}})
	if err := m.RunInit(context.Background(), ws, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Join(dr.args, " ") != "init -non-interactive -config "+cfg {
		t.Fatalf("%v", dr.args)
	}
	if p, owned := m.config(); p != cfg || !owned || m.selector().ConfigPath != cfg {
		t.Fatalf("%q %v", p, owned)
	}
	if err := m.RunInit(context.Background(), ws, cfg); err == nil {
		t.Fatal("an existing config must never be overwritten")
	}
	if _, err := m.BuildInitArgv("relative.yaml"); err == nil {
		t.Fatal("relative path")
	}
}

func TestRunInitFailureAdoptsNothing(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "lambe.yaml")
	m := degradedModule(t, &fakeRunner{resp: map[string]result{}}, Options{InitRunner: func(string) core.Runner { return &dirRun{exit: 1} }})
	if err := m.RunInit(context.Background(), t.TempDir(), cfg); err == nil {
		t.Fatal("want an error")
	}
	if p, _ := m.config(); p != "" {
		t.Fatalf("config %q", p)
	}
}
