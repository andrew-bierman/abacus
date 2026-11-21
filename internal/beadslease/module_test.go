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
		{"ready", &fake{resp: map[string]result{"version": {out: versionLine}}}, found, "", core.Ready},
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
	if !strings.Contains(st.Unavailable, "BEADS_ACTOR is not set") {
		t.Fatalf("unavailable = %q, want the actor variable named", st.Unavailable)
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

func TestDoctorIsASkipUntilBeadsLeaseShipsOne(t *testing.T) {
	m := newMod(&fake{}, Options{})
	checks := m.Doctor(context.Background())
	if len(checks) != 1 || checks[0].Status != core.StatusSkip || !strings.Contains(checks[0].Detail, "beads-lease#7") {
		t.Fatalf("doctor = %+v", checks)
	}
}

func TestOnlyVersionAndJSONListEverExecute(t *testing.T) {
	f := &fake{resp: map[string]result{"version": {out: versionLine}, "--json list": {out: fixture(t, "list.json")}}}
	m := newMod(f, Options{})
	m.Detect(context.Background())
	m.Doctor(context.Background())
	m.Status(context.Background())
	for _, c := range f.calls {
		if c != "version" && c != "--json list" {
			t.Errorf("executed %q; the module is read-only", c)
		}
	}
	if len(f.calls) != 2 {
		t.Fatalf("calls = %v, want exactly version and --json list", f.calls)
	}
}

func TestAllowlistRejectsEveryWriteOrSyncCommand(t *testing.T) {
	f := &fake{}
	r := New(Options{Runner: f, LookPath: found}).run
	for _, argv := range [][]string{
		{"acquire", "x"}, {"release", "x"}, {"heartbeat", "x"}, {"run", "x", "--", "true"},
		{"status"}, {"--json", "status"}, {"sync"}, {"list"}, {"version", "--json"}, {},
	} {
		if _, _, err := r.Run(context.Background(), "/fake/beads-lease", argv...); err == nil {
			t.Errorf("argv %v was allowed", argv)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("a rejected argv reached the runner: %v", f.calls)
	}
}
