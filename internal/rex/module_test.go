package rex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

var (
	_ core.Module            = (*Module)(nil)
	_ core.WorkspaceProvider = (*Module)(nil)
)

type resp struct {
	out  string
	exit int
	err  error
}

// fake is a scripted Runner keyed by the joined argv. It never runs anything; an argv with no
// script is an error, so a test also proves which calls were made.
type fake struct {
	mu    sync.Mutex
	resp  map[string]resp
	calls []string
	hang  bool // block until the context ends, like a hung rex
	// barrier, when positive, makes a call wait until that many calls are in flight at once (or
	// the context ends), so a test can prove reads run concurrently without timing.
	barrier  int
	inflight int
}

func (f *fake) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	key := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	r, ok := f.resp[key]
	hang := f.hang
	f.inflight++
	waiting := f.barrier > 0
	f.mu.Unlock()
	if waiting {
		deadline := time.After(3 * time.Second)
		for {
			f.mu.Lock()
			n := f.inflight
			f.mu.Unlock()
			if n >= f.barrier {
				break
			}
			select {
			case <-deadline:
				return nil, -1, errors.New("calls were not concurrent")
			case <-time.After(time.Millisecond):
			}
		}
	}
	if hang {
		<-ctx.Done()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, -1, fmt.Errorf("%w after 1ms", core.ErrTimedOut)
		}
		return nil, -1, fmt.Errorf("%w: %w", core.ErrCanceled, ctx.Err())
	}
	if !ok {
		return nil, -1, fmt.Errorf("no script for %q", key)
	}
	return []byte(r.out), r.exit, r.err
}

func (f *fake) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	lsLocal  = "--autostart=false ls --json"
	verLocal = "--autostart=false version --json"
)

// testHosts are the machines the tests configure; CacheTTL -1 keeps every read fresh unless a
// test is about the cache.
var testHosts = []string{"mbp", "down", "bad", "h0", "h1", "h2", "h3", "h4", "h5", "h6", "h7", "h8", "h9", "h10", "h11"}

func newMod(f *fake, env map[string]string) *Module {
	return New(Options{
		LookPath: func(string) (string, error) { return "/fake/rex", nil },
		Getenv:   func(k string) string { return env[k] },
		Runner:   f,
		Hostname: func() string { return "me" },
		Hosts:    testHosts,
		CacheTTL: -1,
	})
}

func detect(m *Module) core.Detection { return m.Detect(context.Background()) }

func TestAbsentBinaryIsAbsentQuietAndRunsNothing(t *testing.T) {
	f := &fake{}
	m := New(Options{LookPath: func(string) (string, error) { return "", errors.New("no") }, Getenv: func(string) string { return "" }, Runner: f})
	d := detect(m)
	if d.State != core.Absent || !m.Quiet() || len(f.called()) != 0 {
		t.Fatalf("%+v quiet=%v calls=%v", d, m.Quiet(), f.called())
	}
}

func TestNoServerExit4IsAbsentWorkspaceNotAnError(t *testing.T) {
	f := &fake{resp: map[string]resp{lsLocal: {exit: 4}}}
	m := newMod(f, nil)
	d := detect(m)
	if d.State != core.Ready || d.Detail != "no Rex server running" || m.Quiet() {
		t.Fatalf("%+v", d)
	}
	if got := m.Doctor(context.Background()); len(got) != 1 || got[0].Status != core.StatusSkip {
		t.Fatalf("doctor: %+v", got)
	}
	rep, err := m.Workspace(context.Background(), []core.WorkspaceItem{{Kind: core.WorkspaceRun, ID: "LAM-1"}})
	if err != nil || rep.State != core.NotConnected || !rep.Empty() || rep.HowToConnect == "" {
		t.Fatalf("%+v %v", rep, err)
	}
	if st := m.Status(context.Background()); st.Unavailable == "" || st.HowToConnect == "" {
		t.Fatalf("status: %+v", st)
	}
	if len(f.called()) != 3 { // the probe, then one ls each for Workspace and Status
		t.Fatalf("calls: %v", f.called())
	}
}

func TestProbeFailureIsAWarningNeverAFailure(t *testing.T) {
	for name, r := range map[string]resp{
		"exit 1":  {exit: 1},
		"exit 3":  {exit: 3},
		"runner":  {err: errors.New("exec: permission denied")},
		"garbage": {out: "not json"},
	} {
		f := &fake{resp: map[string]resp{lsLocal: r}}
		m := newMod(f, nil)
		e := []core.Entry{{Module: m, Detection: detect(m)}}
		if e[0].Detection.State != core.Incompatible {
			t.Errorf("%s: %+v", name, e[0].Detection)
			continue
		}
		rep := core.DoctorReport(context.Background(), "t", e)
		if rep.ExitCode() != 0 || len(rep.Modules) != 1 || rep.Modules[0].Checks[0].Status != core.StatusWarn {
			t.Errorf("%s: want a warn and exit 0: %+v", name, rep)
		}
	}
}

func TestProbeTimeoutAndCancelAreBoundedAndWarn(t *testing.T) {
	f := &fake{hang: true}
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f, ProbeTimeout: 20 * time.Millisecond})
	start := time.Now()
	d := detect(m)
	if d.State != core.Incompatible || time.Since(start) > 2*time.Second {
		t.Fatalf("%+v after %s", d, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d := m.Detect(ctx); d.State != core.Incompatible {
		t.Fatalf("canceled: %+v", d)
	}
}

func TestTheVersionCallHasItsOwnBound(t *testing.T) {
	f := &fake{hang: true}
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(k string) string {
		if k == "REX_SESSION" {
			return "s"
		}
		return ""
	}, Runner: f, CallTimeout: 20 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	d := m.Detect(ctx)
	if d.State != core.Incompatible || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("%+v after %s", d, time.Since(start))
	}
}

func TestEnvironmentDetectionSkipsTheProbeButStillGatesTheRevision(t *testing.T) {
	for _, env := range []map[string]string{{"REX_SESSION": "session:x"}, {"REX_SERVER": "unix:///x"}} {
		f := &fake{resp: map[string]resp{verLocal: {out: fixture(t, "version-13.json")}}}
		m := newMod(f, env)
		d := detect(m)
		if d.State != core.Ready || d.APIVersion != 13 || d.Version == "" {
			t.Fatalf("%v: %+v", env, d)
		}
		if got := f.called(); len(got) != 1 || got[0] != verLocal {
			t.Fatalf("the probe must be skipped: %v", got)
		}
		if c := m.Doctor(context.Background()); c[0].Status != core.StatusOK || !strings.Contains(c[0].Detail, "environment") {
			t.Fatalf("%+v", c)
		}
	}
}

func TestRevisionGate(t *testing.T) {
	ver := func(rev string) string {
		return `{"rex_version":"1","platform":"x","dirty":false,"api_revision":` + rev + `}`
	}
	for _, tc := range []struct {
		name string
		r    resp
		want core.State
	}{
		{"min", resp{out: ver("10")}, core.Ready},
		{"max", resp{out: ver("13")}, core.Ready},
		{"below", resp{out: ver("9")}, core.Incompatible},
		{"zero", resp{out: ver("0")}, core.Incompatible},
		{"newer server, still speaks 13", resp{out: ver("14")}, core.Ready},
		{"newer server, oldest it speaks is 14", resp{out: `{"api_revision":14,"api_min_revision":14}`}, core.Incompatible},
		{"no revision", resp{out: fixture(t, "version-no-revision.json")}, core.Incompatible},
		{"garbage", resp{out: "nope"}, core.Incompatible},
		{"exit 1", resp{exit: 1}, core.Incompatible},
		{"error", resp{err: errors.New("x")}, core.Incompatible},
	} {
		f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: tc.r}}
		m := newMod(f, nil)
		d := detect(m)
		if d.State != tc.want {
			t.Errorf("%s: %+v", tc.name, d)
		}
		if tc.want == core.Incompatible {
			if !strings.Contains(d.Detail, "optional") && !strings.Contains(d.Detail, "off") {
				t.Errorf("%s: detail should say what to do: %q", tc.name, d.Detail)
			}
			rep := core.DoctorReport(context.Background(), "t", []core.Entry{{Module: m, Detection: d}})
			if rep.ExitCode() != 0 {
				t.Errorf("%s: exit %d", tc.name, rep.ExitCode())
			}
			if got := core.ReadWorkspace(context.Background(), []core.Entry{{Module: m, Detection: d}}, []core.WorkspaceItem{{ID: "LAM-1"}}); !got.Empty() {
				t.Errorf("%s: an incompatible Rex must provide no workspace: %+v", tc.name, got)
			}
		}
	}
}

