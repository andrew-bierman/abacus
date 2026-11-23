package core_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
	"github.com/klauspost/compress/zstd"
)

// embeddedIn installs a fake embedded set and an extraction root under a temp dir.
func embeddedIn(t *testing.T) (set *embedtest.Set, root string) {
	t.Helper()
	set = embedtest.Standard()
	t.Cleanup(core.SetEmbeddedForTest(set.FS))
	root = filepath.Join(t.TempDir(), "tools")
	t.Setenv(core.ToolsDirEnv, root)
	return set, root
}

func hashDir(t *testing.T, root string) string {
	t.Helper()
	es, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.IsDir() {
			return filepath.Join(root, e.Name())
		}
	}
	t.Fatal("no extraction directory in", root)
	return ""
}

func TestExtractsOnceIntoAPrivateCacheAndRunsIt(t *testing.T) {
	_, root := embeddedIn(t)
	p, why := core.ResolveEmbedded("bd")
	if why != "" {
		t.Fatal(why)
	}
	dir := hashDir(t, root)
	if p != filepath.Join(dir, "bd") {
		t.Fatalf("path %s not in %s", p, dir)
	}
	for path, want := range map[string]os.FileMode{root: 0o700, dir: 0o700, p: 0o700} {
		if fi, err := os.Lstat(path); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %v", path, fi.Mode().Perm(), err, want)
		}
	}
	out, err := exec.Command(p, "hi").Output()
	if err != nil || strings.TrimSpace(string(out)) != "bd hi" {
		t.Fatalf("the extracted tool must run: %q %v", out, err)
	}
	// A second call, and a fresh process-like state, extract nothing again.
	fi1, _ := os.Stat(p)
	for i := 0; i < 2; i++ {
		if i == 1 {
			t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
		}
		p2, why := core.ResolveEmbedded("bd")
		fi2, _ := os.Stat(p2)
		if why != "" || p2 != p || !os.SameFile(fi1, fi2) || !fi1.ModTime().Equal(fi2.ModTime()) {
			t.Fatalf("round %d: re-extracted or moved (%s %q)", i, p2, why)
		}
	}
	for _, e := range mustReadDir(t, dir) {
		if strings.HasPrefix(e, ".tmp-") {
			t.Errorf("temp file left behind: %s", e)
		}
	}
}

func mustReadDir(t *testing.T, d string) []string {
	t.Helper()
	es, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	return n
}

func TestModesDoNotDependOnTheUmask(t *testing.T) {
	_, root := embeddedIn(t)
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
	p, why := core.ResolveEmbedded("lambe")
	if why != "" {
		t.Fatal(why)
	}
	for _, path := range []string{root, filepath.Dir(p), p} {
		if fi, _ := os.Lstat(path); fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is open to others (%v) under umask 0", path, fi.Mode().Perm())
		}
	}
}

func recompress(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, _ := zstd.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestAWrongOrTruncatedOrOversizedBlobFailsClosedAsDamaged(t *testing.T) {
	cases := map[string]func(s *embedtest.Set){
		"same length, other bytes": func(s *embedtest.Set) {
			s.FS["bd.zst"].Data = recompress(t, []byte(strings.Repeat("x", len(embedtest.Script("bd")))))
		},
		"truncated blob":       func(s *embedtest.Set) { d := s.FS["bd.zst"].Data; s.FS["bd.zst"].Data = d[:len(d)/2] },
		"longer than declared": func(s *embedtest.Set) { s.FS["bd.zst"].Data = recompress(t, []byte(embedtest.Script("bd")+"extra")) },
		"blob missing":         func(s *embedtest.Set) { delete(s.FS, "bd.zst") },
		"manifest unreadable":  func(s *embedtest.Set) { s.FS["manifest.json"].Data = []byte("{not json") },
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			set, root := embeddedIn(t)
			damage(set)
			core.SetEmbeddedForTest(set.FS) // re-read the (damaged) manifest
			p, why := core.ResolveEmbedded("bd")
			if p != "" || !strings.Contains(why, "does not match what was built into abacus") {
				t.Fatalf("a damaged copy must fail closed as damaged: %q %q", p, why)
			}
			if _, err := os.Stat(root); err == nil {
				filepath.Walk(root, func(path string, fi os.FileInfo, _ error) error {
					if fi != nil && fi.Mode().IsRegular() && !strings.HasSuffix(path, ".lock") {
						t.Errorf("a failed extraction left %s", path)
					}
					return nil
				})
			}
			f, detail := core.LocateFound("bd", "", func(string) (string, error) { t.Fatal("PATH must not be consulted"); return "", nil })
			if f.Bin != "" || !strings.Contains(core.AbsentSentence("bd", detail), "This copy of abacus is damaged; download it again.") {
				t.Fatalf("%+v %q", f, detail)
			}
		})
	}
}

