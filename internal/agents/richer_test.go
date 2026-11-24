package agents

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Everything here is invented: names, ids, numbers and prompts. No real transcript, database or
// process list is read.

func lines(l ...string) [][]byte {
	var out [][]byte
	for _, s := range l {
		out = append(out, []byte(s))
	}
	return out
}

func TestClaudeUsageGivesContextAndOnePointPerResponse(t *testing.T) {
	s := parseClaude(lines(
		`{"type":"user","message":{"role":"user","content":"count the pears"}}`,
		// one response written as two lines (a text block, then a tool call): same id, same usage
		`{"type":"assistant","message":{"id":"m1","model":"claude-demo-1","content":[{"type":"text","text":"on it"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":1000,"cache_creation_input_tokens":20}}}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude-demo-1","content":[{"type":"tool_use","id":"t1"}],"usage":{"input_tokens":10,"output_tokens":7,"cache_read_input_tokens":1000,"cache_creation_input_tokens":20}}}`,
		`{"type":"assistant","isSidechain":true,"message":{"id":"side","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":999999,"output_tokens":1}}}`,
		`{"type":"assistant","message":{"id":"m2","model":"<synthetic>","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"assistant","message":{"id":"m3","model":"claude-demo-1","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":4,"output_tokens":9,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0}}}`,
	))
	if !slices.Equal(s.Turns, []int64{37, 13}) { // input+output+cache writes; cache reads are left out
		t.Fatalf("turns %v", s.Turns)
	}
	if s.Context == nil || *s.Context != 2004 || s.Window != nil {
		t.Fatalf("context is the last prompt, and Claude's files do not name the window: %v %v", s.Context, s.Window)
	}
	if s.Tokens != nil {
		t.Fatal("a total over a partial read would be a lie: it stays unknown")
	}
}

const codexTokenLine = `{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d},"model_context_window":%d},"rate_limits":%s}}`

func codexTok(ts string, total, in, cached, out, win int, limits string) string {
	return sprintf(codexTokenLine, ts, total, in, cached, out, win, limits)
}

func TestCodexUsageWindowAndLimits(t *testing.T) {
	lim := `{"limit_id":"codex","primary":{"used_percent":12.5,"window_minutes":300,"resets_at":1767268800},"secondary":{"used_percent":40,"window_minutes":10080,"resets_at":1767700000}}`
	s := parseCodex(nil, lines(
		codexTok("2026-01-01T10:00:00Z", 1000, 800, 500, 50, 200000, lim),
		codexTok("2026-01-01T10:00:01Z", 1000, 800, 500, 50, 200000, lim), // the same event again: not a second response
		codexTok("2026-01-01T10:00:02Z", 1600, 900, 100, 70, 200000, `{"limit_id":"codex_other","primary":{"used_percent":99,"window_minutes":300}}`),
		codexTok("2026-01-01T10:00:03Z", 1600, 0, 0, 0, 200000, `null`), // a compaction marker: no response
	))
	if !slices.Equal(s.Turns, []int64{350, 870}) {
		t.Fatalf("turns %v", s.Turns)
	}
	if s.Context == nil || *s.Context != 900 || s.Window == nil || *s.Window != 200000 {
		t.Fatalf("context %v window %v: a marker must not blank the fill", s.Context, s.Window)
	}
	q := s.Quota
	if q == nil || q.Short == nil || q.Short.Pct != 12.5 || q.Long == nil || q.Long.Pct != 40 || q.Short.Minutes != 300 || q.Long.Minutes != 10080 {
		t.Fatalf("a model-specific allowance is not the account's, and the account's survives it: %+v", q)
	}
	if got := iso(q.Updated); got != "2026-01-01T10:00:01Z" || iso(q.Short.Resets) != "2026-01-01T12:00:00Z" {
		t.Fatalf("times: %s %s", got, iso(q.Short.Resets))
	}
	// a free plan's primary slot is a 30-day window: it is the far window, never drawn as 5 hours
	free := parseCodex(nil, lines(codexTok("2026-01-01T10:00:00Z", 10, 5, 0, 5, 1000, `{"primary":{"used_percent":3,"window_minutes":43200}}`)))
	if free.Quota == nil || free.Quota.Short != nil || free.Quota.Long == nil || free.Quota.Long.Minutes != 43200 {
		t.Fatalf("%+v", free.Quota)
	}
	none := parseCodex(nil, lines(codexTok("2026-01-01T10:00:00Z", 10, 5, 0, 5, 1000, `{"primary":{"window_minutes":300}}`)))
	if none.Quota != nil {
		t.Fatal("a window with no share is not a report")
	}
}

