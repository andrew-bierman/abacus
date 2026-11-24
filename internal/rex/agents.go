package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The Agents sweep. It reads, per host, the session list (the existing `ls --json`), then each
// session's panes, then for each terminal pane the status records its programs reported
// (`program_status`) and the foreground process (`process`). All three new calls are in the argv
// allowlist and nothing else is run. The sweep has its own bounds because it makes roughly two
// calls per pane (about 35 ms each, measured): concurrency, a hard budget, one sweep in flight, and
// a per-host breaker so a dead host costs a few calls and not one per pane.
const (
	// terminalFlavor is the only block flavor that is a terminal; the others (tunnels, onboarding)
	// are never asked about further.
	terminalFlavor = "com.superlogical.terminal.shell"

	agentConcurrency    = 8
	agentBudget         = 4 * time.Second
	maxBlocksPerSession = 64
	maxPanesPerHost     = 256
	maxStatusRecords    = 256 // what the protocol keeps per pane
	// processTTL is how long a pane's foreground process is reused: it changes rarely, while the
	// status records are the live part and are read every sweep.
	processTTL   = 12 * time.Second
	maxProcCache = 2048
	// breakerAfter is how many hard failures (a timeout or a call that could not run, not a pane that
	// is gone) on one host make the sweep stop asking it about panes.
	breakerAfter = 3
	maxGitWalk   = 12
	maxGitFile   = 4096
)

// knownAgentProcesses name programs that are agents even when they report no status. It is data:
// extending it is a one-line change. The real process names of these tools are UNTESTED against a
// live Rex (a status record's app, when present, is the better name).
var knownAgentProcesses = map[string]bool{"claude": true, "codex": true, "gemini": true, "aider": true, "opencode": true,
	"cursor-agent": true, "amp": true, "goose": true, "crush": true, "qwen": true, "pi": true}

// errNotAgents is a pane's status or process output in a shape this build does not know. It is an
// error for that pane, never "no agents": a changed schema must not look like an empty fleet.
var errNotAgents = errors.New("unrecognized rex block output")

type block struct{ ID, Label string }

// parseBlocks reads `block ls -s S --json`: {"blocks": null | [{"block_id","label","flavor",...}]}.
// Only terminal blocks are kept, at most maxBlocksPerSession (truncated says there were more).
// Unknown fields are ignored; a missing or mistyped "blocks", or a block with no id, is an error.
func parseBlocks(out []byte) (bs []block, truncated bool, err error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(out, &top) != nil || top == nil {
		return nil, false, errNotAgents
	}
	raw, ok := top["blocks"]
	if !ok {
		return nil, false, errNotAgents
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil, false, errNotAgents
	}
	for _, r := range list {
		var b struct {
			ID     string `json:"block_id"`
			Label  string `json:"label"`
			Flavor string `json:"flavor"`
		}
		if json.Unmarshal(r, &b) != nil || b.ID == "" {
			return nil, false, errNotAgents
		}
		if b.Flavor != terminalFlavor {
			continue
		}
		if len(bs) >= maxBlocksPerSession {
			return bs, true, nil
		}
		bs = append(bs, block{ID: b.ID, Label: b.Label})
	}
	return bs, false, nil
}

// parseStatus reads `block call program_status`: {"records": null | [{id?, state, kind?, app?,
// effective_app?, title?, msg?, progress?, updated_at, owner?}]}. State and kind are taken as text
// (core maps what it does not know); a time that does not parse is left zero; at most
// maxStatusRecords are read. The text is whatever the program printed and stays unsanitized until
// core cleans it.
func parseStatus(out []byte) ([]core.AgentRecord, error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(out, &top) != nil || top == nil {
		return nil, errNotAgents
	}
	raw, ok := top["records"]
	if !ok {
		return nil, errNotAgents
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil, errNotAgents
	}
	var rs []core.AgentRecord
	for _, r := range list {
		if len(rs) >= maxStatusRecords {
			break
		}
		var x struct {
			ID           string   `json:"id"`
			State        string   `json:"state"`
			Kind         string   `json:"kind"`
			App          string   `json:"app"`
			EffectiveApp string   `json:"effective_app"`
			Title        string   `json:"title"`
			Msg          string   `json:"msg"`
			Progress     *float64 `json:"progress"`
			UpdatedAt    string   `json:"updated_at"`
		}
		if json.Unmarshal(r, &x) != nil {
			return nil, errNotAgents
		}
		rec := core.AgentRecord{ID: x.ID, State: core.AgentState(x.State), Kind: core.AgentKind(x.Kind), App: x.EffectiveApp,
			Title: x.Title, Msg: x.Msg, Progress: -1}
		if rec.App == "" {
			rec.App = x.App
		}
		if x.Progress != nil && *x.Progress >= 0 && *x.Progress <= 100 {
			rec.Progress = int(*x.Progress)
		}
		if t, err := time.Parse(time.RFC3339Nano, x.UpdatedAt); err == nil {
			rec.UpdatedAt = t
		}
		rs = append(rs, rec)
	}
	return rs, nil
}

