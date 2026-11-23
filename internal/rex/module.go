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

// exitNoServer is the exit code of `rex ls` when no server is reachable: absent, not an error.
const exitNoServer = 4

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
			return m.incompatible("the Rex probe could not run: "+core.ClipRunes(core.Sanitize(err.Error()), 200), 0, "")
		case exit == exitNoServer:
			m.set(core.Ready, "", 0, "")
			return core.Detection{State: core.Ready, Detail: "no Rex server running"}
		case exit != 0:
			return m.incompatible(fmt.Sprintf("the Rex probe exited %d", exit), 0, "")
		default:
			if _, err := parseLS(out); err != nil {
				return m.incompatible("the Rex probe printed output this abacus cannot read", 0, "")
			}
			server = "probe"
		}
	}
	vctx, cancel := context.WithTimeout(ctx, m.o.CallTimeout)
	defer cancel()
	out, exit, err := m.run.Run(vctx, f.Bin, versionArgv()...)
	if err != nil || exit != 0 {
		return m.incompatible("rex version --json failed; Rex is optional, so the workspace layer is off", 0, "")
	}
	var v struct {
		APIRevision *int   `json:"api_revision"`
		RexVersion  string `json:"rex_version"`
		Version     string `json:"version"`
	}
	if json.Unmarshal(out, &v) != nil || v.APIRevision == nil {
		return m.incompatible("rex version --json has no api_revision; the workspace layer is off", 0, "")
	}
	ver := core.ClipRunes(core.Sanitize(v.RexVersion), 64)
	if ver == "" {
		ver = core.ClipRunes(core.Sanitize(v.Version), 64)
	}
	rev := *v.APIRevision
	if rev < MinAPIRevision || rev > MaxAPIRevision {
		return m.incompatible(fmt.Sprintf("Rex API revision %d is not supported (supported: %d to %d). The workspace layer is off; update abacus or ignore this, Rex is optional.",
			rev, MinAPIRevision, MaxAPIRevision), rev, ver)
	}
	m.set(core.Ready, server, rev, ver)
	return core.Detection{State: core.Ready, Version: ver, APIVersion: rev}
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

type lsOut struct {
	Sessions []struct {
		SessionID string `json:"session_id"`
		Label     string `json:"label"`
	} `json:"sessions"`
}

// parseLS reads `ls --json`, ignoring unknown fields and keeping at most maxSessions sessions,
// each with sanitized, bounded text.
func parseLS(out []byte) ([]session, error) {
	var l lsOut
	if err := json.Unmarshal(out, &l); err != nil {
		return nil, err
	}
	var ss []session
	for _, s := range l.Sessions {
		if len(ss) >= maxSessions {
			break
		}
		ss = append(ss, cleanSession(s.SessionID, s.Label))
	}
	return ss, nil
}

// Status summarizes the sessions on the local server; no server is unavailable, not an error.
func (m *Module) Status(ctx context.Context) core.Status {
	ss, err := m.sessions(ctx, "")
	switch {
	case errors.Is(err, errNoServer):
		return core.Status{Unavailable: noWorkspace, HowToConnect: HowToConnect}
	case err != nil:
		return core.Status{Unavailable: core.ClipRunes(core.Sanitize(err.Error()), 200)}
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
		return nil, fmt.Errorf("rex ls: %w", err)
	case exit == exitNoServer:
		return nil, errNoServer
	case exit != 0:
		return nil, fmt.Errorf("rex ls exited %d", exit)
	}
	ss, err := parseLS(out)
	if err != nil {
		return nil, errors.New("rex ls printed output this abacus cannot read")
	}
	return ss, nil
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
			p, amb := place(it, r.sessions)
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
		rep.Reason = core.ClipRunes(core.Sanitize(strings.Join(problems, "; ")), 300)
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
