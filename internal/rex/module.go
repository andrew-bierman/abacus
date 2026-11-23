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
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Supported Rex API revisions. The server API is additive so far; 10 to 13 is the window the
// local API reference (revision 13) and Rex's own clients advertise. A revision outside it is
// reported incompatible rather than guessed at.
const (
	MinAPIRevision = 10
	MaxAPIRevision = 13
)

// Exit codes of `rex ls` and `rex version` that mean something here (second-hand, see
// docs/tool-contract.md): no server is reachable (absent, not an error), or more than one server
// answers and Rex cannot choose.
const (
	exitNoServer  = 4
	exitAmbiguous = 3
)

// maxHosts bounds how many distinct hosts one read contacts.
const maxHosts = 8

const (
	// RungHandoff is the existing `rex -S <machine>` handoff, the first rung of the open plan.
	RungHandoff = "rex-handoff"

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
	// ProbeTimeout bounds the detection probe, CallTimeout every other call.
	ProbeTimeout time.Duration
	CallTimeout  time.Duration
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
}

// New returns a Rex module with defaults filled in.
func New(o Options) *Module {
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Runner == nil {
		o.Runner = defaultRunner()
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
	return &Module{run: readOnly{inner: o.Runner}, o: o}
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
	s := strings.TrimSpace(string(raw))
	if s == "" || s[0] != '-' && (s[0] < '0' || s[0] > '9') {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 32)
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
	return []core.Check{{Name: "rex", Status: core.StatusOK, Detail: fmt.Sprintf("Rex detected (%s), API revision %d", server, rev)}}
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
	ss, err := m.sessions(ctx, "")
	switch {
	case errors.Is(err, errNoServer):
		return core.Status{Unavailable: noWorkspace, HowToConnect: HowToConnect}
	case err != nil:
		return core.Status{Unavailable: safe(err.Error(), 200)}
	}
	return core.Status{Summary: fmt.Sprintf("%d sessions", len(ss))}
}

var errNoServer = errors.New("no Rex server running")

// sessions reads one host's session list (host "" is the local server).
func (m *Module) sessions(ctx context.Context, host string) ([]session, error) {
	ctx, cancel := context.WithTimeout(ctx, m.o.CallTimeout)
	defer cancel()
	out, exit, err := m.run.Run(ctx, m.Bin(), lsArgv(host)...)
	switch {
	case err != nil:
		return nil, fmt.Errorf("rex ls %s", runFailure(err))
	case exit == exitNoServer:
		return nil, errNoServer
	case exit == exitAmbiguous:
		return nil, errors.New("rex ls found more than one server (exit 3)")
	case exit != 0:
		return nil, fmt.Errorf("rex ls exited %d", exit)
	}
	ss, _, err := parseLS(out)
	return ss, err
}

// hostOf is the server an item is read from: "" for this machine, otherwise the one -S host the
// item already carries.
func (m *Module) hostOf(it core.WorkspaceItem) string {
	if it.Host == "" || strings.EqualFold(it.Host, m.o.Hostname()) {
		return ""
	}
	return it.Host
}

type hostRead struct {
	host     string
	items    []core.WorkspaceItem
	sessions []session
	err      error
}

// Workspace places items among the sessions of the servers they live on: the local one, and any
// one -S host a run already carries (at most maxHosts). A server that is not running is not an
// error; one that fails degrades the report, keeping what the others found. A label claimed
// twice is reported as ambiguous with its candidates.
func (m *Module) Workspace(ctx context.Context, items []core.WorkspaceItem) (core.WorkspaceReport, error) {
	var reads []*hostRead
	byHost := map[string]*hostRead{}
	var skipped, badHost int
	for _, it := range items {
		h := m.hostOf(it)
		if h != "" && !ValidHost(h) {
			badHost++
			continue
		}
		r := byHost[h]
		if r == nil {
			if len(reads) >= maxHosts {
				skipped++
				continue
			}
			r = &hostRead{host: h}
			byHost[h] = r
			reads = append(reads, r)
		}
		r.items = append(r.items, it)
	}
	var wg sync.WaitGroup
	for _, r := range reads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.sessions, r.err = m.sessions(ctx, r.host)
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
		case errors.Is(r.err, errNoServer):
			continue
		case r.err != nil:
			problems = append(problems, r.err.Error())
			continue
		}
		rep.State, rep.Reason, rep.HowToConnect = core.Connected, "", ""
		for _, it := range r.items {
			p, amb := place(it, r.sessions, m.o.LabelConventions)
			if p != nil {
				rep.Placements = append(rep.Placements, *p)
			}
			if amb != nil {
				rep.Ambiguous = append(rep.Ambiguous, *amb)
			}
		}
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

// Open is the plan for one placement: the existing `rex -S <machine>` handoff, then the same
// command to copy. The richer rungs (focus a window, open a new one, select a session) are
// deferred until they have been verified against a real Rex; a rung is data, so adding one is a
// new value here. Nothing is run. The argv carries only the machine name, never a label.
func (m *Module) Open(p core.Placement) core.OpenPlan {
	if !ValidHost(p.Host) {
		return core.OpenPlan{Rungs: []core.OpenRung{}, Note: "No machine is recorded for this item, so there is no command to run. Open Rex and look for the session named \"" + core.Sanitize(p.Label) + "\"."}
	}
	argv := HandoffArgv(p.Host)
	return core.OpenPlan{Rungs: []core.OpenRung{{Name: RungHandoff, Argv: argv}}, Copy: core.ShellJoin(argv)}
}

// HandoffArgv is the command that hands a machine to Rex.
func HandoffArgv(host string) []string { return []string{"rex", "-S", host} }
