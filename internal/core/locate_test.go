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

// fakeTool writes an executable stand-in for a tool into dir.
func fakeTool(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // WriteFile is subject to the umask
		t.Fatal(err)
	}
	return p
}

func withSidecarDirs(t *testing.T, dirs ...string) {
	t.Helper()
	old := core.SidecarDirs
	core.SidecarDirs = func() []string { return dirs }
	t.Cleanup(func() { core.SidecarDirs = old })
}

func TestLocatePrefersABundledSidecarOverPATH(t *testing.T) {
	root := t.TempDir()
	side := fakeTool(t, filepath.Join(root, "bundled"), "bd", 0o755)
	withSidecarDirs(t, filepath.Join(root, "bundled"))
	onPath := func(n string) (string, error) { return "/on/path/" + n, nil }

	bin, src, detail := core.LocateSource("bd", "", onPath)
	if bin != side || src != core.SourceBundled || detail != "" {
		t.Fatalf("got (%q, %q, %q), want the sidecar %q as bundled", bin, src, detail, side)
	}
}

func TestLocateFallsBackToPATHWhenNoSidecar(t *testing.T) {
	withSidecarDirs(t, t.TempDir())
	bin, src, _ := core.LocateSource("bd", "", func(n string) (string, error) { return "/on/path/" + n, nil })
	if bin != "/on/path/bd" || src != core.SourcePATH {
		t.Fatalf("got (%q, %q)", bin, src)
	}
}

func TestLocateSidecarDirsAreTriedInOrder(t *testing.T) {
	root := t.TempDir()
	first := fakeTool(t, filepath.Join(root, "a"), "bd", 0o755)
	fakeTool(t, filepath.Join(root, "b"), "bd", 0o755)
	withSidecarDirs(t, filepath.Join(root, "a"), filepath.Join(root, "b"))
	if bin, _, _ := core.LocateSource("bd", "", nil); bin != first {
		t.Fatalf("got %q, want the first directory's %q", bin, first)
	}
}

func TestLocateAnExplicitOverrideBeatsTheSidecar(t *testing.T) {
	root := t.TempDir()
	fakeTool(t, filepath.Join(root, "bundled"), "bd", 0o755)
	mine := fakeTool(t, filepath.Join(root, "mine"), "bd", 0o755)
	withSidecarDirs(t, filepath.Join(root, "bundled"))
	bin, src, _ := core.LocateSource("bd", mine, nil)
	if bin != mine || src != core.SourceOverride {
		t.Fatalf("got (%q, %q), want the override", bin, src)
	}
}

func TestLocateNeverUsesAnUnsafeSidecar(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bundled")
	fakeTool(t, dir, "bd", 0o777)                                        // world-writable
	if err := os.MkdirAll(filepath.Join(dir, "bv"), 0o755); err != nil { // a directory is not a binary
		t.Fatal(err)
	}
	fakeTool(t, dir, "lambe", 0o644) // not executable
	withSidecarDirs(t, dir)
	for _, tool := range []string{"bd", "bv", "lambe"} {
		bin, _, detail := core.LocateSource(tool, "", func(string) (string, error) { return "", errors.New("no") })
		if bin != "" || !strings.Contains(detail, tool+" not found") {
			t.Errorf("%s: unsafe sidecar was accepted or the detail is wrong: (%q, %q)", tool, bin, detail)
		}
	}
	_, _, detail := core.LocateSource("bd", "", func(string) (string, error) { return "", errors.New("no") })
	if !strings.Contains(detail, "ignored") || !strings.Contains(detail, "writable") {
		t.Errorf("the detail should say why the sidecar was ignored: %q", detail)
	}
}

func TestLocateSkipsAnUnsafeSidecarForASafeOneLater(t *testing.T) {
	root := t.TempDir()
	fakeTool(t, filepath.Join(root, "a"), "bd", 0o777)
	good := fakeTool(t, filepath.Join(root, "b"), "bd", 0o755)
	withSidecarDirs(t, filepath.Join(root, "a"), filepath.Join(root, "b"))
	if bin, _, _ := core.LocateSource("bd", "", nil); bin != good {
		t.Fatalf("got %q, want %q", bin, good)
	}
}

func TestSidecarDirsFollowTheExecutableThroughASymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "abacus-1.0")
	fakeTool(t, real, "abacus", 0o755)
	link := filepath.Join(root, "bin", "abacus")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "abacus"), link); err != nil {
		t.Fatal(err)
	}
	real, _ = filepath.EvalSymlinks(real) // t.TempDir may itself sit behind a symlink (macOS /var)
	want := []string{real, filepath.Join(real, "..", "libexec", "abacus"), filepath.Join(real, "bundled")}
	got := core.SidecarDirsFor(link)
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != filepath.Clean(want[i]) {
			t.Errorf("dir %d: got %q, want %q", i, got[i], filepath.Clean(want[i]))
		}
	}
}

func TestSidecarEnvPutsSidecarDirsFirstOnPATH(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bundled")
	fakeTool(t, dir, "bd", 0o755)
	withSidecarDirs(t, dir, filepath.Join(root, "empty")) // the second holds no tool
	env := core.SidecarEnv([]string{"A=1", "PATH=/usr/bin"})
	var path string
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			path = e
		}
	}
	if path != "PATH="+dir+string(os.PathListSeparator)+"/usr/bin" {
		t.Fatalf("PATH = %q", path)
	}
}
