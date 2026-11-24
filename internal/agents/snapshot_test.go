package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeFS builds a procfs-shaped tree in a temp dir, so every test reads a fixture and never
// this machine. Nothing here touches a real pid.
type fakeFS struct {
	t    *testing.T
	root string
}

func newFakeFS(t *testing.T) *fakeFS {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeFS{t: t, root: root}
	f.writeTCP("tcp")
	return f
}

// writeTCP writes a /proc/net/tcp{6} table with the real header and the given rows.
func (f *fakeFS) writeTCP(name string, rows ...string) {
	f.t.Helper()
	body := "  sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode\n"
	body += strings.Join(rows, "\n")
	if len(rows) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(filepath.Join(f.root, "net", name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// listeningRow is one LISTEN socket row. inode ties it to a process's fd.
func listeningRow(port int, inode string) string {
	return fmt.Sprintf("   0: 0100007F:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 %s  1 0 0 10 0", port, inode)
}

// proc creates a process directory. utime and stime are the CPU ticks a descendant sample is
// derived from.
func (f *fakeFS) proc(pid, ppid int, comm string, utime, stime uint64, argv ...string) {
	f.t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	// Fields after the parenthesised comm: state, ppid, then enough fillers that utime lands
	// on field 14 and stime on 15, exactly as procfs numbers them.
	after := []string{"S", strconv.Itoa(ppid)}
	for len(after) < 11 {
		after = append(after, "0")
	}
	after = append(after, strconv.FormatUint(utime, 10), strconv.FormatUint(stime, 10))
	stat := fmt.Sprintf("%d (%s) %s\n", pid, comm, strings.Join(after, " "))
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if len(argv) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	status := "Name:\t" + comm + "\nVmRSS:\t   12345 kB\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeFS) cwd(pid int, dir string) {
	f.t.Helper()
	if err := os.Symlink(dir, filepath.Join(f.root, strconv.Itoa(pid), "cwd")); err != nil {
		f.t.Fatal(err)
	}
}

// socket gives a process a file descriptor holding a socket inode.
func (f *fakeFS) socket(pid int, fd, inode string) {
	f.t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid), "fd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink("socket:["+inode+"]", filepath.Join(dir, fd)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeFS) remove(pid int) {
	f.t.Helper()
	if err := os.RemoveAll(filepath.Join(f.root, strconv.Itoa(pid))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeFS) machine() Machine { return Machine{Root: f.root} }

func TestProcsReadsNamePPIDAndRSS(t *testing.T) {
	f := newFakeFS(t)
	f.proc(100, 1, "claude", 10, 20, "/usr/local/bin/claude", "--resume")

	procs, err := f.machine().Procs()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := procs[100]
	if !ok {
		t.Fatalf("pid 100 missing from %v", procs)
	}
	if p.PPID != 1 {
		t.Errorf("ppid = %d, want 1", p.PPID)
	}
	if p.Name != "claude" {
		t.Errorf("name = %q, want claude", p.Name)
	}
	if p.RSSKB != 12345 {
		t.Errorf("rss = %d, want 12345", p.RSSKB)
	}
	if p.CPUTime != 30 {
		t.Errorf("cpu = %d, want 30", p.CPUTime)
	}
}

// A comm may contain spaces and parentheses, so the stat fields must be counted from the
// last ')' rather than from the start of the line.
func TestParseStatHandlesParenthesisedComm(t *testing.T) {
	f := newFakeFS(t)
	f.proc(200, 7, "node (weird) name", 3, 4)

	procs, err := f.machine().Procs()
	if err != nil {
		t.Fatal(err)
	}
	if got := procs[200].PPID; got != 7 {
		t.Errorf("ppid = %d, want 7", got)
	}
}

func TestListenersMapPortToPID(t *testing.T) {
	f := newFakeFS(t)
	f.proc(200, 100, "node", 1, 1, "node", "server.js")
	f.socket(200, "3", "999")
	f.writeTCP("tcp", listeningRow(8080, "999"))

	socks, err := f.machine().Listeners()
	if err != nil {
		t.Fatal(err)
	}
	if len(socks) != 1 {
		t.Fatalf("got %d sockets, want 1: %+v", len(socks), socks)
	}
	if socks[0].Port != 8080 || socks[0].PID != 200 {
		t.Errorf("socket = %+v, want port 8080 pid 200", socks[0])
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

// The same agent ships under different executable names, so matching must not depend on the
// exact one an install happens to use.
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

func TestSnapshotFindsSessionChildrenAndPorts(t *testing.T) {
	f := newFakeFS(t)
	f.proc(100, 1, "claude", 10, 20, "/usr/bin/claude")
	f.cwd(100, "/home/dev/proj")
	f.proc(200, 100, "node", 1, 1, "node", "server.js")
	f.cwd(200, "/home/dev/proj")
	f.socket(200, "3", "999")
	f.writeTCP("tcp", listeningRow(8080, "999"))
	f.proc(300, 1, "bash", 1, 1, "bash") // not an agent

	c := &Collector{Machine: f.machine()}
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
	f := newFakeFS(t)
	f.proc(100, 1, "claude", 10, 20, "/usr/bin/claude")
	f.cwd(100, "/home/dev/proj")
	f.proc(200, 100, "cargo", 5, 5, "cargo", "build")

	c := &Collector{Machine: f.machine()}
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}

	f.proc(200, 100, "cargo", 50, 5, "cargo", "build") // the child burned CPU
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Sessions[0].State; got != StateExecuting {
		t.Errorf("state = %q, want executing", got)
	}

	// A second read with no further progress goes back to waiting: accumulated CPU is a
	// level, and a finished child keeps it.
	snap, err = c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Sessions[0].State; got != StateWaiting {
		t.Errorf("state = %q, want waiting once nothing advances", got)
	}
}

func TestSnapshotReportsOrphanPortAfterSessionExits(t *testing.T) {
	f := newFakeFS(t)
	f.proc(100, 1, "claude", 10, 20, "/usr/bin/claude")
	f.cwd(100, "/home/dev/proj")
	f.proc(200, 100, "node", 1, 1, "node", "server.js")
	f.socket(200, "3", "999")
	f.writeTCP("tcp", listeningRow(8080, "999"))

	c := &Collector{Machine: f.machine()}
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The session and its child ended; the server is still listening.
	f.remove(100)
	f.remove(200)

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

func TestSnapshotOnMissingProcFSIsAnError(t *testing.T) {
	c := &Collector{Machine: Machine{Root: filepath.Join(t.TempDir(), "nope")}}
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("want an error for a procfs that does not exist")
	}
}

func TestSnapshotHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := newFakeFS(t)
	c := &Collector{Machine: f.machine()}
	if _, err := c.Snapshot(ctx); err == nil {
		t.Fatal("want the context error")
	}
}
