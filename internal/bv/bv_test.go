package bv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

type fakeRun struct {
	resp  map[string]string
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

func fx(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func found(string) (string, error) { return "/fake/bv", nil }

func fixtureRun(t *testing.T) *fakeRun {
	return &fakeRun{resp: map[string]string{
		"--robot-triage --format json":   fx(t, "triage"),
		"--robot-plan --format json":     fx(t, "plan"),
		"--robot-insights --format json": fx(t, "insights"),
		"--robot-graph --format json":    fx(t, "graph"),
	}, exit: map[string]int{}}
}

func mod(f *fakeRun, o Options) *Module {
	o.Runner, o.LookPath = f, found
	return New(o)
}

func TestInsightsDecodeTheRealOutputShape(t *testing.T) {
	m := mod(fixtureRun(t), Options{})
	if d := m.Detect(context.Background()); d.State != core.Ready {
		t.Fatalf("%+v", d)
	}
	r, err := m.Insights(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != core.SourceGraph || r.Reason != "" {
		t.Fatalf("%+v", r.Header)
	}
	if len(r.TopPicks) == 0 || r.TopPicks[0].ID != "fx-1" || r.TopPicks[0].Unblocks != 1 || len(r.TopPicks[0].Reasons) == 0 {
		t.Fatalf("top picks %+v", r.TopPicks)
	}
	if len(r.Blockers) == 0 || r.Blockers[0].ID != "fx-1" || r.Blockers[0].Unblocks != 1 {
		t.Fatalf("blockers %+v", r.Blockers)
	}
	if len(r.Cycles) != 1 || strings.Join(r.Cycles[0], ",") != "fx-5,fx-6,fx-5" {
		t.Fatalf("cycles %+v", r.Cycles)
	}
	if r.TotalActionable != 2 || r.TotalBlocked != 4 || len(r.Tracks) != 2 || r.Tracks[0].ID != "track-A" || r.Tracks[0].Items[0] != "fx-1" {
		t.Fatalf("plan %+v", r)
	}
	if r.GraphNodes != 7 || r.GraphEdges != 4 || len(r.Bottlenecks) == 0 || r.Bottlenecks[0].ID != "fx-2" {
		t.Fatalf("graph/bottlenecks %+v", r)
	}
	// With a cycle bv skips the critical path; the note says why instead of an empty list.
	if len(r.CriticalPath) != 0 || !strings.Contains(r.CriticalPathNote, "cycle") {
		t.Fatalf("critical path %+v note=%q", r.CriticalPath, r.CriticalPathNote)
	}
}

func TestAcyclicInsightsFillTheCriticalPath(t *testing.T) {
	f := fixtureRun(t)
	f.resp["--robot-insights --format json"] = fx(t, "insights_acyclic")
	r, err := mod(f, Options{}).Insights(context.Background())
	if err != nil || len(r.CriticalPath) == 0 || r.CriticalPath[0].ID != "fx-1" || r.CriticalPath[0].Value != 3 || r.CriticalPathNote != "" || len(r.Cycles) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestOnlyTheFourRobotArgvsRunAndAlwaysWithFormatJSON(t *testing.T) {
	f := fixtureRun(t)
	if _, err := mod(f, Options{}).Insights(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 {
		t.Fatalf("calls %v", f.calls)
	}
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "--robot-") || !strings.HasSuffix(c, "--format json") {
			t.Errorf("unexpected argv %q", c)
		}
	}
}

func TestScopeFlagsAreForwardedWhenConfigured(t *testing.T) {
	f := &fakeRun{resp: map[string]string{}, exit: map[string]int{}}
	for _, m := range []string{"triage", "plan", "insights", "graph"} {
		f.resp["--robot-"+m+" --format json --label backend --recipe actionable"] = fx(t, m)
	}
	if _, err := mod(f, Options{Label: "backend", Recipe: "actionable"}).Insights(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNonZeroExitIsDegradedAndIgnoresStdout(t *testing.T) {
	f := fixtureRun(t)
	for k := range f.resp {
		f.exit[k] = 1
	}
	if _, err := mod(f, Options{}).Insights(context.Background()); err == nil {
		t.Fatal("all sections failing must be an error")
	}
	f = fixtureRun(t)
	f.exit["--robot-plan --format json"] = 1
	r, err := mod(f, Options{}).Insights(context.Background())
	if err != nil || !strings.Contains(r.Reason, "plan") || len(r.TopPicks) == 0 || len(r.Tracks) != 0 {
		t.Fatalf("partial: %+v %v", r.Header, err)
	}
}

func TestMissingSectionsAndUnknownFieldsAreTolerated(t *testing.T) {
	f := fixtureRun(t)
	f.resp["--robot-triage --format json"] = `{"triage":{"unknown":1}}`
	f.resp["--robot-plan --format json"] = `{}`
	f.resp["--robot-insights --format json"] = `{"Surprise":[1]}`
	f.resp["--robot-graph --format json"] = `{"nodes":{"weird":true}}`
	r, err := mod(f, Options{}).Insights(context.Background())
	if err != nil || r.Reason != "" || len(r.TopPicks) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	f.resp["--robot-triage --format json"] = `not json`
	r, err = mod(f, Options{}).Insights(context.Background())
	if err != nil || !strings.Contains(r.Reason, "triage") {
		t.Fatalf("%+v %v", r.Header, err)
	}
}

func TestTextIsSanitized(t *testing.T) {
	f := fixtureRun(t)
	f.resp["--robot-triage --format json"] = `{"triage":{"quick_ref":{"top_picks":[{"id":"x\u001b[2J","title":"a‮b","reasons":["r\u0007"]}]}}}`
	r, _ := mod(f, Options{}).Insights(context.Background())
	p := r.TopPicks[0]
	if strings.ContainsAny(p.ID+p.Title+p.Reasons[0], "\x1b\x07‮") {
		t.Fatalf("%+v", p)
	}
}

func TestAbsentIsAbsentAndNeverRuns(t *testing.T) {
	f := fixtureRun(t)
	m := New(Options{Runner: f, LookPath: func(string) (string, error) { return "", fmt.Errorf("no") }})
	if d := m.Detect(context.Background()); d.State != core.Absent || len(f.calls) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestAllowlistIsExact(t *testing.T) {
	ok := [][]string{
		{"--robot-triage", "--format", "json"},
		{"--robot-graph", "--format", "json", "--label", "backend"},
		{"--robot-plan", "--format", "json", "--recipe", "actionable", "--label", "a.b-c_1"},
	}
	for _, a := range ok {
		if !allowedArgv(a) {
			t.Errorf("should be allowed: %v", a)
		}
	}
	bad := [][]string{
		{},
		{"--robot-triage"}, // no explicit format: env could pick TOON
		{"--robot-triage", "--format", "toon"},
		{"--robot-next", "--format", "json"}, // not one of the four
		{"--robot-triage", "--format", "json", "--export", "x"},
		{"--robot-triage", "--format", "json", "--label"},
		{"--robot-triage", "--format", "json", "--label", "--force"},
		{"--robot-triage", "--format", "json", "--label", "a b"},
		{"--robot-triage", "--format", "json", "--label", "a", "--label", "b"},
		{"--format", "json", "--robot-triage"},
		{"--robot-triage", "--format", "json", "extra"},
		{"--feedback-accept", "x"},
		{"--update"},
		{"--format", "json"},
	}
	for _, a := range bad {
		if allowedArgv(a) {
			t.Errorf("should be refused: %v", a)
		}
	}
}

func TestDetectReportsVersionAndWhereItWasFound(t *testing.T) {
	f := fixtureRun(t)
	f.resp["--version"] = "bv v0.27.0\n"
	m := mod(f, Options{})
	d := core.NewRegistry(m).Detect(context.Background())[0].Detection
	if d.State != core.Ready || d.Version != "0.27.0" || d.Source != core.SourcePATH || d.Path != "/fake/bv" {
		t.Fatalf("%+v", d)
	}
}

func TestDoctorKeepsTheFailureReasonThatTasksDoesNotShow(t *testing.T) {
	f := &fakeRun{resp: map[string]string{}, exit: map[string]int{}}
	for _, mode := range modes {
		f.resp[mode+" --format json"], f.exit[mode+" --format json"] = "", 1
	}
	m := mod(f, Options{})
	m.Detect(context.Background())
	if _, err := m.Insights(context.Background()); err == nil {
		t.Fatal("want an error when every read fails")
	}
	c := m.Doctor(context.Background())
	if len(c) != 1 || c[0].Status != core.StatusWarn || !strings.Contains(c[0].Detail, "triage: exited 1") {
		t.Fatalf("%+v", c)
	}
}
