// Package rex is the optional terminal-workspace adapter. It composes the `rex` CLI only,
// through a Runner seam: `ls --json` and `version --json` behind an exact argv allowlist and a
// scrubbed environment, never starting a server. It imports no Rex or Lambe code and no UI.
//
// Rex is optional and pre-1.0, so every failure here degrades to "no workspace" and never
// to a failed cockpit: an absent server is not an error, an unsupported API revision is a
// warning, and nothing is ever run on a timer except the bounded reads above.
package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Supported Rex API revisions. The server API is additive so far; 10 to 14 is the window checked
// against: revision 13 from the API reference, and 14 from a real Rex (0.1.0-preview.19) whose
// ls, version, block ls, window ls, capture, focus, run, split, new, window new and events were
// run for this module. A revision outside the window is reported incompatible rather than guessed at.
const (
	MinAPIRevision = 10
	MaxAPIRevision = 14
)

// Exit codes of `rex ls` and `rex version` that mean something here (second-hand, see
// docs/tool-contract.md): no server is reachable (absent, not an error), or more than one server
// answers and Rex cannot choose.
const (
	exitNoServer  = 4
	exitAmbiguous = 3
)

// defaultCacheTTL is how long a host's session list is reused (see Module.read) when there is no
// change feed; liveCacheTTL is the longest it is reused while the feed is connected (an event
// drops it sooner), a ceiling in case an event is missed. blockCacheTTL is the same for the panes.
const (
	defaultCacheTTL = 3 * time.Second
	liveCacheTTL    = 60 * time.Second
	blockCacheTTL   = 15 * time.Second
	// blockReadTime bounds the pane reads of one workspace read, so slow ones never fail it.
	blockReadTime = 600 * time.Millisecond
)

// maxHosts bounds how many distinct hosts one read contacts.
const maxHosts = 8

const (
	// RungHandoff is the existing `rex -S <machine>` handoff, the first rung of the open plan.
	// The name is neutral: it is shown, and the UI does not name tools.
	RungHandoff = "handoff"

	// HowToConnect is shown when no workspace is reachable. Rex is optional.
	HowToConnect = "Start a Rex server (rex server start) or run abacus inside a Rex terminal. Rex is optional; nothing else in abacus depends on it."
	noWorkspace  = "No terminal workspace found."
)

// Options configure the module. Every process boundary is injectable so tests never run a real
// Rex or look at a real session.
type Options struct {
	Bin      string // explicit rex binary (ABACUS_REX_BIN); empty means look it up on PATH
	LookPath func(string) (string, error)
	Getenv   func(string) string // REX_SESSION and REX_SERVER detection; nil means os.Getenv
	Runner   core.Runner         // wrapped by the read-only allowlist whatever it is
	Hostname func() string       // this machine's name, so a run on it is read without -S
	// LabelConventions are the labels that name an item exactly, as {id} templates; empty means
	// core.DefaultLabelConventions.
	LabelConventions []string
	// Hosts are the only machines, other than this one, that a read may contact with -S
	// (ABACUS_REX_HOSTS). A machine name a run carries is data from another program and `-S` can
	// open a connection to it, so a name not listed here is never contacted. Invalid entries are
	// dropped.
	Hosts []string
	// PassEnv names extra environment variables to hand to rex (ABACUS_REX_PASS_ENV, for example
	// SSH_AUTH_SOCK so -S can authenticate). Everything not named here or in ScrubEnv is dropped.
	PassEnv []string
	// ProbeTimeout bounds the detection probe, CallTimeout every other call.
	ProbeTimeout time.Duration
	CallTimeout  time.Duration
	// CacheTTL is how long one host's session list is reused within this process: 0 means the
	// default (3 s), negative turns the cache off. Now is the clock for it (nil means time.Now).
	CacheTTL time.Duration
	Now      func() time.Time
	// Stream runs the change feed (`events --json`); nil means the real command. Writes use the
	// same Runner as reads but behind their own allowlist. WriteTimeout bounds one write (default
	// 10 s) and EventSettle is how long the feed must run before it counts as connected.
	Stream       core.StreamFunc
	WriteTimeout time.Duration
	EventSettle  time.Duration
	// Cwd is the directory a new terminal starts in; empty means the process's own.
	Cwd func() string
}

