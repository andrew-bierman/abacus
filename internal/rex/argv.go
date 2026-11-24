package rex

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The whole set of rex argvs this module may run. --autostart=false is always present so nothing
// here can start a server. Every argv is matched against an exact shape (a template of literals and
// typed slots), never prefix-matched, and a word that does not fit its slot rejects the argv.
//
// Reads (never change anything):
//
//	[-S HOST] --autostart=false ls --json
//	--autostart=false version --json
//	[-S HOST] --autostart=false block ls -s SESSION --json
//	[-S HOST] --autostart=false window ls -s SESSION --json
//	[-S HOST] --autostart=false capture -s SESSION -b BLOCK --trim
//	--autostart=false events --json                       (a stream; see allowedStreamArgv)
//
// Writes (they only focus a pane or open a new one; nothing sends input to a terminal, closes,
// moves or renames anything, and `api call` is not reachable):
//
//	[-S HOST] --autostart=false focus -s SESSION BLOCK
//	--autostart=false run -s SESSION --label L [-c DIR] --keep-open --json -- COMMAND...
//	--autostart=false split -s SESSION [-b BLOCK] --split=right --label L [-c DIR] --keep-open --json -- COMMAND...
//	--autostart=false new NAME --json --window TAB [-c DIR] --keep-open -- COMMAND...
//	--autostart=false window new -s SESSION TAB [-c DIR] --keep-open [--focus=false] --json -- COMMAND...
const autostartOff = "--autostart=false"

// lsArgv is the session list, for the local server (host "") or the one host a run carries.
func lsArgv(host string) []string {
	return append(hostArgs(host), autostartOff, "ls", "--json")
}

func versionArgv() []string { return []string{autostartOff, "version", "--json"} }

func blocksArgv(host, session string) []string {
	return append(hostArgs(host), autostartOff, "block", "ls", "-s", session, "--json")
}

func windowsArgv(host, session string) []string {
	return append(hostArgs(host), autostartOff, "window", "ls", "-s", session, "--json")
}

func captureArgv(host, session, block string) []string {
	return append(hostArgs(host), autostartOff, "capture", "-s", session, "-b", block, "--trim")
}

func eventsArgv() []string { return []string{autostartOff, "events", "--json"} }

func hostArgs(host string) []string {
	if host == "" {
		return nil
	}
	return []string{"-S", host}
}

func cwdArgs(cwd string) []string {
	if cwd == "" {
		return nil
	}
	return []string{"-c", cwd}
}

func focusArgv(host, session, block string) []string {
	return append(hostArgs(host), autostartOff, "focus", "-s", session, block)
}

func runArgv(session, label, cwd string, command []string) []string {
	a := []string{autostartOff, "run", "-s", session, "--label", label}
	a = append(a, cwdArgs(cwd)...)
	a = append(a, "--keep-open", "--json", "--")
	return append(a, command...)
}

func splitArgv(session, anchor, label, cwd string, command []string) []string {
	a := []string{autostartOff, "split", "-s", session}
	if anchor != "" {
		a = append(a, "-b", anchor)
	}
	a = append(a, "--split=right", "--label", label)
	a = append(a, cwdArgs(cwd)...)
	a = append(a, "--keep-open", "--json", "--")
	return append(a, command...)
}

func newSessionArgv(name, tab, cwd string, command []string) []string {
	a := []string{autostartOff, "new", name, "--json", "--window", tab}
	a = append(a, cwdArgs(cwd)...)
	a = append(a, "--keep-open", "--")
	return append(a, command...)
}

func newWindowArgv(session, tab, cwd string, focus bool, command []string) []string {
	a := []string{autostartOff, "window", "new", "-s", session, tab}
	a = append(a, cwdArgs(cwd)...)
	a = append(a, "--keep-open")
	if !focus {
		a = append(a, "--focus=false")
	}
	a = append(a, "--json", "--")
	return append(a, command...)
}

type kind int

const (
	kLit kind = iota
	kHost
	kSession
	kBlock
	kLabel
	kCwd
	kRest
	kOptHost  // -S HOST, or nothing
	kOptCwd   // -c DIR, or nothing
	kOptBlock // -b BLOCK, or nothing
	kOptNoFocus
)

type tok struct {
	k   kind
	lit string
}

func shape(ts ...any) []tok {
	var out []tok
	for _, t := range ts {
		switch v := t.(type) {
		case string:
			out = append(out, tok{k: kLit, lit: v})
		case kind:
			out = append(out, tok{k: v})
		}
	}
	return out
}

