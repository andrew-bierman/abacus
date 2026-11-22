package lambe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

const doctorNoConfig = `[{"name":"config","status":"fail","detail":"no config at /x","fix":"run lambe init"}]`
const doctorWithConfig = `[{"name":"config","status":"ok","detail":"/x"}]`

// guideModule is a detected module with no state dir or config of the user's own (those make
// the module offer nothing), isolated through XDG_STATE_HOME.
func guideModule(t *testing.T, f *fakeRunner) *Module {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f.resp["version --json"] = result{out: versionOK}
	m := newModule(f, Options{})
	if d := m.Detect(context.Background()); d.State != core.Ready {
		t.Fatal(d)
	}
	return m
}

func TestNoConfigAnywhereOffersTheQueuesSetupStep(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"doctor -json": {out: doctorNoConfig, exit: 1}, "status -json": {exit: 2}}}
	m := guideModule(t, f)
	g := m.Guide(context.Background())
	if g == nil || g.Step != core.StepQueues || g.Message != "Queues need a supervisor config" || g.Action != "set up" {
		t.Fatalf("%+v", g)
	}
	_, err := m.Queues(context.Background())
	if got, ok := core.AsNeedsSetup(err); !ok || got.Step != core.StepQueues {
		t.Fatalf("%v", err)
	}
	f.resp["doctor -json"] = result{out: doctorWithConfig}
	m.mu.Lock()
	m.guideAt = m.guideAt.Add(-guideTTL * 2) // a "needs setup" answer goes stale
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

func TestANegativeGuideAnswerIsKeptUntilSomethingChanges(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"doctor -json": {out: doctorWithConfig}}}
	m := guideModule(t, f)
	if m.Guide(context.Background()) != nil {
		t.Fatal("a config exists")
	}
	f.resp["doctor -json"] = result{out: doctorNoConfig, exit: 1}
	m.mu.Lock()
	m.guideAt = m.guideAt.Add(-guideTTL * 10)
	m.mu.Unlock()
	calls := len(f.calls)
	if m.Guide(context.Background()) != nil || len(f.calls) != calls {
		t.Fatalf("a settled answer must not be probed again: calls %v", f.calls[calls:])
	}
	m.InvalidateGuide()
	if m.Guide(context.Background()) == nil {
		t.Fatal("after an invalidate the module asks again")
	}
}

func TestAStateDirOrConfigChosenByTheUserMeansNoStarterConfigIsOffered(t *testing.T) {
	for name, o := range map[string]Options{"explicit config": {ConfigPath: "/tmp/mine.yaml"}, "state dir": {StateDir: t.TempDir()}} {
		f := &fakeRunner{resp: map[string]result{"version --json": {out: versionOK}}}
		m := newModule(f, o)
		m.Detect(context.Background())
		if m.Guide(context.Background()) != nil {
			t.Errorf("%s: must not redirect to a starter config", name)
		}
		for _, c := range f.calls {
			if strings.Contains(c, "doctor") {
				t.Errorf("%s: doctor must not run: %v", name, f.calls)
			}
		}
	}
}

func TestTheGuideProbeNeverRunsTwiceAtOnce(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := &blockingRunner{started: started, release: release, out: doctorNoConfig}
	m := New(Options{Runner: f, LookPath: found})
	m.SetFound(core.Found{Bin: "/fake/lambe"})
	done := make(chan *core.Guide)
	go func() { done <- m.Guide(context.Background()) }()
	<-started
	if g := m.Guide(context.Background()); g != nil { // in flight: answers from cache, does not probe
		t.Fatalf("%+v", g)
	}
	close(release)
	if g := <-done; g == nil {
		t.Fatal("the first probe should have found the missing config")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("doctor ran %d times", f.calls.Load())
	}
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	out     string
	calls   atomic.Int32
}

func (b *blockingRunner) Run(context.Context, string, ...string) ([]byte, int, error) {
	if b.calls.Add(1) == 1 {
		close(b.started)
		<-b.release
	}
	return []byte(b.out), 1, nil
}