func TestAManifestCannotNameAPathOutsideTheCache(t *testing.T) {
	for _, name := range []string{"../evil", "a/b", "/abs", "", "BD", "bd\x00"} {
		set := embedtest.Standard()
		set.Manifest.Tools[0].Name = name
		set.Write()
		restore := core.SetEmbeddedForTest(set.FS)
		_, why := core.ResolveEmbedded("bd")
		restore()
		if why == "" {
			t.Errorf("name %q was accepted", name)
		}
	}
}

func TestAnUnwritableCacheSaysWhatFailedWithThePathAndTheOneFix(t *testing.T) {
	embeddedIn(t)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	t.Setenv(core.ToolsDirEnv, filepath.Join(blocker, "tools"))
	f, detail := core.LocateFound("bd", "", func(string) (string, error) { t.Fatal("PATH must not be consulted"); return "", nil })
	s := core.AbsentSentence("bd", detail)
	if f.Bin != "" || !strings.Contains(s, blocker) || !strings.Contains(s, "ABACUS_TOOLS_DIR=<folder>") || strings.Contains(strings.ToLower(s), "install") {
		t.Fatalf("%q", s)
	}
	t.Setenv(core.ToolsDirEnv, "relative/dir")
	core.ClearEmbeddedFailures()
	if _, why := core.ResolveEmbedded("bd"); !strings.Contains(why, "absolute") {
		t.Fatalf("%q", why)
	}
}

func TestALinkInTheCacheIsRefused(t *testing.T) {
	t.Run("hash directory is a link", func(t *testing.T) {
		set, root := embeddedIn(t)
		other := t.TempDir()
		os.MkdirAll(root, 0o700)
		os.Symlink(other, filepath.Join(root, set.Manifest.Hash()))
		if p, why := core.ResolveEmbedded("bd"); p != "" || !strings.Contains(why, "not a plain folder") {
			t.Fatalf("%q %q", p, why)
		}
		if len(mustReadDir(t, other)) != 0 {
			t.Fatal("something was written through the link")
		}
	})
	t.Run("the tool is a link", func(t *testing.T) {
		_, root := embeddedIn(t)
		p, _ := core.ResolveEmbedded("bd")
		os.Remove(p)
		target := filepath.Join(t.TempDir(), "evil")
		os.WriteFile(target, []byte("#!/bin/sh\necho evil\n"), 0o700)
		os.Symlink(target, p)
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS)) // a new run
		if got, why := core.ResolveEmbedded("bd"); got != "" || !strings.Contains(why, "not a plain file") {
			t.Fatalf("%q %q (%s)", got, why, root)
		}
	})
}