// proc is what the Agents view needs of `block call process`: the foreground program's name and
// directory. Rex never returns an argv beyond argv0 (it can hold secrets), and neither is wanted.
type proc struct{ Name, Cwd string }

// parseProcess reads `block call process`: the foreground process (the child when there is none).
// The name is the first of script, argv0 and name that is present (Rex's own preference), reduced
// to its last path element.
func parseProcess(out []byte) (proc, error) {
	var top struct {
		Foreground *procInfo `json:"foreground"`
		Child      *procInfo `json:"child"`
	}
	if json.Unmarshal(out, &top) != nil {
		return proc{}, errNotAgents
	}
	p := top.Foreground
	if p == nil {
		p = top.Child
	}
	if p == nil {
		return proc{}, errNotAgents
	}
	for _, n := range []string{p.Script, p.Argv0, p.Name} {
		if n != "" {
			return proc{Name: path.Base(n), Cwd: p.Cwd}, nil
		}
	}
	return proc{Cwd: p.Cwd}, nil
}

type procInfo struct {
	Name   string `json:"name"`
	Argv0  string `json:"argv0"`
	Script string `json:"script"`
	Cwd    string `json:"cwd"`
}

// flight is one sweep in progress; callers that arrive meanwhile wait for its answer instead of
// starting another, so a slow Rex is never asked twice at once.
type flight struct {
	done chan struct{}
	rep  core.AgentReport
	err  error
}

type procEntry struct {
	at time.Time
	p  proc
}

// hostSweep collects one host's panes and its health for the breaker.
type hostSweep struct {
	host      string
	fails     atomic.Int32
	mu        sync.Mutex
	panes     []core.AgentPane
	terminals int
	truncated bool
	drift     int
	replay    bool
	err       error // the host's session list failed
	tripped   bool
}

// noteDrift counts a pane whose output was not in a known shape.
func (h *hostSweep) noteDrift() {
	h.mu.Lock()
	h.drift++
	h.mu.Unlock()
}

// noteTruncated records that something was cut.
func (h *hostSweep) noteTruncated() {
	h.mu.Lock()
	h.truncated = true
	h.mu.Unlock()
}

func (h *hostSweep) add(p core.AgentPane, terminal bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if terminal {
		h.terminals++
	}
	if p.Session == "" {
		return
	}
	if len(h.panes) >= maxPanesPerHost {
		h.truncated = true
		return
	}
	h.panes = append(h.panes, p)
}

