package beadslease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

type result struct {
	out  string
	exit int
	err  error
}

// fake records every argv it is asked to run, so tests can prove what executed.
type fake struct {
	resp  map[string]result
	calls []string
	block bool
}

func (f *fake) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if f.block {
		<-ctx.Done()
		return nil, -1, ctx.Err()
	}
	r, ok := f.resp[key]
	if !ok {
		return nil, -1, fmt.Errorf("unexpected command %q", key)
	}
	return []byte(r.out), r.exit, r.err
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func found(string) (string, error) { return "/fake/beads-lease", nil }

const versionJSON = `{"api_version":1,"name":"beads-lease","version":"0.2.0"}` + "\n"

const versionLine = "beads-lease 0.1.0 — the fleet's keeper of leases\n"

func newMod(f *fake, o Options) *Module {
	o.Runner, o.LookPath = f, found
	return New(o)
}

func TestDetectStates(t *testing.T) {
	cases := []struct {
		name string
		f    *fake
		look func(string) (string, error)
		bin  string
		want core.State
	}{
		{"ready (legacy)", &fake{resp: map[string]result{"version": {out: versionLine}}}, found, "", core.Ready},
		{"ready (json)", &fake{resp: map[string]result{"version --json": {out: versionJSON}}}, found, "", core.Ready},
		{"json api_version 2 is incompatible", &fake{resp: map[string]result{"version --json": {out: `{"api_version":2,"version":"9.0.0"}`}}}, found, "", core.Incompatible},
		{"json exit 1 then legacy exit 1 is broken", &fake{resp: map[string]result{"version --json": {exit: 1}, "version": {exit: 1}}}, found, "", core.Broken},
		{"missing on PATH is absent", &fake{}, func(string) (string, error) { return "", os.ErrNotExist }, "", core.Absent},
		{"explicit missing bin is absent", &fake{}, found, "/nonexistent/beads-lease", core.Absent},
		{"exit 1 is broken", &fake{resp: map[string]result{"version": {exit: 1}}}, found, "", core.Broken},
		{"runner error is broken", &fake{resp: map[string]result{"version": {err: errors.New("boom")}}}, found, "", core.Broken},
		{"unrelated text is incompatible", &fake{resp: map[string]result{"version": {out: "hello world\n"}}}, found, "", core.Incompatible},
	}
	for _, c := range cases {
		m := New(Options{Runner: c.f, LookPath: c.look, Bin: c.bin})
		if d := m.Detect(context.Background()); d.State != c.want {
			t.Errorf("%s: got %+v, want %s", c.name, d, c.want)
		}
	}
	d := New(Options{Runner: &fake{resp: map[string]result{"version": {out: versionLine}}}, LookPath: found}).Detect(context.Background())
	if d.Version != "0.1.0" || d.APIVersion != 0 {
		t.Fatalf("version/api: %+v (api_version stays unknown until beads-lease ships version --json)", d)
	}
}

func TestStatusSummarizesHeldStaleAndFreeLeases(t *testing.T) {
	f := &fake{resp: map[string]result{"version": {out: versionLine}, "--json list": {out: fixture(t, "list.json")}}}
	m := newMod(f, Options{})
	m.Detect(context.Background())
	st := m.Status(context.Background())
	if st.Unavailable != "" || st.Summary != "1 held, 1 stale, 1 free" {
		t.Fatalf("status = %+v", st)
	}
	joined := strings.Join(st.Lines, "\n")
	for _, want := range []string{"cpu-quiet-window", "glm-andrew-bierman", "2026-11-21T20:15:00Z", "deploy-slot", "old-agent", "stale", "bench", "free"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lines missing %q:\n%s", want, joined)
		}
	}
}

func TestStatusEmptyHub(t *testing.T) {
	f := &fake{resp: map[string]result{"--json list": {out: fixture(t, "list-empty.json")}}}
	m := newMod(f, Options{})
	if st := m.Status(context.Background()); st.Unavailable != "" || st.Summary != "0 held, 0 stale, 0 free" {
		t.Fatalf("status = %+v", st)
	}
}

