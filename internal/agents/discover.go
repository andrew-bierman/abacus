package agents

import (
	"context"
	"sort"
	"strings"
	"time"
)

// maxChildren bounds how many descendants one session may report, so a runaway process tree
// cannot make a single read unbounded.
const maxChildren = 64

// harnessBinaries maps the executable an agent runs as to its harness. A process whose
// basename is not here is not a session, which keeps every other node/python/go process out
// without parsing arguments.
var harnessBinaries = map[string]Harness{
	"claude":       HarnessClaude,
	"claude-code":  HarnessClaude,
	"codex":        HarnessCodex,
	"opencode":     HarnessOpenCode,
	"opencode-cli": HarnessOpenCode,
	"pi":           HarnessPi,
}

// HarnessFor names the harness a process is, if it is one.
//
// A trailing ".exe" is stripped because the same agent ships that name on several platforms
// and package layouts: on this machine opencode runs as "opencode.exe" out of its node
// package, and Claude is shipped as "claude.exe" in some installs. The suffix carries no
// meaning about which product it is.
func HarnessFor(p Proc) (Harness, bool) {
	name := strings.TrimSuffix(p.Name, ".exe")
	h, ok := harnessBinaries[name]
	return h, ok
}

// portOwner remembers which session held a port on the previous read, so a port that
// outlives its session can be reported instead of silently disappearing.
type portOwner struct {
	sessionPID int
	pid        int
	project    string
}

// Collector takes snapshots. It keeps the previous read because two facts cannot be known
// from one: whether a session is executing (its children's CPU advanced) and whether a port
// was orphaned (it was attributed to a session that has since ended). A zero Collector is
// usable; the first snapshot simply reports nothing as executing or orphaned.
//
// A Collector is not safe for concurrent use; keep it on the goroutine that owns the tab.
type Collector struct {
	Machine Machine
	// Now is injectable so a test can pin time; nil means time.Now.
	Now func() time.Time

	prevCPU   map[int]float64
	prevPorts map[int]portOwner
	started   bool
}

// Snapshot reads the machine once. It never fails because one process or socket vanished
// mid-read: a monitor that did would flicker on every tick.
func (c *Collector) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	snap := Snapshot{Taken: now()}

	machine := c.Machine
	if machine == nil {
		machine = Native{}
	}
	procs, err := machine.Procs(ctx)
	if err != nil {
		return snap, err
	}
	listeners, err := machine.Listeners(ctx)
	if err != nil {
		return snap, err
	}

	listenerPID := make(map[int]int, len(listeners))
	portsByPID := make(map[int][]Port, len(listeners))
	for _, s := range listeners {
		listenerPID[s.Port] = s.PID
		if s.PID == 0 {
			continue
		}
		portsByPID[s.PID] = append(portsByPID[s.PID], Port{
			Port:    s.Port,
			PID:     s.PID,
			Command: commandOf(procs, s.PID),
		})
	}

	var (
		cpu      = make(map[int]float64)
		owners   = make(map[int]portOwner)
		sessions []Session
	)

	for pid, p := range procs {
		harness, ok := HarnessFor(p)
		if !ok {
			continue
		}
		cwd := machine.CWD(ctx, pid)
		s := Session{
			Harness:    harness,
			PID:        pid,
			CWD:        cwd,
			Title:      baseName(cwd),
			State:      StateWaiting,
			LastActive: snap.Taken,
		}
		var treeCPU float64
		for _, d := range Descendants(procs, pid, maxChildren) {
			cp, ok := procs[d]
			if !ok {
				continue
			}
			treeCPU += cp.CPUTime
			s.Children = append(s.Children, Child{PID: d, Command: commandLine(cp), RSSKB: cp.RSSKB})
			s.Ports = append(s.Ports, portsByPID[d]...)
		}
		s.Ports = append(s.Ports, portsByPID[pid]...)
		for _, prt := range s.Ports {
			owners[prt.Port] = portOwner{sessionPID: pid, pid: prt.PID, project: cwd}
		}
		cpu[pid] = treeCPU

		// Executing is a delta, not a level: a finished child keeps its accumulated CPU.
		if last, seen := c.prevCPU[pid]; seen && treeCPU > last {
			s.State = StateExecuting
		}
		sort.Slice(s.Children, func(i, j int) bool { return s.Children[i].PID < s.Children[j].PID })
		sort.Slice(s.Ports, func(i, j int) bool { return s.Ports[i].Port < s.Ports[j].Port })
		sessions = append(sessions, s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].State.Active() != sessions[j].State.Active() {
			return sessions[i].State.Active()
		}
		return sessions[i].PID < sessions[j].PID
	})
	snap.Sessions = sessions

	// An orphan is a port the previous read attributed to a session whose process has since
	// ended, while the port is still listening. It takes two reads, so the first snapshot of
	// a process never claims one.
	if c.started {
		for port, prev := range c.prevPorts {
			if _, stillListening := listenerPID[port]; !stillListening {
				continue
			}
			if _, sessionAlive := procs[prev.sessionPID]; sessionAlive {
				continue
			}
			pid := prev.pid
			if live, ok := listenerPID[port]; ok && live != 0 {
				pid = live
			}
			cmd := commandOf(procs, pid)
			snap.Orphans = append(snap.Orphans, Port{
				Port:    port,
				PID:     pid,
				Command: cmd,
				Project: baseName(prev.project),
				Orphan:  true,
			})
		}
		sort.Slice(snap.Orphans, func(i, j int) bool { return snap.Orphans[i].Port < snap.Orphans[j].Port })
	}

	c.prevCPU, c.prevPorts, c.started = cpu, owners, true
	return snap, nil
}

func commandOf(procs map[int]Proc, pid int) string {
	if p, ok := procs[pid]; ok {
		return commandLine(p)
	}
	return ""
}
