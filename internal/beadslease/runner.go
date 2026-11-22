package beadslease

import (
	"context"
	"fmt"

	"github.com/andrew-bierman/abacus/internal/core"
)

// allowedArgv is every argv this module may run, matched element by element. beads-lease
// shares a production hub, so the module can only ever read: no acquire, release, heartbeat,
// run or sync is reachable. The plain "version" is the legacy form for binaries that predate
// `version --json`.
func allowedArgv(a []string) bool {
	switch len(a) {
	case 1:
		return a[0] == "version"
	case 2:
		return a[0] == "version" && a[1] == "--json" ||
			a[0] == "doctor" && a[1] == "--json" ||
			a[0] == "--json" && a[1] == "list"
	}
	return false
}

// readOnly wraps a Runner and rejects any argv outside the allowlist before it reaches
// the underlying runner.
type readOnly struct{ inner core.Runner }

func (r readOnly) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	if !allowedArgv(args) {
		return nil, -1, fmt.Errorf("beads-lease %q is not allowed: this module is read-only", args)
	}
	return r.inner.Run(ctx, name, args...)
}