func TestUnsupportedOrNotOKEnvelopeIsUnavailableNotAnError(t *testing.T) {
	for name, out := range map[string]string{
		"schema 2": `{"schemaVersion":2,"ok":true}`,
		"ok false": `{"schemaVersion":1,"ok":false}`,
		"not json": `oops`,
		"empty":    ``,
	} {
		m := newMod(&fake{resp: map[string]result{"--json list": {out: out}}}, Options{})
		if st := m.Status(context.Background()); st.Unavailable == "" || st.Summary != "" {
			t.Errorf("%s: want unavailable with a reason, got %+v", name, st)
		}
	}
}

func TestUsageErrorNamesTheMissingActorVariableWithoutControlCharacters(t *testing.T) {
	body := `{"schemaVersion":1,"ok":false,"error":{"reason":"usage","exitCode":2,"message":"BEADS_ACTOR is not set; export BEADS_ACTOR=<agent-machine>\u001b[2J"}}`
	m := newMod(&fake{resp: map[string]result{"--json list": {out: body, exit: 2}}}, Options{})
	st := m.Status(context.Background())
	if !strings.Contains(st.Unavailable, "actor name") {
		t.Fatalf("unavailable = %q, want the missing actor named", st.Unavailable)
	}
	for _, r := range st.Unavailable {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			t.Fatalf("control rune %U in %q", r, st.Unavailable)
		}
	}
}

func TestBlockingListReturnsUnavailableWithinTheDeadline(t *testing.T) {
	m := newMod(&fake{block: true}, Options{Timeout: 100 * time.Millisecond})
	start := time.Now()
	st := m.Status(context.Background())
	if st.Unavailable == "" || time.Since(start) > 2*time.Second {
		t.Fatalf("hung list not cut off: %+v after %v", st, time.Since(start))
	}
}

func TestLegacyBinaryDoctorIsASkipThatNeverRuns(t *testing.T) {
	f := &fake{resp: map[string]result{"version": {out: versionLine}}}
	m := newMod(f, Options{})
	m.Detect(context.Background())
	f.calls = nil
	checks := m.Doctor(context.Background())
	if len(checks) != 1 || checks[0].Status != core.StatusSkip || !strings.Contains(checks[0].Detail, "checks are skipped") {
		t.Fatalf("doctor = %+v", checks)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a legacy binary must not be asked to doctor: %v", f.calls)
	}
}

