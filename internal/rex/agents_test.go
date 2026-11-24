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

const (
	s1 = "session:01"
	s2 = "session:02"
	ba = "block:a1"
	bb = "block:b2"
)

func key(argv []string) string { return strings.Join(argv, " ") }

// agentFake scripts a one-session, two-pane machine: pane ba runs an agent that reported status,
// pane bb is a plain shell. Everything is keyed by the exact argv.
func agentFake(t *testing.T, label string) *fake {
	t.Helper()
	f := fleetFake(t)
	f.resp[lsLocal] = resp{out: fmt.Sprintf(`{"sessions":[{"session_id":%q,"label":%q}]}`, s1, label)}
	f.resp[key(blockLsArgv("", s1))] = resp{out: fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"shell","flavor":%q},{"block_id":%q,"label":"shell","flavor":%q}]}`, ba, terminalFlavor, bb, terminalFlavor)}
	f.resp[key(blockCallArgv("", s1, ba, "program_status"))] = resp{out: fixture(t, "program-status.json")}
	f.resp[key(blockCallArgv("", s1, ba, "process"))] = resp{out: fixture(t, "process.json")}
	f.resp[key(blockCallArgv("", s1, bb, "program_status"))] = resp{out: fixture(t, "program-status-empty.json")}
	f.resp[key(blockCallArgv("", s1, bb, "process"))] = resp{out: fixture(t, "process-child-only.json")}
	return f
}

func agents(t *testing.T, m *Module, items ...core.WorkspaceItem) core.AgentReport {
	t.Helper()
	rep, err := m.Agents(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestParseRealFixtures(t *testing.T) {
	bs, trunc, err := parseBlocks([]byte(fixture(t, "block-ls.json")))
	if err != nil || trunc || len(bs) != 1 || bs[0].ID != "block:00000000-0000-7000-8000-00000000000b" || bs[0].Label != "shell" {
		t.Fatalf("%+v %v %v", bs, trunc, err)
	}
	rs, err := parseStatus([]byte(fixture(t, "program-status.json")))
	if err != nil || len(rs) != 3 {
		t.Fatalf("%+v %v", rs, err)
	}
	first, second := rs[0], rs[1]
	if first.State != core.AgentBlocked || first.Kind != core.AgentKindPermission || first.App != "claude-code" || first.Msg != "Approve edit to main.go?" || first.Progress != -1 || first.UpdatedAt.IsZero() {
		t.Fatalf("%+v", first)
	}
	if second.ID != "build/test" || second.State != core.AgentWorking || second.Progress != 40 || second.Title != "Sub task" || second.App != "claude-code" {
		t.Fatalf("effective_app is the name when the record has no app: %+v", second)
	}
	if rs[2].State != core.AgentError || rs[2].ID != "lint" {
		t.Fatalf("%+v", rs[2])
	}
	p, err := parseProcess([]byte(fixture(t, "process.json")))
	if err != nil || p.Name != "sleep" || p.Cwd != "/home/user/work/abacus" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestParseHandWrittenShapes(t *testing.T) {
	bs, _, err := parseBlocks([]byte(fixture(t, "block-ls-mixed.json")))
	if err != nil || len(bs) != 2 || bs[0].Label != "shell" || bs[1].Label != "second shell" {
		t.Fatalf("only terminal flavors are panes: %+v %v", bs, err)
	}
	for _, name := range []string{"program-status-empty.json", "program-status-null.json"} {
		if rs, err := parseStatus([]byte(fixture(t, name))); err != nil || len(rs) != 0 {
			t.Errorf("%s: %+v %v", name, rs, err)
		}
	}
	if rs, err := parseStatus([]byte(fixture(t, "program-status-unknown.json"))); err != nil || len(rs) != 2 || !rs[0].UpdatedAt.IsZero() || rs[1].Progress != -1 {
		t.Fatalf("an unknown state is data, an unparsable time is zero, a progress out of range is none: %+v %v", rs, err)
	}
	if p, err := parseProcess([]byte(fixture(t, "process-agent.json"))); err != nil || p.Name != "codex.js" {
		t.Fatalf("the script is the name, as Rex prefers: %+v %v", p, err)
	}
	if p, err := parseProcess([]byte(fixture(t, "process-child-only.json"))); err != nil || p.Name != "-zsh" || p.Cwd != "/home/user" {
		t.Fatalf("%+v %v", p, err)
	}
	for name, parse := range map[string]func([]byte) error{
		"program-status-drift.json":       func(b []byte) error { _, err := parseStatus(b); return err },
		"program-status-drift-types.json": func(b []byte) error { _, err := parseStatus(b); return err },
		"block-ls-drift.json":             func(b []byte) error { _, _, err := parseBlocks(b); return err },
		"process-drift.json":              func(b []byte) error { _, err := parseProcess(b); return err },
	} {
		if err := parse([]byte(fixture(t, name))); !errors.Is(err, errNotAgents) {
			t.Errorf("%s: schema drift must be an error, not an empty list: %v", name, err)
		}
	}
	for _, bad := range []string{"", "null", "[]", "{", `{"records":[{"state":7}]}`, `{"records":[1]}`} {
		if _, err := parseStatus([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if _, _, err := parseBlocks([]byte(`{"blocks":[{"flavor":"x"}]}`)); err == nil {
		t.Error("a block with no id is drift")
	}
}

func TestParsingIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"records":[`)
	for i := 0; i < maxStatusRecords+50; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"state":"working"}`)
	}
	b.WriteString(`]}`)
	if rs, err := parseStatus([]byte(b.String())); err != nil || len(rs) != maxStatusRecords {
		t.Fatalf("%d %v", len(rs), err)
	}
	b.Reset()
	b.WriteString(`{"blocks":[`)
	for i := 0; i < maxBlocksPerSession+5; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"block_id":"block:%02x","flavor":%q}`, i, terminalFlavor)
	}
	b.WriteString(`]}`)
	if bs, trunc, err := parseBlocks([]byte(b.String())); err != nil || !trunc || len(bs) != maxBlocksPerSession {
		t.Fatalf("%d %v %v", len(bs), trunc, err)
	}
}

