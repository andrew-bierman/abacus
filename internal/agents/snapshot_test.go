package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// testStart is a fixed process start time, so a test never reads the clock.
var testStart = time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

// fakeMachine is the whole test surface for live state: a fixed process table, cwds and
// sockets. No test here reads this machine.
type fakeMachine struct {
	procs map[int]Proc
	cwds  map[int]string
	socks []Socket
	err   error
}

func (f *fakeMachine) Procs(context.Context) (map[int]Proc, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.procs, nil
}

func (f *fakeMachine) CWD(_ context.Context, pid int) string { return f.cwds[pid] }

func (f *fakeMachine) Listeners(context.Context) ([]Socket, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.socks, nil
}

func TestHarnessForMatchesSuffixAndRejectsDaemons(t *testing.T) {
	cases := []struct {
		name string
		want Harness
		ok   bool
	}{
		{"claude", HarnessClaude, true},
		{"claude.exe", HarnessClaude, true},
		{"codex", HarnessCodex, true},
		{"opencode.exe", HarnessOpenCode, true},
		{"pi", HarnessPi, true},
		{"opencode-pty", "", false}, // the pty daemon, not a session
		{"node", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := HarnessFor(Proc{Name: c.name})
		if ok != c.ok || got != c.want {
			t.Errorf("HarnessFor(%q) = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestDescendantsIsBounded(t *testing.T) {
	procs := map[int]Proc{
		1: {PID: 1},
		2: {PID: 2, PPID: 1},
		3: {PID: 3, PPID: 2},
	}
	if got := Descendants(procs, 1, 10); len(got) != 2 {
		t.Errorf("descendants = %v, want two", got)
	}
	if got := Descendants(procs, 1, 1); len(got) != 1 {
		t.Errorf("bounded descendants = %v, want one", got)
	}
}

func TestSnapshotFindsSessionChildrenAndPorts(t *testing.T) {
	m := &fakeMachine{
		procs: map[int]Proc{
			100: {PID: 100, PPID: 1, Name: "claude", Argv: []string{"/usr/bin/claude"}, Started: testStart},
			200: {PID: 200, PPID: 100, Name: "node", Argv: []string{"node", "server.js"}},
			300: {PID: 300, PPID: 1, Name: "bash", Argv: []string{"bash"}},
		},
		cwds:  map[int]string{100: "/home/dev/proj", 200: "/home/dev/proj"},
		socks: []Socket{{Port: 8080, PID: 200}},
	}
	c := &Collector{Machine: m}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1: %+v", len(snap.Sessions), snap.Sessions)
	}
	s := snap.Sessions[0]
	if s.Harness != HarnessClaude || s.PID != 100 {
		t.Errorf("session = %+v, want claude pid 100", s)
	}
	if s.Title != "proj" {
		t.Errorf("title = %q, want proj", s.Title)
	}
	if !s.Started.Equal(testStart) {
		t.Errorf("started = %v, want the process's start time", s.Started)
	}
	if len(s.Children) != 1 || s.Children[0].PID != 200 {
		t.Errorf("children = %+v, want pid 200", s.Children)
	}
	if len(s.Ports) != 1 || s.Ports[0].Port != 8080 {
		t.Errorf("ports = %+v, want 8080", s.Ports)
	}
	// One read cannot tell whether work is happening, so it must not claim it.
	if s.State != StateWaiting {
		t.Errorf("state = %q, want waiting on the first read", s.State)
	}
	if len(snap.Orphans) != 0 {
		t.Errorf("orphans on first read = %+v, want none", snap.Orphans)
	}
}

func TestSnapshotExecutingOnlyWhenChildCPUAdvances(t *testing.T) {
	m := &fakeMachine{
		procs: map[int]Proc{
			100: {PID: 100, PPID: 1, Name: "claude"},
			200: {PID: 200, PPID: 100, Name: "cargo", CPUTime: 0.1},
		},
	}
	c := &Collector{Machine: m}
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}

	m.procs[200] = Proc{PID: 200, PPID: 100, Name: "cargo", CPUTime: 2.5} // the child burned CPU
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Sessions[0].State; got != StateExecuting {
		t.Errorf("state = %q, want executing", got)
	}

	// A further read with no progress goes back to waiting: accumulated CPU is a level, and
	// a finished child keeps it.
	snap, err = c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Sessions[0].State; got != StateWaiting {
		t.Errorf("state = %q, want waiting once nothing advances", got)
	}
}

func TestSnapshotReportsOrphanPortAfterSessionExits(t *testing.T) {
	m := &fakeMachine{
		procs: map[int]Proc{
			100: {PID: 100, PPID: 1, Name: "claude"},
			200: {PID: 200, PPID: 100, Name: "node", Argv: []string{"node", "server.js"}},
		},
		cwds:  map[int]string{100: "/home/dev/proj"},
		socks: []Socket{{Port: 8080, PID: 200}},
	}
	c := &Collector{Machine: m}
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The session and its child ended; the server is still listening.
	m.procs = map[int]Proc{}
	m.socks = []Socket{{Port: 8080, PID: 0}}

	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Orphans) != 1 {
		t.Fatalf("got %d orphans, want 1: %+v", len(snap.Orphans), snap.Orphans)
	}
	o := snap.Orphans[0]
	if o.Port != 8080 || !o.Orphan {
		t.Errorf("orphan = %+v, want port 8080 marked orphaned", o)
	}
	if o.Project != "proj" {
		t.Errorf("orphan project = %q, want proj", o.Project)
	}
	if snap.Active() != 0 {
		t.Errorf("active = %d, want 0", snap.Active())
	}
}

func TestSnapshotReportsReadFailure(t *testing.T) {
	c := &Collector{Machine: &fakeMachine{err: errors.New("boom")}}
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("want the read error")
	}
}

func TestSnapshotHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Collector{Machine: &fakeMachine{}}
	if _, err := c.Snapshot(ctx); err == nil {
		t.Fatal("want the context error")
	}
}

func TestCommandLineIsBounded(t *testing.T) {
	long := Proc{Name: "claude", Argv: []string{"claude", strings.Repeat("x", 200)}}
	if got := commandLine(long); len(got) != 80 {
		t.Errorf("length = %d, want 80", len(got))
	}
	if got := commandLine(Proc{Name: "pi"}); got != "pi" {
		t.Errorf("commandLine with no argv = %q, want pi", got)
	}
}
