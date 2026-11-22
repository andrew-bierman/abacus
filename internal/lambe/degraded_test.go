package lambe

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

const versionOK = `{"version":"v0.10.0","api_version":1}`

// degradedModule is detected, has no live daemon (empty state dir) and probes nothing about
// processes: a tracker read has no daemon, so any process probe is a bug.
func degradedModule(t *testing.T, f *fakeRunner, o Options) *Module {
	t.Helper()
	f.resp["version --json"] = result{out: versionOK}
	o.StateDir = t.TempDir()
	o.PidAlive = func(int) bool { t.Error("pid probed on a tracker-only read"); return false }
	o.Identity = func(int) (string, error) { t.Error("identity probed on a tracker-only read"); return "", errors.New("no") }
	m := newModule(f, o)
	if d := m.Detect(context.Background()); d.State != core.Ready {
		t.Fatal(d)
	}
	return m
}

func TestNoDaemonFallsBackToLabelledTrackerSubset(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"status -json": {out: string(trackerFixture(t))}}}
	m := degradedModule(t, f, Options{})
	st := m.Status(context.Background())
	if st.Unavailable != "" || st.Source != SourceTracker {
		t.Fatalf("status = %+v, want a tracker-subset status", st)
	}
	if !strings.Contains(st.Summary, "tracker subset") || !strings.Contains(st.Summary, "2 runs") || !strings.Contains(st.Summary, "1 need attention") {
		t.Fatalf("summary = %q", st.Summary)
	}
	if len(st.Lines) != 2 || !strings.HasPrefix(st.Lines[0], "T-7  running") {
		t.Fatalf("lines = %q", st.Lines)
	}
	v, err := m.Read(context.Background())
	if err != nil || v.Source != SourceTracker || len(v.Queue.Rows) != 0 || v.Snapshot.Daemon.PID != 0 {
		t.Fatalf("view = %+v err = %v", v, err)
	}
}

func TestTrackerReadPassesTheSelectedConfig(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"status -json -config /tmp/l.yml": {out: string(trackerFixture(t))}}}
	m := degradedModule(t, f, Options{ConfigPath: "/tmp/l.yml"})
	if st := m.Status(context.Background()); st.Source != SourceTracker {
		t.Fatalf("status = %+v", st)
	}
}

func TestFailedTrackerReadKeepsTheNoDaemonSentinelAndNeverInventsState(t *testing.T) {
	cases := map[string]result{
		"exit 1 nothing on stdout": {exit: 1},
		"exit 2 usage":             {exit: 2},
		"exit 0 not json":          {out: "oops"},
		"exit 0 empty":             {},
		"exit 1 with a body":       {out: string(trackerFixture(t)), exit: 1},
		"unsupported schema":       {out: `{"schema_version":2,"runs":[]}`},
		"missing schema":           {out: `{"runs":[]}`},
		"trailing garbage":         {out: string(trackerFixture(t)) + `{"x":1}`},
		"json array":               {out: `[]`},
		"runner error":             {err: errors.New("boom")},
	}
	for name, r := range cases {
		f := &fakeRunner{resp: map[string]result{"status -json": r}}
		m := degradedModule(t, f, Options{})
		if _, err := m.Read(context.Background()); !errors.Is(err, ErrNoDaemon) {
			t.Errorf("%s: want ErrNoDaemon, got %v", name, err)
		}
		if st := m.Status(context.Background()); st.Unavailable == "" || st.Source != "" || st.Summary != "" {
			t.Errorf("%s: status = %+v, want plain unavailable", name, st)
		}
	}
}

func TestAmbiguousDaemonsNeverDegrade(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"version --json": {out: versionOK}, "status -json": {out: string(trackerFixture(t))}}}
	dir := t.TempDir()
	writeDescriptor(t, dir, "stateapi-a.json", baseDesc("lambe-a", "/a.yaml"))
	writeDescriptor(t, dir, "stateapi-b.json", baseDesc("lambe-b", "/b.yaml"))
	m := newModule(f, Options{StateDir: dir, PidAlive: func(int) bool { return true }, Identity: func(int) (string, error) { return "id-ok", nil }})
	m.Detect(context.Background())
	if _, err := m.Read(context.Background()); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ErrAmbiguous, got %v", err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "status") {
			t.Fatalf("ambiguity must not be papered over with a tracker read: %v", f.calls)
		}
	}
}

func TestLiveDaemonNeverTriggersATrackerRead(t *testing.T) {
	var posts atomic.Int32
	srv := fixtureServer(t, &posts)
	f := &fakeRunner{resp: map[string]result{"version --json": {out: versionOK}}}
	dir := t.TempDir()
	d := baseDesc("lambe-live", "")
	d.Addr, d.BearerToken = strings.TrimPrefix(srv.URL, "http://"), secret
	writeDescriptor(t, dir, "stateapi-live.json", d)
	m := newModule(f, Options{StateDir: dir, PidAlive: func(int) bool { return true }, Identity: func(int) (string, error) { return "id-ok", nil }})
	m.Detect(context.Background())
	st := m.Status(context.Background())
	if st.Source != "" || st.Unavailable != "" {
		t.Fatalf("a live read is unlabelled: %+v", st)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "status") {
			t.Fatalf("live daemon but ran %q", c)
		}
	}
}

