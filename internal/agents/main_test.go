package agents

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The real process list and terminals are never read by tests: every test builds its feed from
// injected sources, and a feed built without them panics here instead of reading the machine.
func TestMain(m *testing.M) {
	realProcs = func(context.Context) ([]Proc, error) { panic("a test read the real process list") }
	realPanes = func(context.Context) ([]Pane, error) { panic("a test read the real terminals") }
	realOpenCode = func(context.Context, string, time.Time, int) ([]opencodeSession, error) {
		panic("a test opened a real OpenCode database")
	}
	realDirty = func(context.Context, string) (core.AgentGit, bool) { panic("a test ran git") }
	os.Exit(m.Run())
}
