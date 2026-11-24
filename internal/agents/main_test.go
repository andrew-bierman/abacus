package agents

import (
	"os"
	"testing"
)

// The real process list, session folders and terminals are never read by tests: every test builds
// its feed from injected sources. This guard makes a forgotten injection fail loudly.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
