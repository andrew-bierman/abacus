package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/agents"
)

// SessionsReport is the typed view of the Sessions domain: the live agent processes, what each
// is spending, the ports they hold and the account quota gauges. It embeds the agents snapshot,
// so every front-end renders one read.
type SessionsReport struct {
	Header
	agents.Snapshot
	// Quotas are account-level gauges, at most one per source. Ten live sessions on one
	// account repeat the same numbers, so the join dedupes them here.
	Quotas []agents.Quota `json:"quotas,omitempty"`
}

// normalize keeps the session list a JSON list rather than null, like every other domain.
func (r *SessionsReport) normalize() {
	if r.Sessions == nil {
		r.Sessions = []agents.Session{}
	}
}

// Summarize is the one-line summary shown in the overview.
func (r SessionsReport) Summarize() string {
	s := fmt.Sprintf("%d sessions, %d active", len(r.Sessions), r.Active())
	if n := len(r.Orphans); n > 0 {
		s += fmt.Sprintf(", %d orphaned ports", n)
	}
	if n := len(r.Unreadable); n > 0 {
		s += fmt.Sprintf(", %d unreadable", n)
	}
	return s
}

// Status is the Sessions domain as a module status: one row per live session.
func (r SessionsReport) Status() Status {
	st := Status{Summary: r.Summarize()}
	for _, x := range r.Sessions {
		state := string(x.State)
		if state == "" {
			state = string(agents.StateUnknown)
		}
		row := Row{Resource: sessionLabel(x), State: state, Tone: sessionTone(x.State), Detail: x.Model}
		if row.Detail == "" {
			row.Detail = fmt.Sprintf("%s pid %d", x.Harness, x.PID)
		}
		line := row.Resource + "  " + row.State
		if row.Detail != "" {
			line += "  " + row.Detail
		}
		st.Lines, st.Rows = append(st.Lines, line), append(st.Rows, row)
	}
	return st
}

// sessionLabel names a session by its project, which is what a fleet view is about. A session
// with no readable directory falls back to its harness and pid rather than an empty cell.
func sessionLabel(x agents.Session) string {
	if x.CWD != "" {
		return filepath.Base(x.CWD)
	}
	return fmt.Sprintf("%s:%d", x.Harness, x.PID)
}

// sessionTone is the severity every front-end draws: work in progress is healthy, an idle
// session is quiet, and an unreadable one needs a look.
func sessionTone(s agents.State) Tone {
	switch s {
	case agents.StateThinking, agents.StateExecuting:
		return ToneOK
	case agents.StateWaiting:
		return ToneMuted
	}
	return ToneWarn
}

// maxUnreadable bounds the reasons a partial read reports, so a machine where every store
// failed cannot grow the report with the session count.
const maxUnreadable = 8

// maxUnreadableReason bounds one reason; a path plus a kernel error is enough to act on.
const maxUnreadableReason = 200

// SessionsOptions are the process edges of the sessions join. A zero value reads this machine:
// the process table through gopsutil, commands through ExecRunner, and the stores under the
// user's home. Tests set every edge, so none of them reads this machine.
type SessionsOptions struct {
	Machine agents.Machine
	// Files ties a live Codex process to its open rollout. nil means agents.Native.
	Files  agents.Files
	Runner agents.Runner
	// Home is the user's home directory for default store locations. "" means os.UserHomeDir.
	Home string
	// Getenv resolves CLAUDE_CONFIG_DIR; nil means os.Getenv.
	Getenv func(string) string
	// Now is the clock for store windows; nil means time.Now.
	Now func() time.Time

	// Store locations. Empty means the default under Home, which tests override.
	OpenCodeDB string
	ClaudeRoot string
	PiRoot     string
	CodexRoot  string
	// ClaudeConfigDirs are the directories whose rate-limit hooks are read. Empty means
	// CLAUDE_CONFIG_DIR when set, then ~/.claude.
	ClaudeConfigDirs []string
}

// Sessions is the process-table and transcript join behind the Sessions domain. It is both a
// Module and a SessionProvider, so one instance serves every front-end read.
//
// The store readers keep cross-tick cursors, so Sessions serializes its reads. A front-end may
// overlap a tab refresh with a status read, and two passes over the same cursor would corrupt
// the totals; the read is a few hundred milliseconds, so waiting is cheap.
type Sessions struct {
	files      agents.Files
	codexRoot  string
	claudeDirs []string

	collector agents.Collector
	claude    agents.ClaudeStore
	codex     agents.CodexStore
	pi        agents.PiStore
	opencode  agents.OpenCodeStore
	git       agents.Git

	mu     sync.Mutex
	rotate int
}

