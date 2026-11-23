package lambe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

const fixtureDir = "testdata/contract/v1"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- DTO decoding against the vendored contract fixtures ---

func TestFixturesDecode(t *testing.T) {
	var s Snapshot
	mustUnmarshal(t, fixture(t, "snapshot.json"), &s)
	if len(s.Runs) != 3 || s.Runs[0].Issue != "T-1" || !s.Runs[2].Stalled || len(s.NeedsHuman) != 1 {
		t.Fatalf("snapshot decoded wrong: %+v", s)
	}
	var q Queue
	mustUnmarshal(t, fixture(t, "queue.json"), &q)
	if len(q.Rows) != 2 || !q.Rows[1].Blocked || q.Rows[1].Blockers[0].ID != "api-9" {
		t.Fatalf("queue decoded wrong: %+v", q)
	}
	var h History
	mustUnmarshal(t, fixture(t, "history.json"), &h)
	if len(h.Runs) != 3 || h.Runs[2].Outcome != "completed" {
		t.Fatalf("history decoded wrong: %+v", h)
	}
	var d Descriptor
	mustUnmarshal(t, fixture(t, "descriptor.json"), &d)
	if d.Instance != "lambe-1a2b3c4d5e6f" || d.Addr != "127.0.0.1:53412" || d.PID != 4242 {
		t.Fatalf("descriptor decoded wrong: %+v", d)
	}
	var e errorBody
	mustUnmarshal(t, fixture(t, "error.json"), &e)
	if e.Error != "not_found" {
		t.Fatalf("error decoded wrong: %+v", e)
	}
}

func TestNullAndEmptyListsAreEquivalentAndUnknownFieldsIgnored(t *testing.T) {
	for _, js := range []string{
		`{"schema_version":1,"runs":null,"needs_human":null,"brand_new_field":{"x":1}}`,
		`{"schema_version":1,"runs":[],"needs_human":[]}`,
	} {
		var s Snapshot
		mustUnmarshal(t, []byte(js), &s)
		if len(s.Runs) != 0 || len(s.NeedsHuman) != 0 {
			t.Fatalf("lists not empty for %s", js)
		}
	}
	var r Run
	mustUnmarshal(t, []byte(`{"issue":"X-1"}`), &r)
	if r.Machine != "" || r.Phase != "" || orUnknown(r.Phase) != "?" {
		t.Fatalf("absent fields should be unknown, got %+v", r)
	}
}

func mustUnmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// --- detection and doctor through a fake runner ---

type result struct {
	out  string
	exit int
	err  error
}

type fakeRunner struct {
	resp  map[string]result
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, int, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, name+" "+key)
	r, ok := f.resp[key]
	if !ok {
		return nil, -1, fmt.Errorf("unexpected command %q", key)
	}
	return []byte(r.out), r.exit, r.err
}

func found(string) (string, error) { return "/fake/lambe", nil }

func newModule(f *fakeRunner, o Options) *Module {
	o.Runner, o.LookPath = f, found
	return New(o)
}

func TestDetectStates(t *testing.T) {
	cases := []struct {
		name string
		run  *fakeRunner
		look func(string) (string, error)
		want core.State
		hint string
	}{
		{"ready", &fakeRunner{resp: map[string]result{"version --json": {out: `{"version":"v0.10.0","api_version":1}`}}}, found, core.Ready, ""},
		{"api v2 is incompatible", &fakeRunner{resp: map[string]result{"version --json": {out: `{"version":"v9","api_version":2}`}}}, found, core.Incompatible, "api_version 2"},
		{"missing binary is absent", &fakeRunner{}, func(string) (string, error) { return "", os.ErrNotExist }, core.Absent, "not found"},
		{"garbled output names the command", &fakeRunner{resp: map[string]result{"version --json": {out: "lambe v0.1 plain text"}}}, found, core.Incompatible, "version --json"},
	}
	for _, c := range cases {
		m := New(Options{Runner: c.run, LookPath: c.look})
		d := m.Detect(context.Background())
		if d.State != c.want || !strings.Contains(d.Detail, c.hint) {
			t.Errorf("%s: got %+v", c.name, d)
		}
	}
}

func TestDoctorMapsChecksAndFailExit(t *testing.T) {
	report := `[{"name":"config","status":"fail","detail":"no config","fix":"run lambe init"},{"name":"bd","status":"warn","detail":"old"},{"name":"x","status":"skip","detail":""},{"name":"y","status":"weird","detail":""}]`
	f := &fakeRunner{resp: map[string]result{
		"version --json":                  {out: `{"version":"v0.10.0","api_version":1}`},
		"doctor -json -config /tmp/l.yml": {out: report, exit: 1},
	}}
	m := newModule(f, Options{ConfigPath: "/tmp/l.yml"})
	m.Detect(context.Background())
	got := m.Doctor(context.Background())
	if len(got) != 4 || got[0].Status != core.StatusFail || got[0].Fix != "run lambe init" || got[3].Status != core.StatusWarn {
		t.Fatalf("doctor mapping wrong: %+v", got)
	}
}

