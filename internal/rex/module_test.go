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
}

func (f *fake) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	key := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	r, ok := f.resp[key]
	hang := f.hang
	f.mu.Unlock()
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

func newMod(f *fake, env map[string]string) *Module {
	return New(Options{
		LookPath: func(string) (string, error) { return "/fake/rex", nil },
		Getenv:   func(k string) string { return env[k] },
		Runner:   f,
		Hostname: func() string { return "me" },
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
		{"above", resp{out: ver("14")}, core.Incompatible},
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

func TestMatchLabel(t *testing.T) {
	for _, tc := range []struct {
		label, id string
		want      core.PlacementMatch
		ok        bool
	}{
		{"LAM-1", "lam-1", core.MatchExact, true},
		{"Lambe · LAM-1", "LAM-1", core.MatchExact, true},
		{"lambe: LAM-1", "LAM-1", core.MatchExact, true},
		{"fix LAM-1 now", "LAM-1", core.MatchLabel, true},
		{"(LAM-1)", "LAM-1", core.MatchLabel, true},
		{"LAM-1.", "LAM-1", core.MatchLabel, true},
		{"LAM-12", "LAM-1", "", false},
		{"XLAM-1", "LAM-1", "", false},
		{"LAM-1_x", "LAM-1", "", false},
		{"pre-LAM-1", "LAM-1", "", false},
		{"LAM-12 LAM-1", "LAM-1", core.MatchLabel, true},
		{"anything", "", "", false},
		{"a b c", "a", "", false}, // one-character ids never match
		{"日本 LAM-1 語", "LAM-1", core.MatchLabel, true},
	} {
		got, ok := matchLabel(tc.label, tc.id)
		if got != tc.want || ok != tc.ok {
			t.Errorf("matchLabel(%q, %q) = %q, %v; want %q, %v", tc.label, tc.id, got, ok, tc.want, tc.ok)
		}
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

func TestOpenPlanIsTheExistingHandoffThenTheCommandToCopy(t *testing.T) {
	m := newMod(&fake{}, nil)
	plan := m.Open(core.Placement{Host: "mbp", Label: "Lambe · LAM-1", Session: "s"})
	if len(plan.Rungs) != 1 || plan.Rungs[0].Name != RungHandoff || strings.Join(plan.Rungs[0].Argv, " ") != "rex -S mbp" || plan.Copy != "rex -S mbp" {
		t.Fatalf("%+v", plan)
	}
	if got := m.Open(core.Placement{Host: "me@host.local"}); got.Copy != "rex -S me@host.local" {
		t.Fatalf("%+v", got)
	}
	for _, h := range []string{"", "-x", "a b", "a;b"} {
		plan := m.Open(core.Placement{Host: h, Label: "work"})
		if len(plan.Rungs) != 0 || plan.Copy != "" || !strings.Contains(plan.Note, "work") || plan.Rungs == nil {
			t.Errorf("host %q: %+v", h, plan)
		}
	}
	if f := (&fake{}); len(f.called()) != 0 {
		t.Fatal("Open must not run anything")
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
	ss, err := parseLS([]byte(fixture(t, "ls-help-example.json")))
	if err != nil || len(ss) != 1 || ss[0].Label != "work" || ss[0].ID != "session:<uuid>" {
		t.Fatalf("%+v %v", ss, err)
	}
	if ss, err := parseLS([]byte(fixture(t, "ls-null.json"))); err != nil || len(ss) != 0 {
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
	if ss, _ := parseLS([]byte(b.String())); len(ss) != maxSessions {
		t.Fatalf("%d", len(ss))
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