func TestACacheOthersCanWriteOrThatBelongsToSomeoneElseIsRefused(t *testing.T) {
	t.Run("directory open to the group", func(t *testing.T) {
		_, root := embeddedIn(t)
		p, _ := core.ResolveEmbedded("bd")
		os.Chmod(filepath.Dir(p), 0o770)
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
		if got, why := core.ResolveEmbedded("bd"); got != "" || !strings.Contains(why, "open to other users") {
			t.Fatalf("%q %q (%s)", got, why, root)
		}
	})
	t.Run("file the group can write", func(t *testing.T) {
		embeddedIn(t)
		p, _ := core.ResolveEmbedded("bd")
		os.Chmod(p, 0o770)
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
		if got, why := core.ResolveEmbedded("bd"); got != "" || !strings.Contains(why, "unsafe permissions") {
			t.Fatalf("%q %q", got, why)
		}
	})
	t.Run("file not executable", func(t *testing.T) {
		embeddedIn(t)
		p, _ := core.ResolveEmbedded("bd")
		os.Chmod(p, 0o600)
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
		if got, why := core.ResolveEmbedded("bd"); got != "" || !strings.Contains(why, "unsafe permissions") {
			t.Fatalf("%q %q", got, why)
		}
	})
	t.Run("owned by another user", func(t *testing.T) {
		embeddedIn(t)
		p, _ := core.ResolveEmbedded("bd")
		t.Cleanup(core.SetCurrentUIDForTest(func() int { return os.Getuid() + 1 }))
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
		if got, why := core.ResolveEmbedded("bd"); got != "" || !strings.Contains(why, "belongs to another user") {
			t.Fatalf("%q %q (%s)", got, why, p)
		}
	})
}

func TestADamagedExtractedFileIsReplacedNotTrusted(t *testing.T) {
	embeddedIn(t)
	p, _ := core.ResolveEmbedded("bd")
	good, _ := os.ReadFile(p)
	for name, damage := range map[string]func(){
		"a flipped byte":  func() { b := append([]byte(nil), good...); b[len(b)-2] ^= 0xff; os.WriteFile(p, b, 0o700) },
		"a short file":    func() { os.WriteFile(p, good[:5], 0o700) },
		"removed":         func() { os.Remove(p) },
		"marker removed":  func() { os.Remove(p + ".ok") },
		"marker is wrong": func() { os.WriteFile(p+".ok", []byte("v1 deadbeef 1 1\n"), 0o600) },
	} {
		damage()
		os.Chtimes(p, time.Now().Add(time.Hour), time.Now().Add(time.Hour)) // mtime no longer matches the marker
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
		got, why := core.ResolveEmbedded("bd")
		b, _ := os.ReadFile(got)
		sum := sha256.Sum256(b)
		want := sha256.Sum256(good)
		if why != "" || hex.EncodeToString(sum[:]) != hex.EncodeToString(want[:]) {
			t.Errorf("%s: %q, content restored: %v", name, why, bytes.Equal(b, good))
		}
	}
}

func TestStartingDoesNotHashAFileWhoseMarkerStillMatches(t *testing.T) {
	embeddedIn(t)
	p, _ := core.ResolveEmbedded("bd")
	b, _ := os.ReadFile(p)
	fi, _ := os.Stat(p)
	// Same size and the same mtime: only a full hash would notice. The documented tradeoff is that
	// this takes the user's own account, so a start does not pay for it.
	b[len(b)-2] ^= 0xff
	os.WriteFile(p, b, 0o700)
	os.Chtimes(p, fi.ModTime(), fi.ModTime())
	t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
	if got, why := core.ResolveEmbedded("bd"); got != p || why != "" {
		t.Fatalf("the marker no longer short-cuts the hash (%q %q): if every start now hashes, update the README tradeoff note", got, why)
	}
}

func TestConcurrentExtractionInOneProcessIsSafe(t *testing.T) {
	embeddedIn(t)
	var wg sync.WaitGroup
	paths := make([]string, 16)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], _ = core.ResolveEmbedded([]string{"bd", "lambe", "beads-lease", "board"}[i%4])
		}()
	}
	wg.Wait()
	for i, p := range paths {
		if p == "" || filepath.Base(p) != []string{"bd", "lambe", "beads-lease", "board"}[i%4] {
			t.Fatalf("%d: %q", i, p)
		}
	}
}