func TestClaudeQuotaFile(t *testing.T) {
	good := `{"five_hour":{"used_percentage":21,"resets_at":1767268800},"seven_day":{"used_percentage":8.5,"resets_at":1767700000,"window_minutes":10080},"updated_at":1767265200}`
	q, ok := parseClaudeQuota([]byte(good))
	if !ok || q.Short.Pct != 21 || q.Short.Minutes != 300 || q.Long.Minutes != 10080 || iso(q.Updated) != "2026-01-01T11:00:00Z" {
		t.Fatalf("%+v %v", q, ok)
	}
	rl := q.limit(core.HarnessClaude)
	if rl.FiveHourPct == nil || *rl.FiveHourPct != 21 || rl.SevenDayResets == "" {
		t.Fatalf("%+v", rl)
	}
	only, ok := parseClaudeQuota([]byte(`{"seven_day":{"used_percentage":1}}`))
	if !ok || only.Short != nil || only.limit("x").FiveHourPct != nil {
		t.Fatal("a window the file omits is unknown, never 0% used")
	}
	for _, bad := range []string{``, `nope`, `{}`, `{"five_hour":{}}`} {
		if _, ok := parseClaudeQuota([]byte(bad)); ok {
			t.Errorf("%q is not a report", bad)
		}
	}
	fsys := fstest.MapFS{"h/.claude/" + claudeQuotaFile: {Data: []byte(good)}, "big/.claude/" + claudeQuotaFile: {Data: append([]byte(good), bytes.Repeat([]byte(" "), maxQuotaFile)...)}}
	if _, ok := readClaudeQuota(fsys, "h/.claude"); !ok {
		t.Fatal("a normal file is read")
	}
	if _, ok := readClaudeQuota(fsys, "big/.claude"); ok {
		t.Fatal("an oversized file is refused, not cut")
	}
	if _, ok := readClaudeQuota(fsys, "none"); ok {
		t.Fatal("no file, no quota")
	}
}

func TestPiSessionIsReadFromItsTail(t *testing.T) {
	head := `{"type":"session","version":3,"id":"pi-s1","cwd":"/work/orchard"}`
	s := parsePi([]byte(head), lines(
		head,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"sort the crates"}]}}`,
		`{"type":"message","message":{"role":"assistant","provider":"demo","model":"pi-demo-1","stopReason":"toolUse","content":[],"usage":{"input":100,"output":20,"cacheRead":900,"cacheWrite":30}}}`,
		`{"type":"message","message":{"role":"toolResult","toolCallId":"c1"}}`,
		`{"type":"message","message":{"role":"assistant","provider":"demo","model":"pi-demo-1","stopReason":"stop","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}}`,
		`{"type":"message","message":{"role":"assistant","provider":"demo","model":"pi-demo-1","stopReason":"stop","usage":{"input":120,"output":40,"cacheRead":1000,"cacheWrite":0}}}`,
		`{"type":"model_change","modelId":"ignored"}`,
	))
	if s.ID != "pi-s1" || s.Cwd != "/work/orchard" || s.Task != "sort the crates" || s.Model != "demo/pi-demo-1" || s.End != endDone {
		t.Fatalf("%+v", s)
	}
	if !slices.Equal(s.Turns, []int64{150, 160}) || s.Context == nil || *s.Context != 1120 {
		t.Fatalf("a zero-usage response is not a turn: %v %v", s.Turns, s.Context)
	}
	for _, c := range []struct {
		stop string
		want endKind
	}{{"toolUse", endToolUse}, {"error", endError}, {"deferred", endPrompt}, {"length", endDone}} {
		got := parsePi(nil, lines(`{"type":"message","message":{"role":"assistant","stopReason":"`+c.stop+`"}}`))
		if got.End != c.want {
			t.Errorf("%s: %v", c.stop, got.End)
		}
	}
	if encodePiDir("/home/dev/shop.v2") != "--home-dev-shop.v2--" || encodePiDir(`C:\dev\shop`) != "--C--dev-shop--" {
		t.Fatalf("%s %s", encodePiDir("/home/dev/shop.v2"), encodePiDir(`C:\dev\shop`))
	}
}