// help.json: api_revision is the newest revision the server supports, api_min_revision the oldest.
// The two ranges must overlap: compatible iff api_revision >= 10 and api_min_revision <= 13.
func TestRevisionGateIsAnIntervalOverlap(t *testing.T) {
	for _, tc := range []struct {
		body string
		want core.State
	}{
		{`{"api_revision":14,"api_min_revision":10}`, core.Ready},
		{`{"api_revision":14,"api_min_revision":13}`, core.Ready}, // min == 13: boundary, overlaps
		{`{"api_revision":14,"api_min_revision":14}`, core.Incompatible},
		{`{"api_revision":99,"api_min_revision":99}`, core.Incompatible},
		{`{"api_revision":99}`, core.Ready}, // no min means 0
		{`{"api_revision":10,"api_min_revision":0}`, core.Ready},
		{`{"api_revision":9,"api_min_revision":0}`, core.Incompatible},
		{`{"api_revision":13,"api_min_revision":13}`, core.Ready},
	} {
		f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: {out: tc.body}}}
		if d := detect(newMod(f, nil)); d.State != tc.want {
			t.Errorf("%s: %+v, want %s", tc.body, d, tc.want)
		}
	}
	// The detail names the cause.
	f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: {out: `{"api_revision":14,"api_min_revision":14}`}}}
	if d := detect(newMod(f, nil)); !strings.Contains(d.Detail, "14 or newer") || d.APIVersion != 14 {
		t.Errorf("%+v", d)
	}
	f.resp[verLocal] = resp{out: `{"api_revision":9}`}
	if d := detect(newMod(f, nil)); !strings.Contains(d.Detail, "9") || !strings.Contains(d.Detail, "older") {
		t.Errorf("%+v", d)
	}
}

func TestMalformedRevisionsGetAPreciseMessage(t *testing.T) {
	for _, body := range []string{
		`{"api_revision":"13"}`, `{"api_revision":13.5}`, `{"api_revision":13.0}`, `{"api_revision":null}`, `{"api_revision":-1}`,
		`{"api_revision":true}`, `{"api_revision":[13]}`, `{"api_revision":99999999999999999999}`,
		`{"api_revision":13,"api_min_revision":"10"}`, `{"api_revision":13,"api_min_revision":null}`, `{"api_revision":13,"api_min_revision":-2}`,
	} {
		f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: {out: body}}}
		d := detect(newMod(f, nil))
		if d.State != core.Incompatible || !strings.Contains(d.Detail, "whole number") || strings.Contains(d.Detail, "no api_revision") {
			t.Errorf("%s: %+v", body, d)
		}
	}
	f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: {out: `{"api_revision":13,"api_min_revision":"\u001b[31mx"}`}}}
	if d := detect(newMod(f, nil)); strings.ContainsRune(d.Detail, 0x1b) {
		t.Errorf("a malformed value must be sanitized in the detail: %q", d.Detail)
	}
	f.resp[verLocal] = resp{out: `{"rex_version":"1"}`}
	if d := detect(newMod(f, nil)); !strings.Contains(d.Detail, "no api_revision") {
		t.Errorf("a missing revision keeps its own message: %+v", d)
	}
}

func TestExitCodesWithValidJSONAreStillErrors(t *testing.T) {
	ok := fixture(t, "ls-null.json")
	// probe: valid JSON with a non-zero exit
	for _, exit := range []int{1, 2, 130} {
		f := &fake{resp: map[string]resp{lsLocal: {out: ok, exit: exit}, verLocal: {out: fixture(t, "version-13.json")}}}
		if d := detect(newMod(f, nil)); d.State != core.Incompatible || !strings.Contains(d.Detail, fmt.Sprint(exit)) {
			t.Errorf("probe exit %d with valid JSON: %+v", exit, d)
		}
	}
	// version: valid JSON with a non-zero exit
	f := &fake{resp: map[string]resp{lsLocal: {out: ok}, verLocal: {out: fixture(t, "version-13.json"), exit: 1}}}
	if d := detect(newMod(f, nil)); d.State != core.Incompatible || !strings.Contains(d.Detail, "exited 1") {
		t.Errorf("version exit 1: %+v", d)
	}
	// ls: valid JSON with a non-zero exit is an error, not a healthy list
	f = fleetFake(t)
	m := readyMod(t, f, nil)
	f.resp[lsLocal] = resp{out: fixture(t, "ls-fleet.json"), exit: 1}
	rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{{ID: "LAM-1"}})
	if rep.State != core.Degraded || len(rep.Placements) != 0 {
		t.Errorf("ls exit 1 with valid JSON: %+v", rep)
	}
	if st := m.Status(context.Background()); st.Unavailable == "" || st.Summary != "" {
		t.Errorf("status: %+v", st)
	}
}

// Exit 4 is "no server", wherever it shows up; exit 3 is Rex saying more than one server answers.
func TestNoServerAndAmbiguousServerExitsOnTheVersionCall(t *testing.T) {
	env := map[string]string{"REX_SESSION": "s"}
	f := &fake{resp: map[string]resp{verLocal: {exit: 4}}}
	m := newMod(f, env)
	d := detect(m)
	if d.State != core.Ready || d.Detail != "no Rex server running" || m.Quiet() {
		t.Fatalf("version exit 4 is no server, not incompatible: %+v", d)
	}
	if c := m.Doctor(context.Background()); c[0].Status != core.StatusSkip {
		t.Fatalf("%+v", c)
	}
	f = &fake{resp: map[string]resp{verLocal: {exit: 3}}}
	d = detect(newMod(f, env))
	if d.State != core.Incompatible || !strings.Contains(d.Detail, "more than one") {
		t.Fatalf("version exit 3: %+v", d)
	}
	f = &fake{resp: map[string]resp{lsLocal: {exit: 3}}}
	d = detect(newMod(f, nil))
	if d.State != core.Incompatible || !strings.Contains(d.Detail, "more than one") {
		t.Fatalf("probe exit 3: %+v", d)
	}
	f = fleetFake(t)
	m = readyMod(t, f, nil)
	f.resp[lsLocal] = resp{exit: 3}
	rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{{ID: "LAM-1"}})
	if rep.State != core.Degraded || !strings.Contains(rep.Reason, "more than one") {
		t.Fatalf("ls exit 3: %+v", rep)
	}
}

