package agents

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrNoProcFS means the process table could not be read on this platform.
var ErrNoProcFS = errors.New("agents: process table unavailable")

// Proc is one process, reduced to what a monitor needs. It is deliberately not gopsutil's
// type: the Collector, the report and the tests should not depend on the library that
// happens to fill it.
type Proc struct {
	PID   int
	PPID  int
	Name  string   // executable basename; "comm" is truncated to 15 bytes, so this is argv[0]
	Argv  []string // full argv, for a one-line display
	RSSKB int64
	// CPUTime is user+system seconds accumulated by the process. A delta between two reads
	// is how a session is known to be executing; the level alone says nothing.
	CPUTime float64
	// Started is when the process began. The store readers use it as the earliest transcript
	// a session in this directory could have written, which keeps a live session from
	// adopting an older transcript from the same project.
	Started time.Time
}

// Socket is one listening TCP socket and the process holding it. PID is 0 when the owner is
// not visible to this user, so the port still appears instead of vanishing.
type Socket struct {
	Port int
	PID  int
}

// Machine is the live-state source. gopsutil implements it on every platform abacus ships
// for; tests implement it over a fixed table so no test reads this machine.
type Machine interface {
	Procs(ctx context.Context) (map[int]Proc, error)
	CWD(ctx context.Context, pid int) string
	Listeners(ctx context.Context) ([]Socket, error)
}

// Files is the open-file source a caller uses to tie a live process to the file it is writing.
// Codex keeps no session list, so its rollout is the JSONL the process holds open. This is a
// second seam rather than part of Machine because the Collector never needs it: walking every
// process's file table each tick would cost more than the monitor saves, while only a handful
// of live sessions are ever asked.
type Files interface {
	OpenFiles(ctx context.Context, pid int) ([]string, error)
}

// Prober is the cheap availability check a Machine may implement. Detection asks only whether
// the process table is readable, not what is in it, so a Machine that can answer that without
// the per-process read keeps startup from paying for the table twice.
type Prober interface {
	Probe(ctx context.Context) error
}

// Descendants returns the process ids below root, breadth-first, bounded so a process that
// forks in a loop cannot make one read unbounded.
func Descendants(procs map[int]Proc, root, limit int) []int {
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

// commandLine renders a process for a one-line display, bounded so a long model prompt
// cannot push a column off screen.
func commandLine(p Proc) string {
	line := strings.Join(p.Argv, " ")
	if line == "" {
		return p.Name
	}
	const max = 80
	if len(line) > max {
		return line[:max]
	}
	return line
}

// baseName is filepath.Base with the "." a caller would get for an empty path removed, so a
// session with no readable directory simply has no project name.
func baseName(dir string) string {
	if dir == "" {
		return ""
	}
	return strings.TrimSuffix(dir[strings.LastIndexByte(dir, '/')+1:], "/")
}