func TestLocalReadsPiAndRecognizesItsProcess(t *testing.T) {
	if harnessOf(Proc{Name: "pi", Args: []string{"pi"}}) != core.HarnessPi || harnessOf(Proc{Name: "node", Args: []string{"node", "/x/@mariozechner/pi-coding-agent/cli.js"}}) != core.HarnessPi {
		t.Fatal("pi is recognized by name or package")
	}
	if harnessOf(Proc{Name: "Pi"}) != "" || harnessOf(Proc{Name: "pip"}) != "" {
		t.Fatal("names are matched exactly")
	}
	sess := strings.Join([]string{
		`{"type":"session","id":"pi-s9","cwd":"/work/barn"}`,
		`{"type":"message","message":{"role":"user","content":"paint it"}}`,
		`{"type":"message","message":{"role":"assistant","model":"pi-demo-1","stopReason":"stop","usage":{"input":5,"output":5}}}`,
	}, "\n") + "\n"
	fsys := fstest.MapFS{"home/u/.pi/agent/sessions/--work-barn--/s.jsonl": file(sess, 20*time.Second)}
	procs := []Proc{{PID: 7, PPID: 1, Name: "pi", Args: []string{"pi"}, Cwd: "/work/barn", StartUnix: now.Add(-time.Hour).Unix()}}
	got, _, err := newLocal(fsys, procs, nil, nil).Agents(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	if a := got[0]; a.Harness != core.HarnessPi || a.State != core.AgentIdle || a.Task != "paint it" || a.Model != "pi-demo-1" || a.SessionID != "pi-s9" || len(a.TurnTokens) != 1 || a.ContextTokens == nil {
		t.Fatalf("%+v", a)
	}
}

func TestLocalCarriesCodexLimitsAndClaudeFileLimits(t *testing.T) {
	lim := `{"primary":{"used_percent":30,"window_minutes":300,"resets_at":1767268800}}`
	codex := strings.Join([]string{
		`{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"cx","cwd":"/work/mill"}}`,
		codexTok("2026-01-01T11:55:00Z", 500, 300, 0, 50, 100000, lim),
	}, "\n") + "\n"
	day := now.Local().Format("2006/01/02")
	fsys := fstest.MapFS{
		"home/u/.codex/sessions/" + day + "/r.jsonl": file(codex, time.Minute),
		"home/u/.claude/" + claudeQuotaFile:          {Data: []byte(`{"five_hour":{"used_percentage":50},"updated_at":1767268000}`)},
	}
	procs := []Proc{{PID: 8, PPID: 1, Name: "codex", Cwd: "/work/mill", StartUnix: now.Add(-time.Hour).Unix()}}
	got, extra, err := newLocal(fsys, procs, nil, nil).Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a := byPID(got, 8); a.ContextTokens == nil || *a.ContextTokens != 300 || a.ContextWindow == nil || len(a.TurnTokens) != 1 {
		t.Fatalf("%+v", a)
	}
	if len(extra.RateLimits) != 2 || extra.RateLimits[0].Harness != core.HarnessCodex || *extra.RateLimits[0].FiveHourPct != 30 || extra.RateLimits[1].Harness != core.HarnessClaude {
		t.Fatalf("%+v", extra.RateLimits)
	}
}

// ---- OpenCode ----

func makeOpenCodeDB(t *testing.T, ddl string, inserts ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range append([]string{ddl}, inserts...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err, q)
		}
	}
	return p
}

const ocDDL = `CREATE TABLE session_v2 (id TEXT PRIMARY KEY, parent_id TEXT, title TEXT, model TEXT, directory TEXT,
	tokens_input INTEGER, tokens_output INTEGER, tokens_cache_read INTEGER, tokens_cache_write INTEGER,
	cost REAL, time_updated INTEGER, time_busy INTEGER, time_idle INTEGER, time_compacting INTEGER)`