func TestAnIncompatibleRexIsListedByVersionAndDoctorAsAWarning(t *testing.T) {
	f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: {out: `{"api_revision":14,"api_min_revision":14}`}}}
	m := newMod(f, nil)
	entries := core.NewRegistry(m).Detect(context.Background())
	if m.Quiet() {
		t.Fatal("an incompatible Rex is there, so it is not quiet")
	}
	if got := core.VersionReport("t", entries); len(got.Modules) != 1 || got.Modules[0].ID != "rex" {
		t.Fatalf("version must list an incompatible Rex: %+v", got.Modules)
	}
	doc := core.DoctorReport(context.Background(), "t", entries)
	if len(doc.Modules) != 1 || doc.Modules[0].Checks[0].Status != core.StatusWarn || doc.ExitCode() != 0 {
		t.Fatalf("%+v exit %d", doc.Modules, doc.ExitCode())
	}
}

func TestAnAbsentRexIsNotListedByDoctorOrQuiet(t *testing.T) {
	gone := New(Options{LookPath: func(string) (string, error) { return "", errors.New("x") }, Getenv: func(string) string { return "" }, Runner: &fake{}})
	entries := core.NewRegistry(gone).Detect(context.Background())
	if doc := core.DoctorReport(context.Background(), "t", entries); len(doc.Modules) != 0 {
		t.Fatalf("an absent Rex must not be in Doctor: %+v", doc.Modules)
	}
}

// A Rex that disappears between two detections must go quiet again: Detect resets first.
func TestDetectAfterRexVanishesIsAbsentAndQuiet(t *testing.T) {
	f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: {out: fixture(t, "version-13.json")}}}
	present := true
	m := New(Options{LookPath: func(string) (string, error) {
		if present {
			return "/fake/rex", nil
		}
		return "", errors.New("gone")
	}, Getenv: func(string) string { return "" }, Runner: f})
	if d := detect(m); d.State != core.Ready || m.Quiet() {
		t.Fatalf("%+v", d)
	}
	present = false
	if d := detect(m); d.State != core.Absent || !m.Quiet() {
		t.Fatalf("a vanished rex must be absent and quiet: %+v quiet=%v", d, m.Quiet())
	}
	if c := m.Doctor(context.Background()); c[0].Status != core.StatusSkip || !strings.Contains(c[0].Detail, "not found") {
		t.Fatalf("%+v", c)
	}
}

func TestTimeoutDetailsSayTimedOutNotAContextError(t *testing.T) {
	f := &fake{hang: true}
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f, ProbeTimeout: 10 * time.Millisecond})
	d := detect(m)
	if !strings.Contains(d.Detail, "timed out") || strings.Contains(d.Detail, "context") || strings.Contains(d.Detail, "deadline") {
		t.Fatalf("probe: %q", d.Detail)
	}
	m = New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(k string) string { return map[string]string{"REX_SESSION": "s"}[k] }, Runner: f, CallTimeout: 10 * time.Millisecond})
	if d := detect(m); !strings.Contains(d.Detail, "timed out") || strings.Contains(d.Detail, "deadline") {
		t.Fatalf("version: %q", d.Detail)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m = New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f})
	if d := m.Detect(ctx); !strings.Contains(d.Detail, "canceled") || strings.Contains(d.Detail, "context") {
		t.Fatalf("cancel: %q", d.Detail)
	}
}

// The module sanitizes and bounds everything it hands out itself, not only through core.
func TestModuleOutputsAreSanitizedAndBoundedAtTheSource(t *testing.T) {
	evil := `\u001b[31m` + strings.Repeat("v", 500) // a JSON escape, decoded to a real ESC
	f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: {out: `{"api_revision":13,"rex_version":"` + evil + `"}`}}}
	d := detect(newMod(f, nil))
	if d.State != core.Ready || strings.ContainsRune(d.Version, 0x1b) || len([]rune(d.Version)) != 64 {
		t.Fatalf("version: %q", d.Version)
	}
	f.resp[verLocal] = resp{out: `{"api_revision":13,"version":"` + evil + `"}`}
	if d := detect(newMod(f, nil)); strings.ContainsRune(d.Version, 0x1b) || len([]rune(d.Version)) != 64 {
		t.Fatalf("fallback version: %q", d.Version)
	}
	f = &fake{resp: map[string]resp{lsLocal: {err: errors.New("boom\x1b[31m" + strings.Repeat("e", 500))}}}
	d = detect(newMod(f, nil))
	if strings.ContainsRune(d.Detail, 0x1b) || len([]rune(d.Detail)) > 260 || !strings.Contains(d.Detail, "boom") {
		t.Fatalf("probe error: %q", d.Detail)
	}
	g := fleetFake(t)
	m := readyMod(t, g, nil)
	g.resp[lsLocal] = resp{err: errors.New("bang\x1b[31m" + strings.Repeat("r", 600))}
	st := m.Status(context.Background())
	if strings.ContainsRune(st.Unavailable, 0x1b) || len([]rune(st.Unavailable)) > 200 || !strings.Contains(st.Unavailable, "bang") {
		t.Fatalf("status: %q", st.Unavailable)
	}
	rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{{ID: "LAM-1"}})
	if strings.ContainsRune(rep.Reason, 0x1b) || len([]rune(rep.Reason)) > 300 || !strings.Contains(rep.Reason, "bang") {
		t.Fatalf("reason: %q (%d runes)", rep.Reason, len([]rune(rep.Reason)))
	}
}

func TestDefaultTimeoutsArePinned(t *testing.T) {
	m := New(Options{})
	if m.o.ProbeTimeout != 500*time.Millisecond || m.o.CallTimeout != 2*time.Second {
		t.Fatalf("probe %s, call %s", m.o.ProbeTimeout, m.o.CallTimeout)
	}
	m = New(Options{ProbeTimeout: time.Second, CallTimeout: 3 * time.Second})
	if m.o.ProbeTimeout != time.Second || m.o.CallTimeout != 3*time.Second {
		t.Fatal("explicit timeouts must be kept")
	}
}

func readyMod(t *testing.T, f *fake, env map[string]string) *Module {
	t.Helper()
	m := newMod(f, env)
	if d := detect(m); d.State != core.Ready {
		t.Fatalf("%+v", d)
	}
	return m
}

func fleetFake(t *testing.T) *fake {
	return &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-fleet.json")}, verLocal: {out: fixture(t, "version-13.json")}}}
}

func TestPlacementsFromSessionLabelsAndUnknownValuesPassThrough(t *testing.T) {
	m := readyMod(t, fleetFake(t), nil)
	items := []core.WorkspaceItem{
		{Kind: core.WorkspaceRun, ID: "LAM-1"},
		{Kind: "hologram", ID: "LAM-7"}, // an unknown kind is carried through, not dropped
		{Kind: core.WorkspaceTask, ID: "LAM-9"},
		{Kind: core.WorkspaceLease, ID: "LAM-12"},
	}
	rep, err := m.Workspace(context.Background(), items)
	if err != nil || rep.State != core.Connected || len(rep.Placements) != 3 || len(rep.Ambiguous) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	p, _ := rep.For(items[0])
	if p == nil || p.Session != "session:1111" || p.Label != "Lambe · LAM-1" || p.Match != core.MatchExact || p.ID == "" {
		t.Fatalf("%+v", p)
	}
	if p, _ := rep.For(items[1]); p == nil || p.Item.Kind != "hologram" || p.Match != core.MatchLabel {
		t.Fatalf("unknown kind: %+v", p)
	}
	if p, _ := rep.For(items[2]); p != nil {
		t.Fatalf("LAM-9 is nowhere: %+v", p)
	}
	if p, _ := rep.For(items[3]); p == nil || p.Session != "session:4444" {
		t.Fatalf("LAM-1 must not claim LAM-12 and vice versa: %+v", p)
	}
}