// Module implements core.Module and core.WorkspaceProvider for Rex.
type Module struct {
	core.Locator
	run    readOnly
	o      Options
	mu     sync.Mutex
	state  core.State
	server string // how the server was found: "environment", "probe" or "" (none running)
	rev    int
	ver    string
	cache  map[string]cached
	bcache map[string]cachedBlocks
	wrun   writeOnly   // the workspace writes, behind their own allowlist
	crun   readOnly    // terminal reads (the end of the output is kept, not the start)
	live   atomic.Bool // the change feed is connected
}

// New returns a Rex module with defaults filled in.
func New(o Options) *Module {
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	o.Hosts = ParseHosts(strings.Join(o.Hosts, ","))
	o.PassEnv = ParsePassEnv(strings.Join(o.PassEnv, ","))
	crun := o.Runner
	if o.Runner == nil {
		o.Runner = defaultRunner(o.PassEnv)
		crun = core.ExecRunner{Scrub: ScrubEnvWith(o.PassEnv), MaxOut: maxCapture, KeepTail: true}
	}
	if o.Stream == nil {
		o.Stream = core.ExecStream(ScrubEnvWith(o.PassEnv))
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.EventSettle <= 0 {
		o.EventSettle = 800 * time.Millisecond
	}
	if o.Cwd == nil {
		o.Cwd = func() string { d, _ := os.Getwd(); return d }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheTTL == 0 {
		o.CacheTTL = defaultCacheTTL
	}
	if o.Hostname == nil {
		o.Hostname = shortHostname
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = 500 * time.Millisecond
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 2 * time.Second
	}
	o.LabelConventions = core.ValidConventions(o.LabelConventions)
	return &Module{run: readOnly{inner: o.Runner}, crun: readOnly{inner: crun}, wrun: writeOnly{inner: o.Runner}, o: o}
}

func shortHostname() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	return h
}

func (m *Module) ID() string   { return "rex" }
func (m *Module) Name() string { return "Rex" }

// Quiet keeps an absent Rex out of the tool list, `version` and the overview: it is optional,
// so not having it is not something to report. A Rex that is there is listed.
func (m *Module) Quiet() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state != core.Ready && m.state != core.Incompatible
}

func (m *Module) set(s core.State, server string, rev int, ver string) {
	m.mu.Lock()
	m.state, m.server, m.rev, m.ver = s, server, rev, ver
	m.cache, m.bcache = nil, nil // a new detection starts from a clean slate
	m.mu.Unlock()
}

