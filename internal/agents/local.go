package agents

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Pane is one terminal pane of a multiplexer: the pid of the process the pane started and the
// address to reach it.
type Pane struct {
	PID    int
	ID     string // "%5"
	Target string // "work:2.1"
}

// Options carry every outside source of the native feed, so tests supply their own and never
// read the real process list, session files or terminals.
type Options struct {
	FS    fs.FS // rooted at "/" (os.DirFS("/") in a real run); nil means no session files
	Home  string
	Procs func(context.Context) ([]Proc, error)
	Panes func(context.Context) ([]Pane, error)
	Now   func() time.Time
	// RecentWindow is how long a finished session is still listed (as stopped).
	RecentWindow time.Duration
	// AllUsers includes agents run by other users; by default only the current user's are listed.
	AllUsers   bool
	CurrentUID func() int
}

// Local is the native feed: processes, session files and terminal panes, all read-only.
type Local struct {
	o    Options
	mu   sync.Mutex
	last scan // what the last read saw, for Doctor
}

type scan struct {
	procs, agents, sessionFiles int
	procErr                     string
	dirs                        []string
}

// The real sources, as variables so this package's tests can make reaching for them fail loudly.
var (
	realProcs = scanProcesses
	realPanes = tmuxPanes
)

// NewLocal returns the native feed. Zero fields of o use the real machine.
func NewLocal(o Options) *Local {
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.FS == nil && o.Home != "" {
		o.FS = defaultFS()
	}
	if o.Procs == nil {
		o.Procs = realProcs
	}
	if o.Panes == nil {
		o.Panes = realPanes
	}
	if o.CurrentUID == nil {
		o.CurrentUID = os.Getuid
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.RecentWindow <= 0 {
		o.RecentWindow = 15 * time.Minute
	}
	return &Local{o: o}
}

func (l *Local) ID() string   { return "agents" }
func (l *Local) Name() string { return "agents" }

// Detect: the native feed needs nothing installed.
func (l *Local) Detect(context.Context) core.Detection { return core.Detection{State: core.Ready} }

func (l *Local) Status(context.Context) core.Status {
	l.mu.Lock()
	n := l.last.agents
	l.mu.Unlock()
	return core.Status{Summary: fmt.Sprintf("%d agent processes", n)}
}

func (l *Local) Doctor(context.Context) []core.Check {
	l.mu.Lock()
	s := l.last
	l.mu.Unlock()
	procs := core.Check{Name: "agent processes", Status: core.StatusOK, Detail: fmt.Sprintf("%d processes read, %d agents", s.procs, s.agents)}
	if s.procErr != "" {
		procs = core.Check{Name: "agent processes", Status: core.StatusWarn, Detail: "the process list could not be read: " + s.procErr}
	}
	files := core.Check{Name: "agent session files", Status: core.StatusOK, Detail: fmt.Sprintf("%d read (%s)", s.sessionFiles, strings.Join(s.dirs, ", "))}
	if len(s.dirs) == 0 {
		files = core.Check{Name: "agent session files", Status: core.StatusSkip, Detail: "no Claude Code or Codex session folder found"}
	}
	return []core.Check{procs, files}
}

func rel(p string) string { return strings.TrimPrefix(path.Clean(p), "/") }

func (l *Local) claudeProjects() string { return rel(path.Join(l.o.Home, ".claude", "projects")) }
func (l *Local) codexSessions() string  { return rel(path.Join(l.o.Home, ".codex", "sessions")) }
func iso(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Agents reads the process list, then the session files of the agents it found (and of sessions
// that ended a moment ago). A failed process read still returns the session files, with the error
// so the report can say the process list was missing.
func (l *Local) Agents(ctx context.Context) ([]core.Agent, core.AgentsExtra, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	now := l.o.Now()
	sc := &scan{}
	defer func() { l.mu.Lock(); l.last = *sc; l.mu.Unlock() }()
	procs, perr := l.o.Procs(ctx)
	if perr != nil {
		sc.procErr = core.CleanAgentText(perr.Error(), 120)
	}
	sc.procs = len(procs)
	aps := harnessProcs(procs)
	if !l.o.AllUsers {
		me, kept := l.o.CurrentUID(), aps[:0]
		for _, p := range aps {
			if !p.UIDKnown || me < 0 || p.UID == me {
				kept = append(kept, p)
			}
		}
		aps = kept
	}
	sc.agents = len(aps)
	panes, _ := l.o.Panes(ctx) // no multiplexer is normal
	byPID := map[int]Proc{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	bud := &budget{left: scanBudget}
	claude, codex := l.claudeSessions(ctx, sc, aps, bud, now), l.codexSessionsFor(ctx, sc, aps, bud, now)
	used := map[string]bool{}
	var out []core.Agent
	// newest process first, so the newest process gets the newest session of its directory
	sort.SliceStable(aps, func(i, j int) bool { return aps[i].StartUnix > aps[j].StartUnix })
	for _, p := range aps {
		a := core.Agent{Harness: p.Harness, PID: p.PID, Cwd: p.Cwd, State: core.AgentUnknown, Source: core.EvidenceProcess}
		pool := claude
		if p.Harness == core.HarnessCodex {
			pool = codex
		}
		if p.Harness == core.HarnessClaude || p.Harness == core.HarnessCodex {
			var s session
			var ok bool
			if id := resumeID(p.Args); id != "" && p.Harness == core.HarnessClaude {
				s, ok = takeSession(pool["resume:"+id], used, time.Time{})
			}
			if !ok {
				s, ok = takeSession(pool[p.Cwd], used, time.Unix(p.StartUnix, 0).Add(-10*time.Second))
			}
			if ok {
				a = withSession(a, s, now)
			}
		}
		a.Term = termOf(p.PID, byPID, panes)
		l.fillGit(&a)
		out = append(out, a)
	}
	// sessions that ended a moment ago, or whose process could not be matched
	for _, h := range []struct {
		pool map[string][]session
		id   string
	}{{claude, core.HarnessClaude}, {codex, core.HarnessCodex}} {
		for _, ss := range h.pool {
			for _, s := range ss {
				if used[s.ID] || now.Sub(s.ModTime) > l.o.RecentWindow {
					continue
				}
				a := withSession(core.Agent{Harness: h.id, Cwd: s.Cwd}, s, now)
				if perr == nil { // the process list was read and has no such process
					a.State = core.AgentStopped
				}
				l.fillGit(&a)
				out = append(out, a)
			}
		}
	}
	for i := range out {
		out[i].ID = core.AgentID(out[i])
	}
	if perr != nil {
		perr = fmt.Errorf("the process list could not be read: %w", perr)
	}
	return out, core.AgentsExtra{}, perr
}

func takeSession(ss []session, used map[string]bool, notBefore time.Time) (session, bool) {
	for _, s := range ss {
		if !used[s.ID] && !s.ModTime.Before(notBefore) {
			used[s.ID] = true
			return s, true
		}
	}
	return session{}, false
}

func withSession(a core.Agent, s session, now time.Time) core.Agent {
	a.SessionID, a.Model, a.Task, a.Tokens = s.ID, s.Model, s.Task, s.Tokens
	a.State, a.Source, a.LastActivity = inferState(s, now), core.EvidenceTranscript, iso(s.ModTime)
	a.Branch = s.Branch
	if a.Cwd == "" {
		a.Cwd = s.Cwd
	}
	return a
}

// fillGit sets the repository, branch and worktree from the directory's own git files.
func (l *Local) fillGit(a *core.Agent) {
	if a.Cwd == "" || l.o.FS == nil {
		return
	}
	repo, branch, wt := gitInfo(l.o.FS, a.Cwd)
	a.Repo, a.Worktree = repo, wt
	if branch != "" {
		a.Branch = branch
	}
}

// Enrich fills Repo, Branch and Worktree of agents another feed found, from the same files.
func (l *Local) Enrich(list []core.Agent) {
	for i := range list {
		if list[i].Repo == "" {
			l.fillGit(&list[i])
		}
	}
}

// claudeSessions reads, for each directory a Claude Code process runs in, the newest session
// files of that directory's project folder.
func (l *Local) claudeSessions(ctx context.Context, sc *scan, aps []agentProc, b *budget, now time.Time) map[string][]session {
	out := map[string][]session{}
	if l.o.FS == nil {
		return out
	}
	root := l.claudeProjects()
	if _, err := fs.Stat(l.o.FS, root); err == nil {
		sc.dirs = append(sc.dirs, "Claude Code")
	}
	read := func(f fileRef) (session, bool) {
		if sc.sessionFiles >= maxFilesParsed || ctx.Err() != nil {
			return session{}, false
		}
		sc.sessionFiles++
		return readClaude(l.o.FS, f, b)
	}
	for _, p := range aps {
		if p.Harness != core.HarnessClaude {
			continue
		}
		if id := resumeID(p.Args); id != "" && out["resume:"+id] == nil { // a resumed session may not have written since the process started
			if s, ok := read(fileRef{name: path.Join(root, encodeClaudeDir(p.Cwd), id+".jsonl")}); ok {
				s.Cwd = p.Cwd
				out["resume:"+id] = []session{s}
			}
		}
		if out[p.Cwd] != nil {
			continue
		}
		for _, f := range newest(l.o.FS, path.Join(root, encodeClaudeDir(p.Cwd)), 8) {
			if s, ok := read(f); ok && (s.Cwd == "" || s.Cwd == p.Cwd) {
				s.Cwd = p.Cwd
				out[p.Cwd] = append(out[p.Cwd], s)
			}
		}
		if out[p.Cwd] == nil {
			out[p.Cwd] = []session{}
		}
	}
	// recently finished sessions anywhere
	ents, _ := fs.ReadDir(l.o.FS, root)
	var recent []fileRef
	for i, e := range ents {
		if i >= maxListed || !e.IsDir() {
			continue
		}
		for _, f := range newest(l.o.FS, path.Join(root, e.Name()), 3) {
			if now.Sub(f.mod) <= l.o.RecentWindow {
				recent = append(recent, f)
			}
		}
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].mod.After(recent[j].mod) })
	for _, f := range recent {
		if s, ok := read(f); ok && s.Cwd != "" {
			if !hasSession(out[s.Cwd], s.ID) {
				out[s.Cwd] = append(out[s.Cwd], s)
			}
		}
	}
	for _, ss := range out {
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].ModTime.After(ss[j].ModTime) })
	}
	return out
}

