package core_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// fakeMachine is a fixed process table and socket list: no core test reads this machine.
type fakeMachine struct {
	procs map[int]agents.Proc
	cwds  map[int]string
	socks []agents.Socket
	err   error
}

func (f *fakeMachine) Procs(context.Context) (map[int]agents.Proc, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.procs, nil
}

func (f *fakeMachine) CWD(_ context.Context, pid int) string { return f.cwds[pid] }

func (f *fakeMachine) Listeners(context.Context) ([]agents.Socket, error) { return f.socks, nil }

// fakeFiles is the open-file table the Codex join reads.
type fakeFiles map[int][]string

func (f fakeFiles) OpenFiles(_ context.Context, pid int) ([]string, error) { return f[pid], nil }

func noEnv(string) string { return "" }

// claudeProjectDir names a Claude project directory under the exported root. The tests use a
// directory that is not the cwd's encoded one on purpose: PathFor's cross-project fallback is
// the heuristic the join has to police.
func claudeProjectDir(home, project string) string {
	return filepath.Join(agents.ClaudeProjectsPath(home), project)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setMtime pins a fixture's write time, so a store's `since` filter is tested against a known
// clock instead of the wall clock the test happens to run at.
func setMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// TestSessionsJoinsClaudeTranscriptGitAndQuota is the whole join on one live session: the
// process table names the session, the transcript fills it, git says where the project stands,
// and the account gauge comes from Claude's status-line file.
func TestSessionsJoinsClaudeTranscriptGitAndQuota(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/shop"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	transcript := filepath.Join(claudeProjectDir(home, "-some-project"), "ses_core.jsonl")
	writeFile(t, transcript, strings.Join([]string{
		`{"type":"user","sessionId":"ses_core","cwd":"/home/dev/shop","timestamp":"2026-11-24T09:01:00Z","message":{"role":"user","content":[{"type":"text","text":"trim the invoice list"}]}}`,
		`{"type":"assistant","sessionId":"ses_core","cwd":"/home/dev/shop","timestamp":"2026-11-24T09:01:01Z","message":{"id":"msg_1","model":"claude-sonnet-5","content":[{"type":"text","text":"on it"}],"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":300,"cache_creation_input_tokens":40}}}`,
		`{"type":"user","sessionId":"ses_core","cwd":"/home/dev/shop","timestamp":"2026-11-24T09:01:02Z","message":{"role":"user","content":[{"type":"text","text":"and add a test"}]}}`,
		`{"type":"ai-title","sessionId":"ses_core","aiTitle":"Invoice list trimmed"}`,
	}, "\n")+"\n")
	setMtime(t, transcript, started.Add(time.Minute))
	writeFile(t, agents.ClaudeQuotaPath(filepath.Join(home, ".claude")),
		`{"five_hour":{"used_percentage":42,"resets_at":1893456000},"seven_day":{"used_percentage":7,"resets_at":1894060800},"updated_at":1893400000}`)

	m := &fakeMachine{
		procs: map[int]agents.Proc{1000: {PID: 1000, PPID: 1, Name: "claude", Started: started}},
		cwds:  map[int]string{1000: cwd},
	}
	git := &recRun{out: []byte("## feature/invoices...origin/feature/invoices\n?? new.txt\n M old.txt\n")}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: git, Home: home, Getenv: noEnv,
		Now: func() time.Time { return started.Add(time.Hour) },
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one", rep.Sessions)
	}
	got := rep.Sessions[0]
	if got.Title != "Invoice list trimmed" || got.Model != "claude-sonnet-5" {
		t.Errorf("title/model = %q/%q", got.Title, got.Model)
	}
	if got.Tokens != (agents.Tokens{Input: 100, Output: 20, CacheRead: 300, CacheCreate: 40}) {
		t.Errorf("tokens = %+v", got.Tokens)
	}
	if got.Context != (agents.Context{Used: 440, Limit: 200000}) {
		t.Errorf("context = %+v, want the three prompt counters and the 200k inference", got.Context)
	}
	if got.State != agents.StateThinking {
		t.Errorf("state = %q, want the transcript's open turn to win over the idle process", got.State)
	}
	if got.Git != (agents.GitStatus{Branch: "feature/invoices", Added: 1, Modified: 1}) {
		t.Errorf("git = %+v", got.Git)
	}
	if got.Started != started || got.LastActive.IsZero() {
		t.Errorf("started/last active = %v/%v", got.Started, got.LastActive)
	}
	if len(rep.Quotas) != 1 || rep.Quotas[0].Source != agents.HarnessClaude {
		t.Fatalf("quotas = %+v", rep.Quotas)
	}
	if q := rep.Quotas[0]; q.Short == nil || q.Short.UsedPercent != 42 || q.Long == nil || q.Long.UsedPercent != 7 || q.Short.WindowMinutes != 300 {
		t.Errorf("quota = %+v", q)
	}
	if want := "1 sessions, 1 active"; rep.Summarize() != want {
		t.Errorf("summary = %q, want %q", rep.Summarize(), want)
	}
}

