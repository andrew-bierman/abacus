package core_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestLocateResolvesAToolTheWayAUserWouldExpect(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(exe, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	found := func(name string) (string, error) { return "/on/path/" + name, nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }

	cases := []struct {
		name, explicit string
		look           func(string) (string, error)
		wantBin        string
		wantAbsent     string // substring of the absent detail; "" means found
	}{
		{"no override uses PATH", "", found, "/on/path/tool", ""},
		{"no override and not on PATH", "", missing, "", "tool not found on PATH"},
		{"an explicit path is checked on disk", exe, missing, exe, ""},
		{"an explicit path that does not exist", "/nonexistent/tool", found, "", "tool not found at /nonexistent/tool"},
		{"a bare name is resolved on PATH, not the cwd", "tool", found, "/on/path/tool", ""},
		{"a bare name that is not on PATH", "tool", missing, "", "tool not found on PATH"},
	}
	for _, c := range cases {
		bin, detail := core.Locate("tool", c.explicit, c.look)
		if bin != c.wantBin || (c.wantAbsent == "") != (detail == "") || !strings.Contains(detail, c.wantAbsent) {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, bin, detail, c.wantBin, c.wantAbsent)
		}
	}
}
