package agents

import (
	"context"
	"path/filepath"
	"strings"

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
		out[proc.PID] = proc
	}
	return out, nil
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