func TestTwoSessionsClaimingOneItemIsAmbiguousNeverPicked(t *testing.T) {
	f := &fake{resp: map[string]resp{lsLocal: {out: `{"sessions":[{"session_id":"s1","label":"Lambe · LAM-1"},{"session_id":"s2","label":"fix LAM-1 flake"},{"session_id":"s3","label":"other"}]}`},
		verLocal: {out: fixture(t, "version-13.json")}}}
	m := readyMod(t, f, nil)
	it := core.WorkspaceItem{Kind: core.WorkspaceRun, ID: "LAM-1"}
	rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{it})
	p, cands := rep.For(it)
	if p != nil || len(cands) != 2 || cands[0].Session != "s1" || cands[1].Session != "s2" {
		t.Fatalf("placement=%+v candidates=%+v", p, cands)
	}
	if len(rep.Placements) != 0 {
		t.Fatalf("an ambiguous item must not also be placed: %+v", rep.Placements)
	}
	if rep.Empty() {
		t.Fatal("an ambiguity is a finding")
	}
}

func TestHostileLabelsAreSanitizedBoundedAndCannotForgeAMatch(t *testing.T) {
	big := strings.Repeat("A", 10000)
	wide := strings.Repeat("中", 10000)
	f := &fake{resp: map[string]resp{lsLocal: {out: fmt.Sprintf(`{"sessions":[`+
		`{"session_id":"s1","label":"\u001b]0;pwned\u0007\u001b[2J LAM-1 \u202egnp.exe⁦ x"},`+
		`{"session_id":"s2","label":"LAM-2 %s"},`+
		`{"session_id":"s3","label":"LAM-3 %s"},`+
		`{"session_id":"s4\u001b[31m","label":"LAM-4\u0000"}]}`, big, wide)}, verLocal: {out: fixture(t, "version-13.json")}}}
	m := readyMod(t, f, nil)
	var items []core.WorkspaceItem
	for _, id := range []string{"LAM-1", "LAM-2", "LAM-3", "LAM-4"} {
		items = append(items, core.WorkspaceItem{Kind: core.WorkspaceRun, ID: id})
	}
	rep, err := m.Workspace(context.Background(), items)
	if err != nil || len(rep.Placements) != 4 {
		t.Fatalf("%v %+v", err, rep)
	}
	for _, p := range rep.Placements {
		for _, s := range []string{p.Label, p.Session, p.ID} {
			for _, r := range s {
				if r < 0x20 || r == 0x7f || r == 0x1b || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
					t.Errorf("%s keeps %U in %q", p.Item.ID, r, s)
				}
			}
		}
		if n := len([]rune(p.Label)); n > core.MaxLabelRunes {
			t.Errorf("%s: label has %d runes", p.Item.ID, n)
		}
		if len(p.Label) > 4*core.MaxLabelRunes {
			t.Errorf("%s: label has %d bytes", p.Item.ID, len(p.Label))
		}
	}
	if p, _ := rep.For(items[0]); !strings.HasPrefix(p.Label, "]0;pwned[2J LAM-1") {
		t.Fatalf("controls are dropped, text kept: %q", p.Label)
	}
	// Open never lets a label into the command.
	for _, p := range rep.Placements {
		p.Host = "mbp"
		if plan := m.Open(p); strings.Contains(strings.Join(plan.Rungs[0].Argv, " ")+plan.Copy, "pwned") {
			t.Fatal("a label leaked into argv")
		}
	}
}

func TestOnlyTheReadArgvsEverExecute(t *testing.T) {
	ok := [][]string{
		{"--autostart=false", "ls", "--json"},
		{"--autostart=false", "version", "--json"},
		{"-S", "mbp", "--autostart=false", "ls", "--json"},
		{"-S", "me@mbp.local", "--autostart=false", "ls", "--json"},
		{"-S", "host:22", "--autostart=false", "ls", "--json"},
	}
	for _, a := range ok {
		if !allowedArgv(a) {
			t.Errorf("%v should be allowed", a)
		}
	}
	refused := [][]string{
		nil, {}, {"ls"}, {"ls", "--json"}, {"version", "--json"}, // no --autostart=false: could start a server
		{"--autostart=true", "ls", "--json"}, {"--autostart", "ls", "--json"},
		{"--autostart=false", "ls"}, {"--autostart=false", "ls", "--json", "--extra"},
		{"--autostart=false", "version"}, {"--autostart=false", "version", "--json", "x"},
		{"--autostart=false", "new", "x", "--json"}, {"--autostart=false", "send", "-s", "x", "rm -rf"},
		{"--autostart=false", "capture", "-s", "x"}, {"--autostart=false", "api", "call", "session.view"},
		{"--autostart=false", "window", "focus", "x"}, {"--autostart=false", "window", "new", "x"},
		{"--autostart=false", "server", "start"}, {"--autostart=false", "config", "reload"},
		{"--autostart=false", "do", "session.select"}, {"--autostart=false", "hosts"},
		{"-S", "--autostart=false", "ls", "--json"}, {"-S", "", "--autostart=false", "ls", "--json"},
		{"-S", "-oProxyCommand=x", "--autostart=false", "ls", "--json"}, {"-S", "a b", "--autostart=false", "ls", "--json"},
		{"-S", "a;b", "--autostart=false", "ls", "--json"}, {"-S", "$(x)", "--autostart=false", "ls", "--json"},
		{"-S", "a\nb", "--autostart=false", "ls", "--json"}, {"-S", "ssh://h", "--autostart=false", "ls", "--json"},
		{"-S", strings.Repeat("a", 300), "--autostart=false", "ls", "--json"},
		{"-S", "mbp", "--autostart=false", "version", "--json"}, {"-S", "mbp", "--autostart=false", "new", "x"},
		{"-S", "mbp", "ls", "--json"}, {"--autostart=false", "-S", "mbp", "ls", "--json"},
		{"--server", "unix:///x", "--autostart=false", "ls", "--json"}, {"-s", "x", "--autostart=false", "ls", "--json"},
	}
	for _, a := range refused {
		if allowedArgv(a) {
			t.Errorf("%q must be refused", a)
		}
		var ran bool
		r := readOnly{inner: runnerFunc(func() { ran = true })}
		if _, exit, err := r.Run(context.Background(), "rex", a...); err == nil || exit != -1 || ran {
			t.Errorf("%q reached the runner (err=%v ran=%v)", a, err, ran)
		}
	}
}

type runnerFunc func()

func (f runnerFunc) Run(context.Context, string, ...string) ([]byte, int, error) {
	f()
	return nil, 0, nil
}

func TestEveryCallTheModuleMakesIsAllowed(t *testing.T) {
	f := fleetFake(t)
	f.resp["-S mbp "+lsLocal] = resp{out: fixture(t, "ls-help-example.json")}
	m := readyMod(t, f, nil)
	m.Workspace(context.Background(), []core.WorkspaceItem{{ID: "LAM-1"}, {ID: "LAM-2", Host: "mbp"}})
	m.Status(context.Background())
	m.Doctor(context.Background())
	m.Open(core.Placement{Host: "mbp"})
	for _, c := range f.called() {
		if !allowedArgv(strings.Fields(c)) {
			t.Errorf("called %q", c)
		}
	}
}