func TestSweepReportsTheAgentPaneAndCountsThePlainShell(t *testing.T) {
	f := agentFake(t, "Lambe · LAM-1")
	m := modWith(t, f, nil, nil)
	f.calls = nil
	rep := core.ReadAgentsFrom(context.Background(), m, []core.WorkspaceItem{{Kind: core.WorkspaceTask, ID: "LAM-1"}, {Kind: core.WorkspaceTask, ID: "LAM-2"}})
	if rep.State != core.Connected || rep.Terminals != 2 || len(rep.Panes) != 1 {
		t.Fatalf("%+v", rep)
	}
	p := rep.Panes[0]
	if p.Session != s1 || p.Block != ba || p.Agent != "claude-code" || p.State != core.AgentBlocked || p.Kind != core.AgentKindPermission ||
		p.NRecords != 3 || p.Msg != "Approve edit to main.go?" || !p.Reported || p.Process != "sleep" || p.Cwd != "/home/user/work/abacus" {
		t.Fatalf("%+v", p)
	}
	if p.Item == nil || p.Item.ID != "LAM-1" || p.Match != core.MatchExact || len(p.Candidates) != 0 {
		t.Fatalf("matched by the session label, exactly: %+v", p)
	}
	if p.Open == nil || p.Open.Copy == "" {
		t.Fatalf("the open plan is the workspace one: %+v", p.Open)
	}
	for _, c := range f.called() {
		if !allowedArgv(strings.Fields(c)) {
			t.Errorf("called %q", c)
		}
	}
	want := map[string]bool{lsLocal: true}
	for _, a := range [][]string{blockLsArgv("", s1), blockCallArgv("", s1, ba, "program_status"), blockCallArgv("", s1, ba, "process"),
		blockCallArgv("", s1, bb, "program_status"), blockCallArgv("", s1, bb, "process")} {
		want[key(a)] = true
	}
	for _, c := range f.called() {
		if !want[c] {
			t.Errorf("unexpected call %q", c)
		}
	}
	if len(f.called()) != 6 {
		t.Fatalf("1 ls + 1 block ls + 2 calls for each of 2 panes: %v", f.called())
	}
}