func TestOnlyTheFourReadOnlyArgvsEverExecute(t *testing.T) {
	f := &fake{resp: map[string]result{
		"version --json": {out: versionJSON}, "doctor --json": {out: fixture(t, "doctor-ok.json")},
		"--json list": {out: fixture(t, "list.json")},
	}}
	m := newMod(f, Options{})
	m.Detect(context.Background())
	m.Doctor(context.Background())
	m.Status(context.Background())
	if got, want := strings.Join(f.calls, "|"), "version --json|doctor --json|--json list"; got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestAllowlistAcceptsExactlyTheFourReadOnlyArgvs(t *testing.T) {
	f := &fake{resp: map[string]result{}}
	for _, k := range []string{"version", "version --json", "doctor --json", "--json list"} {
		f.resp[k] = result{}
	}
	r := New(Options{Runner: f, LookPath: found}).run
	for _, argv := range [][]string{{"version"}, {"version", "--json"}, {"doctor", "--json"}, {"--json", "list"}} {
		if _, _, err := r.Run(context.Background(), "/fake/beads-lease", argv...); err != nil {
			t.Errorf("argv %v refused: %v", argv, err)
		}
	}
}

func TestAllowlistRejectsEveryWriteOrSyncCommand(t *testing.T) {
	f := &fake{}
	r := New(Options{Runner: f, LookPath: found}).run
	for _, argv := range [][]string{
		{"acquire", "x"}, {"release", "x"}, {"heartbeat", "x"}, {"run", "x", "--", "true"},
		{"status"}, {"--json", "status"}, {"sync"}, {"list"}, {},
		{"doctor"}, {"acquire"}, {"release"}, {"heartbeat"}, {"run"}, {"--json"}, {"--json", "doctor"}, {"--json", "version"}, {"doctor", "--json", "--fix"}, {"doctor", "--json", "x"},
		{"version", "--json", "x"}, {"version", "x"}, {"list", "--json"}, {"--json", "list", "x"}, {"completion"},
	} {
		if _, _, err := r.Run(context.Background(), "/fake/beads-lease", argv...); err == nil {
			t.Errorf("argv %v was allowed", argv)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("a rejected argv reached the runner: %v", f.calls)
	}
}

func TestLeaseFieldsAreSanitizedAndOddCasesRenderSafely(t *testing.T) {
	body := `{"schemaVersion":1,"ok":true,"leases":[
	  {"resource":"resource:a\u001b]0;x\u0007","holder":"h\u001b[2J","state":"held"},
	  {"resource":"resource:b","holder":"o\u001b[31m","state":"stale"},
	  {"resource":"resource:c\u001b[1m","state":"mystery\u001b[0m"}]}`
	m := newMod(&fake{resp: map[string]result{"--json list": {out: body}}}, Options{})
	st := m.Status(context.Background())
	if st.Summary != "1 held, 1 stale, 0 free" || len(st.Lines) != 3 {
		t.Fatalf("status = %+v (an unknown state still gets a line but is not counted)", st)
	}
	for _, l := range append([]string{st.Summary}, st.Lines...) {
		for _, r := range l {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				t.Fatalf("control rune %U in %q", r, l)
			}
		}
	}
	if !strings.Contains(st.Lines[0], "expires ?") {
		t.Errorf("a held lease with no expiry must show '?': %q", st.Lines[0])
	}
}

func TestListExitAndOutputEdgeCases(t *testing.T) {
	cases := map[string]result{
		"ok envelope but exit 1": {out: `{"schemaVersion":1,"ok":true}`, exit: 1},
		"non-json with exit 1":   {out: "boom", exit: 1},
		"runner error on list":   {err: errors.New("spawn failed")},
	}
	for name, r := range cases {
		m := newMod(&fake{resp: map[string]result{"--json list": r}}, Options{})
		if st := m.Status(context.Background()); st.Unavailable == "" || st.Summary != "" {
			t.Errorf("%s: want unavailable with a reason, got %+v", name, st)
		}
	}
}

func TestBareNameBinOverrideResolvesOnPathNotTheWorkingDirectory(t *testing.T) {
	f := &fake{resp: map[string]result{"version": {out: versionLine}}}
	d := New(Options{Bin: "beads-lease", Runner: f, LookPath: found}).Detect(context.Background())
	if d.State != core.Ready {
		t.Fatalf("a bare-name override must resolve through PATH, got %+v", d)
	}
}

func TestDetectPrefersVersionJSONAndReportsAPIVersion(t *testing.T) {
	f := &fake{resp: map[string]result{"version --json": {out: versionJSON}}}
	d := New(Options{Runner: f, LookPath: found}).Detect(context.Background())
	if d.State != core.Ready || d.Version != "0.2.0" || d.APIVersion != 1 {
		t.Fatalf("detection = %+v", d)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v, want only version --json", f.calls)
	}
}

func TestDetectFallsBackToLegacyWhenJSONIsMissingOrUnparsable(t *testing.T) {
	for name, out := range map[string]string{
		"old binary prints the plain line": versionLine,
		"empty":                            "",
		"json without api_version":         `{"version":"0.1.0"}`,
		"json array":                       `[1]`,
	} {
		f := &fake{resp: map[string]result{"version --json": {out: out}, "version": {out: versionLine}}}
		d := New(Options{Runner: f, LookPath: found}).Detect(context.Background())
		if d.State != core.Ready || d.Version != "0.1.0" || d.APIVersion != 0 {
			t.Errorf("%s: detection = %+v, want ready, 0.1.0, api_version unknown", name, d)
		}
	}
}

func TestDetectSanitizesJSONVersion(t *testing.T) {
	f := &fake{resp: map[string]result{"version --json": {out: `{"api_version":2,"version":"1.0\u001b[2J"}`}}}
	d := New(Options{Runner: f, LookPath: found}).Detect(context.Background())
	for _, r := range d.Version + d.Detail {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			t.Fatalf("control rune %U in %+v", r, d)
		}
	}
}