// Agents reads what the panes of this machine and of the configured hosts are doing. It matches
// each pane against the given items with the workspace matcher (the session label, the pane's
// label, the status title and message, and the working directory's last element), never picking
// between two items. Everything it runs is on the argv allowlist; a host that does not answer is
// skipped after a few failures. One sweep runs at a time.
func (m *Module) Agents(ctx context.Context, items []core.WorkspaceItem) (core.AgentReport, error) {
	m.mu.Lock()
	if f := m.flight; f != nil {
		m.mu.Unlock()
		select {
		case <-f.done:
			return f.rep, f.err
		case <-ctx.Done():
			return core.AgentReport{}, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	m.flight = f
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.flight = nil
		m.mu.Unlock()
		close(f.done)
	}()
	ctx, cancel := context.WithTimeout(ctx, agentBudget)
	defer cancel()
	f.rep, f.err = m.sweep(ctx, items)
	return f.rep, f.err
}

func (m *Module) sweep(ctx context.Context, items []core.WorkspaceItem) (core.AgentReport, error) {
	hosts := []string{""}
	for _, h := range m.o.Hosts {
		if !strings.EqualFold(h, m.o.Hostname()) {
			hosts = append(hosts, h)
		}
	}
	sweeps := make([]*hostSweep, len(hosts))
	sem := make(chan struct{}, agentConcurrency)
	var wg sync.WaitGroup
	for i, h := range hosts {
		sweeps[i] = &hostSweep{host: h}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.sweepHost(ctx, sweeps[i], items, sem)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return core.AgentReport{}, err
	}
	rep := core.AgentReport{State: core.NotConnected, Reason: noWorkspace, HowToConnect: HowToConnect}
	var problems []string
	for _, h := range sweeps {
		switch {
		case errors.Is(h.err, errNoServer):
			continue
		case h.err != nil:
			problems = append(problems, hostName(h.host)+": "+h.err.Error())
			continue
		}
		rep.State, rep.Reason, rep.HowToConnect = core.Connected, "", ""
		rep.Stale = rep.Stale || h.replay
		rep.Truncated = rep.Truncated || h.truncated
		rep.Terminals += h.terminals
		rep.Panes = append(rep.Panes, h.panes...)
		if h.tripped {
			problems = append(problems, hostName(h.host)+" stopped answering, so some panes were not read")
		}
		if h.drift > 0 {
			problems = append(problems, fmt.Sprintf("%s: %d panes printed output this version does not recognize", hostName(h.host), h.drift))
		}
	}
	if len(problems) > 0 {
		rep.State, rep.HowToConnect = core.Degraded, ""
		rep.Reason = safe(strings.Join(problems, "; "), 300)
	}
	return rep, nil
}

func hostName(h string) string {
	if h == "" {
		return "this machine"
	}
	return h
}

// sweepHost reads one host. The session list is the existing cached `ls --json`; if it fails, the
// host's panes are not asked about at all (the breaker's first gate).
func (m *Module) sweepHost(ctx context.Context, h *hostSweep, items []core.WorkspaceItem, sem chan struct{}) {
	res, replay := m.read(ctx, h.host)
	h.replay = replay
	if res.err != nil {
		h.err = res.err
		return
	}
	h.truncated = res.truncated
	var wg sync.WaitGroup
	for _, s := range res.ss {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.sweepSession(ctx, h, s, items, sem)
		}()
	}
	wg.Wait()
	h.tripped = h.fails.Load() >= breakerAfter
}

func (m *Module) sweepSession(ctx context.Context, h *hostSweep, s session, items []core.WorkspaceItem, sem chan struct{}) {
	out, ok := m.call(ctx, h, sem, blockLsArgv(h.host, s.ID))
	if !ok {
		return
	}
	bs, trunc, err := parseBlocks(out)
	if err != nil {
		h.noteDrift()
		return
	}
	if trunc {
		h.noteTruncated()
	}
	var wg sync.WaitGroup
	for _, b := range bs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.sweepBlock(ctx, h, s, b, items, sem)
		}()
	}
	wg.Wait()
}

func (m *Module) sweepBlock(ctx context.Context, h *hostSweep, s session, b block, items []core.WorkspaceItem, sem chan struct{}) {
	var recs []core.AgentRecord
	var statusErr, statusOK bool
	var pr proc
	var procOK bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		out, ok := m.call(ctx, h, sem, blockCallArgv(h.host, s.ID, b.ID, methodProgramStatus))
		if !ok {
			return
		}
		r, err := parseStatus(out)
		recs, statusErr, statusOK = r, err != nil, err == nil
	}()
	go func() {
		defer wg.Done()
		pr, procOK = m.process(ctx, h, sem, s.ID, b.ID)
	}()
	wg.Wait()
	switch {
	case statusErr:
		h.noteDrift()
		return
	case !statusOK && !procOK:
		return // the pane is gone or does not answer: nothing to say, and nothing to claim
	}
	pane := core.AgentPane{Host: h.host, Session: s.ID, SessionLabel: s.Label, Block: b.ID, Process: pr.Name, Cwd: pr.Cwd, State: core.AgentIdle}
	texts := []string{s.text, core.Sanitize(b.Label)}
	var agent string
	if top, n, ok := core.CollapseRecords(recs); ok {
		pane.State, pane.Kind, pane.Msg, pane.Title, pane.RecordID, pane.NRecords, pane.UpdatedAt, pane.Reported =
			top.State, top.Kind, top.Msg, top.Title, top.ID, n, top.UpdatedAt, true
		if top.Progress >= 0 {
			p := top.Progress
			pane.Progress = &p
		}
		agent = top.App
		texts = append(texts, core.Sanitize(top.Title), core.Sanitize(top.Msg))
	}
	if agent == "" && knownAgentProcesses[strings.ToLower(pr.Name)] {
		agent = pr.Name
	}
	if !pane.Reported && agent == "" {
		h.add(core.AgentPane{}, true) // a plain terminal: counted, not listed
		return
	}
	pane.Agent = agent
	if pr.Cwd != "" {
		texts = append(texts, path.Base(pr.Cwd))
		if h.host == "" { // never read a remote path, and never run git in a directory a program controls
			pane.Repo, pane.Branch = gitHead(pr.Cwd)
		}
	}
	pane.Item, pane.Candidates, pane.Match = core.NamedItems(texts, items, h.host, m.o.Hostname(), m.o.LabelConventions)
	h.add(pane, true)
}