// Detect finds the rex binary and a server. REX_SESSION or REX_SERVER in the environment says
// this process runs inside Rex; otherwise a short probe of `ls --json` decides. A missing
// binary is absent; a missing server is still ready (the CLI works; Doctor says there is no
// server); a probe that fails, or an API revision outside the supported window, is incompatible
// (a warning, never a failure).
func (m *Module) Detect(ctx context.Context) core.Detection {
	m.set(core.Absent, "", 0, "")
	f, why := m.Find("rex", m.o.Bin, m.o.LookPath)
	if why != "" {
		return core.Detection{State: core.Absent, Detail: why}
	}
	server := ""
	if m.o.Getenv("REX_SESSION") != "" || m.o.Getenv("REX_SERVER") != "" {
		server = "environment"
	} else {
		pctx, cancel := context.WithTimeout(ctx, m.o.ProbeTimeout)
		out, exit, err := m.run.Run(pctx, f.Bin, lsArgv("")...)
		cancel()
		switch {
		case err != nil:
			return m.incompatible("the Rex probe "+runFailure(err), 0, "")
		case exit == exitNoServer:
			return m.noServer()
		case exit == exitAmbiguous:
			return m.incompatible(ambiguousServer, 0, "")
		case exit != 0:
			return m.incompatible(fmt.Sprintf("the Rex probe exited %d", exit), 0, "")
		}
		if _, _, err := parseLS(out); err != nil {
			return m.incompatible("the Rex probe printed "+errUnrecognized.Error(), 0, "")
		}
		server = "probe"
	}
	vctx, cancel := context.WithTimeout(ctx, m.o.CallTimeout)
	defer cancel()
	out, exit, err := m.run.Run(vctx, f.Bin, versionArgv()...)
	switch {
	case err != nil:
		return m.incompatible("rex version --json "+runFailure(err)+"; Rex is optional, so the workspace layer is off", 0, "")
	case exit == exitNoServer:
		return m.noServer()
	case exit == exitAmbiguous:
		return m.incompatible(ambiguousServer, 0, "")
	case exit != 0:
		return m.incompatible(fmt.Sprintf("rex version --json exited %d; Rex is optional, so the workspace layer is off", exit), 0, "")
	}
	v, bad := parseVersion(out)
	if bad != "" {
		return m.incompatible("rex version --json: "+bad+"; the workspace layer is off", 0, "")
	}
	// help.json: api_revision is the newest revision the server speaks and api_min_revision the
	// oldest, so the server's range [min, rev] must overlap ours [MinAPIRevision, MaxAPIRevision].
	switch {
	case v.rev < MinAPIRevision:
		return m.incompatible(fmt.Sprintf("Rex API revision %d is older than this abacus supports (%d to %d). The workspace layer is off; update Rex or ignore this, Rex is optional.",
			v.rev, MinAPIRevision, MaxAPIRevision), v.rev, v.ver)
	case v.min > MaxAPIRevision:
		return m.incompatible(fmt.Sprintf("Rex needs API revision %d or newer; this abacus supports %d to %d. The workspace layer is off; update abacus or ignore this, Rex is optional.",
			v.min, MinAPIRevision, MaxAPIRevision), v.rev, v.ver)
	}
	m.set(core.Ready, server, v.rev, v.ver)
	return core.Detection{State: core.Ready, Version: v.ver, APIVersion: v.rev}
}

const ambiguousServer = "Rex found more than one server (exit 3) and cannot choose; set REX_SERVER to pick one. The workspace layer is off."

// noServer is a usable Rex with no server running: ready, and Doctor says so.
func (m *Module) noServer() core.Detection {
	m.set(core.Ready, "", 0, "")
	return core.Detection{State: core.Ready, Detail: "no Rex server running"}
}

// runFailure says why a call did not run, without leaking a context error's wording.
func runFailure(err error) string {
	switch {
	case errors.Is(err, core.ErrTimedOut), errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, core.ErrCanceled), errors.Is(err, context.Canceled):
		return "was canceled"
	}
	return "could not run: " + safe(err.Error(), 200)
}

// safe makes text from another program printable and bounded.
func safe(s string, n int) string { return core.ClipRunes(core.Sanitize(s), n) }

type versionInfo struct {
	rev, min int
	ver      string
}

// parseVersion reads `version --json`. A revision present but not a whole non-negative number is
// reported as such (never as missing); a missing api_min_revision means 0.
func parseVersion(out []byte) (versionInfo, string) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(out, &raw) != nil || raw == nil {
		return versionInfo{}, "has no api_revision"
	}
	var v versionInfo
	revRaw, ok := raw["api_revision"]
	if !ok {
		return v, "has no api_revision"
	}
	var good bool
	if v.rev, good = wholeNumber(revRaw); !good {
		return v, "api_revision is not a whole number (got " + safe(string(revRaw), 24) + ")"
	}
	if minRaw, ok := raw["api_min_revision"]; ok {
		if v.min, good = wholeNumber(minRaw); !good {
			return v, "api_min_revision is not a whole number (got " + safe(string(minRaw), 24) + ")"
		}
	}
	for _, k := range []string{"rex_version", "version"} {
		var s string
		if json.Unmarshal(raw[k], &s) == nil && s != "" {
			v.ver = safe(s, 64)
			if v.ver != "" {
				break
			}
		}
	}
	return v, ""
}

// wholeNumber reads a JSON number that is a non-negative integer; strings, null, booleans,
// fractions and exponents are not.
func wholeNumber(raw json.RawMessage) (int, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 32) // quotes, null, true and fractions do not parse
	if err != nil || n < 0 {
		return 0, false
	}
	return int(n), true
}

