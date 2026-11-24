package agents

import (
	"context"
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

const hostile = "a\x1b[31mred\x1b]0;pwned\x07b‮evil⁦x​z\x9b2J"

func unsafeIn(t *testing.T, report any) {
	t.Helper()
	b, _ := json.Marshal(report)
	var v any
	_ = json.Unmarshal(b, &v)
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, r := range x {
				if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x202e || r == 0x2066 || r == 0x200b {
					t.Errorf("unsafe %U in %q", r, x)
					return
				}
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				walk(k)
				walk(e)
			}
		}
	}
	walk(v)
}

func read(t *testing.T, m core.Module) core.AgentsReport {
	t.Helper()
	return core.ReadAgents(context.Background(), []core.Entry{{Module: m, Detection: core.Detection{State: core.Ready}}})
}

func TestHostileMonitorJSONIsSanitizedInEveryField(t *testing.T) {
	h, _ := json.Marshal(hostile)
	snap := `{"sessions":[{"agent_cli":` + string(h) + `,"pid":7,"session_id":` + string(h) + `,"cwd":` + string(h) + `,"status":"Waiting","model":` + string(h) + `,
	 "git_branch":` + string(h) + `,"summary":` + string(h) + `,"current_task":` + string(h) + `,
	 "children":[{"pid":8,"command":` + string(h) + `,"port":9}],"subagents":[{"name":` + string(h) + `,"status":` + string(h) + `}]}],
	 "rate_limits":[{"source":` + string(h) + `,"five_hour_pct":5}],"orphan_ports":[{"port":1,"pid":2,"command":` + string(h) + `}]}`
	m := monitorWith(&fakeRun{out: snap})
	r := read(t, m)
	unsafeIn(t, r)
	if len(r.Agents) != 1 || len(r.RateLimits) != 1 || len(r.OrphanPorts) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestHostileTranscriptTextIsSanitizedInEveryField(t *testing.T) {
	h, _ := json.Marshal(hostile)
	line := `{"type":"user","sessionId":` + string(h) + `,"cwd":"/work/orchard","gitBranch":` + string(h) + `,"message":{"content":` + string(h) + `}}
{"type":"assistant","sessionId":` + string(h) + `,"cwd":"/work/orchard","message":{"model":` + string(h) + `,"content":[{"type":"text","text":"x"}]}}
`
	fsys := fstest.MapFS{"home/u/.claude/projects/-work-orchard/a.jsonl": file(line, time.Second),
		"work/orchard/.git/HEAD": {Data: []byte("ref: refs/heads/" + hostile + "\n")}}
	procs := []Proc{{PID: 5, PPID: 1, Name: "claude", Cwd: "/work/orchard", StartUnix: now.Add(-time.Hour).Unix()}}
	r := read(t, newLocal(fsys, procs, nil, []Pane{{PID: 1, ID: "%1", Target: "w:0.0"}}))
	unsafeIn(t, r)
	if len(r.Agents) != 1 || r.Agents[0].Task == "" {
		t.Fatalf("%+v", r.Agents)
	}
}

func TestGitWalkKeepsHiddenFolderNames(t *testing.T) {
	fsys := fstest.MapFS{".hid/proj/.git/HEAD": {Data: []byte("ref: refs/heads/main\n")}, ".hid/proj/sub/x": {}}
	if r, b, _ := gitInfo(fsys, "/.hid/proj/sub"); r != "proj" || b != "main" {
		t.Fatalf("a dot in a folder name must not be trimmed: %q %q", r, b)
	}
	if r, _, _ := gitInfo(fsys, "/.hid"); r != "" {
		t.Fatal("not a repo")
	}
	root := fstest.MapFS{".git/HEAD": {Data: []byte("ref: refs/heads/top\n")}}
	if _, b, _ := gitInfo(root, "/deep/er"); b != "top" {
		t.Fatalf("the root repo is found: %q", b)
	}
}

func TestOtherUsersAgentsAreHiddenByDefault(t *testing.T) {
	procs := []Proc{
		{PID: 1, PPID: 0, Name: "claude", Cwd: "/a", UID: 501, UIDKnown: true},
		{PID: 2, PPID: 0, Name: "claude", Cwd: "/b", UID: 502, UIDKnown: true},
		{PID: 3, PPID: 0, Name: "claude", Cwd: "/c"}, // owner unknown: kept
	}
	mk := func(all bool) *Local {
		l := newLocal(fstest.MapFS{}, procs, nil, nil)
		l.o.AllUsers, l.o.CurrentUID = all, func() int { return 501 }
		return l
	}
	got, _, _ := mk(false).Agents(context.Background())
	if len(got) != 2 || byPID(got, 2).PID != 0 {
		t.Fatalf("%+v", got)
	}
	if all, _, _ := mk(true).Agents(context.Background()); len(all) != 3 {
		t.Fatalf("--all shows every user: %+v", all)
	}
}

// blockingFS stands for a file system whose Open would block on a FIFO (os.DirFS does): the stat
// before the open must refuse it first.
type blockingFS struct{ fstest.MapFS }

func (b blockingFS) Open(name string) (fs.File, error) {
	if fi, err := b.MapFS.Stat(name); err == nil && fi.Mode()&fs.ModeNamedPipe != 0 {
		panic("opened a FIFO: this would block forever")
	}
	return b.MapFS.Open(name)
}

func TestAFifoIsRefusedBeforeItIsOpened(t *testing.T) {
	fsys := blockingFS{fstest.MapFS{"x.jsonl": {Data: []byte("{}"), Mode: fs.ModeNamedPipe}, "ok.jsonl": {Data: []byte("{}")}}}
	if _, _, err := openRegular(fsys, "x.jsonl"); err == nil {
		t.Fatal("a FIFO was accepted")
	}
	f, _, err := openRegular(fsys, "ok.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