func TestScrubEnvPassesOnlyHomePathXDGAndRex(t *testing.T) {
	in := []string{"HOME=/h", "PATH=/p", "XDG_RUNTIME_DIR=/r", "XDG_CONFIG_HOME=/c", "REX_SESSION=s", "REX_SERVER=x", "REX_BLOCK=b",
		"VERCEL_AUTH_TOKEN=secret", "BEADS_DIR=/b", "BD_X=1", "SSH_AUTH_SOCK=/s", "AWS_SECRET_ACCESS_KEY=k", "LD_PRELOAD=/x", "REXX=1", "XDGX=1", "HOMEX=1", "TERM=xterm"}
	got := strings.Join(ScrubEnv(in), " ")
	want := "HOME=/h PATH=/p XDG_RUNTIME_DIR=/r XDG_CONFIG_HOME=/c REX_SESSION=s REX_SERVER=x REX_BLOCK=b"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestDefaultRunnerScrubsTheEnvironmentOfARealProcess(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	stub := filepath.Join(dir, "stub-not-rex")
	script := "#!/bin/sh\n/usr/bin/env > \"$REX_TEST_OUT\"\ncase \"$*\" in *version*) echo '{\"api_revision\":13}';; *) echo '{\"sessions\":[]}';; esac\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX_TEST_OUT", out)
	t.Setenv("ABACUS_TEST_SECRET", "hunter2")
	t.Setenv("BEADS_DIR", "/real/tracker")
	t.Setenv("VERCEL_AUTH_TOKEN", "tok")
	m := New(Options{Bin: stub, Getenv: func(string) string { return "" }})
	if d := m.Detect(context.Background()); d.State != core.Ready || d.APIVersion != 13 {
		t.Fatalf("%+v", d)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		name, _, _ := strings.Cut(line, "=")
		if name == "PWD" || name == "SHLVL" || name == "_" { // set by /bin/sh itself, not passed by abacus
			continue
		}
		if name != "HOME" && name != "PATH" && !strings.HasPrefix(name, "XDG_") && !strings.HasPrefix(name, "REX_") {
			t.Errorf("rex was given %q", name)
		}
	}
	if strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "tok") || strings.Contains(string(b), "real/tracker") {
		t.Fatalf("a secret reached rex:\n%s", b)
	}
	if !strings.Contains(string(b), "REX_TEST_OUT=") {
		t.Fatal("REX_* must pass")
	}
}

func TestHostPropagationIsOnlyTheOneMachineARunCarries(t *testing.T) {
	f := fleetFake(t)
	f.resp["-S mbp "+lsLocal] = resp{out: `{"sessions":[{"session_id":"r1","label":"Lambe · LAM-5"}]}`}
	m := readyMod(t, f, nil)
	f.calls = nil
	items := []core.WorkspaceItem{
		{Kind: core.WorkspaceRun, ID: "LAM-5", Host: "mbp"},
		{Kind: core.WorkspaceTask, ID: "LAM-5", Host: "mbp"}, // same host: one call
		{Kind: core.WorkspaceRun, ID: "LAM-1", Host: "ME"},   // this machine, any case: no -S
		{Kind: core.WorkspaceLease, ID: "LAM-7"},             // no host: local
	}
	rep, err := m.Workspace(context.Background(), items)
	if err != nil || len(rep.Placements) != 4 || rep.State != core.Connected {
		t.Fatalf("%v %+v", err, rep)
	}
	calls := f.called()
	counts := map[string]int{}
	for _, c := range calls {
		counts[c]++
	}
	if len(calls) != 2 || counts["-S mbp "+lsLocal] != 1 || counts[lsLocal] != 1 {
		t.Fatalf("calls: %v", calls)
	}
	if p, _ := rep.For(items[0]); p.Host != "mbp" || p.Session != "r1" {
		t.Fatalf("%+v", p)
	}
}

func TestUnsafeAndTooManyHostsAreNotContactedAndDegrade(t *testing.T) {
	f := fleetFake(t)
	m := readyMod(t, f, nil)
	f.calls = nil
	items := []core.WorkspaceItem{{ID: "LAM-1", Host: "-oProxyCommand=x"}, {ID: "LAM-1", Host: "a b"}}
	for i := 0; i < 12; i++ {
		items = append(items, core.WorkspaceItem{Kind: core.WorkspaceRun, ID: "LAM-1", Host: fmt.Sprintf("h%d", i)})
	}
	for _, h := range items[2:] {
		f.resp["-S "+h.Host+" "+lsLocal] = resp{out: `{"sessions":[]}`}
	}
	rep, _ := m.Workspace(context.Background(), items)
	if rep.State != core.Degraded || !strings.Contains(rep.Reason, "not safe") || !strings.Contains(rep.Reason, "more than 8 hosts") {
		t.Fatalf("%+v", rep)
	}
	if n := len(f.called()); n != 8 {
		t.Fatalf("%d hosts contacted: %v", n, f.called())
	}
	for _, c := range f.called() {
		if strings.Contains(c, "ProxyCommand") || strings.Contains(c, "a b") {
			t.Fatalf("unsafe host contacted: %q", c)
		}
	}
}

func TestOneHostFailingDegradesButKeepsTheOthers(t *testing.T) {
	f := fleetFake(t)
	f.resp["-S down "+lsLocal] = resp{exit: 4}
	f.resp["-S bad "+lsLocal] = resp{exit: 1}
	m := readyMod(t, f, nil)
	rep, err := m.Workspace(context.Background(), []core.WorkspaceItem{{ID: "LAM-1"}, {ID: "LAM-1", Host: "down"}, {ID: "LAM-1", Host: "bad"}})
	if err != nil || rep.State != core.Degraded || len(rep.Placements) != 1 || !strings.Contains(rep.Reason, "exited 1") {
		t.Fatalf("%v %+v", err, rep)
	}
}

