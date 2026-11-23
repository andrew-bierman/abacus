package core

import (
	"os"
	"testing"
)

// These tests describe a development build (no tools built in) unless a test sets up a fake tool
// set itself, so they pass under -tags embedtools too.
func TestMain(m *testing.M) {
	SetEmbeddedForTest(nil)
	os.Exit(m.Run())
}