// call runs one allowlisted block call under the concurrency limit and the per-call timeout. ok is
// false when there is nothing to read: the host is tripped, the call did not run, or rex exited
// non-zero. Any non-zero exit means "this pane or session is gone" here, deliberately not the
// meaning the same code has for `ls` (3 there is "more than one server"). Only a call that could
// not run at all (a timeout, a missing binary) counts toward the breaker.
func (m *Module) call(ctx context.Context, h *hostSweep, sem chan struct{}, argv []string) ([]byte, bool) {
	if h.fails.Load() >= breakerAfter {
		return nil, false
	}
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, m.o.CallTimeout)
	defer cancel()
	out, exit, err := m.run.Run(cctx, m.Bin(), argv...)
	if err != nil {
		if ctx.Err() == nil {
			h.fails.Add(1)
		}
		return nil, false
	}
	return out, exit == 0
}

// process is a pane's foreground process, reused for processTTL.
func (m *Module) process(ctx context.Context, h *hostSweep, sem chan struct{}, session, blk string) (proc, bool) {
	key := h.host + "\x00" + session + "\x00" + blk
	m.mu.Lock()
	e, hit := m.procs[key]
	m.mu.Unlock()
	if hit && m.o.Now().Sub(e.at) < processTTL {
		return e.p, true
	}
	out, ok := m.call(ctx, h, sem, blockCallArgv(h.host, session, blk, methodProcess))
	if !ok {
		return proc{}, false
	}
	p, err := parseProcess(out)
	if err != nil {
		h.noteDrift()
		return proc{}, false
	}
	if ctx.Err() == nil {
		m.mu.Lock()
		if m.procs == nil || len(m.procs) >= maxProcCache {
			m.procs = map[string]procEntry{}
		}
		m.procs[key] = procEntry{at: m.o.Now(), p: p}
		m.mu.Unlock()
	}
	return p, true
}

// gitHead returns the repository name and branch for a local directory by reading files, never by
// running git: git run in a directory a program controls can execute helpers its config names.
// It walks up from cwd (at most maxGitWalk levels) to a `.git` directory or `.git` file (a worktree)
// that is not a symlink, and reads HEAD from it: "ref: refs/heads/x" is the branch x, anything else
// is a short hash. Anything unexpected gives empty strings.
func gitHead(cwd string) (repo, branch string) {
	if !filepath.IsAbs(cwd) {
		return "", ""
	}
	dir := filepath.Clean(cwd)
	for i := 0; i < maxGitWalk; i++ {
		dotgit := filepath.Join(dir, ".git")
		if fi, err := os.Lstat(dotgit); err == nil {
			gitdir, root := dotgit, dir
			switch {
			case fi.IsDir():
			case fi.Mode().IsRegular():
				b, ok := readSmall(dotgit)
				target, found := strings.CutPrefix(strings.TrimSpace(b), "gitdir: ")
				if !ok || !found {
					return "", ""
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(dir, target)
				}
				gitdir = filepath.Clean(target)
				// A worktree's gitdir is <main>/.git/worktrees/<name>: the repository is <main>.
				if main, _, cut := strings.Cut(filepath.ToSlash(gitdir), "/.git/worktrees/"); cut {
					root = filepath.FromSlash(main)
				}
			default:
				return "", ""
			}
			head, ok := readSmall(filepath.Join(gitdir, "HEAD"))
			if !ok {
				return "", ""
			}
			head = strings.TrimSpace(head)
			if ref, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
				branch = ref
			} else if len(head) >= 7 && isHex(head[:7]) {
				branch = head[:7]
			}
			return filepath.Base(root), branch
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// readSmall reads a regular, non-symlink file of at most maxGitFile bytes.
func readSmall(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxGitFile))
	if err != nil {
		return "", false
	}
	return string(b), true
}