func TestOpenCodeReaderReadsAnImmutableCopyAndWritesNothing(t *testing.T) {
	ms := now.UnixMilli()
	p := makeOpenCodeDB(t, ocDDL,
		`INSERT INTO session_v2 VALUES ('a','','Fix the gate','{"id":"oc-demo-1","providerID":"demo"}','/work/barn',100,20,5000,7,0.25,`+itoa(ms)+`,`+itoa(ms-10)+`,`+itoa(ms-500)+`,0)`,
		`INSERT INTO session_v2 VALUES ('b','a',NULL,NULL,'/work/barn',NULL,NULL,NULL,NULL,NULL,`+itoa(ms-1000)+`,NULL,NULL,NULL)`,
		`INSERT INTO session_v2 VALUES ('old','','Ancient','',  '/work/attic',1,1,0,0,0,1000,0,0,0)`,
	)
	before, _ := os.Stat(p)
	rows, err := readOpenCodeDB(context.Background(), p, now.Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "a" || rows[1].ParentID != "a" {
		t.Fatalf("a window, newest first, parents kept for the caller to skip: %+v", rows)
	}
	a := rows[0]
	if a.Model != "demo/oc-demo-1" || a.Title != "Fix the gate" || a.Input != 100 || a.CacheWrite != 7 || a.Cost == nil || *a.Cost != 0.25 || a.Busy <= a.Idle {
		t.Fatalf("%+v", a)
	}
	if rows[1].Cost != nil || rows[1].Input != 0 || rows[1].Title != "" {
		t.Fatalf("nulls are unknown or empty, never an error: %+v", rows[1])
	}
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("the database changed")
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("reading created files next to the database: %v", ents)
	}
	if got, _ := readOpenCodeDB(context.Background(), p, now.Add(-time.Hour), 1); len(got) != 1 {
		t.Fatal("the limit is honored")
	}
}