func TestAPaneIsMatchedByAnyOfItsTextsAndTwoItemsAreNeverPicked(t *testing.T) {
	// the session label names LAM-1 and the working directory's last element names ac-9
	f := agentFake(t, "work on LAM-1")
	f.resp[key(blockCallArgv("", s1, ba, "process"))] = resp{out: `{"foreground":{"name":"node","cwd":"/w/ac-9"}}`}
	m := modWith(t, f, nil, nil)
	rep := agents(t, m, core.WorkspaceItem{ID: "LAM-1", Kind: core.WorkspaceTask}, core.WorkspaceItem{ID: "ac-9", Kind: core.WorkspaceLease}, core.WorkspaceItem{ID: "LAM-1", Kind: core.WorkspaceLease})
	p := rep.Panes[0]
	if p.Item != nil || len(p.Candidates) != 2 || p.Candidates[0].ID != "LAM-1" || p.Candidates[1].ID != "ac-9" {
		t.Fatalf("two different ids: ambiguous, one candidate per id: %+v", p)
	}
	// the message names an item too
	f.resp[key(blockCallArgv("", s1, ba, "program_status"))] = resp{out: `{"records":[{"state":"blocked","msg":"waiting on ZED-5 review","updated_at":"2026-11-24T01:21:46Z"}]}`}
	f.resp[lsLocal] = resp{out: fmt.Sprintf(`{"sessions":[{"session_id":%q,"label":"work"}]}`, s1)}
	f.resp[key(blockCallArgv("", s1, ba, "process"))] = resp{out: fixture(t, "process.json")}
	rep = agents(t, m, core.WorkspaceItem{ID: "ZED-5", Kind: core.WorkspaceTask})
	if p := rep.Panes[0]; p.Item == nil || p.Item.ID != "ZED-5" || p.Match != core.MatchLabel {
		t.Fatalf("%+v", p)
	}
	// an item that lives on another machine does not claim a pane on this one
	rep = agents(t, m, core.WorkspaceItem{ID: "ZED-5", Kind: core.WorkspaceRun, Host: "mbp"})
	if p := rep.Panes[0]; p.Item != nil {
		t.Fatalf("%+v", p)
	}
}

func TestAShellRunningAKnownAgentIsListedWithoutStatus(t *testing.T) {
	f := agentFake(t, "work")
	f.resp[key(blockCallArgv("", s1, bb, "process"))] = resp{out: `{"foreground":{"name":"claude","argv0":"claude","cwd":"/w"}}`}
	rep := core.ReadAgentsFrom(context.Background(), modWith(t, f, nil, nil), nil)
	var found *core.AgentPane
	for i := range rep.Panes {
		if rep.Panes[i].Block == bb {
			found = &rep.Panes[i]
		}
	}
	if found == nil || found.Reported || found.Agent != "claude" || found.State != core.AgentIdle {
		t.Fatalf("%+v", rep.Panes)
	}
	// and sorts last
	if rep.Panes[len(rep.Panes)-1].Block != bb {
		t.Fatalf("no status sorts last: %+v", rep.Panes)
	}
}

func TestNoServerIsNotConnectedAndAsksNothingElse(t *testing.T) {
	f := fleetFake(t)
	f.resp[lsLocal] = resp{exit: exitNoServer}
	m := modWith(t, f, nil, nil)
	f.calls = nil
	rep := agents(t, m)
	if rep.State != core.NotConnected || rep.HowToConnect == "" || len(rep.Panes) != 0 || rep.Reason == "" {
		t.Fatalf("%+v", rep)
	}
	if got := f.called(); len(got) != 1 || got[0] != lsLocal {
		t.Fatalf("%v", got)
	}
}

func TestZeroSessionsAndSessionsWithoutPanesAreConnectedAndEmpty(t *testing.T) {
	f := fleetFake(t)
	f.resp[lsLocal] = resp{out: `{"sessions":null}`}
	rep := agents(t, modWith(t, f, nil, nil))
	if rep.State != core.Connected || len(rep.Panes) != 0 || rep.Terminals != 0 {
		t.Fatalf("%+v", rep)
	}
	f.resp[lsLocal] = resp{out: fmt.Sprintf(`{"sessions":[{"session_id":%q,"label":"x"}]}`, s1)}
	f.resp[key(blockLsArgv("", s1))] = resp{out: `{"blocks":[{"block_id":"block:c3","flavor":"com.superlogical.tunnel.dial"}]}`}
	rep = agents(t, modWith(t, f, nil, nil))
	if rep.State != core.Connected || len(rep.Panes) != 0 || rep.Terminals != 0 || rep.Reason != "" {
		t.Fatalf("a session with only non-terminal blocks has no panes: %+v", rep)
	}
}