func TestHungRexIsBoundedAndCancelReturnsTheContextError(t *testing.T) {
	f := fleetFake(t)
	m := readyMod(t, f, nil)
	m.o.CallTimeout = 20 * time.Millisecond
	f.mu.Lock()
	f.hang = true
	f.mu.Unlock()
	start := time.Now()
	rep, err := m.Workspace(context.Background(), []core.WorkspaceItem{{ID: "LAM-1"}})
	if err != nil || rep.State != core.Degraded || time.Since(start) > 2*time.Second {
		t.Fatalf("%v %+v after %s", err, rep, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Workspace(ctx, []core.WorkspaceItem{{ID: "LAM-1"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// Through core a failure is a degraded report, never an error.
	got := core.ReadWorkspace(ctx, []core.Entry{{Module: m, Detection: core.Detection{State: core.Ready}}}, []core.WorkspaceItem{{ID: "LAM-1"}})
	if got.State != core.Degraded {
		t.Fatalf("%+v", got)
	}
}

func TestOpenPlanIsTheHandoffThenTheCommandToCopy(t *testing.T) {
	m := newMod(&fake{}, nil) // not detected yet: no located binary, so the bare name
	plan := m.Open(core.Placement{Host: "mbp", Label: "Lambe · LAM-1", Session: "s"})
	if len(plan.Rungs) != 1 || plan.Rungs[0].Name != "handoff" || !plan.Rungs[0].Interactive || strings.Join(plan.Rungs[0].Argv, " ") != "rex -S mbp" || plan.Copy != "rex -S mbp" {
		t.Fatalf("%+v", plan)
	}
	if got := m.Open(core.Placement{Host: "me@host.local"}); got.Copy != "rex -S me@host.local" {
		t.Fatalf("%+v", got)
	}
	// With a located binary, the handoff and the copy line use it.
	d := readyMod(t, fleetFake(t), nil)
	plan = d.Open(core.Placement{Host: "mbp"})
	if strings.Join(plan.Rungs[0].Argv, " ") != "/fake/rex -S mbp" || plan.Copy != "/fake/rex -S mbp" {
		t.Fatalf("the located binary must be used: %+v", plan)
	}
	for _, h := range []string{"-x", "a b", "a;b"} {
		plan := m.Open(core.Placement{Host: h, Label: "work"})
		if len(plan.Rungs) != 0 || plan.Copy != "" || plan.Note == "" || plan.Rungs == nil {
			t.Errorf("host %q: %+v", h, plan)
		}
	}
	if f := (&fake{}); len(f.called()) != 0 {
		t.Fatal("Open must not run anything")
	}
}

func TestOpenPlanForASessionOnThisMachineIsACopyPathNotARung(t *testing.T) {
	m := newMod(&fake{}, nil)
	plan := m.Open(core.Placement{Label: "Lambe · LAM-1"})
	if plan.Rungs == nil || len(plan.Rungs) != 0 || plan.Copy != "rex" || !strings.Contains(plan.Note, "Lambe · LAM-1") {
		t.Fatalf("%+v", plan)
	}
	if plan := m.Open(core.Placement{}); plan.Copy != "rex" || !strings.Contains(plan.Note, "this item") {
		t.Fatalf("no label: %+v", plan)
	}
	d := readyMod(t, fleetFake(t), nil)
	if plan := d.Open(core.Placement{Label: "x"}); plan.Copy != "/fake/rex" {
		t.Fatalf("the located binary: %+v", plan)
	}
	// A label with quotes and control characters stays inside the note, never in the command.
	plan = m.Open(core.Placement{Label: "a\x1b[2J\"; rm -rf /"})
	if strings.ContainsRune(plan.Note, 0x1b) || strings.Contains(plan.Copy, "rm") {
		t.Fatalf("%+v", plan)
	}
}

func TestNoToolNamesInTheOpenPlanText(t *testing.T) {
	m := newMod(&fake{}, nil)
	for _, p := range []core.Placement{{Label: "x"}, {Host: "-x"}, {Host: "mbp", Match: core.MatchHostNotConfigured}} {
		plan := m.Open(p)
		for _, tool := range []string{"rex", "lambe", "beads", "board", "bd "} {
			if strings.Contains(strings.ToLower(plan.Note), tool) {
				t.Errorf("%+v: the note names %q: %q", p, tool, plan.Note)
			}
		}
		for _, r := range plan.Rungs {
			if strings.Contains(r.Name, "rex") {
				t.Errorf("rung name %q", r.Name)
			}
		}
	}
}

func TestOpenRunsNothing(t *testing.T) {
	f := fleetFake(t)
	m := readyMod(t, f, nil)
	before := len(f.called())
	m.Open(core.Placement{Host: "mbp"})
	if len(f.called()) != before {
		t.Fatal("Open ran a command")
	}
}

func TestQuietFollowsPresence(t *testing.T) {
	f := &fake{resp: map[string]resp{lsLocal: {out: fixture(t, "ls-null.json")}, verLocal: {out: fixture(t, "version-13.json")}}}
	m := newMod(f, nil)
	if !m.Quiet() {
		t.Fatal("before detection: quiet")
	}
	detect(m)
	if m.Quiet() {
		t.Fatal("a Rex that is there is listed")
	}
	reg := core.NewRegistry(m)
	if got := core.VersionReport("t", reg.Detect(context.Background())); len(got.Modules) != 1 {
		t.Fatalf("%+v", got.Modules)
	}
	gone := New(Options{LookPath: func(string) (string, error) { return "", errors.New("x") }, Getenv: func(string) string { return "" }, Runner: f})
	if got := core.VersionReport("t", core.NewRegistry(gone).Detect(context.Background())); len(got.Modules) != 0 {
		t.Fatalf("an absent Rex must not be listed: %+v", got.Modules)
	}
	if ov := core.BuildOverview(context.Background(), core.NewRegistry(gone).Detect(context.Background())); len(ov.Tools) != 0 {
		t.Fatalf("%+v", ov.Tools)
	}
}

func TestStatusSummarizesSessions(t *testing.T) {
	m := readyMod(t, fleetFake(t), nil)
	if st := m.Status(context.Background()); st.Summary != "5 sessions" || st.Unavailable != "" {
		t.Fatalf("%+v", st)
	}
}

func TestLsFixtureFromTheHelpExampleParses(t *testing.T) {
	ss, _, err := parseLS([]byte(fixture(t, "ls-help-example.json")))
	if err != nil || len(ss) != 1 || ss[0].Label != "work" || ss[0].ID != "session:<uuid>" {
		t.Fatalf("%+v %v", ss, err)
	}
	if ss, _, err := parseLS([]byte(fixture(t, "ls-null.json"))); err != nil || len(ss) != 0 {
		t.Fatalf("%+v %v", ss, err)
	}
}

func TestSessionListIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"sessions":[`)
	for i := 0; i < maxSessions+50; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"session_id":"s%d","label":"x"}`, i)
	}
	b.WriteString(`]}`)
	if ss, trunc, err := parseLS([]byte(b.String())); len(ss) != maxSessions || !trunc || err != nil {
		t.Fatalf("%d %v %v", len(ss), trunc, err)
	}
}

func TestDetectResetsStateEachRun(t *testing.T) {
	f := &fake{resp: map[string]resp{lsLocal: {exit: 4}}}
	m := newMod(f, nil)
	detect(m)
	delete(f.resp, lsLocal)
	f.resp[lsLocal] = resp{exit: 1}
	if d := detect(m); d.State != core.Incompatible {
		t.Fatalf("%+v", d)
	}
}

func TestSchemaDriftInLsIsAnErrorNotAHealthyEmptyList(t *testing.T) {
	for _, body := range []string{``, `null`, `{}`, `[]`, `"x"`, `{"items":[]}`, `{"sessions":{}}`, `{"sessions":"x"}`, `{"sessions":[1]}`,
		`{"sessions":[{"label":"x"}]}`, `{"sessions":[{"session_id":""}]}`, `{"sessions":[{"session_id":5}]}`, `{"sessions":[{"session_id":"a","label":7}]}`} {
		if ss, _, err := parseLS([]byte(body)); err == nil || ss != nil || err.Error() != "unrecognized rex ls output" {
			t.Errorf("%q: %v %v", body, ss, err)
		}
	}
	for _, body := range []string{`{"sessions":null}`, `{"sessions":[]}`, `{"sessions":[{"session_id":"a"}],"x":1}`} {
		if _, _, err := parseLS([]byte(body)); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
	// Through the module: a drifted shape is Degraded with the reason, and Status is unavailable.
	f := fleetFake(t)
	m := readyMod(t, f, nil)
	f.resp[lsLocal] = resp{out: `{"data":{"sessions":[]}}`}
	rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{{ID: "LAM-1"}})
	if rep.State != core.Degraded || !strings.Contains(rep.Reason, "unrecognized rex ls output") {
		t.Fatalf("%+v", rep)
	}
	if st := m.Status(context.Background()); !strings.Contains(st.Unavailable, "unrecognized rex ls output") {
		t.Fatalf("%+v", st)
	}
	// and at detection time the probe says so
	f = &fake{resp: map[string]resp{lsLocal: {out: `{}`}}}
	if d := detect(newMod(f, nil)); d.State != core.Incompatible || !strings.Contains(d.Detail, "unrecognized rex ls output") {
		t.Fatalf("%+v", d)
	}
}

func sessionsJSON(n int) string {
	var b strings.Builder
	b.WriteString(`{"sessions":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"session_id":"s%d","label":"x"}`, i)
	}
	return b.String() + `]}`
}

func TestTheSessionLimitIsExactlyMaxSessions(t *testing.T) {
	for _, tc := range []struct {
		n     int
		kept  int
		trunc bool
	}{{499, 499, false}, {500, 500, false}, {501, 500, true}} {
		ss, trunc, err := parseLS([]byte(sessionsJSON(tc.n)))
		if err != nil || len(ss) != tc.kept || trunc != tc.trunc {
			t.Errorf("%d sessions: kept %d truncated %v err %v", tc.n, len(ss), trunc, err)
		}
	}
}

func TestMatchingSeesTheWholeLabelWhileTheDisplayIsClipped(t *testing.T) {
	long := strings.Repeat("x ", 100) + "LAM-1 at the end"
	f := &fake{resp: map[string]resp{lsLocal: {out: fmt.Sprintf(`{"sessions":[{"session_id":"s1","label":%q}]}`, long)}, verLocal: {out: fixture(t, "version-13.json")}}}
	m := readyMod(t, f, nil)
	it := core.WorkspaceItem{Kind: core.WorkspaceRun, ID: "LAM-1"}
	rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{it})
	p, _ := rep.For(it)
	if p == nil || p.Match != core.MatchLabel || len([]rune(p.Label)) != core.MaxLabelRunes {
		t.Fatalf("an id past the clip must still match, with a clipped display: %+v", p)
	}
}

func TestDottedChildIDsDoNotClaimTheirParent(t *testing.T) {
	f := &fake{resp: map[string]resp{lsLocal: {out: `{"sessions":[{"session_id":"s1","label":"Lambe · bd-1.2"}]}`}, verLocal: {out: fixture(t, "version-13.json")}}}
	m := readyMod(t, f, nil)
	parent, child := core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "bd-1"}, core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "bd-1.2"}
	rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{parent, child})
	if p, _ := rep.For(parent); p != nil {
		t.Fatalf("bd-1 must not match the window of bd-1.2: %+v", p)
	}
	if p, _ := rep.For(child); p == nil || p.Match != core.MatchExact {
		t.Fatalf("%+v", p)
	}
}

