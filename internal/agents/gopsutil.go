package agents

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Native reads the machine through gopsutil, which already carries the per-platform detail
// this package would otherwise reimplement four times: procfs on Linux, libproc on macOS,
// and the toolhelp API on Windows. Keeping the seam here means the Collector never learns
// which one it is running on.
type Native struct{}

// Procs reads every visible process. A process that exits mid-read is skipped, not an error:
// a monitor that failed on that would flicker on every tick.
func (Native) Procs(ctx context.Context) (map[int]Proc, error) {
	list, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int]Proc, len(list))
	for _, p := range list {
		if p == nil {
			continue
		}
		proc := Proc{PID: int(p.Pid)}
		if ppid, err := p.PpidWithContext(ctx); err == nil {
			proc.PPID = int(ppid)
		} else {
			continue // gone between listing and reading; nothing to report
		}
		if argv, err := p.CmdlineSliceWithContext(ctx); err == nil {
			proc.Argv = argv
		}
		if len(proc.Argv) > 0 {
			proc.Name = filepath.Base(proc.Argv[0])
		} else if name, err := p.NameWithContext(ctx); err == nil {
			proc.Name = name
		}
		if mi, err := p.MemoryInfoWithContext(ctx); err == nil && mi != nil {
			proc.RSSKB = int64(mi.RSS / 1024)
		}
		if t, err := p.TimesWithContext(ctx); err == nil && t != nil {
			proc.CPUTime = t.User + t.System
		}
		// Only a session process consumes Started, and gopsutil's create time re-reads the
		// same stat file Times just read. Paying that for every process on the machine would
		// double the stat parse to timestamp a handful of agents.
		if _, isSession := HarnessFor(proc); isSession {
			if ms, err := p.CreateTimeWithContext(ctx); err == nil && ms > 0 {
				proc.Started = time.UnixMilli(ms)
			}
		}
		out[proc.PID] = proc
	}
	return out, nil
}

// Probe reports whether the process table can be read at all, without the per-process detail
// Procs pays for. Session detection uses it so a startup probe does not read the whole table
// only for the first snapshot to read it again.
func (Native) Probe(ctx context.Context) error {
	_, err := process.PidsWithContext(ctx)
	return err
}

// CWD reads a process's working directory. This is the only trustworthy way to know which
// project a session belongs to: a transcript may predate a cd.
func (Native) CWD(ctx context.Context, pid int) string {
	p, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return ""
	}
	dir, err := p.CwdWithContext(ctx)
	if err != nil {
		return "" // another user's process, or it exited
	}
	return dir
}

// OpenFiles returns the paths a process holds open. gopsutil implements this on the platforms
// with a file table; elsewhere it returns an error, which the caller reads as "no rollout
// found" rather than as a failed snapshot.
func (Native) OpenFiles(ctx context.Context, pid int) ([]string, error) {
	p, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return nil, err
	}
	opens, err := p.OpenFilesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(opens))
	for _, o := range opens {
		if o.Path != "" {
			out = append(out, o.Path)
		}
	}
	return out, nil
}

// Listeners returns every listening TCP socket with its owner. One call covers the whole
// table, so this does not multiply with the number of sessions.
func (Native) Listeners(ctx context.Context) ([]Socket, error) {
	conns, err := gnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, err
	}
	out := make([]Socket, 0, len(conns))
	for _, c := range conns {
		if !strings.EqualFold(c.Status, "LISTEN") || c.Laddr.Port == 0 {
			continue
		}
		out = append(out, Socket{Port: int(c.Laddr.Port), PID: int(c.Pid)})
	}
	return out, nil
}