func TestPlainTerminalsAreCountedNotListed(t *testing.T) {
	f := agentFake(t, "x")
	f.resp[key(blockCallArgv("", s1, ba, "program_status"))] = resp{out: fixture(t, "program-status-empty.json")}
	f.resp[key(blockCallArgv("", s1, ba, "process"))] = resp{out: `{"foreground":{"name":"bash","cwd":"/w"}}`}
	rep := agents(t, modWith(t, f, nil, nil))
	if rep.State != core.Connected || len(rep.Panes) != 0 || rep.Terminals != 2 {
		t.Fatalf("%+v", rep)
	}
}

func TestAPaneThatVanishesMidSweepIsSkippedQuietly(t *testing.T) {
	f := agentFake(t, "x")
	for _, m := range []string{"program_status", "process"} {
		f.resp[key(blockCallArgv("", s1, ba, m))] = resp{exit: 3} // exit 3 here is "not found", not "more than one server"
	}
	rep := agents(t, modWith(t, f, nil, nil))
	if rep.State != core.Connected || rep.Reason != "" || len(rep.Panes) != 0 {
		t.Fatalf("%+v", rep)
	}
	// a session that vanished between ls and block ls
	f.resp[key(blockLsArgv("", s1))] = resp{exit: 3}
	rep = agents(t, modWith(t, f, nil, nil))
	if rep.State != core.Connected || rep.Reason != "" || len(rep.Panes) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestSchemaDriftIsADegradedReadNotNoAgents(t *testing.T) {
	f := agentFake(t, "x")
	f.resp[key(blockCallArgv("", s1, ba, "program_status"))] = resp{out: fixture(t, "program-status-drift.json")}
	rep := agents(t, modWith(t, f, nil, nil))
	if rep.State != core.Degraded || !strings.Contains(rep.Reason, "does not recognize") || len(rep.Panes) != 0 {
		t.Fatalf("%+v", rep)
	}
	f = agentFake(t, "x")
	f.resp[key(blockLsArgv("", s1))] = resp{out: fixture(t, "block-ls-drift.json")}
	rep = agents(t, modWith(t, f, nil, nil))
	if rep.State != core.Degraded {
		t.Fatalf("%+v", rep)
	}
}

func TestHostileStatusTextIsMadeSafeByCore(t *testing.T) {
	f := agentFake(t, "x")
	f.resp[key(blockCallArgv("", s1, ba, "program_status"))] = resp{out: fixture(t, "program-status-hostile.json")}
	m := modWith(t, f, nil, nil)
	rep := core.ReadAgentsFrom(context.Background(), m, nil)
	p := rep.Panes[0]
	for name, v := range map[string]string{"msg": p.Msg, "title": p.Title, "agent": p.Agent, "id": p.RecordID} {
		if core.Sanitize(v) != v || strings.ContainsAny(v, "\x1b\x07\n\r‮") {
			t.Errorf("%s: %q", name, v)
		}
	}
	if p.Progress != nil {
		t.Fatalf("a progress out of range is none: %v", *p.Progress)
	}
	f.resp[key(blockCallArgv("", s1, ba, "program_status"))] = resp{out: fixture(t, "program-status-oversized.json")}
	rep = core.ReadAgentsFrom(context.Background(), m, nil)
	p = rep.Panes[0]
	if len([]rune(p.Msg)) > 200 || len([]rune(p.Title)) > core.MaxLabelRunes || len([]rune(p.Agent)) > 32 || len([]rune(p.RecordID)) > core.MaxLabelRunes {
		t.Fatalf("%d %d %d", len(p.Msg), len(p.Title), len(p.Agent))
	}
	f.resp[key(blockCallArgv("", s1, ba, "program_status"))] = resp{out: fixture(t, "program-status-unknown.json")}
	rep = core.ReadAgentsFrom(context.Background(), m, nil)
	// the working record outranks the unknown one, which reads as idle
	if p := rep.Panes[0]; p.State != core.AgentWorking {
		t.Fatalf("%+v", p)
	}
	f.resp[key(blockCallArgv("", s1, ba, "program_status"))] = resp{out: `{"records":[{"state":"melting","kind":"sudo","effective_app":"future"}]}`}
	rep = core.ReadAgentsFrom(context.Background(), m, nil)
	if p := rep.Panes[0]; p.State != core.AgentIdle || p.Kind != core.AgentKindNone || p.Agent != "future" {
		t.Fatalf("%+v", p)
	}
}

// runFn is a Runner whose behavior a test writes, with a record of what it was asked.
type runFn struct {
	mu    sync.Mutex
	calls []string
	f     func(ctx context.Context, args []string) ([]byte, int, error)
}

func (r *runFn) Run(ctx context.Context, _ string, args ...string) ([]byte, int, error) {
	r.mu.Lock()
	r.calls = append(r.calls, key(args))
	r.mu.Unlock()
	return r.f(ctx, args)
}

func (r *runFn) count(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func modWith(t *testing.T, r core.Runner, hosts []string, now func() time.Time) *Module {
	t.Helper()
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: r,
		Hostname: func() string { return "me" }, Hosts: hosts, CacheTTL: -1, Now: now})
	m.SetFound(core.Found{Bin: "/fake/rex"})
	return m
}

