package agents

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func file(data string, age time.Duration) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), ModTime: now.Add(-age)}
}

func newLocal(fsys fstest.MapFS, procs []Proc, perr error, panes []Pane) *Local {
	return NewLocal(Options{
		FS: fsys, Home: "/home/u", Now: func() time.Time { return now }, RecentWindow: 15 * time.Minute,
		Procs: func(context.Context) ([]Proc, error) { return procs, perr },
		Panes: func(context.Context) ([]Pane, error) { return panes, nil },
		Dirty: func(context.Context, string) (core.AgentGit, bool) { return core.AgentGit{}, false },
	})
}

func byPID(l []core.Agent, pid int) core.Agent {
	for _, a := range l {
		if a.PID == pid {
			return a
		}
	}
	return core.Agent{}
}

func TestLocalAgentsEndToEnd(t *testing.T) {
	codexDir := "home/u/.codex/sessions/" + now.Local().Format("2006/01/02") + "/"
	fsys := fstest.MapFS{
		"home/u/.claude/projects/-work-orchard/a.jsonl":                                    file(fixture(t, "claude-toolwait.jsonl"), 20*time.Second),
		"home/u/.claude/projects/-work-orchard/b.jsonl":                                    file(fixture(t, "claude-working.jsonl"), 2*time.Second),
		"home/u/.claude/projects/-work-cellar/c.jsonl":                                     file(fixture(t, "claude-done.jsonl"), 5*time.Minute),
		"home/u/.claude/projects/-work-orchard/0a1b2c3d-1111-2222-3333-444455556666.jsonl": file(strings.ReplaceAll(fixture(t, "claude-done.jsonl"), "/work/cellar", "/work/orchard"), 30*time.Minute),
		"home/u/.claude/projects/-work-attic/d.jsonl":                                      file(strings.ReplaceAll(fixture(t, "claude-done.jsonl"), "/work/cellar", "/work/attic"), 3*time.Hour),
		codexDir + "rollout-x.jsonl":                                                       file(fixture(t, "codex-session.jsonl"), 40*time.Second),
		"work/orchard/.git/HEAD":                                                           {Data: []byte("ref: refs/heads/feat/pears\n")},
		"work/mill/.git/HEAD":                                                              {Data: []byte("ref: refs/heads/main\n")},
	}
	start := now.Add(-time.Hour).Unix()
	procs := []Proc{
		{PID: 90, PPID: 1, Name: "zsh"},
		{PID: 100, PPID: 90, Name: "claude", Cwd: "/work/orchard", StartUnix: start},
		{PID: 101, PPID: 1, Name: "claude", Cwd: "/work/orchard", StartUnix: start + 600},
		{PID: 102, PPID: 101, Name: "claude", Cwd: "/work/orchard", StartUnix: start + 601}, // a helper of 101
		{PID: 200, PPID: 1, Name: "codex", Cwd: "/work/mill", StartUnix: start},
		{PID: 300, PPID: 1, Name: "node", Args: []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, Cwd: "/work/empty", StartUnix: start},
		{PID: 400, PPID: 1, Name: "vim"},
	}
	l := newLocal(fsys, procs, nil, []Pane{{PID: 90, ID: "%3", Target: "work:2.1"}})
	got, _, err := l.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if byPID(got, 102).Harness != "" || byPID(got, 400).Harness != "" {
		t.Fatalf("a helper process or a non-agent was reported: %+v", got)
	}
	// newest process gets the newest session
	if a := byPID(got, 101); a.State != core.AgentRunning || a.SessionID != "sess-claude-3" || a.Source != core.EvidenceTranscript {
		t.Fatalf("101: %+v", a)
	}
	a := byPID(got, 100)
	if a.State != core.AgentWaiting || a.SessionID != "sess-claude-1" || a.Task != "Add a pear counter to the orchard report" ||
		a.Model != "claude-demo-1" || a.Repo != "orchard" || a.Branch != "feat/pears" {
		t.Fatalf("100: %+v", a)
	}
	if a.Term == nil || a.Term.Ref != "work:2.1" || a.Term.Command != "tmux switch-client -t %3" || a.Term.Kind != core.TermTmux {
		t.Fatalf("100 term: %+v", a.Term)
	}
	if c := byPID(got, 200); c.State != core.AgentIdle || c.Tokens == nil || *c.Tokens != 4321 || c.Task != "Grind the wheat module into a package" || c.Branch != "main" {
		t.Fatalf("200: %+v", c)
	}
	if e := byPID(got, 300); e.State != core.AgentUnknown || e.Source != core.EvidenceProcess || e.Harness != core.HarnessClaude || e.Tokens != nil {
		t.Fatalf("300 (no session file): %+v", e)
	}
	var stopped []core.Agent
	for _, g := range got {
		if g.PID == 0 {
			stopped = append(stopped, g)
		}
	}
	if len(stopped) != 1 || stopped[0].SessionID != "sess-claude-2" || stopped[0].State != core.AgentStopped {
		t.Fatalf("recently ended sessions: %+v (the 3h-old one must not be listed)", stopped)
	}
	for _, m := range core.MergeAgents(got) {
		if strings.Contains(m.Task, "sk-demo") {
			t.Fatalf("a credential in a prompt was shown: %q", m.Task)
		}
	}
}