func readyMod(t *testing.T, doctor result) (*Module, *fake) {
	t.Helper()
	f := &fake{resp: map[string]result{"version --json": {out: versionJSON}, "doctor --json": doctor}}
	m := newMod(f, Options{})
	if d := m.Detect(context.Background()); d.State != core.Ready {
		t.Fatal(d)
	}
	return m, f
}

func TestDoctorRunsRealChecksAndMapsThem(t *testing.T) {
	m, _ := readyMod(t, result{out: fixture(t, "doctor-ok.json")})
	checks := m.Doctor(context.Background())
	if len(checks) != 4 || checks[0].Name != "actor" || checks[3].Name != "hub" {
		t.Fatalf("checks = %+v", checks)
	}
	for _, c := range checks {
		if c.Status != core.StatusOK {
			t.Errorf("%+v", c)
		}
	}
}

func TestDoctorFailExitWithFailingCheckIsNotDoubled(t *testing.T) {
	m, _ := readyMod(t, result{out: fixture(t, "doctor-fail.json"), exit: 1})
	checks := m.Doctor(context.Background())
	if len(checks) != 4 || checks[0].Status != core.StatusFail || checks[0].Fix == "" {
		t.Fatalf("checks = %+v", checks)
	}
}

func TestDoctorExitBackstopAddsFailWhenExitNonZeroWithoutAFailCheck(t *testing.T) {
	m, _ := readyMod(t, result{out: fixture(t, "doctor-ok.json"), exit: 64})
	checks := m.Doctor(context.Background())
	last := checks[len(checks)-1]
	if len(checks) != 5 || last.Status != core.StatusFail || !strings.Contains(last.Detail, "64") {
		t.Fatalf("a non-zero exit with no failing check must not read healthy: %+v", checks)
	}
}

func TestDoctorUnreadableOrFailedToRunReportFails(t *testing.T) {
	for name, r := range map[string]result{
		"not json": {out: "oops"}, "empty": {}, "object not array": {out: `{"a":1}`},
		"runner error": {err: errors.New("boom")},
	} {
		m, _ := readyMod(t, r)
		checks := m.Doctor(context.Background())
		if len(checks) != 1 || checks[0].Status != core.StatusFail {
			t.Errorf("%s: %+v", name, checks)
		}
	}
}

func TestDoctorEmptyReportIsNotHealthy(t *testing.T) {
	m, _ := readyMod(t, result{out: "[]"})
	checks := m.Doctor(context.Background())
	if len(checks) != 1 || checks[0].Status == core.StatusOK {
		t.Fatalf("an empty report proves nothing: %+v", checks)
	}
}

func TestDoctorNormalizesUnknownStatusesAndSanitizesText(t *testing.T) {
	body := `[{"name":"a\u001b[2J","status":"WEIRD","detail":"d\u001b]0;x\u0007","fix":"f\u001b[31m"},{"name":"b","status":"","detail":"ok"}]`
	m, _ := readyMod(t, result{out: body})
	checks := m.Doctor(context.Background())
	if len(checks) != 2 || checks[0].Status != core.StatusWarn || checks[1].Status != core.StatusWarn {
		t.Fatalf("unknown statuses must read warn: %+v", checks)
	}
	for _, c := range checks {
		for _, r := range c.Name + c.Detail + c.Fix {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				t.Fatalf("control rune %U in %+v", r, c)
			}
		}
	}
}