func TestTrackerViewIsSanitized(t *testing.T) {
	body := `{"schema_version":1,"daemon":{"pid":0},"runs":[{"issue":` + jsonStr(hostile()) + `,"state":"running"}],"needs_human":[{"issue":"T-1","reason":` + jsonStr(hostile()) + `}]}`
	m := degradedModule(t, &fakeRunner{resp: map[string]result{"status -json": {out: body}}}, Options{})
	st := m.Status(context.Background())
	controlFree(t, "summary", st.Summary)
	for _, l := range st.Lines {
		controlFree(t, "line", l)
	}
	v, _ := m.Read(context.Background())
	controlFree(t, "needs_human", v.Snapshot.NeedsHuman[0].Reason)
}

// --- state dir from `lambe state-dir -json` ---

func TestStateDirComesFromLambeWhenUnsetAndIsCached(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // the re-derived default must not be what is used
	dir := t.TempDir()
	f := &fakeRunner{resp: map[string]result{
		"version --json":  {out: versionOK},
		"state-dir -json": {out: `{"state_dir":` + jsonStr(dir) + `,"source":"env","exists":true,"live_dir":"x","descriptors":[]}`},
	}}
	m := newModule(f, Options{})
	m.Detect(context.Background())
	for i := 0; i < 2; i++ {
		if got := m.stateDir(context.Background()); got != dir {
			t.Fatalf("stateDir = %q, want %q", got, dir)
		}
	}
	n := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c, "state-dir -json") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("state-dir ran %d times, want once (cached): %v", n, f.calls)
	}
}

func TestExplicitStateDirWinsAndRunsNothing(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"version --json": {out: versionOK}}}
	m := newModule(f, Options{StateDir: "/explicit/dir"})
	m.Detect(context.Background())
	if got := m.stateDir(context.Background()); got != "/explicit/dir" {
		t.Fatalf("stateDir = %q", got)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestStateDirFallsBackToTheDerivedDefault(t *testing.T) {
	x := t.TempDir()
	t.Setenv("XDG_STATE_HOME", x)
	want := DefaultStateDir()
	bad := map[string]result{
		"command failed":  {exit: 2},
		"runner error":    {err: errors.New("boom")},
		"not json":        {out: "oops"},
		"empty state_dir": {out: `{"state_dir":""}`},
		"relative path":   {out: `{"state_dir":"relative/dir"}`},
		"control chars":   {out: `{"state_dir":"/a\u001b[2J"}`},
	}
	for name, r := range bad {
		f := &fakeRunner{resp: map[string]result{"version --json": {out: versionOK}, "state-dir -json": r}}
		m := newModule(f, Options{})
		m.Detect(context.Background())
		if got := m.stateDir(context.Background()); got != want {
			t.Errorf("%s: stateDir = %q, want fallback %q", name, got, want)
		}
	}
	// Never detected: no binary, so nothing may run.
	f := &fakeRunner{resp: map[string]result{}}
	if got := newModule(f, Options{}).stateDir(context.Background()); got != want || len(f.calls) != 0 {
		t.Fatalf("undetected module: %q calls=%v", got, f.calls)
	}
}

// --- allowlist ---

func TestOnlyTheReadOnlyLambeArgvsAreAllowed(t *testing.T) {
	ok := [][]string{
		{"version", "--json"}, {"state-dir", "-json"}, {"doctor", "-json"}, {"status", "-json"},
		{"doctor", "-json", "-config", "/x.yml"}, {"status", "-json", "-config", "/x.yml"},
	}
	for _, a := range ok {
		if !allowedArgv(a) {
			t.Errorf("%v refused", a)
		}
	}
	bad := [][]string{
		{}, {"version"}, {"state-dir"}, {"status"}, {"doctor"}, {"run"}, {"serve"}, {"init"}, {"daemon"},
		{"version", "--json", "x"}, {"state-dir", "-json", "-config", "/x"}, {"status", "-json", "-config"},
		{"status", "-json", "-config", ""}, {"status", "-json", "-refresh"}, {"doctor", "-json", "-fix"},
		{"status", "-config", "/x", "-json"}, {"-json", "status"}, {"state-dir", "-json", "x"},
	}
	for _, a := range bad {
		if allowedArgv(a) {
			t.Errorf("%v allowed", a)
		}
	}
}

func TestRunnerRefusesDisallowedArgvBeforeTheInnerRunner(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{}}
	m := newModule(f, Options{})
	for _, a := range [][]string{{"run", "T-1"}, {"serve"}, {"status"}, {"refresh"}} {
		if _, _, err := m.o.Runner.Run(context.Background(), "/fake/lambe", a...); err == nil {
			t.Errorf("%v reached the runner", a)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("inner runner saw %v", f.calls)
	}
}

func TestEveryExecutedLambeArgvIsAllowlisted(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"status -json": {out: string(trackerFixture(t))}, "state-dir -json": {out: `{"state_dir":"/nonexistent-abacus-dir"}`}, "doctor -json": {out: "[]"}}}
	m := degradedModule(t, f, Options{})
	m.o.StateDir = ""
	m.Doctor(context.Background())
	m.Status(context.Background())
	for _, c := range f.calls {
		if !allowedArgv(strings.Fields(strings.TrimPrefix(c, "/fake/lambe "))) {
			t.Errorf("executed %q", c)
		}
	}
}

func trackerFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/tracker/status.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