func hasSession(l []session, id string) bool {
	for _, s := range l {
		if s.ID == id {
			return true
		}
	}
	return false
}

// codexSessionsFor reads Codex session files from the last three days' folders.
func (l *Local) codexSessionsFor(ctx context.Context, sc *scan, aps []agentProc, b *budget, now time.Time) map[string][]session {
	out := map[string][]session{}
	if l.o.FS == nil {
		return out
	}
	root := l.codexSessions()
	if _, err := fs.Stat(l.o.FS, root); err != nil {
		return out
	}
	sc.dirs = append(sc.dirs, "Codex")
	var files []fileRef
	for d := 0; d < 3; d++ {
		day := now.AddDate(0, 0, -d).Local()
		files = append(files, newest(l.o.FS, path.Join(root, day.Format("2006"), day.Format("01"), day.Format("02")), 40)...)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files {
		if sc.sessionFiles >= maxFilesParsed || ctx.Err() != nil {
			break
		}
		sc.sessionFiles++
		if s, ok := readCodex(l.o.FS, f, b); ok {
			out[s.Cwd] = append(out[s.Cwd], s)
		}
	}
	for _, ss := range out {
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].ModTime.After(ss[j].ModTime) })
	}
	return out
}

// resumeID is the session id of `claude --resume ID` or `-r ID` ("" when the process is not one).
func resumeID(args []string) string {
	for i, a := range args {
		if (a == "--resume" || a == "-r" || a == "--session-id") && i+1 < len(args) && sessionIDRE.MatchString(args[i+1]) {
			return args[i+1]
		}
	}
	return ""
}

var sessionIDRE = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

// termOf finds the pane a process runs in: the pane's own process is the process or one of its
// ancestors.
func termOf(pid int, procs map[int]Proc, panes []Pane) *core.TermRef {
	if len(panes) == 0 {
		return nil
	}
	by := map[int]Pane{}
	for _, p := range panes {
		by[p.PID] = p
	}
	for cur, i := pid, 0; cur > 1 && i < 16; i++ {
		if p, ok := by[cur]; ok {
			return &core.TermRef{Kind: core.TermTmux, Ref: p.Target, Command: "tmux switch-client -t " + p.ID}
		}
		cur = procs[cur].PPID
	}
	return nil
}