// TestHelperExtract is the child of TestTwoProcessesCannotRaceOnExtraction.
func TestHelperExtract(t *testing.T) {
	if os.Getenv("ABACUS_TEST_HELPER_EXTRACT") == "" {
		t.Skip("helper process")
	}
	core.SetEmbeddedForTest(embedtest.Standard().FS)
	var out []string
	for _, tool := range core.KnownTools {
		p, why := core.ResolveEmbedded(tool)
		if why != "" {
			p = "ERR " + why
		}
		out = append(out, p)
	}
	fmt.Println("RESULT " + strings.Join(out, ";"))
}

func TestTwoProcessesCannotRaceOnExtraction(t *testing.T) {
	_, root := embeddedIn(t)
	var wg sync.WaitGroup
	results := make([]string, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestHelperExtract$")
			cmd.Env = append(os.Environ(), "ABACUS_TEST_HELPER_EXTRACT=1", core.ToolsDirEnv+"="+root)
			out, err := cmd.CombinedOutput()
			if err != nil {
				results[i] = "error: " + err.Error() + string(out)
				return
			}
			for _, l := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(l, "RESULT ") {
					results[i] = strings.TrimPrefix(l, "RESULT ")
				}
			}
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r == "" || strings.Contains(r, "error") || strings.Contains(r, "ERR") || r != results[0] {
			t.Fatalf("process %d: %q (first %q)", i, r, results[0])
		}
	}
	dir := hashDir(t, root)
	for _, n := range mustReadDir(t, dir) {
		if strings.HasPrefix(n, ".tmp-") {
			t.Errorf("temp file left behind: %s", n)
		}
	}
}

func TestADifferentToolSetGetsADifferentDirectory(t *testing.T) {
	a, root := embeddedIn(t)
	b := embedtest.Standard()
	b.Manifest.Tools[0].SHA256 = strings.Repeat("a", 64)
	b.Write()
	if a.Manifest.Hash() == b.Manifest.Hash() {
		t.Fatal("the hash ignores the executable hashes")
	}
	pa, _ := core.ResolveEmbedded("lambe")
	if filepath.Dir(pa) != filepath.Join(root, a.Manifest.Hash()) {
		t.Fatalf("set A extracted to %s, want %s", filepath.Dir(pa), filepath.Join(root, a.Manifest.Hash()))
	}
	// Set B differs in its first tool (beads-lease sorts before bd? the first by name), so it lives elsewhere.
	t.Cleanup(core.SetEmbeddedForTest(b.FS))
	pb, _ := core.ResolveEmbedded("lambe")
	if filepath.Dir(pb) != filepath.Join(root, b.Manifest.Hash()) || filepath.Dir(pa) == filepath.Dir(pb) {
		t.Fatalf("set B extracted to %s, want %s (and not A's %s)", filepath.Dir(pb), filepath.Join(root, b.Manifest.Hash()), filepath.Dir(pa))
	}
}

func TestNoEnvironmentVariableCanReplaceTheEmbeddedHashes(t *testing.T) {
	set, _ := embeddedIn(t)
	for _, k := range []string{"ABACUS_BD_SHA256", "ABACUS_TOOLS_SHA256", "ABACUS_SKIP_VERIFY", "ABACUS_TOOLS_HASH"} {
		t.Setenv(k, strings.Repeat("0", 64))
	}
	set.FS["bd.zst"].Data = recompress(t, []byte(strings.Repeat("x", len(embedtest.Script("bd")))))
	core.SetEmbeddedForTest(set.FS)
	if p, why := core.ResolveEmbedded("bd"); p != "" || why == "" {
		t.Fatalf("%q %q", p, why)
	}
}

