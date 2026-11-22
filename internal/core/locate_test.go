package core_test

import (
	"encoding/json"
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

// fakeTool writes an executable stand-in for a tool into dir with the given mode.
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

func real(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func noPath(string) (string, error) { return "", errors.New("no") }

func TestLocatePrefersABundledSidecarOverPATHAndReturnsTheResolvedPath(t *testing.T) {
	root := t.TempDir()
	side := fakeTool(t, filepath.Join(root, "bundled"), "bd", 0o755)
	withSidecarDirs(t, filepath.Join(root, "bundled"))
	onPath := func(n string) (string, error) { return "/on/path/" + n, nil }

	f, detail := core.LocateFound("bd", "", onPath)
	if f.Bin != real(t, side) || f.Src != core.SourceBundled || detail != "" || f.Ignored != "" {
		t.Fatalf("got (%+v, %q), want the resolved sidecar %q as bundled", f, detail, real(t, side))
	}
}

func TestLocateExecsExactlyTheResolvedPathNotTheSymlink(t *testing.T) {
	root := t.TempDir()
	target := fakeTool(t, filepath.Join(root, "store"), "bd-real", 0o755)
	dir := filepath.Join(root, "bundled")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "bd")); err != nil {
		t.Fatal(err)
	}
	withSidecarDirs(t, dir)
	f, _ := core.LocateFound("bd", "", noPath)
	if f.Bin != real(t, target) {
		t.Fatalf("a symlink to a safe target is accepted, and the target is what runs: %q", f.Bin)
	}
}

func TestLocateRefusesASymlinkToAnUnsafeTarget(t *testing.T) {
	root := t.TempDir()
	target := fakeTool(t, filepath.Join(root, "store"), "bd-real", 0o777)
	dir := filepath.Join(root, "bundled")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "bd")); err != nil {
		t.Fatal(err)
	}
	withSidecarDirs(t, dir)
	if f, detail := core.LocateFound("bd", "", noPath); f.Bin != "" || !strings.Contains(detail, "writable") {
		t.Fatalf("%+v %q", f, detail)
	}
}

func TestLocateFallsBackToPATHWhenNoSidecar(t *testing.T) {
	withSidecarDirs(t, t.TempDir())
	f, _ := core.LocateFound("bd", "", func(n string) (string, error) { return "/on/path/" + n, nil })
	if f.Bin != "/on/path/bd" || f.Src != core.SourcePATH || f.Ignored != "" {
		t.Fatalf("%+v", f)
	}
}

func TestLocateSidecarDirsAreTriedInOrder(t *testing.T) {
	root := t.TempDir()
	first := fakeTool(t, filepath.Join(root, "a"), "bd", 0o755)
	fakeTool(t, filepath.Join(root, "b"), "bd", 0o755)
	withSidecarDirs(t, filepath.Join(root, "a"), filepath.Join(root, "b"))
	if f, _ := core.LocateFound("bd", "", nil); f.Bin != real(t, first) {
		t.Fatalf("got %q, want the first directory's %q", f.Bin, first)
	}
}

func TestLocateAnExplicitOverrideBeatsTheSidecar(t *testing.T) {
	root := t.TempDir()
	fakeTool(t, filepath.Join(root, "bundled"), "bd", 0o755)
	mine := fakeTool(t, filepath.Join(root, "mine"), "bd", 0o755)
	withSidecarDirs(t, filepath.Join(root, "bundled"))
	f, _ := core.LocateFound("bd", mine, nil)
	if f.Bin != mine || f.Src != core.SourceOverride {
		t.Fatalf("got %+v, want the override", f)
	}
}

func TestLocateNeverUsesAnUnsafeSidecar(t *testing.T) {
	cases := map[string]struct {
		mode os.FileMode
		why  string
	}{
		"world-writable": {0o757, "world-writable"},
		"group-writable": {0o775, "group-writable"},
		"not executable": {0o644, "not executable"},
	}
	for name, c := range cases {
		dir := filepath.Join(t.TempDir(), "bundled")
		fakeTool(t, dir, "bd", c.mode)
		withSidecarDirs(t, dir)
		f, detail := core.LocateFound("bd", "", noPath)
		if f.Bin != "" || !strings.Contains(detail, "bd not found") || !strings.Contains(detail, "ignored") || !strings.Contains(detail, c.why) {
			t.Errorf("%s: (%+v, %q)", name, f, detail)
		}
	}
	dir := filepath.Join(t.TempDir(), "bundled")
	if err := os.MkdirAll(filepath.Join(dir, "bv"), 0o755); err != nil { // a directory is not a binary
		t.Fatal(err)
	}
	withSidecarDirs(t, dir)
	if f, _ := core.LocateFound("bv", "", noPath); f.Bin != "" {
		t.Errorf("a directory was accepted: %+v", f)
	}
}

func TestLocateRefusesAWritableDirectoryIncludingStickyUnlessRootOwned(t *testing.T) {
	for name, mode := range map[string]os.FileMode{"world-writable": 0o777, "group-writable": 0o775, "sticky world-writable": 0o777 | os.ModeSticky} {
		dir := filepath.Join(t.TempDir(), "bundled")
		fakeTool(t, dir, "bd", 0o755)
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		withSidecarDirs(t, dir)
		f, detail := core.LocateFound("bd", "", noPath)
		if f.Bin != "" || !strings.Contains(detail, "directory") {
			t.Errorf("%s: (%+v, %q)", name, f, detail)
		}
	}
}