func sessionsOf(n int) string {
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf(`{"session_id":"session:%03x","label":"s%d"}`, i, i))
	}
	return `{"sessions":[` + strings.Join(parts, ",") + `]}`
}

func TestOneHostTimingOutDegradesButKeepsTheOthersAndAsksItNothingMore(t *testing.T) {
	good := agentFake(t, "work")
	r := &runFn{f: func(ctx context.Context, args []string) ([]byte, int, error) {
		if len(args) > 1 && args[0] == "-S" && args[1] == "down" {
			return nil, -1, fmt.Errorf("%w after 2s", core.ErrTimedOut)
		}
		return good.Run(ctx, "rex", args...)
	}}
	m := modWith(t, r, []string{"down"}, nil)
	rep := agents(t, m)
	if rep.State != core.Degraded || !strings.Contains(rep.Reason, "down") || len(rep.Panes) != 1 || rep.Panes[0].Host != "" {
		t.Fatalf("%+v", rep)
	}
	if n := r.count("-S down"); n != 1 {
		t.Fatalf("the first call to a dead host is its session list and nothing follows it: %d calls", n)
	}
}

func TestTheBreakerStopsAskingAHostThatStopsAnswering(t *testing.T) {
	r := &runFn{f: func(ctx context.Context, args []string) ([]byte, int, error) {
		switch {
		case strings.HasSuffix(key(args), "ls --json") && !strings.Contains(key(args), "block"):
			return []byte(sessionsOf(60)), 0, nil
		}
		return nil, -1, fmt.Errorf("%w after 2s", core.ErrTimedOut)
	}}
	m := modWith(t, r, nil, nil)
	rep := agents(t, m)
	if rep.State != core.Degraded || !strings.Contains(rep.Reason, "stopped answering") {
		t.Fatalf("%+v", rep)
	}
	if n := r.count("--autostart=false block ls"); n > breakerAfter+agentConcurrency {
		t.Fatalf("%d block calls for 60 sessions: the breaker should have stopped them", n)
	}
}

func TestOnlyThisMachineAndConfiguredHostsAreSwept(t *testing.T) {
	good := agentFake(t, "work")
	r := &runFn{f: func(ctx context.Context, args []string) ([]byte, int, error) {
		if args[0] == "-S" {
			return []byte(`{"sessions":null}`), 0, nil
		}
		return good.Run(ctx, "rex", args...)
	}}
	m := modWith(t, r, []string{"MBP", "-bad", "a b", "me"}, nil) // "me" is this machine: not asked twice
	agents(t, m)
	for _, c := range r.calls {
		if a := strings.Fields(c); a[0] == "-S" && a[1] != "MBP" {
			t.Fatalf("contacted %q", c)
		}
		if !allowedArgv(strings.Fields(c)) {
			t.Fatalf("not allowed: %q", c)
		}
	}
	if r.count("-S MBP") != 1 || r.count("-S me") != 0 {
		t.Fatalf("%v", r.calls)
	}
}

