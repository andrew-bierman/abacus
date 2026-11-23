package lambe

import (
	"os"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// These tests describe a development build (no tools built in) unless a test sets up a fake tool
// set itself, so they pass under -tags embedtools too, where the real blobs would otherwise be seen.
func TestMain(m *testing.M) {
	core.SetEmbeddedForTest(nil)
	os.Exit(m.Run())
}