func TestLabelConventionsComeFromOptions(t *testing.T) {
	f := &fake{resp: map[string]resp{lsLocal: {out: `{"sessions":[{"session_id":"s1","label":"agent/LAM-1"},{"session_id":"s2","label":"Lambe · LAM-2"}]}`}, verLocal: {out: fixture(t, "version-13.json")}}}
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f,
		LabelConventions: []string{"agent/{id}"}})
	detect(m)
	a, b := core.WorkspaceItem{ID: "LAM-1"}, core.WorkspaceItem{ID: "LAM-2"}
	rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{a, b})
	if p, _ := rep.For(a); p == nil || p.Match != core.MatchExact {
		t.Fatalf("%+v", p)
	}
	if p, _ := rep.For(b); p == nil || p.Match != core.MatchLabel {
		t.Fatalf("the default Lambe form is not exact under a custom list: %+v", p)
	}
}

func TestValidHost(t *testing.T) {
	for _, h := range []string{"mbp", "h0", "me@mbp.local", "host:22", "a-b.c_d", "user.name@host-1:65535", "10.0.0.1", "a", "Mbp", "u_1@h:1",
		strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)} { // 253 characters
		if !ValidHost(h) {
			t.Errorf("%q should be valid", h)
		}
	}
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 62)
	for _, h := range []string{"", "a@b@c", "a:b:c", "host:", "host:0", "host:65536", "host:99999", "host:000000", ":22", "@host", "user@", "-h", "-oProxyCommand=x",
		".h", "h.", "h..x", "h-", "-h-", "a b", "a;b", "a\nb", "a/b", "[::1]", "::1", "é", "h:a", "-u@h", "u@@h", "u@-h", "host:22:22", "a@:22",
		strings.Repeat("u", 33) + "@h", long, "ssh://h", "h?x", "h#x", "h%20", "a\x00b", "h:+22"} {
		if ValidHost(h) {
			t.Errorf("%q should be invalid", h)
		}
	}
	if len(long) != core.MaxHostRunes+1 {
		t.Fatalf("test host is %d long", len(long))
	}
}

func TestParseHostsAndPassEnvDropWhatIsInvalid(t *testing.T) {
	if got := ParseHosts(" mbp , bad host,MBP,-x, me@h:22 ,,srv"); strings.Join(got, "|") != "mbp|me@h:22|srv" {
		t.Fatalf("%q", got)
	}
	if got := ParseHosts(""); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, fmt.Sprintf("h%d", i))
	}
	if got := ParseHosts(strings.Join(many, ",")); len(got) != 16 {
		t.Fatalf("%d hosts", len(got))
	}
	if got := ParsePassEnv("SSH_AUTH_SOCK, LD_PRELOAD,DYLD_X,BASH_ENV,ENV,lower,A B,SSH_AUTH_SOCK,HTTPS_PROXY,1X,"); strings.Join(got, "|") != "SSH_AUTH_SOCK|HTTPS_PROXY" {
		t.Fatalf("%q", got)
	}
	many = nil
	for i := 0; i < 20; i++ {
		many = append(many, fmt.Sprintf("V%d", i))
	}
	if got := ParsePassEnv(strings.Join(many, ",")); len(got) != 8 {
		t.Fatalf("%d names", len(got))
	}
}

func TestScrubEnvWithOptInKeepsOnlyNamedExtras(t *testing.T) {
	in := []string{"HOME=/h", "SSH_AUTH_SOCK=/s", "HTTPS_PROXY=p", "DISPLAY=:0", "LD_PRELOAD=/x", "XDG_CURRENT_DESKTOP=d", "XDG_SESSION_ID=3", "XDG_CACHE_HOME=/c", "XDG_STATE_HOME=/st", "XDG_DATA_HOME=/d"}
	if got := strings.Join(ScrubEnv(in), " "); got != "HOME=/h XDG_CACHE_HOME=/c XDG_STATE_HOME=/st XDG_DATA_HOME=/d" {
		t.Fatalf("default: %s", got)
	}
	got := strings.Join(ScrubEnvWith([]string{"SSH_AUTH_SOCK", "LD_PRELOAD", "bad name"})(in), " ")
	if got != "HOME=/h SSH_AUTH_SOCK=/s XDG_CACHE_HOME=/c XDG_STATE_HOME=/st XDG_DATA_HOME=/d" {
		t.Fatalf("opt-in: %s", got)
	}
}

func TestPassEnvReachesARealProcessOnlyWhenOptedIn(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	stub := filepath.Join(dir, "stub-not-rex")
	script := "#!/bin/sh\n/usr/bin/env > \"$REX_TEST_OUT\"\ncase \"$*\" in *version*) echo '{\"api_revision\":13}';; *) echo '{\"sessions\":[]}';; esac\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX_TEST_OUT", out)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	for _, pass := range [][]string{nil, {"SSH_AUTH_SOCK"}} {
		m := New(Options{Bin: stub, Getenv: func(string) string { return "" }, PassEnv: pass})
		if d := m.Detect(context.Background()); d.State != core.Ready {
			t.Fatalf("%+v", d)
		}
		b, _ := os.ReadFile(out)
		if has := strings.Contains(string(b), "SSH_AUTH_SOCK=/tmp/agent.sock"); has != (pass != nil) {
			t.Errorf("pass %v: SSH_AUTH_SOCK present = %v", pass, has)
		}
	}
}

func TestOnlyReadArgvsWithTheAutostartGuardInEveryPositionRun(t *testing.T) {
	for _, a := range [][]string{
		{"-S", "mbp", "--autostart=true", "ls", "--json"},
		{"-S", "mbp", "--autostart", "ls", "--json"},
		{"-S", "mbp", "--autostart=", "ls", "--json"},
		{"-S", "mbp", "-autostart=false", "ls", "--json"},
		{"-S", "mbp", "ls", "--autostart=false", "--json"},
		{"-S", "mbp", "--autostart=false", "ls", "--json "},
		{"-s", "mbp", "--autostart=false", "ls", "--json"},
		{"-S", "mbp", "--autostart=false", "ls", "-json"},
		{"-S", "mbp", "--autostart=false", "ls", "--json", ""},
		{"--autostart=false", "ls", "--json", "x"},
	} {
		if allowedArgv(a) {
			t.Errorf("%q must be refused", a)
		}
	}
	if !allowedArgv([]string{"-S", "mbp", "--autostart=false", "ls", "--json"}) {
		t.Error("the one remote read must stay allowed")
	}
}

