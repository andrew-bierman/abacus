package agents

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
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

func TestDirtyArgvDisablesWhatARepositoryCouldMakeGitRun(t *testing.T) {
	argv, ok := dirtyArgv("/work/x", []byte("filter.lfs.clean\nfilter.lfs.smudge\nfilter.a.b.process\n"))
	joined := " " + strings.Join(argv, " ") + " "
	for _, want := range []string{" core.fsmonitor=false ", " core.untrackedCache=false ", " filter.lfs.clean= ", " filter.lfs.smudge= ", " filter.lfs.process= ", " filter.a.b.clean= ", " filter.a.b.process= ", " --ignore-submodules=all ", " --no-optional-locks ", " status --porcelain=v1 "} {
		if !ok || !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, argv)
		}
	}
	if _, ok := dirtyArgv("/work/x", []byte("filter.we ird.clean\n")); ok {
		t.Fatal("a filter name that is not plain text cannot be neutralized, so the count is unknown")
	}
	if _, ok := dirtyArgv("/work/x", []byte("filter.x.clean=evil\n")); ok {
		t.Fatal("anything but a key name is refused")
	}
}

func TestGitDirtyNeverGetsARelativeDirectory(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git in this environment")
	}
	repo := t.TempDir()
	c := exec.Command(git, "-C", repo, "init", "-q")
	c.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Chdir(repo) // "." is a real repository here, so only the guard can say no
	if _, ok := gitDirty(context.Background(), "."); ok {
		t.Fatal("a directory that is not absolute is never passed to git")
	}
	if _, ok := gitDirty(context.Background(), repo); !ok {
		t.Fatal("the same directory, absolute, is read")
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

func dirDigest(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range ents {
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		fi, _ := e.Info()
		b.WriteString(e.Name() + ":" + itoa(int64(len(raw))) + ":" + fi.ModTime().String() + ":" + string(raw) + "\n")
	}
	return b.String()
}

// OpenCode keeps its database in write-ahead mode while it runs. Reading it must neither create nor
// change any file beside it, and the price (rows still in the log are not seen) is the documented lag.
func TestOpenCodeReaderLeavesALiveWriteAheadDatabaseUntouched(t *testing.T) {
	p := filepath.Join(t.TempDir(), "opencode.db")
	w, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	ms := itoa(now.UnixMilli())
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, ocDDL, `PRAGMA wal_checkpoint(TRUNCATE)`,
		`INSERT INTO session_v2 (id, directory, time_updated) VALUES ('checkpointed','/work/a',` + ms + `)`, `PRAGMA wal_checkpoint(TRUNCATE)`,
		`INSERT INTO session_v2 (id, directory, time_updated) VALUES ('in-the-log','/work/b',` + ms + `)`} {
		if _, err := w.Exec(q); err != nil {
			t.Fatal(err, q)
		}
	}
	dir := filepath.Dir(p)
	before := dirDigest(t, dir)
	if !strings.Contains(before, "opencode.db-wal") {
		t.Fatal("the fixture has no write-ahead log, so it proves nothing")
	}
	rows, err := readOpenCodeDB(context.Background(), p, now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if after := dirDigest(t, dir); after != before {
		t.Fatal("reading changed or created a file next to the database")
	}
	if len(rows) != 1 || rows[0].ID != "checkpointed" {
		t.Fatalf("rows still in the log are not seen (the documented lag): %+v", rows)
	}
}

// hostileRepo makes a repository in a temp directory, with every command it runs for setup using a
// private HOME. run executes git there (and, for setup, may itself run the repository's programs: the
// caller removes the marker afterwards).
func hostileRepo(t *testing.T) (dir string, run func(args ...string) string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git in this environment")
	}
	dir = t.TempDir()
	env := []string{"HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "PATH=" + os.Getenv("PATH"), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
	run = func(args ...string) string {
		c := exec.Command(git, append([]string{"-C", dir}, args...)...)
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	return dir, run
}

func markerScript(t *testing.T) (script, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "ran")
	script = filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, marker
}

func TestGitDirtyNeverRunsARepositoryFilter(t *testing.T) {
	dir, run := hostileRepo(t)
	script, marker := markerScript(t)
	run("config", "filter.x.clean", script)
	run("config", "filter.x.process", script)
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	os.Remove(marker)
	// same content, newer stat: this is what makes git run the clean filter to compare it with the index
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(f, later, later); err != nil {
		t.Fatal(err)
	}
	if _, ok := gitDirty(context.Background(), dir); !ok {
		t.Fatal("a repository with a filter is still counted")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's own filter program ran")
	}
}

func TestGitDirtyNeverRunsASubmodulesFsmonitor(t *testing.T) {
	dir, run := hostileRepo(t)
	script, marker := markerScript(t)
	sub, subRun := t.TempDir(), (func(args ...string) string)(nil)
	_ = subRun
	git, _ := exec.LookPath("git")
	sc := func(args ...string) {
		c := exec.Command(git, append([]string{"-C", sub}, args...)...)
		c.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	sc("init", "-q")
	if err := os.WriteFile(filepath.Join(sub, "s.txt"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	sc("add", ".")
	sc("commit", "-q", "-m", "s")
	run("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "inner")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "add the submodule")
	// a tracked file changed inside the submodule: this is what makes git start a status there
	if err := os.WriteFile(filepath.Join(dir, "inner", "s.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(git, "-C", filepath.Join(dir, "inner"), "config", "core.fsmonitor", script)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.Remove(marker)
	if _, ok := gitDirty(context.Background(), dir); !ok {
		t.Fatal("a repository with a submodule is still counted")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a submodule's own fsmonitor program ran")
	}
}

// ---- review fixes ----

func TestABareCommonNameIsNotAnAgentWithoutCorroboration(t *testing.T) {
	start := now.Add(-time.Hour).Unix()
	procs := []Proc{
		{PID: 1, PPID: 0, Name: "pi", Args: []string{"pi", "--version"}, Cwd: "/work/none", StartUnix: start},                           // nothing vouches
		{PID: 2, PPID: 0, Name: "aider", Args: []string{"aider"}, Cwd: "/work/none", StartUnix: start},                                  // nothing vouches
		{PID: 3, PPID: 0, Name: "aider", Args: []string{"aider"}, Cwd: "/work/ai", StartUnix: start},                                    // its history file is there
		{PID: 4, PPID: 0, Name: "python3", Args: []string{"python3", "/x/aider-chat/aider/main.py"}, Cwd: "/work/zz", StartUnix: start}, // strong: package path
		{PID: 5, PPID: 0, Name: "pi", Args: []string{"pi"}, Cwd: "/work/pi", StartUnix: start},                                          // its session folder is there
	}
	fsys := fstest.MapFS{
		"work/ai/.aider.chat.history.md":                {Data: []byte("x")},
		"home/u/.pi/agent/sessions/--work-pi--/s.jsonl": file(`{"type":"session","id":"p"}`+"\n", time.Minute),
	}
	got, _, _ := newLocal(fsys, procs, nil, nil).Agents(context.Background())
	var pids []int
	for _, a := range got {
		pids = append(pids, a.PID)
	}
	slices.Sort(pids)
	if !slices.Equal(pids, []int{3, 4, 5}) {
		t.Fatalf("listed %v, want 3 4 5", pids)
	}
	if id, weak := harnessMatch(Proc{Name: "pi"}); id != core.HarnessPi || !weak {
		t.Fatal("a bare name is a weak match")
	}
	if _, weak := harnessMatch(Proc{Name: "node", Args: []string{"node", "/x/@mariozechner/pi-coding-agent/cli.js"}}); weak {
		t.Fatal("a package path is strong")
	}
}

func TestExtremeTokenFiguresNeverOverflowOrPanic(t *testing.T) {
	huge := []string{"9223372036854775807", "-9223372036854775808", "4000000000000000000", "1e30", "-1", "0"}
	for _, a := range huge {
		for _, b := range huge {
			claude := `{"type":"assistant","message":{"id":"m","usage":{"input_tokens":` + a + `,"output_tokens":` + b + `,"cache_read_input_tokens":` + a + `,"cache_creation_input_tokens":` + b + `},"content":[]}}`
			codex := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":` + a + `},"last_token_usage":{"input_tokens":` + a + `,"cached_input_tokens":` + b + `,"output_tokens":` + b + `},"model_context_window":` + a + `}}}`
			pi := `{"type":"message","message":{"role":"assistant","usage":{"input":` + a + `,"output":` + b + `,"cacheRead":` + a + `,"cacheWrite":` + b + `}}}`
			for _, s := range []session{parseClaude(lines(claude, claude)), parseCodex(nil, lines(codex, codex)), parsePi(nil, lines(pi))} {
				for _, v := range s.Turns {
					if v < 0 || v > core.MaxTokenCount*4 {
						t.Fatalf("point %d from %s/%s", v, a, b)
					}
				}
				if s.Context != nil && (*s.Context < 0 || *s.Context > core.MaxTokenCount*4) {
					t.Fatalf("context %d", *s.Context)
				}
				// whatever the reader kept, the door makes it safe
				c := core.Agent{Harness: "h", TurnTokens: s.Turns, ContextTokens: s.Context, ContextWindow: s.Window, Tokens: s.Tokens}.Clean()
				for _, v := range c.TurnTokens {
					if v < 0 || v > core.MaxTokenCount {
						t.Fatal("clean let a point through")
					}
				}
			}
			o := opencodeSession{Input: mustInt(a), Output: mustInt(b), CacheWrite: mustInt(a)}.asSession()
			if o.Tokens != nil && (*o.Tokens < 0 || *o.Tokens > core.MaxTokenCount*3) {
				t.Fatalf("opencode tokens %d", *o.Tokens)
			}
		}
	}
}

func mustInt(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 1 << 62
	}
	return n
}

func TestOpenCodePathsWithURICharactersOpen(t *testing.T) {
	ms := itoa(now.UnixMilli())
	for _, name := range []string{"a?b", "a#b", "a%20b", "a&b=c", "a b", "100%", "q?mode=rw&x=1"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Skip("file system refuses this name:", err)
		}
		p := filepath.Join(dir, "opencode.db")
		u := url.URL{Scheme: "file", Path: p} // the driver cuts a plain path at '?': even the fixture needs a URI
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{`CREATE TABLE session_v2 (id TEXT, directory TEXT, time_updated INTEGER)`, `INSERT INTO session_v2 VALUES ('a','/w',` + ms + `)`} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(name, err)
			}
		}
		db.Close()
		rows, err := readOpenCodeDB(context.Background(), p, now.Add(-time.Hour), 5)
		if err != nil || len(rows) != 1 {
			t.Errorf("%q: %v %v", name, err, rows)
		}
	}
}

func TestOpenCodeTextColumnsAreBoundedAndTheFileSizeIsCapped(t *testing.T) {
	ms := itoa(now.UnixMilli())
	long := strings.Repeat("é", 100_000)
	p := makeOpenCodeDB(t, ocDDL, `INSERT INTO session_v2 VALUES ('`+long+`','','`+long+`','`+long+`','/`+long+`',1,1,1,1,0,`+ms+`,0,0,0)`)
	rows, err := readOpenCodeDB(context.Background(), p, now.Add(-time.Hour), 5)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	r := rows[0]
	if len([]rune(r.Title)) > 400 || len([]rune(r.Dir)) > 1024 || len([]rune(r.ID)) > 200 || len([]rune(r.Model)) > 400 {
		t.Fatalf("unbounded: %d %d %d %d", len(r.Title), len(r.Dir), len(r.ID), len(r.Model))
	}
	// a database file larger than the cap is not opened at all
	called := false
	l := newLocal(fstest.MapFS{"home/u/.local/share/opencode/opencode.db": &fstest.MapFile{Data: []byte("x"), ModTime: now}}, nil, nil, nil)
	l.o.OpenCode = func(context.Context, string, time.Time, int) ([]opencodeSession, error) {
		called = true
		return nil, nil
	}
	if _, _, err := l.Agents(context.Background()); err != nil || !called {
		t.Fatalf("a normal size is read: %v %v", err, called)
	}
	called = false
	big := &sizedFS{FS: fstest.MapFS{"home/u/.local/share/opencode/opencode.db": &fstest.MapFile{Data: []byte("x"), ModTime: now}}, name: "home/u/.local/share/opencode/opencode.db", size: maxOpenCodeDB + 1}
	l = newLocal(nil, nil, nil, nil)
	l.o.FS = big
	l.o.OpenCode = func(context.Context, string, time.Time, int) ([]opencodeSession, error) {
		called = true
		return nil, nil
	}
	_, _, err = l.Agents(context.Background())
	if called || err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("an oversized database is refused with a note: called=%v err=%v", called, err)
	}
}

// sizedFS reports a fixed size for one file, so a 2 GiB database need not exist.
type sizedFS struct {
	FS   fstest.MapFS
	name string
	size int64
}

func (s *sizedFS) Open(n string) (fs.File, error) { return s.FS.Open(n) }
func (s *sizedFS) Stat(n string) (fs.FileInfo, error) {
	fi, err := s.FS.Stat(n)
	if err == nil && n == s.name {
		return sizeInfo{fi, s.size}, nil
	}
	return fi, err
}

type sizeInfo struct {
	fs.FileInfo
	n int64
}

func (s sizeInfo) Size() int64 { return s.n }