var (
	// A session is named by its id, or by the label of one this module is about to create.
	sessionRE = regexp.MustCompile(`^(?:session:[0-9A-Za-z-]{8,64}|[A-Za-z0-9][A-Za-z0-9._-]{0,63})$`)
	blockRE   = regexp.MustCompile(`^block:[0-9A-Za-z-]{8,64}$`)
	// A label is an item id or a fixed tab name: printable, no leading dash.
	labelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/ -]{0,63}$`)
)

// maxRestWords and maxRestRunes bound the command after `--`.
const (
	maxRestWords = 16
	maxRestRunes = 1024
)

func restOK(w string) bool {
	return utf8.RuneCountInString(w) <= maxRestRunes && utf8.ValidString(w) && core.Sanitize(w) == w
}

func cwdOK(c string) bool {
	return filepath.IsAbs(c) && filepath.Clean(c) == c && utf8.RuneCountInString(c) <= 1024 && core.Sanitize(c) == c
}

// match reports whether a is exactly the shape.
func match(shape []tok, a []string) bool {
	i := 0
	has := func(flag string) bool { return i+1 < len(a) && a[i] == flag }
	for _, t := range shape {
		switch t.k {
		case kLit:
			if i >= len(a) || a[i] != t.lit {
				return false
			}
			i++
		case kHost, kSession, kBlock, kLabel, kCwd:
			if i >= len(a) || !slotOK(t.k, a[i]) {
				return false
			}
			i++
		case kOptHost:
			if has("-S") {
				if !ValidHost(a[i+1]) {
					return false
				}
				i += 2
			}
		case kOptCwd:
			if has("-c") {
				if !cwdOK(a[i+1]) {
					return false
				}
				i += 2
			}
		case kOptBlock:
			if has("-b") {
				if !blockRE.MatchString(a[i+1]) {
					return false
				}
				i += 2
			}
		case kOptNoFocus:
			if i < len(a) && a[i] == "--focus=false" {
				i++
			}
		case kRest:
			rest := a[i:]
			if len(rest) == 0 || len(rest) > maxRestWords {
				return false
			}
			for _, w := range rest {
				if !restOK(w) {
					return false
				}
			}
			i = len(a)
		}
	}
	return i == len(a)
}

func slotOK(k kind, v string) bool {
	switch k {
	case kHost:
		return ValidHost(v)
	case kSession:
		return sessionRE.MatchString(v)
	case kBlock:
		return blockRE.MatchString(v)
	case kLabel:
		return labelRE.MatchString(v)
	case kCwd:
		return cwdOK(v)
	}
	return false
}

var (
	readShapes = [][]tok{
		shape(kOptHost, autostartOff, "ls", "--json"),
		shape(autostartOff, "version", "--json"),
		shape(kOptHost, autostartOff, "block", "ls", "-s", kSession, "--json"),
		shape(kOptHost, autostartOff, "window", "ls", "-s", kSession, "--json"),
		shape(kOptHost, autostartOff, "capture", "-s", kSession, "-b", kBlock, "--trim"),
	}
	streamShape = shape(autostartOff, "events", "--json")
	writeShapes = [][]tok{
		shape(kOptHost, autostartOff, "focus", "-s", kSession, kBlock),
		shape(autostartOff, "run", "-s", kSession, "--label", kLabel, kOptCwd, "--keep-open", "--json", "--", kRest),
		shape(autostartOff, "split", "-s", kSession, kOptBlock, "--split=right", "--label", kLabel, kOptCwd, "--keep-open", "--json", "--", kRest),
		shape(autostartOff, "new", kLabel, "--json", "--window", kLabel, kOptCwd, "--keep-open", "--", kRest),
		shape(autostartOff, "window", "new", "-s", kSession, kLabel, kOptCwd, "--keep-open", kOptNoFocus, "--json", "--", kRest),
	}
)

func matchAny(shapes [][]tok, a []string) bool {
	for _, s := range shapes {
		if match(s, a) {
			return true
		}
	}
	return false
}

func allowedArgv(a []string) bool       { return matchAny(readShapes, a) }
func allowedWriteArgv(a []string) bool  { return matchAny(writeShapes, a) }
func allowedStreamArgv(a []string) bool { return match(streamShape, a) }

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

// writeOnly rejects any argv outside the write shapes before it reaches the runner.
type writeOnly struct{ inner core.Runner }

func (w writeOnly) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	if !allowedWriteArgv(args) {
		return nil, -1, fmt.Errorf("rex %q is not an allowed workspace write", args)
	}
	return w.inner.Run(ctx, name, args...)
}