func TestLocatePrecedenceOverrideThenBuiltInThenSidecarThenPathInDevOnly(t *testing.T) {
	sidecar := t.TempDir()
	os.WriteFile(filepath.Join(sidecar, "bd"), []byte("#!/bin/sh\n"), 0o755)
	restoreDirs := core.SidecarDirs
	core.SidecarDirs = func() []string { return []string{sidecar} }
	t.Cleanup(func() { core.SidecarDirs = restoreDirs })
	pathCalls := 0
	look := func(n string) (string, error) { pathCalls++; return "/on/path/" + n, nil }
	override := filepath.Join(t.TempDir(), "mybd")
	os.WriteFile(override, []byte("#!/bin/sh\n"), 0o755)

	_, root := embeddedIn(t)
	if f, d := core.LocateFound("bd", override, look); d != "" || f.Src != core.SourceOverride || f.Bin != override {
		t.Fatalf("override: %+v %q", f, d)
	}
	f, d := core.LocateFound("bd", "", look)
	if d != "" || f.Src != core.SourceEmbedded || !strings.HasPrefix(f.Bin, root) {
		t.Fatalf("built-in beats the sidecar and PATH: %+v %q", f, d)
	}
	if pathCalls != 0 {
		t.Fatal("PATH was consulted in an embedded build")
	}
	// A tool abacus does not carry still uses the sidecar then PATH in an embedded build.
	if f, _ := core.LocateFound("rex", "", look); f.Bin != "/on/path/rex" {
		t.Fatalf("%+v", f)
	}

	// Development build: sidecar, then PATH.
	t.Cleanup(core.SetEmbeddedForTest(nil))
	if f, _ := core.LocateFound("bd", "", look); f.Src != core.SourceBundled || filepath.Base(f.Bin) != "bd" {
		t.Fatalf("dev sidecar: %+v", f)
	}
	core.SidecarDirs = func() []string { return nil }
	if f, _ := core.LocateFound("bd", "", look); f.Src != core.SourcePATH || f.Bin != "/on/path/bd" {
		t.Fatalf("dev PATH: %+v", f)
	}
}

func TestAnEmbeddedBuildNeverFallsBackToPathOrASidecarWhenTheBuiltInFails(t *testing.T) {
	set, _ := embeddedIn(t)
	sidecar := t.TempDir()
	os.WriteFile(filepath.Join(sidecar, "bd"), []byte("#!/bin/sh\n"), 0o755)
	restoreDirs := core.SidecarDirs
	core.SidecarDirs = func() []string { return []string{sidecar} }
	t.Cleanup(func() { core.SidecarDirs = restoreDirs })
	delete(set.FS, "bd.zst")
	core.SetEmbeddedForTest(set.FS)
	called := 0
	f, d := core.LocateFound("bd", "", func(n string) (string, error) { called++; return "/usr/bin/bd", nil })
	if f.Bin != "" || d == "" || called != 0 {
		t.Fatalf("a different bd must never run in place of the built-in one: %+v %q (PATH asked %d times)", f, d, called)
	}
}

func TestSidecarEnvExposesOnlyBuiltInToolsAlreadySetUpAndExtractsNothing(t *testing.T) {
	_, root := embeddedIn(t)
	t.Cleanup(core.CleanupSidecarEnv)
	pathOf := func() string {
		for _, e := range core.SidecarEnv([]string{"PATH=/usr/bin"}) {
			if v, ok := strings.CutPrefix(e, "PATH="); ok {
				return v
			}
		}
		return ""
	}
	if got := pathOf(); got != "/usr/bin" {
		t.Fatalf("with nothing set up the PATH is left alone: %q", got)
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("building a child's environment must not extract anything")
	}
	bd, _ := core.ResolveEmbedded("bd")
	dir, _, _ := strings.Cut(pathOf(), string(os.PathListSeparator))
	target, err := os.Readlink(filepath.Join(dir, "bd"))
	if err != nil || target != bd {
		t.Fatalf("bd should link to the extracted copy: %q %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "board")); err == nil {
		t.Fatal("the Board CLI was not set up and must not be exposed (or extracted) for a child")
	}
}