func TestDoctorUnreadableReportFails(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{
		"version --json": {out: `{"version":"v0.10.0","api_version":1}`},
		"doctor -json":   {out: "oops"},
	}}
	m := newModule(f, Options{})
	m.Detect(context.Background())
	got := m.Doctor(context.Background())
	if len(got) != 1 || got[0].Status != core.StatusFail {
		t.Fatalf("got %+v", got)
	}
}

// --- discovery ---

func writeDescriptor(t *testing.T, stateDir, name string, d Descriptor) {
	t.Helper()
	live := filepath.Join(stateDir, "live")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if err := os.WriteFile(filepath.Join(live, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func baseDesc(instance, cfg string) Descriptor {
	return Descriptor{SchemaVersion: 1, Instance: instance, Addr: "127.0.0.1:5000", BearerToken: "tok", PID: 4242, Identity: "id-ok", ConfigPath: cfg}
}

func discoverOpts(dir string, sel Selector) DiscoverOptions {
	return DiscoverOptions{
		StateDir: dir, Selector: sel,
		PidAlive: func(pid int) bool { return pid == 4242 },
		Identity: func(int) (string, error) { return "id-ok", nil },
	}
}

func TestDiscoverSkipsStaleImpostorNonLoopbackAndMalformed(t *testing.T) {
	dir := t.TempDir()
	good := baseDesc("lambe-good", "/a.yaml")
	writeDescriptor(t, dir, "stateapi-lambe-good.json", good)

	stale := baseDesc("lambe-stale", "")
	stale.PID = 1111 // pid not alive
	writeDescriptor(t, dir, "stateapi-lambe-stale.json", stale)

	impostor := baseDesc("lambe-impostor", "")
	impostor.Identity = "id-other" // live pid, wrong incarnation
	writeDescriptor(t, dir, "stateapi-lambe-impostor.json", impostor)

	remote := baseDesc("lambe-remote", "")
	remote.Addr = "10.0.0.5:80"
	writeDescriptor(t, dir, "stateapi-lambe-remote.json", remote)

	localhostName := baseDesc("lambe-name", "")
	localhostName.Addr = "localhost:80"
	writeDescriptor(t, dir, "stateapi-lambe-name.json", localhostName)

	future := baseDesc("lambe-future", "")
	future.SchemaVersion = 2
	writeDescriptor(t, dir, "stateapi-lambe-future.json", future)

	if err := os.WriteFile(filepath.Join(dir, "live", "stateapi-bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := Discover(discoverOpts(dir, Selector{}))
	if err != nil || d.Instance != "lambe-good" {
		t.Fatalf("got %+v, %v", d, err)
	}
}

func TestDiscoverIdentityMismatchSkippedAndUnverifiableFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "stateapi-a.json", baseDesc("lambe-a", ""))
	o := discoverOpts(dir, Selector{})
	o.Identity = func(int) (string, error) { return "", fmt.Errorf("unsupported") }
	if _, err := Discover(o); err != ErrNoDaemon {
		t.Fatalf("unverifiable identity should fail closed, got %v", err)
	}
	o.Identity = func(int) (string, error) { return "someone-else", nil }
	if _, err := Discover(o); err != ErrNoDaemon {
		t.Fatalf("identity mismatch should be skipped, got %v", err)
	}
}

func TestDiscoverSelectionAndAmbiguity(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "stateapi-a.json", baseDesc("lambe-a", "/a.yaml"))
	writeDescriptor(t, dir, "stateapi-b.json", baseDesc("lambe-b", "/b.yaml"))
	if _, err := Discover(discoverOpts(dir, Selector{})); err != ErrAmbiguous {
		t.Fatalf("want ErrAmbiguous, got %v", err)
	}
	d, err := Discover(discoverOpts(dir, Selector{ConfigPath: "/b.yaml"}))
	if err != nil || d.Instance != "lambe-b" {
		t.Fatalf("config selector: %+v %v", d, err)
	}
	d, err = Discover(discoverOpts(dir, Selector{Instance: "lambe-a"}))
	if err != nil || d.Instance != "lambe-a" {
		t.Fatalf("instance selector: %+v %v", d, err)
	}
	if _, err := Discover(discoverOpts(dir, Selector{ConfigPath: "/none.yaml"})); err != ErrNoDaemon {
		t.Fatalf("no match should be ErrNoDaemon, got %v", err)
	}
}

func TestDiscoverEmptyAndStaleOnlyAreUnavailableNotErrors(t *testing.T) {
	empty := t.TempDir()
	if _, err := Discover(discoverOpts(empty, Selector{})); err != ErrNoDaemon {
		t.Fatalf("empty dir: %v", err)
	}
	stale := t.TempDir()
	d := baseDesc("lambe-s", "")
	d.PID = 1
	writeDescriptor(t, stale, "stateapi-s.json", d)
	if _, err := Discover(discoverOpts(stale, Selector{})); err != ErrNoDaemon {
		t.Fatalf("stale only: %v", err)
	}
	m := New(Options{StateDir: empty, PidAlive: func(int) bool { return false }})
	st := m.Status(context.Background())
	if st.Unavailable == "" || st.Summary != "" {
		t.Fatalf("status should be unavailable with a reason, got %+v", st)
	}
}

func TestRealProcessIdentityOfSelfIsStable(t *testing.T) {
	a, err := processIdentity(os.Getpid())
	if err != nil {
		t.Skipf("no process identity on this platform: %v", err)
	}
	b, _ := processIdentity(os.Getpid())
	if a == "" || a != b {
		t.Fatalf("identity not stable: %q vs %q", a, b)
	}
}

// --- client and the full status read ---

const secret = "super-secret-token-value"

func fixtureServer(t *testing.T, posts *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(file string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts.Add(1)
			}
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"schema_version":1,"error":"unauthorized","message":"bad token"}`))
				return
			}
			_, _ = w.Write(fixture(t, file))
		}
	}
	mux.HandleFunc("/api/v1/state", serve("snapshot.json"))
	mux.HandleFunc("/api/v1/queue", serve("queue.json"))
	mux.HandleFunc("/api/v1/history", serve("history.json"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFullStatusReadAgainstFixtureServerIsReadOnly(t *testing.T) {
	var posts atomic.Int32
	srv := fixtureServer(t, &posts)
	dir := t.TempDir()
	d := baseDesc("lambe-live", "")
	d.Addr = strings.TrimPrefix(srv.URL, "http://")
	d.BearerToken = secret
	writeDescriptor(t, dir, "stateapi-live.json", d)

	m := New(Options{StateDir: dir, PidAlive: func(int) bool { return true }, Identity: func(int) (string, error) { return "id-ok", nil }})
	st := m.Status(context.Background())
	if st.Unavailable != "" || st.Summary != "3 runs, 2 queued, 1 need attention" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if posts.Load() != 0 {
		t.Fatalf("read-only violated: %d POSTs", posts.Load())
	}
}

func TestClientRefusesNonLoopbackBeforeSendingToken(t *testing.T) {
	d := baseDesc("x", "")
	d.Addr = "evil.example:80"
	if _, err := NewClient(d, 0); err == nil {
		t.Fatal("expected refusal")
	}
}

func TestClientNeverFollowsRedirectsAndNeverLeaksToken(t *testing.T) {
	var hit atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer redir.Close()
	d := baseDesc("r", "")
	d.Addr, d.BearerToken = strings.TrimPrefix(redir.URL, "http://"), secret
	c, err := NewClient(d, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Snapshot(context.Background())
	if err == nil || hit.Load() != 0 {
		t.Fatalf("redirect must be an error and not followed (err=%v hits=%d)", err, hit.Load())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("token leaked into error")
	}
}

func TestClientTimeoutFailsFastAndWrongTokenErrorHasNoToken(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
	}))
	defer slow.Close()
	d := baseDesc("s", "")
	d.Addr, d.BearerToken = strings.TrimPrefix(slow.URL, "http://"), secret
	c, _ := NewClient(d, 50*time.Millisecond)
	start := time.Now()
	_, err := c.Snapshot(context.Background())
	if err == nil || time.Since(start) > 300*time.Millisecond || strings.Contains(err.Error(), secret) {
		t.Fatalf("timeout not fast or leaked (err=%v)", err)
	}

	var posts atomic.Int32
	srv := fixtureServer(t, &posts)
	d.Addr, d.BearerToken = strings.TrimPrefix(srv.URL, "http://"), "wrong"
	c, _ = NewClient(d, time.Second)
	_, err = c.Snapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("want a surfaced 401 unauthorized error, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("token leaked into error: %v", err)
	}
}

func TestClientExposesOnlyReadMethods(t *testing.T) {
	allowed := map[string]bool{"Snapshot": true, "Queue": true, "History": true, "View": true}
	typ := reflect.TypeOf(&Client{})
	for i := 0; i < typ.NumMethod(); i++ {
		if name := typ.Method(i).Name; !allowed[name] {
			t.Errorf("Client gained method %s; the state client must stay read-only", name)
		}
	}
}

func TestLoopbackRejectsMalformedPortsBeforeAnyTokenUse(t *testing.T) {
	bad := []string{"127.0.0.1:80@evil.example", "127.0.0.1:80/x", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1:http"}
	for _, addr := range bad {
		if loopback(addr) {
			t.Errorf("loopback(%q) = true, want false", addr)
		}
		d := baseDesc("x", "")
		d.Addr = addr
		if _, err := NewClient(d, 0); err == nil {
			t.Errorf("NewClient accepted %q", addr)
		}
		dir := t.TempDir()
		writeDescriptor(t, dir, "stateapi-x.json", d)
		if _, err := Discover(discoverOpts(dir, Selector{})); err != ErrNoDaemon {
			t.Errorf("Discover accepted %q: %v", addr, err)
		}
	}
	if !loopback("127.0.0.1:53412") {
		t.Error("well-formed loopback address rejected")
	}
}

func TestDetectLocatedButFailingBinaryIsBrokenNotAbsent(t *testing.T) {
	cases := map[string]result{
		"exit 2":       {exit: 2},
		"runner error": {err: fmt.Errorf("timed out after 10s")},
	}
	for name, r := range cases {
		f := &fakeRunner{resp: map[string]result{"version --json": r}}
		d := New(Options{Runner: f, LookPath: found}).Detect(context.Background())
		if d.State != core.Broken || d.Detail == "" {
			t.Errorf("%s: got %+v, want broken with a detail", name, d)
		}
	}
	d := New(Options{Runner: &fakeRunner{}, Bin: "/nonexistent/lambe"}).Detect(context.Background())
	if d.State != core.Absent {
		t.Errorf("explicit missing Bin: got %+v, want absent", d)
	}
}

func TestDoctorExitBackstopAddsFailWhenLambeFailedWithoutAFailCheck(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{
		"version --json": {out: `{"version":"v0.10.0","api_version":1}`},
		"doctor -json":   {out: `[{"name":"bd","status":"error","detail":"new status"}]`, exit: 1},
	}}
	m := newModule(f, Options{})
	m.Detect(context.Background())
	got := m.Doctor(context.Background())
	if len(got) != 2 || got[0].Status != core.StatusWarn || got[1].Status != core.StatusFail {
		t.Fatalf("want [warn, synthetic fail], got %+v", got)
	}
	// A real fail check is not duplicated by the backstop.
	f.resp["doctor -json"] = result{out: `[{"name":"config","status":"fail","detail":"x"}]`, exit: 1}
	if got := m.Doctor(context.Background()); len(got) != 1 {
		t.Fatalf("backstop duplicated an existing fail: %+v", got)
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func hostile() string { return "t\x1b]0;pwned\x07x" }

func controlFree(t *testing.T, label, s string) {
	t.Helper()
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			t.Errorf("%s contains control rune %U in %q", label, r, s)
			return
		}
	}
}

func TestReadSanitizesEveryExternalStringAndStatusLines(t *testing.T) {
	js := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	mux := http.NewServeMux()
	body := `{"schema_version":1,"daemon":{"pid":1,"machine":` + js(hostile()) + `},"runs":[{"issue":` + js(hostile()) + `,"state":"running","phase":` + js(hostile()) + `,"machine":` + js(hostile()) + `}],"needs_human":[{"issue":"a","reason":` + js(hostile()) + `,"detail":` + js(hostile()) + `}]}`
	qbody := `{"schema_version":1,"rows":[{"id":"q","title":` + js(hostile()) + `,"reason":` + js(hostile()) + `,"blockers":[{"id":"b","title":` + js(hostile()) + `}]}]}`
	mux.HandleFunc("/api/v1/state", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
	mux.HandleFunc("/api/v1/queue", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(qbody)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	d := baseDesc("lambe-live", "")
	d.Addr, d.BearerToken = strings.TrimPrefix(srv.URL, "http://"), secret
	writeDescriptor(t, dir, "stateapi-live.json", d)
	m := New(Options{StateDir: dir, PidAlive: func(int) bool { return true }, Identity: func(int) (string, error) { return "id-ok", nil }})

	v, err := m.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := v.Snapshot.Runs[0]
	for label, s := range map[string]string{"issue": r.Issue, "phase": r.Phase, "machine": r.Machine,
		"reason": v.Snapshot.NeedsHuman[0].Reason, "detail": v.Snapshot.NeedsHuman[0].Detail,
		"queue title": v.Queue.Rows[0].Title, "queue reason": v.Queue.Rows[0].Reason, "blocker": v.Queue.Rows[0].Blockers[0].Title} {
		controlFree(t, label, s)
	}
	for _, l := range m.Status(context.Background()).Lines {
		controlFree(t, "status line", l)
	}
}

func TestClientErrorTextIsSanitizedAndTokenFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"schema_version":1,"error":"boom","message":` + jsonStr(hostile()) + `}`))
	}))
	defer srv.Close()
	d := baseDesc("x", "")
	d.Addr, d.BearerToken = strings.TrimPrefix(srv.URL, "http://"), secret
	c, _ := NewClient(d, time.Second)
	_, err := c.Snapshot(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	controlFree(t, "client error", err.Error())
	if strings.Contains(err.Error(), secret) {
		t.Fatal("token leaked")
	}
}