func TestARemotePaneIsAskedThroughTheHostAndItsDirectoryIsNeverRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &runFn{f: func(ctx context.Context, args []string) ([]byte, int, error) {
		k := key(args)
		switch {
		case k == "-S mbp "+lsLocal:
			return []byte(fmt.Sprintf(`{"sessions":[{"session_id":%q,"label":"r"}]}`, s1)), 0, nil
		case strings.Contains(k, "block ls"):
			return []byte(fmt.Sprintf(`{"blocks":[{"block_id":%q,"flavor":%q}]}`, ba, terminalFlavor)), 0, nil
		case strings.HasSuffix(k, "program_status"):
			return []byte(fixture(t, "program-status.json")), 0, nil
		case strings.HasSuffix(k, "process"):
			return []byte(fmt.Sprintf(`{"foreground":{"name":"node","cwd":%q}}`, dir)), 0, nil
		}
		return []byte(`{"sessions":null}`), 0, nil
	}}
	m := modWith(t, r, []string{"mbp"}, nil)
	rep := agents(t, m)
	if len(rep.Panes) != 1 || rep.Panes[0].Host != "mbp" || rep.Panes[0].Cwd != dir || rep.Panes[0].Repo != "" || rep.Panes[0].Branch != "" {
		t.Fatalf("a remote path is shown as text and never read: %+v", rep.Panes)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "block") && !strings.HasPrefix(c, "-S mbp ") {
			t.Fatalf("%q", c)
		}
	}
}

func TestOneSweepRunsAtATimeAndOthersShareItsAnswer(t *testing.T) {
	gate := make(chan struct{})
	good := agentFake(t, "work")
	var once sync.Once
	started := make(chan struct{})
	r := &runFn{f: func(ctx context.Context, args []string) ([]byte, int, error) {
		if key(args) == lsLocal {
			once.Do(func() { close(started) })
			<-gate
		}
		return good.Run(ctx, "rex", args...)
	}}
	m := modWith(t, r, nil, nil)
	var wg sync.WaitGroup
	reps := make([]core.AgentReport, 3)
	wg.Add(1)
	go func() { defer wg.Done(); reps[0], _ = m.Agents(context.Background(), nil) }()
	<-started
	for i := 1; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); reps[i], _ = m.Agents(context.Background(), nil) }()
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	if n := r.count(lsLocal); n != 1 {
		t.Fatalf("%d session lists for 3 callers", n)
	}
	for i, rep := range reps {
		if len(rep.Panes) != 1 {
			t.Errorf("caller %d: %+v", i, rep)
		}
	}
	if _, err := m.Agents(context.Background(), nil); err != nil || r.count(lsLocal) != 2 {
		t.Fatalf("a later sweep runs again: %v %d", err, r.count(lsLocal))
	}
}

