package cli_test

import (
	"os"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// See internal/app/main_test.go.
func TestMain(m *testing.M) {
	core.SetEmbeddedForTest(nil)
	os.Exit(m.Run())
}