func TestLocateRefusesAFileOrDirectoryOwnedBySomeoneElse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bundled")
	fakeTool(t, dir, "bd", 0o755)
	withSidecarDirs(t, dir)
	restore := core.SetCurrentUIDForTest(func() int { return os.Getuid() + 4242 })
	defer restore()
	f, detail := core.LocateFound("bd", "", noPath)
	if os.Getuid() == 0 { // root owns everything it may use
		return
	}
	if f.Bin != "" || !strings.Contains(detail, "owned by") {
		t.Fatalf("(%+v, %q)", f, detail)
	}
}

func TestLocateSkipsAnUnsafeSidecarForASafeOneLater(t *testing.T) {
	root := t.TempDir()
	fakeTool(t, filepath.Join(root, "a"), "bd", 0o777)
	good := fakeTool(t, filepath.Join(root, "b"), "bd", 0o755)
	withSidecarDirs(t, filepath.Join(root, "a"), filepath.Join(root, "b"))
	if f, _ := core.LocateFound("bd", "", nil); f.Bin != real(t, good) {
		t.Fatalf("got %q, want %q", f.Bin, good)
	}
}

func TestAnIgnoredSidecarIsCarriedOnTheResultWhenPATHIsUsed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bundled")
	fakeTool(t, dir, "bd", 0o777)
	withSidecarDirs(t, dir)
	f, detail := core.LocateFound("bd", "", func(n string) (string, error) { return "/on/path/" + n, nil })
	if detail != "" || f.Src != core.SourcePATH || f.Bin != "/on/path/bd" || !strings.Contains(f.Ignored, "world-writable") {
		t.Fatalf("a rejected bundled tool must not vanish silently: (%+v, %q)", f, detail)
	}
}

func TestSidecarDirsFollowTheExecutableThroughASymlinkAndAreAbsolute(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "abacus-1.0")
	fakeTool(t, realDir, "abacus", 0o755)
	link := filepath.Join(root, "bin", "abacus")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(realDir, "abacus"), link); err != nil {
		t.Fatal(err)
	}
	realDir, _ = filepath.EvalSymlinks(realDir) // t.TempDir may itself sit behind a symlink (macOS /var)
	want := []string{realDir, filepath.Join(realDir, "..", "libexec", "abacus"), filepath.Join(realDir, "bundled")}
	got := core.SidecarDirsFor(link)
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != filepath.Clean(want[i]) {
			t.Errorf("dir %d: got %q, want %q", i, got[i], filepath.Clean(want[i]))
		}
	}
	// A relative executable path is made absolute rather than searched relative to the cwd.
	t.Chdir(realDir)
	for _, d := range core.SidecarDirsFor("abacus") {
		if !filepath.IsAbs(d) {
			t.Errorf("relative sidecar dir %q", d)
		}
	}
}

func pathOf(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			return v
		}
	}
	return ""
}

func TestSidecarEnvPutsAPrivateDirOfVerifiedLinksFirstNotTheWholeSidecarDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bundled")
	bd := fakeTool(t, dir, "bd", 0o755)
	fakeTool(t, dir, "extra-binary", 0o755) // not a known tool: must not become visible
	withSidecarDirs(t, dir, filepath.Join(root, "empty"))
	defer core.CleanupSidecarEnv()
	env := core.SidecarEnv([]string{"A=1", "PATH=/usr/bin"})
	first, rest, _ := strings.Cut(pathOf(env), string(os.PathListSeparator))
	if rest != "/usr/bin" || first == dir {
		t.Fatalf("PATH = %q", pathOf(env))
	}
	if got, err := filepath.EvalSymlinks(filepath.Join(first, "bd")); err != nil || got != real(t, bd) {
		t.Fatalf("the private dir must hold a link to the verified binary: %q %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(first, "extra-binary")); err == nil {
		t.Fatal("only known, verified tools are exposed")
	}
	if fi, _ := os.Stat(first); fi.Mode().Perm() != 0o700 {
		t.Fatalf("the private dir must be 0700, got %v", fi.Mode().Perm())
	}
}

func TestSidecarEnvOmitsUnsafeDirsAndFiles(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "bad")
	fakeTool(t, bad, "bd", 0o777) // world-writable file
	wdir := filepath.Join(root, "wdir")
	fakeTool(t, wdir, "bv", 0o755)
	if err := os.Chmod(wdir, 0o777); err != nil { // world-writable dir
		t.Fatal(err)
	}
	withSidecarDirs(t, bad, wdir)
	defer core.CleanupSidecarEnv()
	env := []string{"PATH=/usr/bin"}
	if got := core.SidecarEnv(env); pathOf(got) != "/usr/bin" {
		t.Fatalf("an unsafe sidecar must not reach a child's PATH: %q", pathOf(got))
	}
}

func TestTheManifestShipsExactlyTheToolsAbacusLooksFor(t *testing.T) {
	b, err := os.ReadFile("../../bundled-tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range m.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != strings.Join(core.KnownTools, ",") {
		t.Fatalf("manifest tools %v != core.KnownTools %v: a shipped tool abacus never looks for, or the reverse", names, core.KnownTools)
	}
}
