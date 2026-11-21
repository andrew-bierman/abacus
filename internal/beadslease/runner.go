package beadslease

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// allowed is every argv this module may run. beads-lease shares a production hub, so the
// module can only ever read: no acquire, release, heartbeat, run or sync is reachable.
var allowed = map[string]bool{"version": true, "--json list": true}

// readOnly wraps a Runner and rejects any argv outside the allowlist before it reaches
// the underlying runner.
type readOnly struct{ inner core.Runner }

func (r readOnly) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	if key := strings.Join(args, " "); !allowed[key] {
		return nil, -1, fmt.Errorf("beads-lease %q is not allowed: this module is read-only", key)
	}
	return r.inner.Run(ctx, name, args...)
}