// TestSessionsRejectsATranscriptFromAnotherProject: Claude's path fallback can find a newer
// transcript anywhere, so the join checks the directory the transcript recorded. Adopting
// another project's turns would put its title and tokens on this session.
func TestSessionsRejectsATranscriptFromAnotherProject(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/shop"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	other := filepath.Join(claudeProjectDir(home, "-other-project"), "ses_other.jsonl")
	writeFile(t, other, strings.Join([]string{
		`{"type":"user","sessionId":"ses_other","cwd":"/home/dev/tools","timestamp":"2026-11-24T09:01:00Z","message":{"role":"user","content":[{"type":"text","text":"tidy the scripts"}]}}`,
		`{"type":"assistant","sessionId":"ses_other","cwd":"/home/dev/tools","timestamp":"2026-11-24T09:01:01Z","message":{"id":"msg_9","model":"claude-sonnet-5","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":30,"cache_creation_input_tokens":0}}}`,
	}, "\n")+"\n")
	setMtime(t, other, started.Add(time.Minute))

	m := &fakeMachine{
		procs: map[int]agents.Proc{1000: {PID: 1000, PPID: 1, Name: "claude", Started: started}},
		cwds:  map[int]string{1000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Sessions[0]
	if got.Title != "shop" || got.Model != "" || got.Tokens != (agents.Tokens{}) {
		t.Fatalf("session = %+v, want no join from another project's transcript", got)
	}
}

// TestSessionsJoinsCodexRolloutFromOpenFiles proves the join does not need Codex to have a
// session list: the rollout is the file the live process holds open, and one account's gauge
// keeps the freshest report instead of the last one read.
func TestSessionsJoinsCodexRolloutFromOpenFiles(t *testing.T) {
	home, started := t.TempDir(), time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	root := agents.CodexSessionsPath(home)
	rollout := func(name, cwd, title, eventAt string, usedPercent int) string {
		path := filepath.Join(root, "2026", "11", "24", name)
		writeFile(t, path, strings.Join([]string{
			`{"timestamp":"2026-11-24T09:00:00Z","type":"session_meta","payload":{"id":"ses_` + name + `","cwd":"` + cwd + `"}}`,
			`{"timestamp":"2026-11-24T09:00:01Z","type":"turn_context","payload":{"model":"gpt-5.5","model_context_window":258000}}`,
			`{"timestamp":"2026-11-24T09:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"` + title + `"}}`,
			`{"timestamp":"` + eventAt + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":50},"last_token_usage":{"input_tokens":500,"cached_input_tokens":200,"output_tokens":25},"model_context_window":258000},"rate_limits":{"limit_id":"codex","primary":{"used_percent":` + strconv.Itoa(usedPercent) + `,"window_minutes":10080,"resets_at":1893456000}}}}`,
		}, "\n")+"\n")
		return path
	}
	// The shop session reports last but has the fresher gauge; the tools rollout is older and
	// must not overwrite it.
	a := rollout("rollout-a.jsonl", "/home/dev/shop", "fix the flaky test", "2026-11-24T09:30:03Z", 25)
	b := rollout("rollout-b.jsonl", "/home/dev/tools", "tidy the scripts", "2026-11-24T08:00:03Z", 80)

	m := &fakeMachine{
		procs: map[int]agents.Proc{
			2000: {PID: 2000, PPID: 1, Name: "codex", Started: started},
			2001: {PID: 2001, PPID: 1, Name: "codex", Started: started},
		},
		cwds: map[int]string{2000: "/home/dev/shop", 2001: "/home/dev/tools"},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{2000: {a, "/tmp/not-a-rollout.jsonl"}, 2001: {b}},
		Runner: &recRun{}, Home: home, Getenv: noEnv,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sessions) != 2 {
		t.Fatalf("sessions = %+v", rep.Sessions)
	}
	byTitle := map[string]agents.Session{}
	for _, x := range rep.Sessions {
		byTitle[x.Title] = x
	}
	shop, ok := byTitle["fix the flaky test"]
	if !ok {
		t.Fatalf("the rollout's title did not join: %+v", rep.Sessions)
	}
	if shop.Model != "gpt-5.5" || shop.Tokens != (agents.Tokens{Input: 600, Output: 50, CacheRead: 400}) {
		t.Errorf("model/tokens = %q/%+v, cached input must not be counted twice", shop.Model, shop.Tokens)
	}
	if shop.Context != (agents.Context{Used: 500, Limit: 258000}) {
		t.Errorf("context = %+v, want the last prompt and the recorded window", shop.Context)
	}
	// Two sessions on one account: one gauge, and it is the fresher report (25%) rather than
	// the older one (80%) read later.
	if len(rep.Quotas) != 1 || rep.Quotas[0].Source != agents.HarnessCodex || rep.Quotas[0].Long == nil {
		t.Fatalf("quotas = %+v", rep.Quotas)
	}
	q := rep.Quotas[0]
	if q.Short != nil || q.Long.WindowMinutes != 10080 || q.Long.UsedPercent != 25 {
		t.Errorf("quota = %+v, want the 30-day primary in the long slot and the freshest report", q)
	}
	if want := time.Date(2026, 11, 24, 9, 30, 3, 0, time.UTC); !q.Updated.Equal(want) {
		t.Errorf("quota updated = %v, want the fresher event's %v", q.Updated, want)
	}
}

// newOpenCodeDB builds the v2 subset the reader uses, with invented rows.
func newOpenCodeDB(t *testing.T, rows ...map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE session_v2 (
		id text PRIMARY KEY, parent_id text, title text, model text, directory text NOT NULL,
		cost real DEFAULT 0 NOT NULL, tokens_input integer DEFAULT 0 NOT NULL,
		tokens_output integer DEFAULT 0 NOT NULL, tokens_reasoning integer DEFAULT 0 NOT NULL,
		tokens_cache_read integer DEFAULT 0 NOT NULL, tokens_cache_write integer DEFAULT 0 NOT NULL,
		time_busy integer, time_idle integer, time_compacting integer, time_updated integer NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO session_v2 (id, title, model, directory, tokens_input,
			tokens_output, tokens_cache_read, tokens_cache_write, time_updated) VALUES (?,?,?,?,?,?,?,?,?)`,
			r["id"], r["title"], r["model"], r["dir"], r["in"], r["out"], r["cache_read"], r["cache_write"], r["updated"]); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestSessionsJoinsOpenCodeByDirectory(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/shop"
	updated := time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)
	db := newOpenCodeDB(t,
		map[string]any{"id": "ses_old", "title": "older work", "model": `{"id":"glm-5.3"}`, "dir": cwd, "in": int64(1), "out": int64(1), "cache_read": int64(0), "cache_write": int64(0), "updated": updated.Add(-time.Hour).UnixMilli()},
		map[string]any{"id": "ses_new", "title": "Payment refactor", "model": `{"id":"glm-5.3"}`, "dir": cwd, "in": int64(90), "out": int64(10), "cache_read": int64(400), "cache_write": int64(5), "updated": updated.UnixMilli()},
	)
	m := &fakeMachine{
		procs: map[int]agents.Proc{3000: {PID: 3000, PPID: 1, Name: "opencode.exe", Started: updated.Add(-2 * time.Hour)}},
		cwds:  map[int]string{3000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv, OpenCodeDB: db,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sessions) != 1 {
		t.Fatalf("sessions = %+v", rep.Sessions)
	}
	got := rep.Sessions[0]
	if got.Title != "Payment refactor" {
		t.Errorf("title = %q, want the newest session in the directory", got.Title)
	}
	if got.Tokens != (agents.Tokens{Input: 90, Output: 10, CacheRead: 400, CacheCreate: 5}) {
		t.Errorf("tokens = %+v", got.Tokens)
	}
	// opencode records no context window, so the fill stays zero rather than a guess.
	if got.Context != (agents.Context{}) {
		t.Errorf("context = %+v, want the does-not-record sentinel", got.Context)
	}
}

// TestSessionsKeepsTheLiveSessionWhenAStoreCannotBeRead: one unreadable store is reported,
// bounded, and never turns the live process table into an empty tab.
func TestSessionsKeepsTheLiveSessionWhenAStoreCannotBeRead(t *testing.T) {
	home := t.TempDir()
	notADB := filepath.Join(home, "opencode.db")
	writeFile(t, notADB, "this is not a database")
	m := &fakeMachine{
		procs: map[int]agents.Proc{3000: {PID: 3000, PPID: 1, Name: "opencode.exe"}},
		cwds:  map[int]string{3000: "/home/dev/shop"},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv, OpenCodeDB: notADB,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatalf("an unreadable store must not fail the live read: %v", err)
	}
	if len(rep.Sessions) != 1 || rep.Sessions[0].Title != "shop" {
		t.Fatalf("sessions = %+v, want the live session with its directory name", rep.Sessions)
	}
	if rep.Sessions[0].State != agents.StateUnknown {
		t.Errorf("state = %q, want a session whose store could not be read marked unknown", rep.Sessions[0].State)
	}
	if len(rep.Unreadable) != 1 || !strings.Contains(rep.Unreadable[0], "opencode") {
		t.Fatalf("unreadable = %+v", rep.Unreadable)
	}
	if !strings.Contains(rep.Summarize(), "1 unreadable") {
		t.Errorf("summary = %q", rep.Summarize())
	}
}

type sessionsMod struct {
	*modtest.Fake
	rep   core.SessionsReport
	reads int
}

func (m *sessionsMod) Sessions(context.Context) (core.SessionsReport, error) {
	m.reads++
	return m.rep, nil
}

// TestSessionsProviderSeamAndMissingReason pins detection and the reason a machine without a
// process table shows instead of an empty tab.
func TestSessionsProviderSeamAndMissingReason(t *testing.T) {
	table := &fakeMachine{err: errors.New("no process table")}
	s := core.NewSessions(core.SessionsOptions{Machine: table, Files: fakeFiles{}, Runner: &recRun{}, Home: t.TempDir(), Getenv: noEnv})
	d := s.Detect(context.Background())
	if d.State != core.Absent || !strings.Contains(d.Detail, "no process table") {
		t.Fatalf("detection = %+v", d)
	}
	p := core.ResolveProviders([]core.Entry{{Module: s, Detection: d}})
	if p.Sessions != nil {
		t.Fatalf("an absent process table must not be usable: %+v", p.Sessions)
	}
	h := p.Missing[core.DomainSessions]
	if h.State != core.NotConnected || h.Title != "Sessions" || !strings.Contains(h.Reason, "no process table") {
		t.Fatalf("missing header = %+v, want the detection reason", h)
	}
}

func TestSessionsDomainReadsOnceAndFeedsStatus(t *testing.T) {
	m := &sessionsMod{Fake: &modtest.Fake{IDStr: "sessions", Det: core.Detection{State: core.Ready}},
		rep: core.SessionsReport{
			Header: core.Header{Source: core.SourceLive},
			Snapshot: agents.Snapshot{Sessions: []agents.Session{
				{Harness: agents.HarnessClaude, PID: 10, CWD: "/home/dev/shop", State: agents.StateExecuting, Model: "claude-sonnet-5"},
				{Harness: agents.HarnessPi, PID: 11, CWD: "/home/dev/tools", State: agents.StateWaiting},
			}},
		}}
	entries := []core.Entry{{Module: m, Detection: m.Det}}
	d := core.ReadDomains(context.Background(), entries)
	if d.Sessions.State != core.Connected || d.Sessions.Source != core.SourceLive {
		t.Fatalf("sessions header = %+v", d.Sessions.Header)
	}
	if m.reads != 1 {
		t.Errorf("provider read %d times, want once", m.reads)
	}
	if got := d.Sessions.Summarize(); got != "2 sessions, 1 active" {
		t.Errorf("summary = %q", got)
	}
	if p := core.ResolveProviders(entries); p.Sessions != m {
		t.Errorf("Providers.Sessions = %v", p.Sessions)
	}
	// One domain read must not run the other providers.
	tasks := &provMod{Fake: &modtest.Fake{IDStr: "x"}, tasks: func() (core.TasksReport, error) {
		t.Error("ReadDomain(Sessions) read Tasks too")
		return core.TasksReport{}, nil
	}}
	only := core.ReadDomain(context.Background(), []core.Entry{{Module: m, Detection: m.Det}, {Module: tasks, Detection: core.Detection{State: core.Ready}}}, core.DomainSessions)
	if only.Sessions.State != core.Connected || only.Tasks.State != "" {
		t.Fatalf("single-domain read = %+v / %+v", only.Sessions.Header, only.Tasks.Header)
	}
	st := *core.FleetStatusReport(context.Background(), "v", entries).Modules[0].Status
	if st.Summary != d.Sessions.Summarize() || len(st.Rows) != 2 || st.Rows[0].Resource != "shop" || st.Rows[1].State != "waiting" {
		t.Fatalf("status = %+v", st)
	}
}

func TestSessionsJSONIsAListAndCarriesQuotas(t *testing.T) {
	taken := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	m := &sessionsMod{Fake: &modtest.Fake{IDStr: "sessions", Det: core.Detection{State: core.Ready}},
		rep: core.SessionsReport{Header: core.Header{Source: core.SourceLive}, Snapshot: agents.Snapshot{Taken: taken},
			Quotas: []agents.Quota{{Source: agents.HarnessClaude}}}}
	b, err := json.Marshal(core.ReadDomains(context.Background(), []core.Entry{{Module: m, Detection: m.Det}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"sessions":{`, `"domain":"sessions"`, `"taken":"2026-11-24T09:00:00Z"`, `"sessions":[]`, `"quotas":[{"source":"claude"}]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}

// TestSessionsJoinsPiTranscript covers the pi arm of the join: pi has no session list, so the
// transcript is found from the live process's directory and read for title, model and tokens.
func TestSessionsJoinsPiTranscript(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/shop"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(agents.PiSessionsPath(home), "fixture-session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"type":"session","version":3,"id":"ses_pi","cwd":"/home/dev/shop"}`,
		`{"type":"model_change","provider":"fixture-labs","modelId":"shop-model-1"}`,
		`{"type":"message","message":{"role":"user","content":"tidy the shop"}}`,
		`{"type":"message","message":{"role":"assistant","provider":"fixture-labs","model":"shop-model-1","content":[{"type":"text","text":"done"}],"usage":{"input":100,"output":20,"cacheRead":300,"cacheWrite":40,"totalTokens":460},"stopReason":"stop"}}`,
	}, "\n")+"\n")
	setMtime(t, path, started.Add(time.Minute))

	m := &fakeMachine{
		procs: map[int]agents.Proc{5000: {PID: 5000, PPID: 1, Name: "pi", Started: started}},
		cwds:  map[int]string{5000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Sessions[0]
	if got.Title != "tidy the shop" || got.Model != "fixture-labs/shop-model-1" {
		t.Errorf("title/model = %q/%q", got.Title, got.Model)
	}
	if got.Tokens != (agents.Tokens{Input: 100, Output: 20, CacheRead: 300, CacheCreate: 40}) {
		t.Errorf("tokens = %+v", got.Tokens)
	}
	if got.Context != (agents.Context{Used: 440}) {
		t.Errorf("context = %+v, want the three prompt counters and no inferred limit", got.Context)
	}
	if got.State != agents.StateWaiting {
		t.Errorf("state = %q, want the completed response to close the turn", got.State)
	}
}

func TestSessionsJoinsGrokSession(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/shop"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	root := filepath.Join(home, ".grok", "sessions")
	dir := filepath.Join(root, "%2Fhome%2Fdev%2Fshop", "ses_grok")
	writeFile(t, filepath.Join(dir, "summary.json"), `{
		"info":{"id":"ses_grok","cwd":"/home/dev/shop"},
		"generated_title":"Shop fixture",
		"current_model_id":"fixture-model-2",
		"created_at":"2026-11-24T09:00:00Z",
		"updated_at":"2026-11-24T09:05:00Z"
	}`)
	writeFile(t, filepath.Join(dir, "updates.jsonl"), strings.Join([]string{
		`{"timestamp":1784278801,"method":"session/update","params":{"sessionId":"ses_grok","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"tidy the shop"}},"_meta":{"agentTimestampMs":1784278801000}}}`,
		`{"timestamp":1784278802,"method":"session/update","params":{"sessionId":"ses_grok","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}},"_meta":{"agentTimestampMs":1784278802000}}}`,
	}, "\n")+"\n")
	writeFile(t, filepath.Join(dir, "usage.json"), `{
		"sessionId":"ses_grok",
		"turns":[{"turnNumber":1,"endedAt":"2026-11-24T09:05:00Z",
			"inputTokens":300,"outputTokens":40,"cachedReadTokens":100,
			"cacheCreationTokens":20,"reasoningTokens":5,"totalTokens":340,"modelCalls":1}]
	}`)

	m := &fakeMachine{
		procs: map[int]agents.Proc{4000: {PID: 4000, PPID: 1, Name: "grok", Started: started}},
		cwds:  map[int]string{4000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv,
		GrokRoot: root,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Sessions[0]
	if got.Title != "Shop fixture" || got.Model != "fixture-model-2" {
		t.Errorf("title/model = %q/%q", got.Title, got.Model)
	}
	if got.Tokens.Total() == 0 || len(got.TokenHistory) == 0 {
		t.Errorf("tokens/history = %+v/%v, want the Grok usage joined", got.Tokens, got.TokenHistory)
	}
	if got.State != agents.StateWaiting {
		t.Errorf("state = %q, want waiting after the response", got.State)
	}
}

// TestSessionsJoinsGeminiTranscript covers the Gemini arm of the join: the project registry maps
// the live process's directory to a chats folder, and the session file supplies the title, model
// and tokens.
func TestSessionsJoinsGeminiTranscript(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/atelier"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	// A relocated root proves SessionsOptions.GeminiRoot is plumbed, not just the default.
	root := filepath.Join(home, ".gemini", "relocated")
	writeFile(t, filepath.Join(home, ".gemini", "projects.json"),
		`{"projects":{"`+cwd+`":"atelier"}}`)
	writeFile(t, filepath.Join(root, "atelier", ".project_root"), cwd+"\n")
	path := filepath.Join(root, "atelier", "chats", "session-2026-11-24T09-00-abcdef12.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"sessionId":"ses_gemini","projectHash":"fixture","startTime":"2026-11-24T09:00:00Z","lastUpdated":"2026-11-24T09:00:00Z"}`,
		`{"id":"msg_prompt","timestamp":"2026-11-24T09:00:01Z","type":"user","content":"tidy the atelier"}`,
		`{"id":"msg_reply","timestamp":"2026-11-24T09:00:02Z","type":"gemini","content":[{"text":"done"}],"model":"fixture-model-3","tokens":{"input":120,"output":30,"cached":20,"thoughts":0,"tool":0,"total":150}}`,
	}, "\n")+"\n")
	setMtime(t, path, started.Add(time.Minute))

	m := &fakeMachine{
		procs: map[int]agents.Proc{6000: {PID: 6000, PPID: 1, Name: "gemini", Started: started}},
		cwds:  map[int]string{6000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv,
		GeminiRoot: root,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Sessions[0]
	if got.Title != "tidy the atelier" || got.Model != "fixture-model-3" {
		t.Errorf("title/model = %q/%q", got.Title, got.Model)
	}
	if got.Tokens != (agents.Tokens{Input: 100, Output: 30, CacheRead: 20}) {
		t.Errorf("tokens = %+v", got.Tokens)
	}
	if got.Context != (agents.Context{Used: 120}) {
		t.Errorf("context = %+v, want the prompt tokens", got.Context)
	}
	if got.State != agents.StateWaiting {
		t.Errorf("state = %q, want waiting after the response", got.State)
	}
}

// TestSessionsJoinsCopilotSession covers the Copilot arm of the join: the live process's
// directory resolves the newest session event log, and the workspace sidecar plus the event
// stream supply title, model, settled tokens and state.
func TestSessionsJoinsCopilotSession(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/atelier"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	relocatedRoot := filepath.Join(home, ".copilot", "relocated")
	dir := filepath.Join(relocatedRoot, "ses_copilot")
	writeFile(t, filepath.Join(dir, "workspace.yaml"),
		"id: ses_copilot\ncwd: "+cwd+"\nname: \"Atelier fixture\"\n")
	path := filepath.Join(dir, "events.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"type":"session.start","timestamp":"2026-11-24T09:00:01Z","data":{"sessionId":"ses_copilot","context":{"cwd":"/home/dev/atelier"}}}`,
		`{"type":"user.message","timestamp":"2026-11-24T09:00:02Z","data":{"content":"tidy the atelier"}}`,
		`{"type":"assistant.message","timestamp":"2026-11-24T09:00:03Z","data":{"content":"done","model":"fixture-labs/copilot-1","outputTokens":10}}`,
		`{"type":"assistant.usage","timestamp":"2026-11-24T09:00:04Z","data":{"model":"fixture-labs/copilot-1","inputTokens":120,"outputTokens":30,"cacheReadTokens":20,"cacheWriteTokens":10}}`,
		`{"type":"assistant.turn_end","timestamp":"2026-11-24T09:00:05Z","data":{"turnId":"1"}}`,
	}, "\n")+"\n")
	setMtime(t, path, started.Add(time.Minute))

	m := &fakeMachine{
		procs: map[int]agents.Proc{7000: {PID: 7000, PPID: 1, Name: "copilot", Started: started}},
		cwds:  map[int]string{7000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv,
		CopilotRoot: relocatedRoot,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Sessions[0]
	if got.Title != "Atelier fixture" || got.Model != "fixture-labs/copilot-1" {
		t.Errorf("title/model = %q/%q", got.Title, got.Model)
	}
	if got.Tokens != (agents.Tokens{Input: 90, Output: 30, CacheRead: 20, CacheCreate: 10}) {
		t.Errorf("tokens = %+v", got.Tokens)
	}
	if got.Context != (agents.Context{Used: 120}) {
		t.Errorf("context = %+v, want the call's gross input", got.Context)
	}
	if got.State != agents.StateWaiting {
		t.Errorf("state = %q, want waiting after the turn ended", got.State)
	}
}

// TestSessionsJoinsQwenTranscript covers the Qwen arm of the join: the sanitized project
// directory holds the session, and its records supply the title, model, tokens and window.
func TestSessionsJoinsQwenTranscript(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/atelier"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	// A relocated root proves SessionsOptions.QwenRoot is plumbed, not just the default.
	root := filepath.Join(home, ".qwen", "relocated")
	path := filepath.Join(root, "-home-dev-atelier", "chats", "7f3c9a1b2d4e4f6089ab01cd23ef4567.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"type":"user","uuid":"u1","sessionId":"7f3c9a1b2d4e4f6089ab01cd23ef4567","cwd":"/home/dev/atelier","timestamp":"2026-11-24T09:00:01Z","provenance":"real_user","message":{"role":"user","parts":[{"text":"tidy the atelier"}]}}`,
		`{"type":"assistant","uuid":"u2","sessionId":"7f3c9a1b2d4e4f6089ab01cd23ef4567","cwd":"/home/dev/atelier","timestamp":"2026-11-24T09:00:02Z","provenance":"assistant_output","model":"fixture-labs/qwen-1","contextWindowSize":200000,"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":30,"cachedContentTokenCount":20},"message":{"role":"model","parts":[{"text":"done"}]}}`,
	}, "\n")+"\n")
	setMtime(t, path, started.Add(time.Minute))

	m := &fakeMachine{
		procs: map[int]agents.Proc{8000: {PID: 8000, PPID: 1, Name: "qwen", Started: started}},
		cwds:  map[int]string{8000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv,
		QwenRoot: root,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Sessions[0]
	if got.Title != "tidy the atelier" || got.Model != "fixture-labs/qwen-1" {
		t.Errorf("title/model = %q/%q", got.Title, got.Model)
	}
	if got.Tokens != (agents.Tokens{Input: 100, Output: 30, CacheRead: 20}) {
		t.Errorf("tokens = %+v", got.Tokens)
	}
	if got.Context != (agents.Context{Used: 120, Limit: 200_000}) {
		t.Errorf("context = %+v, want the prompt tokens and recorded window", got.Context)
	}
	if got.State != agents.StateWaiting {
		t.Errorf("state = %q, want waiting after the response", got.State)
	}
}

// TestSessionsIgnoresATranscriptWrittenBeforeTheProcessStarted pins the `since` argument: an
// older transcript in the same project is not this process's session.
func TestSessionsIgnoresATranscriptWrittenBeforeTheProcessStarted(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/shop"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(claudeProjectDir(home, "-some-project"), "old.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"type":"user","sessionId":"ses_old","cwd":"/home/dev/shop","message":{"role":"user","content":[{"type":"text","text":"yesterday's work"}]}}`,
	}, "\n")+"\n")
	setMtime(t, path, started.Add(-time.Hour))

	m := &fakeMachine{
		procs: map[int]agents.Proc{1000: {PID: 1000, PPID: 1, Name: "claude", Started: started}},
		cwds:  map[int]string{1000: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Sessions[0]; got.Title != "shop" || got.Tokens != (agents.Tokens{}) {
		t.Fatalf("session = %+v, want no join from a transcript older than the process", got)
	}
}

// TestSessionsTwoLiveSessionsInOneDirectoryShareTheNewestTranscript pins the documented limit:
// Claude and pi have no session list, so two live sessions in one directory cannot be told
// apart and both read the newest transcript there.
func TestSessionsTwoLiveSessionsInOneDirectoryShareTheNewestTranscript(t *testing.T) {
	home, cwd := t.TempDir(), "/home/dev/shop"
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	dir := claudeProjectDir(home, "-some-project")
	writeFile(t, filepath.Join(dir, "older.jsonl"), `{"type":"user","sessionId":"ses_a","cwd":"/home/dev/shop","message":{"role":"user","content":[{"type":"text","text":"older work"}]}}`+"\n")
	writeFile(t, filepath.Join(dir, "newer.jsonl"), `{"type":"user","sessionId":"ses_b","cwd":"/home/dev/shop","message":{"role":"user","content":[{"type":"text","text":"newer work"}]}}`+"\n")
	setMtime(t, filepath.Join(dir, "older.jsonl"), started.Add(time.Minute))
	setMtime(t, filepath.Join(dir, "newer.jsonl"), started.Add(2*time.Minute))

	m := &fakeMachine{
		procs: map[int]agents.Proc{
			1000: {PID: 1000, PPID: 1, Name: "claude", Started: started},
			1001: {PID: 1001, PPID: 1, Name: "claude", Started: started},
		},
		cwds: map[int]string{1000: cwd, 1001: cwd},
	}
	s := core.NewSessions(core.SessionsOptions{Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sessions) != 2 || rep.Sessions[0].Title != "newer work" || rep.Sessions[1].Title != "newer work" {
		t.Fatalf("sessions = %+v, want the documented newest-transcript fallback for both", rep.Sessions)
	}
}

// TestSessionsPicksTheNewestCodexRollout: a resumed Codex process can hold several rollouts
// open, and the current one is the file written most recently.
func TestSessionsPicksTheNewestCodexRollout(t *testing.T) {
	home, started := t.TempDir(), time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	root := agents.CodexSessionsPath(home)
	makeRollout := func(name, title string) string {
		path := filepath.Join(root, "2026", "11", "24", name)
		writeFile(t, path, strings.Join([]string{
			`{"timestamp":"2026-11-24T09:00:00Z","type":"session_meta","payload":{"id":"ses_` + name + `","cwd":"/home/dev/shop"}}`,
			`{"timestamp":"2026-11-24T09:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"` + title + `"}}`,
		}, "\n")+"\n")
		return path
	}
	oldPath, newPath := makeRollout("old.jsonl", "old thread"), makeRollout("new.jsonl", "new thread")
	setMtime(t, oldPath, started.Add(time.Minute))
	setMtime(t, newPath, started.Add(2*time.Minute))

	m := &fakeMachine{
		procs: map[int]agents.Proc{2000: {PID: 2000, PPID: 1, Name: "codex", Started: started}},
		cwds:  map[int]string{2000: "/home/dev/shop"},
	}
	s := core.NewSessions(core.SessionsOptions{
		Machine: m, Files: fakeFiles{2000: {oldPath, newPath}}, Runner: &recRun{}, Home: home, Getenv: noEnv,
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Sessions[0]; got.Title != "new thread" {
		t.Fatalf("title = %q, want the newest open rollout", got.Title)
	}
}

// TestSessionsReportsAnUnreadableRollout: a store that cannot be read is named, bounded, and
// leaves the live session visible as unknown rather than idle.
func TestSessionsReportsAnUnreadableRollout(t *testing.T) {
	home, started := t.TempDir(), time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	root := agents.CodexSessionsPath(home)
	bad := filepath.Join(root, "2026", "11", "24", "bad.jsonl")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &fakeMachine{
		procs: map[int]agents.Proc{2000: {PID: 2000, PPID: 1, Name: "codex", Started: started}},
		cwds:  map[int]string{2000: "/home/dev/shop"},
	}
	s := core.NewSessions(core.SessionsOptions{Machine: m, Files: fakeFiles{2000: {bad}}, Runner: &recRun{}, Home: home, Getenv: noEnv})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatalf("an unreadable rollout must not fail the live read: %v", err)
	}
	if len(rep.Unreadable) != 1 || !strings.Contains(rep.Unreadable[0], "codex") {
		t.Fatalf("unreadable = %+v", rep.Unreadable)
	}
	if got := rep.Sessions[0]; got.Title != "shop" || got.State != agents.StateUnknown {
		t.Fatalf("session = %+v, want the live row marked unknown", got)
	}
}

// TestSessionsTreatsAnOpenCodeV1StoreAsNothingThere: the reader's sentinel says an older store
// is not a failure, so it must not fill Unreadable every tick.
func TestSessionsTreatsAnOpenCodeV1StoreAsNothingThere(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	m := &fakeMachine{
		procs: map[int]agents.Proc{3000: {PID: 3000, PPID: 1, Name: "opencode.exe"}},
		cwds:  map[int]string{3000: "/home/dev/shop"},
	}
	s := core.NewSessions(core.SessionsOptions{Machine: m, Files: fakeFiles{}, Runner: &recRun{}, Home: home, Getenv: noEnv, OpenCodeDB: dbPath})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unreadable) != 0 {
		t.Fatalf("unreadable = %+v, want a v1 store to read as nothing there", rep.Unreadable)
	}
	if got := rep.Sessions[0]; got.Title != "shop" || got.State != agents.StateWaiting {
		t.Fatalf("session = %+v", got)
	}
}