func TestOpenCodeReaderToleratesMissingColumnsAndRefusesOtherFiles(t *testing.T) {
	ms := itoa(now.UnixMilli())
	p := makeOpenCodeDB(t, `CREATE TABLE session_v2 (id TEXT, directory TEXT, time_updated INTEGER)`, `INSERT INTO session_v2 VALUES ('a','/work/x',`+ms+`)`)
	rows, err := readOpenCodeDB(context.Background(), p, now.Add(-time.Hour), 5)
	if err != nil || len(rows) != 1 || rows[0].Dir != "/work/x" || rows[0].Cost != nil {
		t.Fatalf("a layout with fewer columns costs fields, not the read: %v %+v", err, rows)
	}
	for name, ddl := range map[string]string{
		"no table":      `CREATE TABLE other (x INTEGER)`,
		"missing a key": `CREATE TABLE session_v2 (id TEXT, time_updated INTEGER)`,
		"directory-ish": `CREATE TABLE session_v2 (id TEXT, dir TEXT, time_updated INTEGER)`,
	} {
		_, err := readOpenCodeDB(context.Background(), makeOpenCodeDB(t, ddl), now.Add(-time.Hour), 5)
		if !errors.Is(err, errNoOpenCodeSessions) {
			t.Errorf("%s: %v", name, err)
		}
	}
	junk := filepath.Join(t.TempDir(), "x.db")
	if err := os.WriteFile(junk, []byte("this is not a database, just text, repeated enough to look like a page. "+strings.Repeat("x", 600)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOpenCodeDB(context.Background(), junk, now.Add(-time.Hour), 5); err == nil {
		t.Fatal("a file that is not a database is an error the caller turns into a note")
	}
}

func TestLocalMatchesOpenCodeSessionsAndSkipsSubagents(t *testing.T) {
	ms := now.UnixMilli()
	var asked string
	read := func(_ context.Context, p string, since time.Time, limit int) ([]opencodeSession, error) {
		asked = p
		return []opencodeSession{
			{ID: "live", Title: "Fix the gate", Model: "demo/oc-1", Dir: "/work/barn", Input: 100, Output: 20, CacheWrite: 5, Updated: time.UnixMilli(ms - 20_000), Busy: ms - 30_000, Idle: ms - 60_000},
			{ID: "child", ParentID: "live", Dir: "/work/barn", Updated: time.UnixMilli(ms)},
			{ID: "ended", Title: "Old news", Dir: "/work/attic", Updated: time.UnixMilli(ms - 5*60_000), Busy: ms - 400_000, Idle: ms - 300_000},
			{ID: "ancient", Dir: "/work/cellar", Updated: now.Add(-3 * time.Hour)},
		}, nil
	}
	fsys := fstest.MapFS{"home/u/.local/share/opencode/opencode.db": file("x", time.Minute)}
	l := newLocal(fsys, []Proc{{PID: 5, PPID: 1, Name: "opencode", Cwd: "/work/barn", StartUnix: now.Add(-time.Hour).Unix()}}, nil, nil)
	l.o.OpenCode = read
	got, _, err := l.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if asked != "/home/u/.local/share/opencode/opencode.db" {
		t.Fatalf("path %q", asked)
	}
	a := byPID(got, 5)
	if a.SessionID != "live" || a.State != core.AgentRunning || a.Source != core.EvidenceTranscript || a.Tokens == nil || *a.Tokens != 125 || a.Task != "Fix the gate" {
		t.Fatalf("%+v", a)
	}
	var stopped []string
	for _, g := range got {
		if g.PID == 0 {
			stopped = append(stopped, g.SessionID+":"+string(g.State))
		}
	}
	if !slices.Equal(stopped, []string{"ended:stopped"}) {
		t.Fatalf("a subagent's session is not an agent, nor is one older than the recent window: %v", stopped)
	}
}

func TestOpenCodeBusyAfterAWhileIsNotClaimedRunning(t *testing.T) {
	r := opencodeSession{ID: "s", Dir: "/w", Updated: now.Add(-time.Hour), Busy: 10, Idle: 5}
	if st := inferState(r.asSession(), now); st != core.AgentUnknown {
		t.Fatalf("a store read can lag a live session; an hour-old busy stamp is %s, not running", st)
	}
}

func TestLocalSaysWhenTheOpenCodeDatabaseCannotBeRead(t *testing.T) {
	fsys := fstest.MapFS{"home/u/.local/share/opencode/opencode.db": file("x", time.Minute)}
	l := newLocal(fsys, nil, nil, nil)
	l.o.OpenCode = func(context.Context, string, time.Time, int) ([]opencodeSession, error) {
		return nil, errors.New("database disk image is malformed")
	}
	_, _, err := l.Agents(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OpenCode session database could not be read") {
		t.Fatalf("%v", err)
	}
	l.o.OpenCode = func(context.Context, string, time.Time, int) ([]opencodeSession, error) {
		return nil, errNoOpenCodeSessions
	}
	if _, _, err := l.Agents(context.Background()); err != nil {
		t.Fatalf("an older layout reads as nothing here: %v", err)
	}
	called := false
	l = newLocal(fstest.MapFS{"home/u/.local/share/opencode/opencode.db/x": file("x", 0)}, nil, nil, nil) // a directory, not a file
	l.o.OpenCode = func(context.Context, string, time.Time, int) ([]opencodeSession, error) {
		called = true
		return nil, nil
	}
	l.Agents(context.Background())
	if called {
		t.Fatal("only a regular file is opened")
	}
}

// ---- uncommitted files ----

func TestParseDirty(t *testing.T) {
	out := " M a.go\nM  b.go\nA  c.go\n?? d.go\n?? e/\n D f.go\nR  g.go -> h.go\n!! junk\nUU conflict.go\n\nx\n"
	if g := parseDirty([]byte(out)); g.Added != 3 || g.Modified != 5 {
		t.Fatalf("%+v", g)
	}
	if g := parseDirty(nil); g != (core.AgentGit{}) {
		t.Fatal("a clean tree is zero counts, which is known")
	}
}

func TestDirtyArgvIsReadOnlyAndDisablesRepositoryPrograms(t *testing.T) {
	argv := dirtyArgv("/work/x")
	want := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", "/work/x", "status", "--porcelain=v1"}
	if !slices.Equal(argv, want) {
		t.Fatalf("%v", argv)
	}
	if _, ok := gitDirty(context.Background(), "."); ok { // inside a repository when run from a checkout, so only the guard says no
		t.Fatal("a directory that is not absolute is never passed to git")
	}
}

func TestGitDirtyRealRepositoryNeverRunsItsFsmonitor(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git in this environment")
	}
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "PATH=" + os.Getenv("PATH")}
	run := func(args ...string) {
		c := exec.Command(git, append([]string{"-C", dir}, args...)...)
		c.Env = env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "core.fsmonitor", hook)
	for f, body := range map[string]string{"a.txt": "a", "b.txt": "b"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "a.txt") // git itself runs the program on this very command: that is the risk
	os.Remove(marker)
	t.Setenv("HOME", t.TempDir())
	g, ok := gitDirty(context.Background(), dir)
	if !ok || g.Added != 2 || g.Modified != 0 {
		t.Fatalf("%+v %v", g, ok)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository's own fsmonitor program ran")
	}
	if _, ok := gitDirty(context.Background(), filepath.Join(dir, "nope")); ok {
		t.Fatal("a directory that is not there is unknown")
	}
}

