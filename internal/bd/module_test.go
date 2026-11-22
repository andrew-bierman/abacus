package bd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

type fakeRun struct {
	resp  map[string]string // joined argv -> stdout
	exit  map[string]int
	calls []string
}

func (f *fakeRun) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	k := strings.Join(args, " ")
	f.calls = append(f.calls, k)
	out, ok := f.resp[k]
	if !ok {
		return nil, -1, fmt.Errorf("unexpected argv %q", k)
	}
	return []byte(out), f.exit[k], nil
}

func found(string) (string, error) { return "/fake/bd", nil }

const (
	verJSON    = `{"version":"1.3.4","schema_version":1}`
	statusJSON = `{"schema_version":1,"summary":{"total_issues":5,"open_issues":2,"in_progress_issues":1,"blocked_issues":1,"closed_issues":1,"deferred_issues":0,"ready_issues":2}}`
	listJSON   = `[{"id":"t-2","title":"newer\u001b[2J","status":"in_progress","priority":1,"issue_type":"task","assignee":"bob","updated_at":"2026-11-22T05:00:00Z"},{"id":"t-1","title":"older","status":"open","priority":2,"issue_type":"bug","updated_at":"2026-11-21T05:00:00Z"}]`
)

func newFake() *fakeRun {
	return &fakeRun{resp: map[string]string{
		"--readonly version --json":                                       verJSON,
		"--readonly status --json --no-activity":                          statusJSON,
		"--readonly list --json --brief --all --sort updated --flat -n 8": listJSON,
	}}
}

func mod(f *fakeRun, o Options) *Module {
	o.Runner, o.LookPath = f, found
	return New(o)
}

func TestDetectReadyIncompatibleBrokenAbsent(t *testing.T) {
	m := mod(newFake(), Options{})
	if d := m.Detect(context.Background()); d.State != core.Ready || d.Version != "1.3.4" || d.APIVersion != 1 {
		t.Fatalf("%+v", d)
	}
	f := newFake()
	f.resp["--readonly version --json"] = `{"version":"9","schema_version":2}`
	if d := mod(f, Options{}).Detect(context.Background()); d.State != core.Incompatible {
		t.Fatalf("%+v", d)
	}
	f = newFake()
	f.exit = map[string]int{"--readonly version --json": 3}
	if d := mod(f, Options{}).Detect(context.Background()); d.State != core.Broken {
		t.Fatalf("%+v", d)
	}
	m = New(Options{LookPath: func(string) (string, error) { return "", fmt.Errorf("no") }, Runner: newFake()})
	if d := m.Detect(context.Background()); d.State != core.Absent {
		t.Fatalf("%+v", d)
	}
}

func TestTasksIsACompactSummaryNotABrowser(t *testing.T) {
	f := newFake()
	m := mod(f, Options{})
	m.Detect(context.Background())
	r, err := m.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := r.Counts
	if r.Source != core.SourceLive || c.Total != 5 || c.Open != 2 || c.InProgress != 1 || c.Blocked != 1 || c.Closed != 1 || c.Ready != 2 {
		t.Fatalf("%+v", r)
	}
	if len(r.Recent) != 2 || r.Recent[0].ID != "t-2" || r.Recent[0].Assignee != "bob" || strings.ContainsRune(r.Recent[0].Title, 0x1b) {
		t.Fatalf("recent %+v", r.Recent)
	}
	var _ core.TaskProvider = m
}

func TestReadsAlwaysRunReadonlyAndUseTheConfiguredTrackerDir(t *testing.T) {
	f := &fakeRun{resp: map[string]string{
		"-C /trk --readonly version --json":                                       verJSON,
		"-C /trk --readonly status --json --no-activity":                          statusJSON,
		"-C /trk --readonly list --json --brief --all --sort updated --flat -n 8": listJSON,
	}}
	m := mod(f, Options{Dir: "/trk"})
	m.Detect(context.Background())
	if _, err := m.Tasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if !strings.Contains(c, "--readonly") {
			t.Errorf("read without --readonly: %s", c)
		}
	}
}

func TestNoTrackerIsAnErrorWithAHintNotAFailure(t *testing.T) {
	f := newFake()
	f.exit = map[string]int{"--readonly status --json --no-activity": 1}
	f.resp["--readonly status --json --no-activity"] = ""
	m := mod(f, Options{})
	m.Detect(context.Background())
	if _, err := m.Tasks(context.Background()); err == nil || !strings.Contains(err.Error(), "ABACUS_BD_DIR") {
		t.Fatalf("%v", err)
	}
	if cs := m.Doctor(context.Background()); len(cs) == 0 || cs[0].Status != core.StatusWarn {
		t.Fatalf("doctor %+v", cs)
	}
}

func TestStatusMirrorsTheSummary(t *testing.T) {
	m := mod(newFake(), Options{})
	m.Detect(context.Background())
	if st := m.Status(context.Background()); !strings.Contains(st.Summary, "5 tasks") {
		t.Fatalf("%+v", st)
	}
}
