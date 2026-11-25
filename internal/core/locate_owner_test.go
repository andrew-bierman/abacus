//go:build unix

package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeToolRejectsUnknownOwnership(t *testing.T) {
	dir := t.TempDir()
	const tool = "fixture-tool"
	if err := os.WriteFile(filepath.Join(dir, tool), []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		dir  bool
		want string
	}{
		{name: "file", want: "ownership cannot be verified"},
		{name: "directory", dir: true, want: "its directory: ownership cannot be verified"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old := sidecarOwnerUID
			t.Cleanup(func() { sidecarOwnerUID = old })
			sidecarOwnerUID = func(fi os.FileInfo) (int, bool) {
				if fi.IsDir() == tt.dir {
					return 0, false
				}
				return old(fi)
			}

			if got, why := safeTool(dir, tool); got != "" || why != tt.want {
				t.Fatalf("safeTool = (%q, %q), want (%q, %q)", got, why, "", tt.want)
			}
		})
	}
}