func TestDetectHonoursAnExplicitUnsupportedAPIVersionEvenWithoutAVersion(t *testing.T) {
	for name, out := range map[string]string{
		"api_version 2 alone":   `{"api_version":2}`,
		"api_version 0 present": `{"api_version":0,"version":"1.0.0"}`,
		"api_version 3":         `{"api_version":3,"version":"3.0.0"}`,
	} {
		f := &fake{resp: map[string]result{"version --json": {out: out}, "version": {out: versionLine}}}
		d := New(Options{Runner: f, LookPath: found}).Detect(context.Background())
		if d.State != core.Incompatible {
			t.Errorf("%s: %+v, want incompatible", name, d)
		}
		if len(f.calls) != 1 {
			t.Errorf("%s: fell through to legacy: %v", name, f.calls)
		}
	}
}

func TestDetectStringAPIVersionIsNotReadAsV1(t *testing.T) {
	f := &fake{resp: map[string]result{"version --json": {out: `{"api_version":"1","version":"0.2.0"}`}, "version": {out: versionLine}}}
	d := New(Options{Runner: f, LookPath: found}).Detect(context.Background())
	if d.APIVersion == 1 || d.State == core.Ready && d.APIVersion != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestDetectLegacyWhenVersionJSONFailsButPlainVersionWorks(t *testing.T) {
	f := &fake{resp: map[string]result{"version --json": {exit: 2}, "version": {out: versionLine}}}
	d := New(Options{Runner: f, LookPath: found}).Detect(context.Background())
	if d.State != core.Ready || d.Version != "0.1.0" || d.APIVersion != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestDoctorEmptyReportWithNonZeroExitFails(t *testing.T) {
	for _, body := range []string{"[]", "null"} {
		m, _ := readyMod(t, result{out: body, exit: 64})
		if c := m.Doctor(context.Background()); len(c) != 1 || c[0].Status != core.StatusFail {
			t.Errorf("%s: %+v", body, c)
		}
	}
}

func TestAllowlistMatchesElementsNotJoinedStrings(t *testing.T) {
	f := &fake{resp: map[string]result{"doctor --json": {}, "--json list": {}, "version --json": {}}}
	r := New(Options{Runner: f, LookPath: found}).run
	for _, argv := range [][]string{{"doctor --json"}, {"--json list"}, {"version --json"}, {"version", "--json "}} {
		if _, _, err := r.Run(context.Background(), "/fake/beads-lease", argv...); err == nil {
			t.Errorf("single-element or padded argv %q was allowed", argv)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("reached the runner: %v", f.calls)
	}
}

func TestStatusCarriesTypedRowsMatchingTheTextLines(t *testing.T) {
	body := `{"schemaVersion":1,"ok":true,"leases":[
	  {"resource":"r:a","holder":"alice","state":"held"},
	  {"resource":"r:b","holder":"bob","state":"stale"},
	  {"resource":"r:c","state":"free"},
	  {"resource":"r:d","state":"mystery"}]}`
	m := newMod(&fake{resp: map[string]result{"--json list": {out: body}}}, Options{})
	st := m.Status(context.Background())
	want := []core.Row{
		{Resource: "r:a", State: "held", Tone: core.ToneInfo, Detail: "by alice (expires ?)"},
		{Resource: "r:b", State: "stale", Tone: core.ToneWarn, Detail: "(last holder bob)"},
		{Resource: "r:c", State: "free", Tone: core.ToneOK},
		{Resource: "r:d", State: "mystery", Tone: core.ToneMuted},
	}
	if len(st.Rows) != len(want) || len(st.Lines) != len(want) {
		t.Fatalf("rows=%+v lines=%q", st.Rows, st.Lines)
	}
	for i, w := range want {
		if st.Rows[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, st.Rows[i], w)
		}
	}
	if st.Lines[0] != "r:a  held by alice (expires ?)" {
		t.Errorf("text line must stay unchanged: %q", st.Lines[0])
	}
}

func TestLeasesAreTheTypedDomainReadAndSourceIsLive(t *testing.T) {
	f := &fake{resp: map[string]result{"version": {out: versionLine}, "--json list": {out: fixture(t, "list.json")}}}
	m := newMod(f, Options{})
	m.Detect(context.Background())
	r, err := m.Leases(context.Background())
	if err != nil || r.Source != core.SourceLive || len(r.Leases) != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	if l := r.Leases[0]; l.State != core.LeaseHeld || l.Holder != "glm-andrew-bierman" || l.ExpiresAt != "2026-11-21T20:15:00Z" {
		t.Fatalf("held lease = %+v", l)
	}
	var _ core.LeaseProvider = m
}

func TestLeasesReturnsAnErrorWhenTheListIsUnavailable(t *testing.T) {
	f := &fake{resp: map[string]result{"version": {out: versionLine}, "--json list": {out: "", exit: 2}}}
	m := newMod(f, Options{})
	m.Detect(context.Background())
	if _, err := m.Leases(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}

func TestMissingActorIsAGuidedSetupStateNotAnError(t *testing.T) {
	t.Setenv("BEADS_ACTOR", "")
	body := `{"schemaVersion":1,"ok":false,"error":{"reason":"invalid-argument","exitCode":64,"message":"BEADS_ACTOR is not set; export BEADS_ACTOR=<agent-machine>"}}`
	m := newMod(&fake{resp: map[string]result{"--json list": {out: body, exit: 64}}}, Options{})
	_, err := m.Leases(context.Background())
	g, ok := core.AsNeedsSetup(err)
	if !ok || g.Step != core.StepActor || g.Message != "Leases need an actor name" || g.Action != "choose one" {
		t.Fatalf("%+v %v", g, err)
	}
	if m.Guide(context.Background()) == nil {
		t.Fatal("no actor anywhere: the module should offer the step")
	}
	if withActor := newMod(&fake{}, Options{Actor: func() string { return "me-mac" }}); withActor.Guide(context.Background()) != nil {
		t.Fatal("an actor from config means nothing to set up")
	}
	t.Setenv("BEADS_ACTOR", "from-env")
	if m.Guide(context.Background()) != nil {
		t.Fatal("an actor from the environment means nothing to set up")
	}
}

func TestMissingHubIsAPlainNotConnectedReason(t *testing.T) {
	body := `{"schemaVersion":1,"ok":false,"error":{"reason":"hub-failure","exitCode":1,"message":"coord list failed: fork/exec /h/Code/agent-coordination/bin/coord: no such file or directory"}}`
	m := newMod(&fake{resp: map[string]result{"--json list": {out: body, exit: 1}}}, Options{})
	_, err := m.Leases(context.Background())
	var nc *core.NotConnectedError
	if !errors.As(err, &nc) || !strings.Contains(nc.Reason, "coordination hub") || strings.Contains(nc.Reason, "fork/exec") {
		t.Fatalf("%v", err)
	}
}

func TestRealRunnerHandsTheConfiguredActorToTheTool(t *testing.T) {
	t.Setenv("BEADS_ACTOR", "")
	tool := filepath.Join(t.TempDir(), "beads-lease")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho \"actor=$BEADS_ACTOR\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Bin: tool, Actor: func() string { return "cfg-actor" }})
	out, _, err := m.run.inner.Run(context.Background(), tool, "version")
	if err != nil || !strings.Contains(string(out), "actor=cfg-actor") {
		t.Fatalf("%q %v", out, err)
	}
}