func TestOnlyThisMachineAndConfiguredHostsAreEverContacted(t *testing.T) {
	f := fleetFake(t)
	f.resp["-S mbp "+lsLocal] = resp{out: `{"sessions":[{"session_id":"r1","label":"Lambe · LAM-5"}]}`}
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f,
		Hostname: func() string { return "me" }, Hosts: []string{"MBP", "-bad", "a b"}, CacheTTL: -1})
	detect(m)
	f.calls = nil
	items := []core.WorkspaceItem{
		{Kind: core.WorkspaceRun, ID: "LAM-5", Host: "mbp"},          // configured (any case)
		{Kind: core.WorkspaceRun, ID: "LAM-1", Host: "ME"},           // this machine
		{Kind: core.WorkspaceRun, ID: "LAM-7", Host: "evil.example"}, // well-formed but nobody configured it
		{Kind: core.WorkspaceRun, ID: "LAM-8", Host: "a b"},          // not even a name
	}
	rep, err := m.Workspace(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.called() {
		if c != "-S MBP "+lsLocal && c != lsLocal {
			t.Fatalf("contacted %q; the allowlist's spelling and this machine are all that may be", c)
		}
	}
	if len(f.called()) != 2 {
		t.Fatalf("%v", f.called())
	}
	p, _ := rep.For(items[2])
	if p == nil || p.Match != core.MatchHostNotConfigured || p.Session != "" || p.Host != "evil.example" || p.ID == "" {
		t.Fatalf("an unconfigured host is a placement that says so: %+v", p)
	}
	if p, _ := rep.For(items[3]); p != nil {
		t.Fatalf("an invalid host is not placed: %+v", p)
	}
	if rep.State != core.Degraded || !strings.Contains(rep.Reason, "not safe") {
		t.Fatalf("%+v", rep)
	}
	plan := m.Open(*p)
	if len(plan.Rungs) != 0 || plan.Rungs == nil || plan.Copy != "/fake/rex -S evil.example" || !strings.Contains(plan.Note, "configured") {
		t.Fatalf("copy-string handoff only, no rung: %+v", plan)
	}
	// no allowlist at all: every remote machine is "not configured"
	m = New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f, Hostname: func() string { return "me" }, CacheTTL: -1})
	detect(m)
	f.calls = nil
	rep, _ = m.Workspace(context.Background(), items[:1])
	if len(f.called()) != 0 || len(rep.Placements) != 1 || rep.Placements[0].Match != core.MatchHostNotConfigured {
		t.Fatalf("%v %+v", f.called(), rep)
	}
}

func TestHostReadsRunConcurrently(t *testing.T) {
	f := fleetFake(t)
	for _, h := range []string{"h0", "h1", "h2"} {
		f.resp["-S "+h+" "+lsLocal] = resp{out: `{"sessions":[]}`}
	}
	m := readyMod(t, f, nil)
	f.mu.Lock()
	f.calls, f.barrier, f.inflight = nil, 4, 0
	f.mu.Unlock()
	items := []core.WorkspaceItem{{ID: "LAM-1"}, {ID: "LAM-1", Host: "h0"}, {ID: "LAM-1", Host: "h1"}, {ID: "LAM-1", Host: "h2"}}
	rep, err := m.Workspace(context.Background(), items)
	if err != nil || rep.State != core.Connected {
		t.Fatalf("four hosts must be read at the same time: %v %+v", err, rep)
	}
}

func TestSessionListsAreCachedForAShortTTLAndMarkedStale(t *testing.T) {
	now := time.Unix(1000, 0)
	f := fleetFake(t)
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f, Hostname: func() string { return "me" },
		CacheTTL: 3 * time.Second, Now: func() time.Time { return now }})
	detect(m)
	f.calls = nil
	it := []core.WorkspaceItem{{ID: "LAM-1"}}
	first, _ := m.Workspace(context.Background(), it)
	if first.Stale || len(f.called()) != 1 {
		t.Fatalf("first read is fresh: %+v %v", first, f.called())
	}
	now = now.Add(2999 * time.Millisecond)
	again, _ := m.Workspace(context.Background(), it)
	m.Status(context.Background())
	if !again.Stale || len(f.called()) != 1 || len(again.Placements) != 1 {
		t.Fatalf("within the TTL the cached list is reused and marked stale: %+v %v", again, f.called())
	}
	now = now.Add(2 * time.Millisecond) // 3.001 s
	fresh, _ := m.Workspace(context.Background(), it)
	if fresh.Stale || len(f.called()) != 2 {
		t.Fatalf("after the TTL it is read again: %+v %v", fresh, f.called())
	}
	// a new detection forgets the cache
	detect(m)
	f.calls = nil
	m.Workspace(context.Background(), it)
	if len(f.called()) != 1 {
		t.Fatal("detect must clear the cache")
	}
}

func TestASlowRexIsPaidForOncePerTTLAndACanceledReadIsNotCached(t *testing.T) {
	now := time.Unix(1000, 0)
	f := fleetFake(t)
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f, Hostname: func() string { return "me" },
		CallTimeout: 10 * time.Millisecond, CacheTTL: time.Minute, Now: func() time.Time { return now }})
	detect(m)
	f.mu.Lock()
	f.hang, f.calls = true, nil
	f.mu.Unlock()
	it := []core.WorkspaceItem{{ID: "LAM-1"}}
	for i := 0; i < 3; i++ {
		rep, _ := m.Workspace(context.Background(), it)
		if rep.State != core.Degraded {
			t.Fatalf("%+v", rep)
		}
	}
	if n := len(f.called()); n != 1 {
		t.Fatalf("a hung rex must cost one call per TTL, not one per read: %d", n)
	}
	// a canceled read says nothing about rex and is not remembered
	g := fleetFake(t)
	m2 := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: g, Hostname: func() string { return "me" }, CacheTTL: time.Minute})
	detect(m2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2.Workspace(ctx, it)
	g.calls = nil
	if rep, _ := m2.Workspace(context.Background(), it); rep.State != core.Connected || len(g.called()) != 1 {
		t.Fatalf("%+v %v", rep, g.called())
	}
}

func TestTruncationIsSurfacedAsAMarker(t *testing.T) {
	for _, tc := range []struct {
		n     int
		trunc bool
	}{{500, false}, {501, true}} {
		f := &fake{resp: map[string]resp{lsLocal: {out: sessionsJSON(tc.n)}, verLocal: {out: fixture(t, "version-13.json")}}}
		m := readyMod(t, f, nil)
		rep, _ := m.Workspace(context.Background(), []core.WorkspaceItem{{ID: "LAM-1"}})
		if rep.Truncated != tc.trunc {
			t.Errorf("%d sessions: truncated = %v", tc.n, rep.Truncated)
		}
		if st := m.Status(context.Background()); (st.Summary == "500+ sessions") != tc.trunc || st.Summary == "" {
			t.Errorf("%d sessions: status %q", tc.n, st.Summary)
		}
	}
}

func TestStatusSaysSessionInTheSingular(t *testing.T) {
	for n, want := range map[int]string{0: "0 sessions", 1: "1 session", 2: "2 sessions"} {
		f := &fake{resp: map[string]resp{lsLocal: {out: sessionsJSON(n)}, verLocal: {out: fixture(t, "version-13.json")}}}
		if st := readyMod(t, f, nil).Status(context.Background()); st.Summary != want {
			t.Errorf("%d: %q, want %q", n, st.Summary, want)
		}
	}
}