func (m *Module) incompatible(detail string, rev int, ver string) core.Detection {
	m.set(core.Incompatible, "", rev, ver)
	return core.Detection{State: core.Incompatible, Version: ver, APIVersion: rev, Detail: detail}
}

// Doctor reports one check. No server is a skip, never a failure: Rex is optional.
func (m *Module) Doctor(context.Context) []core.Check {
	m.mu.Lock()
	state, server, rev := m.state, m.server, m.rev
	m.mu.Unlock()
	switch {
	case state == core.Incompatible:
		return []core.Check{{Name: "rex", Status: core.StatusWarn, Detail: "Rex could not be used; the workspace layer is off", Fix: "Rex is optional; see the compatibility check"}}
	case state != core.Ready:
		return []core.Check{{Name: "rex", Status: core.StatusSkip, Detail: "Rex not found (optional)"}}
	case server == "":
		return []core.Check{{Name: "rex", Status: core.StatusSkip, Detail: "no Rex server running (optional)", Fix: "start one with: rex server start"}}
	}
	live := "updates by polling"
	if m.live.Load() {
		live = "live updates on"
	}
	return []core.Check{{Name: "rex", Status: core.StatusOK, Detail: fmt.Sprintf("Rex detected (%s), API revision %d (checked against %d to %d), %s", server, rev, MinAPIRevision, MaxAPIRevision, live)}}
}

// errUnrecognized is `ls --json` printing something other than the documented shape. It is
// an error, never a healthy empty list: a changed schema must not look like "no sessions".
var errUnrecognized = errors.New("unrecognized rex ls output")