// TestSessionsDoctor covers the one dependency the read cannot work without.
func TestSessionsDoctor(t *testing.T) {
	bad := core.NewSessions(core.SessionsOptions{Machine: &fakeMachine{err: errors.New("no procfs")}, Files: fakeFiles{}, Runner: &recRun{}, Home: t.TempDir(), Getenv: noEnv})
	checks := bad.Doctor(context.Background())
	if len(checks) != 1 || checks[0].Status != core.StatusWarn || !strings.Contains(checks[0].Detail, "no procfs") || checks[0].Fix == "" {
		t.Fatalf("doctor = %+v", checks)
	}
	ok := core.NewSessions(core.SessionsOptions{Machine: &fakeMachine{}, Files: fakeFiles{}, Runner: &recRun{}, Home: t.TempDir(), Getenv: noEnv})
	if c := ok.Doctor(context.Background()); len(c) != 1 || c[0].Status != core.StatusOK {
		t.Fatalf("doctor = %+v", c)
	}
}

// TestSessionsReadsClaudeConfigDirFromEnv: the join, not the reader, resolves the config
// directory, so CLAUDE_CONFIG_DIR must reach the quota list.
func TestSessionsReadsClaudeConfigDirFromEnv(t *testing.T) {
	home, cfg := t.TempDir(), t.TempDir()
	writeFile(t, agents.ClaudeQuotaPath(cfg), `{"five_hour":{"used_percentage":12},"updated_at":1893400000}`)
	s := core.NewSessions(core.SessionsOptions{
		Machine: &fakeMachine{}, Files: fakeFiles{}, Runner: &recRun{}, Home: home,
		Getenv: func(k string) string {
			if k == "CLAUDE_CONFIG_DIR" {
				return cfg
			}
			return ""
		},
	})
	rep, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Quotas) != 1 || rep.Quotas[0].Source != agents.HarnessClaude || rep.Quotas[0].Short == nil || rep.Quotas[0].Short.UsedPercent != 12 {
		t.Fatalf("quotas = %+v, want the env directory's gauge", rep.Quotas)
	}
}

// TestSessionsPropagatesAnUnreadableProcessTable is the one error the read does not swallow.
func TestSessionsPropagatesAnUnreadableProcessTable(t *testing.T) {
	s := core.NewSessions(core.SessionsOptions{Machine: &fakeMachine{err: errors.New("no procfs")}, Files: fakeFiles{}, Runner: &recRun{}, Home: t.TempDir(), Getenv: noEnv})
	if _, err := s.Sessions(context.Background()); err == nil || !strings.Contains(err.Error(), "no procfs") {
		t.Fatalf("err = %v", err)
	}
}
