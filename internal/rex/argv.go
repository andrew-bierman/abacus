package rex

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The whole set of rex argvs this module may run, all read-only (SESSION and BLOCK are ids that
// pass validID; nothing else is ever a word of an argv):
//
//	[-S HOST] --autostart=false ls --json
//	--autostart=false version --json
//	[-S HOST] --autostart=false block ls -s SESSION --json
//	[-S HOST] --autostart=false block call -s SESSION -b BLOCK program_status
//	[-S HOST] --autostart=false block call -s SESSION -b BLOCK process
//
// --autostart=false is always present so a read can never start a server. Nothing that creates,
// renames, focuses, closes or types into a session is reachable, and neither is `api call`.
//
// `rex block call` is a generic verb: the same command with another method (write, reset, clear,
// ...) changes a terminal. The method name is therefore a literal in the matcher below, and no
// argument word is accepted after it.
const autostartOff = "--autostart=false"

// The two block methods the Agents view reads. They are literals, never a variable: widening this
// set is a change to what abacus may do to a terminal.
const (
	methodProgramStatus = "program_status"
	methodProcess       = "process"
)

// idRE is the id grammar seen on a live Rex: a kind prefix, then hex digits and hyphens, never a
// leading dash, at most 128 characters.
var idRE = regexp.MustCompile(`^(session|block):[0-9a-f][0-9a-f-]{0,100}$`)

func validID(prefix, id string) bool {
	return strings.HasPrefix(id, prefix+":") && idRE.MatchString(id)
}

// withHost puts the optional -S HOST prefix in front of an argv.
func withHost(host string, a ...string) []string {
	if host == "" {
		return a
	}
	return append([]string{"-S", host}, a...)
}

// blockLsArgv lists the panes (blocks) of one session.
func blockLsArgv(host, session string) []string {
	return withHost(host, autostartOff, "block", "ls", "-s", session, "--json")
}

// blockCallArgv calls one of the two read-only block methods on one pane.
func blockCallArgv(host, session, block, method string) []string {
	return withHost(host, autostartOff, "block", "call", "-s", session, "-b", block, method)
}

// lsArgv is the session list, for the local server (host "") or the one host a run carries.
func lsArgv(host string) []string {
	var a []string
	if host != "" {
		a = append(a, "-S", host)
	}
	return append(a, autostartOff, "ls", "--json")
}

func versionArgv() []string { return []string{autostartOff, "version", "--json"} }

// ValidHost reports whether host is safe to hand to `rex -S` (the one rule is core.ValidHandoffHost,
// shared with the front-ends that run the handoff).
func ValidHost(host string) bool { return core.ValidHandoffHost(host) }

// maxConfiguredHosts bounds ABACUS_REX_HOSTS.
const maxConfiguredHosts = 16

// ParseHosts reads a comma-separated host list (ABACUS_REX_HOSTS): entries are trimmed, invalid
// ones are dropped (a bad entry never widens what is contacted), duplicates and the excess over
// maxConfiguredHosts are ignored.
func ParseHosts(csv string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range strings.Split(csv, ",") {
		h = strings.TrimSpace(h)
		if !ValidHost(h) || seen[strings.ToLower(h)] || len(out) >= maxConfiguredHosts {
			continue
		}
		seen[strings.ToLower(h)] = true
		out = append(out, h)
	}
	return out
}

func allowedArgv(a []string) bool {
	if len(a) == 3 { // the local-only shapes: no -S prefix
		return a[0] == autostartOff && a[1] == "ls" && a[2] == "--json" ||
			a[0] == autostartOff && a[1] == "version" && a[2] == "--json"
	}
	if len(a) >= 2 && a[0] == "-S" { // the optional host prefix, valid for every other shape
		if !ValidHost(a[1]) {
			return false
		}
		a = a[2:]
	}
	if len(a) == 0 || a[0] != autostartOff {
		return false
	}
	r := a[1:]
	switch len(r) {
	case 2:
		return r[0] == "ls" && r[1] == "--json"
	case 5:
		return r[0] == "block" && r[1] == "ls" && r[2] == "-s" && validID("session", r[3]) && r[4] == "--json"
	case 7:
		return r[0] == "block" && r[1] == "call" && r[2] == "-s" && validID("session", r[3]) && r[4] == "-b" && validID("block", r[5]) &&
			(r[6] == methodProgramStatus || r[6] == methodProcess)
	}
	return false
}

// readOnly rejects any argv outside allowedArgv before it reaches the real runner.
type readOnly struct{ inner core.Runner }

func (r readOnly) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	if !allowedArgv(args) {
		return nil, -1, fmt.Errorf("rex %q is not allowed: this module is read-only", args)
	}
	return r.inner.Run(ctx, name, args...)
}

// scrubbed are the variables rex needs to find its server: HOME and PATH, the XDG base
// directories (the server socket lives under XDG_RUNTIME_DIR), and the REX_* variables Rex sets
// itself (REX_SESSION, REX_SERVER, REX_BLOCK, ...).
var scrubbed = map[string]bool{"HOME": true, "PATH": true, "XDG_RUNTIME_DIR": true, "XDG_CONFIG_HOME": true,
	"XDG_STATE_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true}

// ScrubEnv is the environment rex runs in. Everything else is dropped: tokens and other tools'
// settings, and also, deliberately, proxy settings, SSH_AUTH_SOCK (so `-S` to a host that needs a
// key from the agent cannot authenticate), DISPLAY and WAYLAND_DISPLAY. Passing any of those is an
// opt-in the user makes with ABACUS_REX_PASS_ENV (see ScrubEnvWith).
func ScrubEnv(env []string) []string { return ScrubEnvWith(nil)(env) }

// ScrubEnvWith is ScrubEnv that also keeps the named variables. Names are validated like
// ParsePassEnv; a name that fails is not kept.
func ScrubEnvWith(extra []string) func([]string) []string {
	pass := map[string]bool{}
	for _, n := range ParsePassEnv(strings.Join(extra, ",")) {
		pass[n] = true
	}
	return func(env []string) []string {
		out := make([]string, 0, len(env))
		for _, e := range env {
			name, _, _ := strings.Cut(e, "=")
			if scrubbed[name] || strings.HasPrefix(name, "REX_") || pass[name] {
				out = append(out, e)
			}
		}
		return out
	}
}

var passEnvRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// maxPassEnv bounds ABACUS_REX_PASS_ENV.
const maxPassEnv = 8

// ParsePassEnv reads a comma-separated list of variable names to pass to rex. Names must be
// upper-case words; loader and shell-startup variables (LD_*, DYLD_*, BASH_ENV, ENV) are refused
// because they would let the environment run code.
func ParsePassEnv(csv string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range strings.Split(csv, ",") {
		n = strings.TrimSpace(n)
		bad := !passEnvRE.MatchString(n) || strings.HasPrefix(n, "LD_") || strings.HasPrefix(n, "DYLD_") || n == "BASH_ENV" || n == "ENV"
		if bad || seen[n] || len(out) >= maxPassEnv {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// maxOut caps what is read from one rex call.
const maxOut = 1 << 20

func defaultRunner(passEnv []string) core.Runner {
	return core.ExecRunner{Scrub: ScrubEnvWith(passEnv), MaxOut: maxOut}
}
