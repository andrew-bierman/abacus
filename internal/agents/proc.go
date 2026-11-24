package agents

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// ErrNoProcFS means this platform has no /proc to read. Darwin uses a `ps`-based source
// instead, which lands with it; until then a Darwin build reports no live sessions rather
// than guessing from stale files.
var ErrNoProcFS = errors.New("agents: no process filesystem on this platform")

// Proc is one process as the kernel reports it.
type Proc struct {
	PID     int
	PPID    int
	Name    string   // argv[0] basename, the reliable one; "comm" is truncated to 15 bytes
	Argv    []string // full argv, bounded by the kernel to ~128 KiB
	RSSKB   int64
	CPUTime uint64 // clock ticks in user+system, for "is a child actually working?"
}

// Machine reads the live process table and socket table. Root is the procfs mount, which
// is injectable so tests read a fixture tree and never this machine.
type Machine struct {
	// Root is the procfs root; empty means /proc on Linux.
	Root string
}

// DefaultProcRoot is where Linux mounts procfs.
const DefaultProcRoot = "/proc"

// Available reports whether a process table can be read here.
func (m Machine) Available() bool {
	return runtime.GOOS == "linux" || m.Root != ""
}

func (m Machine) root() string {
	if m.Root != "" {
		return m.Root
	}
	return DefaultProcRoot
}

// Procs reads every process. Unreadable entries are skipped, not fatal: processes exit
// while they are being read, and a monitor must not fail because of one.
func (m Machine) Procs() (map[int]Proc, error) {
	if !m.Available() {
		return nil, ErrNoProcFS
	}
	root := m.root()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make(map[int]Proc, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid directory ("self", "net", "sys", …)
		}
		if p, ok := m.proc(root, pid); ok {
			out[pid] = p
		}
	}
	return out, nil
}

func (m Machine) proc(root string, pid int) (Proc, bool) {
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return Proc{}, false
	}
	ppid, cpu, ok := parseStat(string(raw))
	if !ok {
		return Proc{}, false
	}
	p := Proc{PID: pid, PPID: ppid, CPUTime: cpu}
	if b, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline")); err == nil {
		p.Argv = splitNUL(b)
	}
	if len(p.Argv) > 0 {
		p.Name = filepath.Base(p.Argv[0])
	} else if b, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "comm")); err == nil {
		p.Name = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status")); err == nil {
		p.RSSKB = parseRSS(string(b))
	}
	return p, true
}

// parseStat pulls the parent pid and accumulated CPU ticks out of /proc/<pid>/stat.
// The comm field is parenthesised and may itself contain spaces and parentheses, so the
// fields are counted from the last ')', never from the start of the line.
func parseStat(line string) (ppid int, cpu uint64, ok bool) {
	close := strings.LastIndexByte(line, ')')
	if close < 0 || close+2 >= len(line) {
		return 0, 0, false
	}
	f := strings.Fields(line[close+1:])
	if len(f) < 13 {
		return 0, 0, false
	}
	// f[0] is state (field 3), so field N is f[N-3]: ppid is 4, utime 14, stime 15.
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, 0, false
	}
	ut, err1 := strconv.ParseUint(f[11], 10, 64)
	st, err2 := strconv.ParseUint(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return ppid, ut + st, true
}

func parseRSS(status string) int64 {
	for _, line := range strings.Split(status, "\n") {
		if rest, found := strings.CutPrefix(line, "VmRSS:"); found {
			f := strings.Fields(rest)
			if len(f) > 0 {
				if kb, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					return kb
				}
			}
		}
	}
	return 0
}

func splitNUL(b []byte) []string {
	var out []string
	for _, part := range strings.Split(string(b), "\x00") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// CWD reads a process's working directory. This is the only trustworthy way to know which
// project a session belongs to: the transcript may predate a cd.
func (m Machine) CWD(pid int) string {
	if !m.Available() {
		return ""
	}
	dir, err := os.Readlink(filepath.Join(m.root(), strconv.Itoa(pid), "cwd"))
	if err != nil {
		return ""
	}
	return dir
}

// Socket is one listening TCP socket and the process holding it.
type Socket struct {
	Port int
	PID  int
	// Inode is kept so a caller can match a socket to an orphan candidate across reads.
	Inode string
}

// Listeners maps every listening TCP socket to its owning pid. Sockets with no visible
// owner (another user's process, or one that just exited) are kept with PID 0 so the port
// still appears rather than silently vanishing.
func (m Machine) Listeners() ([]Socket, error) {
	if !m.Available() {
		return nil, ErrNoProcFS
	}
	root := m.root()
	ports, err := m.listenPorts(root)
	if err != nil {
		return nil, err
	}
	if len(ports) == 0 {
		return nil, nil
	}
	owners := m.socketOwners(root)
	out := make([]Socket, 0, len(ports))
	for _, p := range ports {
		out = append(out, Socket{Port: p.port, Inode: p.inode, PID: owners[p.inode]})
	}
	return out, nil
}

type listenPort struct {
	port  int
	inode string
}

// listenPorts parses /proc/net/tcp and tcp6 for sockets in LISTEN (state 0A). The tables are
// world-readable, so this sees ports the caller could not otherwise enumerate.
func (m Machine) listenPorts(root string) ([]listenPort, error) {
	var out []listenPort
	seen := 0
	for _, name := range []string{"tcp", "tcp6"} {
		raw, err := os.ReadFile(filepath.Join(root, "net", name))
		if err != nil {
			continue // tcp6 is absent on a host with IPv6 disabled
		}
		seen++
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) < 10 || f[3] != "0A" {
				continue
			}
			_, hexPort, ok := strings.Cut(f[1], ":")
			if !ok {
				continue
			}
			port, err := strconv.ParseUint(hexPort, 16, 16)
			if err != nil {
				continue
			}
			out = append(out, listenPort{port: int(port), inode: f[9]})
		}
	}
	if seen == 0 {
		return nil, ErrNoProcFS
	}
	return out, nil
}

// socketOwners walks every visible process's file descriptors and maps socket inode to pid.
// This is the inverse of what lsof does, without a subprocess: on a machine with a few
// thousand processes it costs a few milliseconds, and it works where lsof is not installed.
func (m Machine) socketOwners(root string) map[string]int {
	owners := make(map[string]int)
	pids, err := os.ReadDir(root)
	if err != nil {
		return owners
	}
	for _, e := range pids {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(root, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // another user's process; its sockets stay ownerless
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			inode, ok := strings.CutPrefix(target, "socket:[")
			if !ok {
				continue
			}
			if inode, ok = strings.CutSuffix(inode, "]"); ok {
				owners[inode] = pid
			}
		}
	}
	return owners
}

// Descendants returns the process ids below root, breadth-first, bounded so a process that
// forks in a loop cannot make one read unbounded.
func Descendants(procs map[int]Proc, root int, limit int) []int {
	if limit <= 0 {
		return nil
	}
	kids := make(map[int][]int, len(procs))
	for pid, p := range procs {
		kids[p.PPID] = append(kids[p.PPID], pid)
	}
	var out []int
	queue := append([]int(nil), kids[root]...)
	for len(queue) > 0 && len(out) < limit {
		pid := queue[0]
		queue = queue[1:]
		out = append(out, pid)
		queue = append(queue, kids[pid]...)
	}
	return out
}

// Signature identifies a process well enough to notice it was replaced, not merely re-pid'd.
func (p Proc) Signature() string {
	return p.Name + "\x00" + strings.Join(p.Argv, "\x00")
}

// Alive reports whether a pid currently exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
