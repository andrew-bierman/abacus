package rex

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The whole set of rex argvs this module may run, all read-only:
//
//	[-S HOST] --autostart=false ls --json
//	--autostart=false version --json
//
// --autostart=false is always present so a read can never start a server. Nothing that creates,
// renames, focuses, closes or types into a session is reachable, and neither is `api call`.
const autostartOff = "--autostart=false"

// lsArgv is the session list, for the local server (host "") or the one host a run carries.
func lsArgv(host string) []string {
	var a []string
	if host != "" {
		a = append(a, "-S", host)
	}
	return append(a, autostartOff, "ls", "--json")
}

func versionArgv() []string { return []string{autostartOff, "version", "--json"} }

// hostRE is what a -S host may look like: a name, user@name or name:port, never starting with
// a dash (which would be read as another option) and never containing spaces or shell
// metacharacters.
var hostRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:-]{0,252}$`)

// ValidHost reports whether host is safe to hand to `rex -S`.
func ValidHost(host string) bool { return hostRE.MatchString(host) }

func allowedArgv(a []string) bool {
	switch len(a) {
	case 3:
		return a[0] == autostartOff && a[1] == "ls" && a[2] == "--json" ||
			a[0] == autostartOff && a[1] == "version" && a[2] == "--json"
	case 5:
		return a[0] == "-S" && ValidHost(a[1]) && a[2] == autostartOff && a[3] == "ls" && a[4] == "--json"
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

// ScrubEnv is the environment rex runs in: HOME, PATH, the XDG directories (the server socket
// lives under XDG_RUNTIME_DIR) and the REX_* variables Rex itself sets. Everything else, such as
// tokens and other tools' settings, is dropped.
func ScrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if name == "HOME" || name == "PATH" || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "REX_") {
			out = append(out, e)
		}
	}
	return out
}

// maxOut caps what is read from one rex call.
const maxOut = 1 << 20

func defaultRunner() core.Runner {
	return core.ExecRunner{Scrub: ScrubEnv, MaxOut: maxOut}
}