func TestDoctorChecksAreSanitized(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{
		"version --json": {out: `{"version":"v0.10.0","api_version":1}`},
		"doctor -json":   {out: `[{"name":` + jsonStr(hostile()) + `,"status":"ok","detail":` + jsonStr(hostile()) + `,"fix":` + jsonStr(hostile()) + `}]`},
	}}
	m := newModule(f, Options{})
	m.Detect(context.Background())
	c := m.Doctor(context.Background())[0]
	controlFree(t, "name", c.Name)
	controlFree(t, "detail", c.Detail)
	controlFree(t, "fix", c.Fix)
}

func TestReadReturnsSentinelErrorsThatErrorsIsCanMatch(t *testing.T) {
	m := New(Options{StateDir: t.TempDir(), PidAlive: func(int) bool { return false }})
	if _, err := m.Read(context.Background()); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("want ErrNoDaemon, got %v", err)
	}
	dir := t.TempDir()
	writeDescriptor(t, dir, "stateapi-a.json", baseDesc("lambe-a", "/a.yaml"))
	writeDescriptor(t, dir, "stateapi-b.json", baseDesc("lambe-b", "/b.yaml"))
	m = New(Options{StateDir: dir, PidAlive: func(int) bool { return true }, Identity: func(int) (string, error) { return "id-ok", nil }})
	if _, err := m.Read(context.Background()); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ErrAmbiguous, got %v", err)
	}
}