func TestLocalProcessListFailureStillReadsFiles(t *testing.T) {
	fsys := fstest.MapFS{"home/u/.claude/projects/-work-cellar/c.jsonl": file(fixture(t, "claude-done.jsonl"), time.Minute)}
	got, _, err := newLocal(fsys, nil, errors.New("denied"), nil).Agents(context.Background())
	if err == nil || len(got) != 1 {
		t.Fatalf("err=%v got=%+v", err, got)
	}
	if got[0].State == core.AgentStopped { // with no process list "no process" is not known
		t.Fatalf("claimed stopped without a process list: %+v", got[0])
	}
}

func TestLocalNoFilesNoProcsIsEmptyNotError(t *testing.T) {
	got, _, err := newLocal(fstest.MapFS{}, nil, nil, nil).Agents(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestInferState(t *testing.T) {
	cases := []struct {
		end  endKind
		age  time.Duration
		want core.AgentState
	}{
		{endPrompt, time.Minute, core.AgentRunning}, {endPrompt, 10 * time.Minute, core.AgentUnknown},
		{endToolResult, 2 * time.Minute, core.AgentRunning},
		{endToolUse, 2 * time.Second, core.AgentRunning}, {endToolUse, time.Minute, core.AgentWaiting}, {endToolUse, 2 * time.Hour, core.AgentIdle},
		{endDone, time.Second, core.AgentIdle}, {endError, time.Minute, core.AgentError}, {endError, time.Hour, core.AgentIdle},
		{endNone, time.Second, core.AgentUnknown},
	}
	for _, c := range cases {
		if got := inferState(session{End: c.end, ModTime: now.Add(-c.age)}, now); got != c.want {
			t.Errorf("end %d age %v: %s, want %s", c.end, c.age, got, c.want)
		}
	}
}

func TestParseClaudeIgnoresMetaSidechainAndBrokenLines(t *testing.T) {
	s := parseClaude([][]byte{
		[]byte(`{"type":"user","sessionId":"s","cwd":"/a","message":{"content":"real prompt"}}`),
		[]byte(`{"type":"user","isSidechain":true,"cwd":"/sub","message":{"content":"subagent prompt"}}`),
		[]byte(`{"type":"user","isMeta":true,"message":{"content":"meta"}}`),
		[]byte(`{broken`),
		[]byte(`{"type":"user","message":{"content":"<command-name>/x</command-name>"}}`),
	})
	if s.Task != "real prompt" || s.Cwd != "/a" || s.End != endPrompt {
		t.Fatalf("%+v", s)
	}
}

func TestTailIsBoundedAndBudgeted(t *testing.T) {
	big := `{"type":"user","message":{"content":"FIRST-LINE-MUST-NOT-BE-READ"}}` + "\n" +
		strings.Repeat(`{"type":"system","x":"`+strings.Repeat("a", 900)+`"}`+"\n", 3000) + // ~2.7 MB
		`{"type":"user","message":{"content":"last prompt"}}` + "\n"
	fsys := fstest.MapFS{"f.jsonl": file(big, 0)}
	lines, _, err := tailLines(fsys, "f.jsonl", &budget{left: scanBudget})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, l := range lines {
		total += len(l) + 1
		if strings.Contains(string(l), "FIRST-LINE") {
			t.Fatal("read beyond the tail window")
		}
	}
	if total > tailBytes+1 || parseClaude(lines).Task != "last prompt" {
		t.Fatalf("read %d bytes, task %q", total, parseClaude(lines).Task)
	}
	if _, _, err := tailLines(fsys, "f.jsonl", &budget{left: 1000}); err == nil {
		t.Fatal("a scan past its byte budget kept reading")
	}
}

func TestParseCodexHeadAndTail(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(fixture(t, "codex-session.jsonl")), "\n")
	s := parseCodex([]byte(lines[0]), nil) // head only: identity, no activity
	if s.ID != "sess-codex-1" || s.Cwd != "/work/mill" || s.End != endNone {
		t.Fatalf("%+v", s)
	}
	b := make([][]byte, 0)
	for _, l := range lines[1:] {
		b = append(b, []byte(l))
	}
	s = parseCodex([]byte(lines[0]), b)
	if s.End != endDone || s.Tokens == nil || *s.Tokens != 4321 || s.Model != "codex-demo-1" {
		t.Fatalf("%+v", s)
	}
}

func TestHarnessMatching(t *testing.T) {
	cases := []struct {
		p    Proc
		want string
	}{
		{Proc{Name: "claude"}, core.HarnessClaude},
		{Proc{Name: "Claude Helper"}, ""},
		{Proc{Name: "Codex"}, ""}, // the desktop app is not the command-line agent
		{Proc{Name: "2.1.325", Args: []string{"claude", "--resume", "x"}}, core.HarnessClaude},
		{Proc{Name: "2.1.325", Args: []string{"/home/u/.local/bin/claude", "--chrome-native-host"}}, ""},
		{Proc{Name: "codex", Args: []string{"codex", "app-server"}}, ""},
		{Proc{Name: "2.1.325"}, ""},
		{Proc{Name: "codex"}, core.HarnessCodex},
		{Proc{Name: "node", Args: []string{"node", "/x/@openai/codex/bin/codex.js"}}, core.HarnessCodex},
		{Proc{Name: "node", Args: []string{"node", "server.js"}}, ""},
		{Proc{Name: "opencode"}, core.HarnessOpenCode},
		{Proc{Name: "aider"}, core.HarnessAider},
		{Proc{Name: "bash"}, ""},
	}
	for _, c := range cases {
		if got := harnessOf(c.p); got != c.want {
			t.Errorf("%+v: %q want %q", c.p, got, c.want)
		}
	}
}

func TestGitInfo(t *testing.T) {
	fsys := fstest.MapFS{
		"repos/app/.git/HEAD":                         {Data: []byte("ref: refs/heads/main\n")},
		"repos/app/pkg/deep/x":                        {},
		"wt/app-feat/.git":                            {Data: []byte("gitdir: /repos/app/.git/worktrees/app-feat\n")},
		"repos/app/.git/worktrees/app-feat/HEAD":      {Data: []byte("ref: refs/heads/feat/x\n")},
		"repos/app/.git/worktrees/app-feat/commondir": {Data: []byte("../..\n")},
		"det/.git/HEAD":                               {Data: []byte("0123456789abcdef0123\n")},
	}
	if r, b, w := gitInfo(fsys, "/repos/app/pkg/deep"); r != "app" || b != "main" || w != "" {
		t.Fatalf("%q %q %q", r, b, w)
	}
	if r, b, w := gitInfo(fsys, "/wt/app-feat"); r != "app" || b != "feat/x" || w != "app-feat" {
		t.Fatalf("linked worktree: %q %q %q", r, b, w)
	}
	if _, b, _ := gitInfo(fsys, "/det"); b != "0123456 (detached)" {
		t.Fatal(b)
	}
	if r, b, w := gitInfo(fsys, "/nowhere/at/all"); r+b+w != "" {
		t.Fatal("not a repo")
	}
}

func TestPanesAndTermOf(t *testing.T) {
	got := parsePanes("10\t%1\twork:0.0\nbad\n11\t%2; rm -rf\twork:0.1\n12\t%3\tbad target;$(x)\n13\t%4\twork:1.0\n")
	if len(got) != 2 || got[0].ID != "%1" || got[1].ID != "%4" {
		t.Fatalf("a pane id or target that is not what tmux prints must be dropped: %+v", got)
	}
	procs := map[int]Proc{500: {PPID: 400}, 400: {PPID: 10}}
	if tr := termOf(500, procs, got); tr == nil || tr.Ref != "work:0.0" {
		t.Fatalf("%+v", tr)
	}
	if termOf(500, procs, nil) != nil || termOf(999, procs, got) != nil {
		t.Fatal("no pane must give nil")
	}
}

func TestResumedSessionIsFoundByIDEvenIfNotWrittenSinceStart(t *testing.T) {
	id := "0a1b2c3d-1111-2222-3333-444455556666"
	fsys := fstest.MapFS{
		"home/u/.claude/projects/-work-orchard/" + id + ".jsonl": file(fixture(t, "claude-toolwait.jsonl"), 30*time.Minute),
		"home/u/.claude/projects/-work-orchard/other.jsonl":      file(fixture(t, "claude-working.jsonl"), 40*time.Minute),
	}
	procs := []Proc{{PID: 600, PPID: 1, Name: "2.9.9", Args: []string{"claude", "--resume", id}, Cwd: "/work/orchard", StartUnix: now.Add(-10 * time.Minute).Unix()},
		{PID: 601, PPID: 1, Name: "2.9.9", Args: []string{"claude"}, Cwd: "/work/orchard", StartUnix: now.Add(-10 * time.Minute).Unix()}}
	got, _, _ := newLocal(fsys, procs, nil, nil).Agents(context.Background())
	if a := byPID(got, 600); a.SessionID != "sess-claude-1" || a.Source != core.EvidenceTranscript {
		t.Fatalf("%+v", a)
	}
	if a := byPID(got, 601); a.SessionID != "" || a.State != core.AgentUnknown { // no session written since it started: nothing is claimed
		t.Fatalf("a fresh process must not inherit an older session: %+v", a)
	}
	if resumeID([]string{"claude", "--resume", "../../etc"}) != "" || resumeID([]string{"claude", "--resume"}) != "" {
		t.Fatal("only a plain id is a session id")
	}
}