func TestFillDirtyAsksOncePerDirectoryReusesAnswersAndBoundsTheWork(t *testing.T) {
	var calls atomic.Int32
	l := newLocal(fstest.MapFS{}, nil, nil, nil)
	l.o.Dirty = func(_ context.Context, d string) (core.AgentGit, bool) {
		calls.Add(1)
		if d == "/work/bad" {
			return core.AgentGit{}, false
		}
		return core.AgentGit{Added: 1, Modified: 2}, true
	}
	list := []core.Agent{
		{Repo: "a", Cwd: "/work/a"}, {Repo: "a", Cwd: "/work/a"}, // two agents, one directory
		{Repo: "b", Cwd: "/work/bad"},
		{Cwd: "/work/norepo"},        // no repository: git is not asked
		{Repo: "c", Cwd: "relative"}, // never handed to git
	}
	l.fillDirty(context.Background(), list)
	if calls.Load() != 2 || list[0].Git == nil || list[0].Git.Modified != 2 || list[1].Git == nil || list[2].Git != nil || list[3].Git != nil || list[4].Git != nil {
		t.Fatalf("%d calls: %+v", calls.Load(), list)
	}
	l.fillDirty(context.Background(), list)
	if calls.Load() != 2 {
		t.Fatal("an answer younger than the TTL is reused, a failed one too")
	}
	l.o.Now = func() time.Time { return now.Add(time.Minute) }
	l.fillDirty(context.Background(), list)
	if calls.Load() != 4 {
		t.Fatalf("an old answer is read again: %d", calls.Load())
	}
	many := make([]core.Agent, 0, 100)
	for i := 0; i < 100; i++ {
		many = append(many, core.Agent{Repo: "r", Cwd: "/work/d" + itoa(int64(i))})
	}
	calls.Store(0)
	l.fillDirty(context.Background(), many)
	if calls.Load() != maxDirty {
		t.Fatalf("one read asks about at most %d directories, asked %d", maxDirty, calls.Load())
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func TestMonitorLimitsAreMarkedStaleAgainstItsClock(t *testing.T) {
	at := time.Unix(1795501634, 0)
	for _, c := range []struct {
		clock       time.Time
		claude, cdx bool
	}{{at.Add(time.Minute), false, true}, {at.Add(time.Hour), true, true}} { // codex's carries no time at all
		m := NewMonitor(MonitorOptions{Runner: &fakeRun{out: fixture(t, "monitor-v0.5.5.json")}, Bin: "/fake/monitor", Now: func() time.Time { return c.clock }})
		m.SetFound(core.Found{Bin: "/fake/monitor", Src: core.SourceOverride})
		_, x, err := m.Agents(context.Background())
		if err != nil || len(x.RateLimits) != 2 || x.RateLimits[0].Stale != c.claude || x.RateLimits[1].Stale != c.cdx {
			t.Fatalf("%v %+v", err, x.RateLimits)
		}
	}
}