// parseLS reads `ls --json`: {"sessions": null | [{"session_id": string, "label": string}, ...]}.
// Unknown fields are ignored; a missing or mistyped "sessions", or a session with no id, is
// errUnrecognized. At most maxSessions are kept, with sanitized text, and truncated says there
// were more.
func parseLS(out []byte) (ss []session, truncated bool, err error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(out, &top) != nil || top == nil {
		return nil, false, errUnrecognized
	}
	raw, ok := top["sessions"]
	if !ok {
		return nil, false, errUnrecognized
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil { // null is fine: no sessions
		return nil, false, errUnrecognized
	}
	for _, r := range list {
		if len(ss) >= maxSessions {
			return ss, true, nil
		}
		var s struct {
			SessionID string `json:"session_id"`
			Label     string `json:"label"`
		}
		if json.Unmarshal(r, &s) != nil || s.SessionID == "" {
			return nil, false, errUnrecognized
		}
		ss = append(ss, cleanSession(s.SessionID, s.Label))
	}
	return ss, false, nil
}

// Status summarizes the sessions on the local server; no server is unavailable, not an error.
func (m *Module) Status(ctx context.Context) core.Status {
	res, _ := m.read(ctx, "")
	info := m.info(core.Connected)
	switch {
	case errors.Is(res.err, errNoServer):
		info.Connection = core.NotConnected
		return core.Status{Unavailable: noWorkspace, HowToConnect: HowToConnect, Workspace: &info}
	case res.err != nil:
		info.Connection = core.Degraded
		return core.Status{Unavailable: safe(res.err.Error(), 200), Workspace: &info}
	}
	n := fmt.Sprintf("%d sessions", len(res.ss))
	switch {
	case res.truncated:
		n = fmt.Sprintf("%d+ sessions", len(res.ss))
	case len(res.ss) == 1:
		n = "1 session"
	}
	return core.Status{Summary: n, Workspace: &info}
}

// info is the typed state for status JSON.
func (m *Module) info(c core.Connection) core.WorkspaceInfo {
	m.mu.Lock()
	state, server, rev := m.state, m.server, m.rev
	m.mu.Unlock()
	return core.WorkspaceInfo{Connection: c, Server: server, APIRevision: rev, SupportedMin: MinAPIRevision, SupportedMax: MaxAPIRevision,
		Compatible: state == core.Ready, Live: m.live.Load(), InSession: m.o.Getenv("REX_SESSION") != ""}
}

var errNoServer = errors.New("no Rex server running")

// lsResult is one host's session list as read: the sessions, whether there were more than
// maxSessions, or why there is none.
type lsResult struct {
	ss        []session
	truncated bool
	err       error
}

type cached struct {
	at  time.Time
	res lsResult
}

// read returns one host's session list (host "" is the local server). The adapter owns caching:
// a result, success or failure, is kept for a short TTL (Options.CacheTTL) in this process, so a
// slow or hung rex is paid for once per TTL and not once per read. core owns the time budget and
// runs the whole workspace read concurrently with other reads. cached says the result is a
// replay, so the report can be marked stale.
func (m *Module) read(ctx context.Context, host string) (res lsResult, replay bool) {
	ttl := m.o.CacheTTL
	if ttl > 0 && m.live.Load() {
		ttl = liveCacheTTL // the change feed drops the cache when something changes
	}
	if ttl > 0 {
		m.mu.Lock()
		c, ok := m.cache[host]
		m.mu.Unlock()
		if ok && m.o.Now().Sub(c.at) < ttl {
			return c.res, true
		}
	}
	res = m.sessions(ctx, host)
	if ttl > 0 && ctx.Err() == nil { // a canceled read says nothing about rex
		m.mu.Lock()
		if m.cache == nil {
			m.cache = map[string]cached{}
		}
		m.cache[host] = cached{at: m.o.Now(), res: res}
		m.mu.Unlock()
	}
	return res, false
}

// sessions runs one `ls --json` for a host.
func (m *Module) sessions(ctx context.Context, host string) lsResult {
	ctx, cancel := context.WithTimeout(ctx, m.o.CallTimeout)
	defer cancel()
	out, exit, err := m.run.Run(ctx, m.Bin(), lsArgv(host)...)
	switch {
	case err != nil:
		return lsResult{err: fmt.Errorf("reading sessions %s", runFailure(err))}
	case exit == exitNoServer:
		return lsResult{err: errNoServer}
	case exit == exitAmbiguous:
		return lsResult{err: errors.New("more than one workspace server answers (exit 3)")}
	case exit != 0:
		return lsResult{err: fmt.Errorf("reading sessions exited %d", exit)}
	}
	ss, trunc, err := parseLS(out)
	return lsResult{ss: ss, truncated: trunc, err: err}
}

// hostKind says what may be done with the machine an item carries.
type hostKind int

const (
	hostLocal      hostKind = iota // this machine: read without -S
	hostAllowed                    // in the explicit allowlist: read with -S
	hostNotAllowed                 // a well-formed name nobody configured: never contacted
	hostInvalid                    // not a safe name at all
)

// classify decides, for the machine an item carries, whether it may be contacted. Hosts come from
// run data (any program can write them), and `-S` can make rex open a connection to the host, so
// only this machine and the hosts in Options.Hosts (ABACUS_REX_HOSTS) are ever contacted. The
// second result is the allowlist's own spelling of the host, which is what goes into argv.
func (m *Module) classify(it core.WorkspaceItem) (hostKind, string) {
	h := it.Host
	switch {
	case h == "" || strings.EqualFold(h, m.o.Hostname()):
		return hostLocal, ""
	case !ValidHost(h):
		return hostInvalid, ""
	}
	for _, a := range m.o.Hosts {
		if strings.EqualFold(a, h) {
			return hostAllowed, a
		}
	}
	return hostNotAllowed, ""
}

type hostRead struct {
	host   string
	items  []core.WorkspaceItem
	res    lsResult
	blocks map[string][]block
	stale  bool
}

// Workspace places items among the sessions of the servers they live on: the local one, and any
// configured -S host a run already carries (at most maxHosts). A server that is not running is
// not an error; one that fails degrades the report, keeping what the others found. A label
// claimed twice is reported as ambiguous with its candidates. An item on a machine nobody
// configured is not contacted: it gets a placement saying so, with the command to copy.
func (m *Module) Workspace(ctx context.Context, items []core.WorkspaceItem) (core.WorkspaceReport, error) {
	var reads []*hostRead
	byHost := map[string]*hostRead{}
	var unconfigured []core.WorkspaceItem
	var skipped, badHost int
	for _, it := range items {
		kind, h := m.classify(it)
		switch kind {
		case hostInvalid:
			badHost++
			continue
		case hostNotAllowed:
			unconfigured = append(unconfigured, it)
			continue
		}
		key := strings.ToLower(h)
		r := byHost[key]
		if r == nil {
			if len(reads) >= maxHosts {
				skipped++
				continue
			}
			r = &hostRead{host: h}
			byHost[key] = r
			reads = append(reads, r)
		}
		r.items = append(r.items, it)
	}
	var wg sync.WaitGroup
	for _, r := range reads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.res, r.stale = m.read(ctx, r.host)
			if r.res.err == nil && len(r.res.ss) > 0 {
				r.blocks = m.readBlocks(ctx, r.host, r.res.ss)
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return core.WorkspaceReport{}, err
	}
	rep := core.WorkspaceReport{State: core.NotConnected, Reason: noWorkspace, HowToConnect: HowToConnect}
	var problems []string
	for _, r := range reads {
		switch {
		case errors.Is(r.res.err, errNoServer):
			continue
		case r.res.err != nil:
			problems = append(problems, r.res.err.Error())
			continue
		}
		rep.State, rep.Reason, rep.HowToConnect = core.Connected, "", ""
		rep.Stale = rep.Stale || r.stale
		rep.Truncated = rep.Truncated || r.res.truncated
		for _, it := range r.items {
			p, amb := place(it, r.res.ss, r.blocks, m.o.LabelConventions)
			if p != nil {
				rep.Placements = append(rep.Placements, *p)
			}
			if amb != nil {
				rep.Ambiguous = append(rep.Ambiguous, *amb)
			}
		}
	}
	for _, it := range unconfigured {
		rep.Placements = append(rep.Placements, core.Placement{ID: core.PlacementID(it, it.Host, ""), Item: it, Host: it.Host, Match: core.MatchHostNotConfigured})
	}
	if skipped > 0 {
		problems = append(problems, fmt.Sprintf("more than %d hosts; some were not read", maxHosts))
	}
	if badHost > 0 {
		problems = append(problems, fmt.Sprintf("%d items carry a machine name that is not safe to pass to rex", badHost))
	}
	if len(problems) > 0 {
		rep.State, rep.HowToConnect = core.Degraded, ""
		rep.Reason = safe(strings.Join(problems, "; "), 300)
	}
	return rep, nil
}

// Open is the plan for one placement. Nothing is run here; a front-end runs a rung only on an
// explicit keypress. The argv carries only the machine name, never a label, and uses the located
// binary so the command shown is the command that works.
//
//   - a machine that was not configured for reads: no rung, only the command to copy
//   - a machine: the handoff rung, then the same command to copy
//   - no machine (a session on this one): no rung, the command to open the workspace and a note
//     saying which session to pick. Focusing or selecting a session needs calls that have not been
//     verified against a real Rex.
//
// Richer rungs are deferred; a rung is data, so adding one is a new value here.
func (m *Module) Open(p core.Placement) core.OpenPlan {
	bin := m.Bin()
	which := "Pick the session named \"" + safe(p.Label, core.MaxLabelRunes) + "\"."
	if p.Label == "" {
		which = "Pick the session for this item."
	}
	switch {
	case p.Host != "" && !ValidHost(p.Host):
		return core.OpenPlan{Rungs: []core.OpenRung{}, Note: "This item's machine name is not safe to pass on, so there is no command to run. Open the terminal workspace and look for the item."}
	case p.Host != "" && p.Match == core.MatchHostNotConfigured:
		return core.OpenPlan{Rungs: []core.OpenRung{}, Copy: core.ShellJoin(core.HandoffArgv(bin, p.Host)),
			Note: "This machine is not in the configured list of machines abacus may contact, so its sessions were not read. Run the command to open it yourself."}
	case p.Host != "":
		argv := core.HandoffArgv(bin, p.Host)
		return core.OpenPlan{Rungs: []core.OpenRung{{Name: RungHandoff, Argv: argv, Interactive: true}}, Copy: core.ShellJoin(argv)}
	}
	if bin == "" {
		bin = core.RexBin
	}
	return core.OpenPlan{Rungs: []core.OpenRung{}, Copy: core.ShellJoin([]string{bin}), Note: "Open the terminal workspace on this machine. " + which}
}
