package agents

import (
	"context"
	"io/fs"
	"os"
	"runtime"

	"github.com/shirou/gopsutil/v4/process"
)

// defaultFS is the real file system rooted at "/" (nil on Windows, where session files are not read).
func defaultFS() fs.FS {
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.DirFS("/")
}

// scanProcesses reads the process list in this process (no helper, no daemon). Only processes
// whose name could be a harness are asked for their arguments and directory, and every per-process
// failure (a process that exited, one we may not inspect) just leaves that process out.
func scanProcesses(ctx context.Context) ([]Proc, error) {
	ps, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, p := range ps {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		name, err := p.NameWithContext(ctx)
		if err != nil {
			continue
		}
		ppid, _ := p.PpidWithContext(ctx)
		pr := Proc{PID: int(p.Pid), PPID: int(ppid), Name: name}
		if !candidateName(name) {
			out = append(out, pr)
			continue
		}
		pr.Args, _ = p.CmdlineSliceWithContext(ctx)
		pr.Cwd, _ = p.CwdWithContext(ctx)
		if ms, err := p.CreateTimeWithContext(ctx); err == nil {
			pr.StartUnix = ms / 1000
		}
		out = append(out, pr)
	}
	return out, nil
}

// candidateName is whether a process name is worth asking for details: a harness binary or one
// of the runtimes a harness runs under.
func candidateName(name string) bool {
	return harnessOf(Proc{Name: name}) != "" || harnessRunner(name)
}