// NewSessions builds the sessions provider. Every unset edge falls back to this machine, so a
// front-end constructs one with no options and a test constructs one with fakes.
func NewSessions(o SessionsOptions) *Sessions {
	if o.Machine == nil {
		o.Machine = agents.Native{}
	}
	if o.Files == nil {
		o.Files = agents.Native{}
	}
	if o.Runner == nil {
		o.Runner = ExecRunner{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.OpenCodeDB == "" {
		o.OpenCodeDB = agents.OpenCodeDBPath(o.Home)
	}
	if o.ClaudeRoot == "" {
		o.ClaudeRoot = agents.ClaudeProjectsPath(o.Home)
	}
	if o.PiRoot == "" {
		o.PiRoot = agents.PiSessionsPath(o.Home)
	}
	if o.CodexRoot == "" {
		o.CodexRoot = agents.CodexSessionsPath(o.Home)
	}
	// The kernel reports a process's open file by its resolved path, so a symlinked store root
	// would not prefix-match the rollout. Resolve it once; a missing root keeps its name.
	if real, err := filepath.EvalSymlinks(o.CodexRoot); err == nil {
		o.CodexRoot = real
	}
	dirs := o.ClaudeConfigDirs
	if len(dirs) == 0 {
		if d := o.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			dirs = append(dirs, d)
		}
		if o.Home != "" {
			dirs = append(dirs, filepath.Join(o.Home, ".claude"))
		}
	}
	return &Sessions{
		files:      o.Files,
		codexRoot:  o.CodexRoot,
		claudeDirs: dedupeDirs(dirs),
		collector:  agents.Collector{Machine: o.Machine, Now: o.Now},
		claude:     agents.ClaudeStore{Root: o.ClaudeRoot},
		codex:      agents.CodexStore{},
		pi:         agents.PiStore{Root: o.PiRoot},
		opencode:   agents.OpenCodeStore{Path: o.OpenCodeDB},
		git:        agents.Git{Runner: o.Runner, Now: o.Now},
	}
}

// dedupeDirs keeps one path per cleaned directory, so CLAUDE_CONFIG_DIR pointing at ~/.claude
// does not read the same hook file twice per tick.
func dedupeDirs(dirs []string) []string {
	seen := make(map[string]bool, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		key := filepath.Clean(d)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
}

// ID implements Module.
func (*Sessions) ID() string { return string(DomainSessions) }

// Name implements Module.
func (*Sessions) Name() string { return "Sessions" }

// Quiet keeps sessions out of the tool list: it is a read abacus does itself, not a tool the
// user installed or abacus ships.
func (*Sessions) Quiet() bool { return true }

// Detect implements Module. A machine with no readable process table has no sessions to show,
// so the domain reads not-connected with that reason instead of looking empty.
func (s *Sessions) Detect(ctx context.Context) Detection {
	if err := s.probe(ctx); err != nil {
		return Detection{State: Absent, Detail: Sanitize(err.Error())}
	}
	return Detection{State: Ready}
}

// Doctor implements Module; the process table is the one thing the read cannot work without.
func (s *Sessions) Doctor(ctx context.Context) []Check {
	if err := s.probe(ctx); err != nil {
		return []Check{{Name: "process table", Status: StatusWarn, Detail: Sanitize(err.Error()),
			Fix: "abacus reads sessions from the kernel's process table; this machine did not expose it"}}
	}
	return []Check{{Name: "process table", Status: StatusOK, Detail: "readable"}}
}

// probe asks a Machine that can answer cheaply, and only falls back to a full read for a fake
// or a platform without the narrow probe.
func (s *Sessions) probe(ctx context.Context) error {
	if p, ok := s.collector.Machine.(agents.Prober); ok {
		return p.Probe(ctx)
	}
	_, err := s.collector.Machine.Procs(ctx)
	return err
}

// Status implements Module; the domain read supplies the real one.
func (*Sessions) Status(context.Context) Status { return Status{} }

// Sessions reads every live agent process once and joins the store that belongs to each.
//
// The join never fails because one store could not be read: the live process table is the
// session list, and a missing transcript only means fewer filled-in fields. A machine that
// cannot list processes is the one error.
func (s *Sessions) Sessions(ctx context.Context) (SessionsReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.collector.Snapshot(ctx)
	if err != nil {
		return SessionsReport{}, err
	}
	rep := SessionsReport{Header: Header{Source: SourceLive}, Snapshot: snap}
	rep.normalize()

	open, openErr := s.openCodeByDir(ctx, rep.Sessions)
	if openErr != nil && !errors.Is(openErr, agents.ErrNoOpenCodeV2) {
		rep.unreadable(agents.HarnessOpenCode, openErr)
		for i := range rep.Sessions {
			if rep.Sessions[i].Harness == agents.HarnessOpenCode && rep.Sessions[i].State != agents.StateExecuting {
				rep.Sessions[i].State = agents.StateUnknown
			}
		}
	}
	quotas := map[agents.Harness]agents.Quota{}
	for _, i := range s.readOrder(len(rep.Sessions)) {
		sess := &rep.Sessions[i]
		stored, ok, err := s.storedFor(ctx, *sess, open)
		if err != nil {
			rep.unreadable(sess.Harness, err)
			if sess.State != agents.StateExecuting {
				sess.State = agents.StateUnknown
			}
		}
		if ok {
			mergeStored(sess, stored)
			if stored.Quota != nil {
				keepQuota(quotas, *stored.Quota)
			}
		}
		sess.Git = s.git.Status(ctx, sess.CWD)
	}
	rep.Quotas = s.quotaList(quotas)
	return rep, nil
}

// readOrder rotates where the store reads start. Every JSONL store shares one 8 MB budget per
// tick across the files it reads, so reading in a fixed order would let one large backlog take
// the whole budget while every later session advanced one line.
func (s *Sessions) readOrder(n int) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	if n < 2 {
		return order
	}
	k := s.rotate % n
	s.rotate = (s.rotate + 1) % n
	out := make([]int, 0, n)
	out = append(out, order[k:]...)
	return append(out, order[:k]...)
}

// storedFor returns the stored session behind one live process. Only opencode has a list; the
// JSONL harnesses resolve one transcript from the process's cwd and start time, and Codex
// finds the rollout it holds open. A read error is returned so the caller can report it and
// mark the session unknown; a store that simply has nothing for the session is not an error.
func (s *Sessions) storedFor(ctx context.Context, sess agents.Session, open map[string]agents.StoredSession) (agents.StoredSession, bool, error) {
	switch sess.Harness {
	case agents.HarnessOpenCode:
		st, ok := open[cleanDir(sess.CWD)]
		return st, ok, nil
	case agents.HarnessClaude:
		return s.transcriptFor(&s.claude, sess)
	case agents.HarnessPi:
		return s.transcriptFor(&s.pi, sess)
	case agents.HarnessCodex:
		path, ok := s.rolloutFor(ctx, sess.PID)
		if !ok {
			return agents.StoredSession{}, false, nil
		}
		st, err := s.codex.Rollout(path)
		if err != nil {
			return agents.StoredSession{}, false, err
		}
		return st, true, nil
	}
	return agents.StoredSession{}, false, nil
}

// transcriptStore is the pair of reads the Claude and pi readers share: resolve the file a
// session in a directory is writing, then parse it.
type transcriptStore interface {
	PathFor(cwd string, since time.Time) (string, bool)
	Transcript(path string) (agents.StoredSession, error)
}

// transcriptFor runs that pair and rejects a file whose recorded directory is another project.
func (s *Sessions) transcriptFor(store transcriptStore, sess agents.Session) (agents.StoredSession, bool, error) {
	path, ok := store.PathFor(sess.CWD, sess.Started)
	if !ok {
		return agents.StoredSession{}, false, nil
	}
	st, err := store.Transcript(path)
	if err != nil {
		return agents.StoredSession{}, false, err
	}
	if !sameSessionDir(st.Dir, sess.CWD) {
		return agents.StoredSession{}, false, nil
	}
	return st, true, nil
}

// sameSessionDir reports whether a store's session describes the live directory. Claude's and
// pi's PathFor fall back to the newest transcript anywhere because a worktree keeps its
// transcript under a branch-keyed directory, so a heuristic hit can belong to another project.
// A recorded directory that disagrees is not this process's transcript; an empty one is left to
// the reader's heuristic rather than rejected.
func sameSessionDir(stored, live string) bool {
	if stored == "" || live == "" {
		return true
	}
	return filepath.Clean(stored) == filepath.Clean(live)
}

// mergeStored fills a live session from its store. The CPU delta still wins for executing: a
// transcript can be ahead of an I/O-bound tool, and a finished child keeps its CPU level.
func mergeStored(sess *agents.Session, st agents.StoredSession) {
	if st.Title != "" {
		sess.Title = st.Title
	}
	if st.Model != "" {
		sess.Model = st.Model
	}
	sess.Tokens = st.Tokens
	sess.Context = st.Context
	if !st.Updated.IsZero() {
		sess.LastActive = st.Updated
	}
	if sess.State != agents.StateExecuting && st.State != "" {
		sess.State = st.State
	}
}

// openCodeByDir asks opencode's database for the sessions live processes could be in. The
// store is the session list, so one query covers the window; the newest session per directory
// is the one a process there is running.
func (s *Sessions) openCodeByDir(ctx context.Context, live []agents.Session) (map[string]agents.StoredSession, error) {
	var since time.Time
	any := false
	for _, x := range live {
		if x.Harness != agents.HarnessOpenCode {
			continue
		}
		any = true
		if since.IsZero() || (!x.Started.IsZero() && x.Started.Before(since)) {
			since = x.Started
		}
	}
	if !any {
		return nil, nil
	}
	if since.IsZero() {
		since = s.collector.Now().Add(-24 * time.Hour)
	}
	list, err := s.opencode.Sessions(ctx, since, 200)
	if err != nil {
		return nil, err
	}
	by := make(map[string]agents.StoredSession, len(list))
	for _, st := range list {
		key := cleanDir(st.Dir)
		if key == "" {
			continue
		}
		if prev, ok := by[key]; !ok || st.Updated.After(prev.Updated) {
			by[key] = st
		}
	}
	return by, nil
}

// rolloutFor finds the rollout a live Codex process holds open. Codex keeps no list, and
// sweeping its session tree every tick is what the cost model forbids. A resumed session can
// hold several rollouts open, so the one written most recently is the current one.
func (s *Sessions) rolloutFor(ctx context.Context, pid int) (string, bool) {
	if s.codexRoot == "" {
		return "", false
	}
	files, err := s.files.OpenFiles(ctx, pid)
	if err != nil {
		return "", false
	}
	var best string
	var bestAt time.Time
	for _, f := range files {
		if !strings.HasSuffix(f, ".jsonl") || !within(s.codexRoot, f) {
			continue
		}
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestAt) {
			best, bestAt = f, info.ModTime()
		}
	}
	return best, best != ""
}