func TestDiscoverRejectsBadPidAndInstanceBeforeAnyProcessProbe(t *testing.T) {
	dir := t.TempDir()
	for i, mutate := range []func(*Descriptor){
		func(d *Descriptor) { d.PID = 0 },
		func(d *Descriptor) { d.PID = -1 },
		func(d *Descriptor) { d.Instance = "" },
	} {
		d := baseDesc(fmt.Sprintf("lambe-%d", i), "")
		mutate(&d)
		writeDescriptor(t, dir, fmt.Sprintf("stateapi-%d.json", i), d)
	}
	probed := false
	o := discoverOpts(dir, Selector{})
	o.PidAlive = func(int) bool { probed = true; return true }
	if _, err := Discover(o); err != ErrNoDaemon || probed {
		t.Fatalf("err=%v probed=%v; bad descriptors must be skipped before PidAlive", err, probed)
	}
}

func TestDiscoverAcceptsEmptyIdentityWhenPidIsAlive(t *testing.T) {
	// Lambe's spec makes identity optional; a descriptor without one passes on pid liveness.
	dir := t.TempDir()
	d := baseDesc("lambe-noid", "")
	d.Identity = ""
	writeDescriptor(t, dir, "stateapi-noid.json", d)
	o := discoverOpts(dir, Selector{})
	o.Identity = func(int) (string, error) { t.Fatal("identity must not be consulted"); return "", nil }
	got, err := Discover(o)
	if err != nil || got.Instance != "lambe-noid" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestBareNameBinOverrideResolvesOnPathNotTheWorkingDirectory(t *testing.T) {
	f := &fakeRunner{resp: map[string]result{"version --json": {out: `{"version":"v0.10.0","api_version":1}`}}}
	d := New(Options{Bin: "lambe", Runner: f, LookPath: found}).Detect(context.Background())
	if d.State != core.Ready {
		t.Fatalf("a bare-name override must resolve through PATH, got %+v", d)
	}
}

func TestDemoModuleDetectsWithTheDemoSource(t *testing.T) {
	if d := NewDemo().Detect(context.Background()); d.Source != core.SourceDemo || d.State != core.Ready {
		t.Fatalf("demo detection = %+v", d)
	}
}
