package agents

import (
	"context"
	"os"
	"testing"
)

// The real process list and terminals are never read by tests: every test builds its feed from
// injected sources, and a feed built without them panics here instead of reading the machine.
func TestMain(m *testing.M) {
	realProcs = func(context.Context) ([]Proc, error) { panic("a test read the real process list") }
	realPanes = func(context.Context) ([]Pane, error) { panic("a test read the real terminals") }
	os.Exit(m.Run())
}