func TestAHungRexIsBoundedByTheContextAndANewSweepIsNotBlocked(t *testing.T) {
	f := agentFake(t, "x")
	f.hang = true
	m := modWith(t, f, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := m.Agents(ctx, nil); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
	f.mu.Lock()
	f.hang = false
	f.mu.Unlock()
	if rep := agents(t, m); len(rep.Panes) != 1 {
		t.Fatalf("a cancelled sweep leaves nothing behind: %+v", rep)
	}
}

func TestTheForegroundProcessIsReusedBrieflyAndTheStatusIsNot(t *testing.T) {
	f := agentFake(t, "x")
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	m := New(Options{LookPath: func(string) (string, error) { return "/fake/rex", nil }, Getenv: func(string) string { return "" }, Runner: f,
		Hostname: func() string { return "me" }, CacheTTL: -1, Now: func() time.Time { return now }})
	detect(m)
	agents(t, m)
	agents(t, m)
	count := func(suffix string) (n int) {
		for _, c := range f.called() {
			if strings.HasSuffix(c, suffix) {
				n++
			}
		}
		return
	}
	if count(" process") != 2 || count(" program_status") != 4 {
		t.Fatalf("process once per pane, status every sweep: %v", f.called())
	}
	now = now.Add(processTTL + time.Second)
	agents(t, m)
	if count(" process") != 4 {
		t.Fatalf("the process is read again after its TTL: %v", f.called())
	}
}

func TestPanesAreCappedAndTheCutIsMarked(t *testing.T) {
	r := &runFn{f: func(ctx context.Context, args []string) ([]byte, int, error) {
		k := key(args)
		switch {
		case k == lsLocal:
			return []byte(sessionsOf(maxSessions)), 0, nil
		case strings.Contains(k, "block ls"):
			return []byte(fmt.Sprintf(`{"blocks":[{"block_id":"block:aa","flavor":%q}]}`, terminalFlavor)), 0, nil
		case strings.HasSuffix(k, "program_status"):
			return []byte(`{"records":[{"state":"working","updated_at":"2026-11-24T01:21:46Z"}]}`), 0, nil
		}
		return []byte(`{"foreground":{"name":"node","cwd":"/w"}}`), 0, nil
	}}
	m := modWith(t, r, nil, nil)
	rep := agents(t, m)
	if len(rep.Panes) != maxPanesPerHost || !rep.Truncated {
		t.Fatalf("%d panes, truncated=%v", len(rep.Panes), rep.Truncated)
	}
}

func TestGitHeadReadsFilesAndNeverRunsGit(t *testing.T) {
	root := t.TempDir()
	write := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// a normal repository, asked from a subdirectory
	write(filepath.Join(root, "repo", ".git", "HEAD"), "ref: refs/heads/feat/x\n")
	write(filepath.Join(root, "repo", "a", "b", "keep"), "")
	if r, b := gitHead(filepath.Join(root, "repo", "a", "b")); r != "repo" || b != "feat/x" {
		t.Fatalf("%q %q", r, b)
	}
	// a detached head is a short hash
	write(filepath.Join(root, "det", ".git", "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")
	if r, b := gitHead(filepath.Join(root, "det")); r != "det" || b != "0123456" {
		t.Fatalf("%q %q", r, b)
	}
	// a worktree: .git is a file naming <main>/.git/worktrees/<name>; the repository is <main>
	write(filepath.Join(root, "main", ".git", "worktrees", "wt", "HEAD"), "ref: refs/heads/topic\n")
	write(filepath.Join(root, "wt", ".git"), "gitdir: "+filepath.Join(root, "main", ".git", "worktrees", "wt")+"\n")
	if r, b := gitHead(filepath.Join(root, "wt")); r != "main" || b != "topic" {
		t.Fatalf("%q %q", r, b)
	}
	// a relative gitdir
	write(filepath.Join(root, "rel", ".git"), "gitdir: ../main/.git/worktrees/wt\n")
	if r, b := gitHead(filepath.Join(root, "rel")); r != "main" || b != "topic" {
		t.Fatalf("%q %q", r, b)
	}
	// anything unexpected is nothing: no repository, a relative cwd, a symlinked .git or HEAD, a huge
	// HEAD, a .git file that is not a gitdir pointer, garbage in HEAD
	if r, b := gitHead(filepath.Join(root, "nowhere")); r != "" || b != "" {
		t.Fatalf("%q %q", r, b)
	}
	if r, b := gitHead("relative/path"); r != "" || b != "" {
		t.Fatalf("%q %q", r, b)
	}
	if err := os.MkdirAll(filepath.Join(root, "link"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "repo", ".git"), filepath.Join(root, "link", ".git")); err != nil {
		t.Fatal(err)
	}
	if r, b := gitHead(filepath.Join(root, "link")); r != "" || b != "" {
		t.Fatalf("a symlinked .git is never followed: %q %q", r, b)
	}
	write(filepath.Join(root, "linkhead", ".git", "x"), "")
	if err := os.Symlink(filepath.Join(root, "repo", ".git", "HEAD"), filepath.Join(root, "linkhead", ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if r, b := gitHead(filepath.Join(root, "linkhead")); r != "" || b != "" {
		t.Fatalf("a symlinked HEAD is never followed: %q %q", r, b)
	}
	write(filepath.Join(root, "big", ".git", "HEAD"), "ref: refs/heads/"+strings.Repeat("x", 3*maxGitFile))
	if _, b := gitHead(filepath.Join(root, "big")); len(b) > maxGitFile {
		t.Fatalf("%d", len(b))
	}
	write(filepath.Join(root, "bad", ".git"), "not a pointer")
	if r, b := gitHead(filepath.Join(root, "bad")); r != "" || b != "" {
		t.Fatalf("%q %q", r, b)
	}
	write(filepath.Join(root, "junk", ".git", "HEAD"), "who knows\n")
	if r, b := gitHead(filepath.Join(root, "junk")); r != "junk" || b != "" {
		t.Fatalf("%q %q", r, b)
	}
	// the walk is bounded
	deep := filepath.Join(root, "repo")
	for i := 0; i < maxGitWalk+2; i++ {
		deep = filepath.Join(deep, "d")
	}
	if r, _ := gitHead(deep); r != "" {
		t.Fatalf("the walk up stops after %d levels, found %q", maxGitWalk, r)
	}
}

func TestTheRepoAndBranchComeFromTheLocalPanesDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "abacus", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "abacus", ".git", "HEAD"), []byte("ref: refs/heads/feat/agents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := agentFake(t, "x")
	f.resp[key(blockCallArgv("", s1, ba, "process"))] = resp{out: fmt.Sprintf(`{"foreground":{"name":"node","cwd":%q}}`, filepath.Join(root, "abacus"))}
	rep := agents(t, modWith(t, f, nil, nil))
	if p := rep.Panes[0]; p.Repo != "abacus" || p.Branch != "feat/agents" {
		t.Fatalf("%+v", p)
	}
}

func TestASlowHostDoesNotThrowAwayTheLocalPanes(t *testing.T) {
	old := agentBudget
	agentBudget = 150 * time.Millisecond
	defer func() { agentBudget = old }()
	good := agentFake(t, "work")
	r := &runFn{f: func(ctx context.Context, args []string) ([]byte, int, error) {
		if len(args) > 1 && args[0] == "-S" && args[1] == "slow" {
			<-ctx.Done()
			return nil, -1, fmt.Errorf("%w: %w", core.ErrCanceled, ctx.Err())
		}
		return good.Run(ctx, "rex", args...)
	}}
	m := modWith(t, r, []string{"slow"}, nil)
	rep := agents(t, m)
	if rep.State != core.Degraded || len(rep.Panes) != 1 || !strings.Contains(rep.Reason, "slow did not answer in time") {
		t.Fatalf("%+v", rep)
	}
}

func TestAnExitedPaneKeepsTheReportConnected(t *testing.T) {
	f := agentFake(t, "work")
	f.resp[key(blockCallArgv("", s1, bb, "process"))] = resp{out: fixture(t, "process-exited.json")}
	rep := agents(t, modWith(t, f, nil, nil))
	if rep.State != core.Connected || rep.Reason != "" || len(rep.Panes) != 1 {
		t.Fatalf("%+v", rep)
	}
}

func TestANonConformingIdIsDriftAndNeverTripsTheBreaker(t *testing.T) {
	r := &runFn{f: func(ctx context.Context, args []string) ([]byte, int, error) {
		if !strings.Contains(key(args), "block") {
			var parts []string
			for i := 0; i < 5; i++ {
				parts = append(parts, fmt.Sprintf(`{"session_id":"session:ABC%d","label":"s"}`, i))
			}
			return []byte(`{"sessions":[` + strings.Join(parts, ",") + `]}`), 0, nil
		}
		return nil, -1, fmt.Errorf("unexpected call %v", args)
	}}
	rep := agents(t, modWith(t, r, nil, nil))
	if rep.State != core.Degraded || strings.Contains(rep.Reason, "stopped answering") || !strings.Contains(rep.Reason, "5 panes") {
		t.Fatalf("%+v", rep)
	}
	if n := r.count("--autostart=false block"); n != 0 {
		t.Fatalf("%d block calls for refused ids", n)
	}
}