// within reports whether path is inside root. The separator keeps a sibling directory with the
// same prefix from matching.
func within(root, path string) bool {
	root, path = filepath.Clean(root), filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// cleanDir is a comparison key for a session directory; an empty directory has none.
func cleanDir(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}

// keepQuota stores one account gauge per source, keeping the freshest when two sessions on
// the same account both report it.
func keepQuota(m map[agents.Harness]agents.Quota, q agents.Quota) {
	if q.Source == "" {
		return
	}
	if prev, ok := m[q.Source]; ok && !q.Updated.After(prev.Updated) {
		return
	}
	m[q.Source] = q
}

// quotaList adds Claude's hook files and returns the gauges in source order. The readers read
// no environment, so the join resolves each config directory and hands it over.
func (s *Sessions) quotaList(seen map[agents.Harness]agents.Quota) []agents.Quota {
	for _, root := range s.claudeDirs {
		if root == "" {
			continue
		}
		if q, ok := agents.ClaudeQuota(root); ok {
			keepQuota(seen, q)
		}
	}
	out := make([]agents.Quota, 0, len(seen))
	for _, q := range seen {
		out = append(out, q)
	}
	slices.SortFunc(out, func(a, b agents.Quota) int { return strings.Compare(string(a.Source), string(b.Source)) })
	return out
}

// unreadable records why part of the read is missing, bounded so a broken machine cannot grow
// the report with the session count.
func (r *SessionsReport) unreadable(source agents.Harness, err error) {
	if len(r.Unreadable) >= maxUnreadable {
		return
	}
	r.Unreadable = append(r.Unreadable, ClipRunes(Sanitize(string(source)+": "+err.Error()), maxUnreadableReason))
}
