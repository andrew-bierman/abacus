package core_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
)

func flockExclusive(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// oldDir makes another version's extraction directory with a last-used stamp of the given age and a
// directory mtime of dirAge (negative stamp age: no stamp at all).
func oldDir(t *testing.T, root, name string, stampAge, dirAge time.Duration) string {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "bd"), []byte("old"), 0o700)
	os.WriteFile(d+".lock", nil, 0o600)
	if stampAge >= 0 {
		st := filepath.Join(d, core.UsedStamp)
		os.WriteFile(st, nil, 0o600)
		at := time.Now().Add(-stampAge)
		os.Chtimes(st, at, at)
	}
	at := time.Now().Add(-dirAge)
	os.Chtimes(d, at, at)
	return d
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func fullSet(t *testing.T) {
	t.Helper()
	for _, tool := range core.KnownTools {
		if p, why := core.ResolveEmbedded(tool); p == "" {
			t.Fatal(why)
		}
	}
}

func TestPruneKeepsAVersionAnotherProcessHoldsTheLockOn(t *testing.T) {
	_, root := embeddedIn(t)
	defer core.SetPruneForTest(time.Hour, 0)()
	os.MkdirAll(root, 0o700)
	d := oldDir(t, root, "0123456789abcdef", 48*time.Hour, 48*time.Hour)
	use := flockExclusive(t, d+".use") // a process running from it holds its in-use lock
	fullSet(t)
	if !exists(d) {
		t.Fatal("a version a process is running from must never be removed")
	}
	use.Close()
	lk := flockExclusive(t, d+".lock") // or one that is preparing it
	core.PruneForTest()
	if !exists(d) {
		t.Fatal("a version someone is preparing must never be removed")
	}
	lk.Close()
	core.PruneForTest()
	if exists(d) {
		t.Fatal("with both locks free and the version stale it is removed")
	}
	if !exists(d+".lock") || !exists(d+".use") {
		t.Fatal("lock files are never removed")
	}
}

func TestPruneDecidesByLastUseNotDirectoryMtime(t *testing.T) {
	_, root := embeddedIn(t)
	defer core.SetPruneForTest(time.Hour, 0)()
	os.MkdirAll(root, 0o700)
	newerDir := oldDir(t, root, "1111111111111111", -1, 0)                       // no stamp, directory touched just now
	recentUse := oldDir(t, root, "2222222222222222", time.Minute, 400*time.Hour) // used a minute ago, old directory
	stale := oldDir(t, root, "3333333333333333", 100*time.Hour, 0)               // stamp old, directory mtime NEW
	fullSet(t)
	if !exists(newerDir) || !exists(recentUse) {
		t.Fatal("a version with a newer mtime or a recent last-use stamp must be kept")
	}
	if exists(stale) {
		t.Fatal("a version unused beyond the window is pruned whatever its directory mtime says")
	}
}

func TestPruneAlwaysKeepsTheNewestTwoOtherVersions(t *testing.T) {
	_, root := embeddedIn(t)
	defer core.SetPruneForTest(time.Hour, 2)()
	os.MkdirAll(root, 0o700)
	var ds []string
	for i, n := range []string{"aaaaaaaaaaaaaaa1", "aaaaaaaaaaaaaaa2", "aaaaaaaaaaaaaaa3", "aaaaaaaaaaaaaaa4"} {
		ds = append(ds, oldDir(t, root, n, time.Duration(100+i)*time.Hour, 0)) // 1 is the most recently used
	}
	fullSet(t)
	if !exists(ds[0]) || !exists(ds[1]) || exists(ds[2]) || exists(ds[3]) {
		t.Fatalf("the two newest stale versions stay, the rest go: %v %v %v %v", exists(ds[0]), exists(ds[1]), exists(ds[2]), exists(ds[3]))
	}
}

func TestPruneWaitsUntilTheCurrentVersionIsComplete(t *testing.T) {
	_, root := embeddedIn(t)
	defer core.SetPruneForTest(time.Hour, 0)()
	os.MkdirAll(root, 0o700)
	d := oldDir(t, root, "0123456789abcdef", 100*time.Hour, 100*time.Hour)
	core.ResolveEmbedded("bd") // one of four
	if !exists(d) {
		t.Fatal("the previous version stays until the new one is complete")
	}
	notHash := filepath.Join(root, "keep-me")
	os.MkdirAll(notHash, 0o700)
	link := t.TempDir()
	os.WriteFile(filepath.Join(link, "precious"), nil, 0o600)
	os.Symlink(link, filepath.Join(root, "fedcba9876543210"))
	for _, tool := range []string{"lambe", "beads-lease", "board"} {
		core.ResolveEmbedded(tool)
	}
	if exists(d) || !exists(notHash) || !exists(filepath.Join(link, "precious")) {
		t.Fatal("complete current version prunes the stale one only; non-hash names and links are untouched")
	}
}

func TestARunningProcessSurvivesAPruneByResolvingAgain(t *testing.T) {
	_, root := embeddedIn(t)
	p, _ := core.ResolveEmbedded("bd")
	if err := core.CheckBuiltIn(p); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Dir(p)) // another abacus pruned the directory under this process
	if err := core.CheckBuiltIn(p); err == nil {
		t.Fatal("a path that is gone must not be executed")
	}
	p2, why := core.ResolveEmbedded("bd")
	if why != "" || !exists(p2) || !strings.HasPrefix(p2, root) {
		t.Fatalf("the process must extract again, not keep the dead path: %q %q", p2, why)
	}
	if out, err := exec.Command(p2).Output(); err != nil || !strings.Contains(string(out), "bd") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestACachedPathIsRecheckedBeforeEachUse(t *testing.T) {
	embeddedIn(t)
	p, _ := core.ResolveEmbedded("bd")
	os.WriteFile(p, []byte("#!/bin/sh\necho evil\n"), 0o700) // replaced after it was verified
	if _, _, err := (core.ExecRunner{}).Run(context.Background(), p); err == nil || !strings.Contains(err.Error(), "changed after abacus checked it") {
		t.Fatalf("a replaced built-in copy must not run: %v", err)
	}
	p, _ = core.ResolveEmbedded("bd") // re-extracted
	os.Chmod(p, 0o777)
	if err := core.CheckBuiltIn(p); err == nil {
		t.Fatal("a mode change is caught too")
	}
}

type countFS struct {
	fstest.MapFS
	opens atomic.Int32
}

func (c *countFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".zst") {
		c.opens.Add(1)
	}
	return c.MapFS.Open(name)
}

func TestAFailedExtractionIsRememberedUntilARefresh(t *testing.T) {
	set := embedtest.Standard()
	c := &countFS{MapFS: set.FS}
	data := set.FS["bd.zst"].Data
	delete(c.MapFS, "bd.zst")
	t.Cleanup(core.SetEmbeddedForTest(c))
	t.Setenv(core.ToolsDirEnv, filepath.Join(t.TempDir(), "tools"))
	_, why1 := core.ResolveEmbedded("bd")
	_, why2 := core.ResolveEmbedded("bd")
	if why1 == "" || why1 != why2 || c.opens.Load() != 1 {
		t.Fatalf("the failure is cached for the process (%d attempts): %q", c.opens.Load(), why2)
	}
	c.MapFS["bd.zst"] = &fstest.MapFile{Data: data} // fixed
	if _, why := core.ResolveEmbedded("bd"); why == "" {
		t.Fatal("still cached before the refresh")
	}
	core.ClearEmbeddedFailures()
	if p, why := core.ResolveEmbedded("bd"); p == "" {
		t.Fatalf("after a refresh it tries again: %s", why)
	}
	// Every detection is a refresh.
	delete(c.MapFS, "lambe.zst")
	core.ResolveEmbedded("lambe")
	core.NewRegistry().Detect(context.Background())
	c.MapFS["lambe.zst"] = &fstest.MapFile{Data: embedtest.Standard().FS["lambe.zst"].Data}
	if p, why := core.ResolveEmbedded("lambe"); p == "" {
		t.Fatalf("Registry.Detect clears the remembered failures: %s", why)
	}
}

type failAfter struct {
	w io.Writer
	n int
}

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n -= len(p); f.n < 0 {
		return 0, &fs.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}
	}
	return f.w.Write(p)
}

func TestAFullDiskIsAWriteFailureNotADamagedDownload(t *testing.T) {
	embeddedIn(t)
	defer core.SetExtractWriterForTest(func(w io.Writer) io.Writer { return &failAfter{w, 10} })()
	p, why := core.ResolveEmbedded("bd")
	if p != "" || !strings.Contains(why, "could not write to") || !strings.Contains(why, "no space left on device") || !strings.Contains(why, "disk full") {
		t.Fatalf("%q", why)
	}
	if strings.Contains(why, "damaged") || strings.Contains(why, "does not match") {
		t.Fatalf("a write failure is not a damaged copy: %q", why)
	}
	if f, d := core.LocateFound("bd", "", nil); f.Bin != "" || strings.Contains(core.AbsentSentence("bd", d), "download it again") || !strings.Contains(core.AbsentSentence("bd", d), "ABACUS_TOOLS_DIR") {
		t.Fatalf("%q", core.AbsentSentence("bd", d))
	}
}

// cancelFS hands out blobs whose first read cancels the extraction.
type cancelFS struct {
	fs.FS
	cancel context.CancelFunc
}

type cancelFile struct {
	fs.File
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelFile) Read(p []byte) (int, error) {
	n, err := c.File.Read(p)
	c.once.Do(c.cancel)
	return n, err
}

func (c cancelFS) Open(name string) (fs.File, error) {
	f, err := c.FS.Open(name)
	if err == nil && strings.HasSuffix(name, ".zst") {
		return &cancelFile{File: f, cancel: c.cancel}, nil
	}
	return f, err
}

func TestAnInterruptedExtractionRemovesItsTempFileAndTheNextRunIsClean(t *testing.T) {
	set, root := embeddedIn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(core.SetEmbeddedForTest(cancelFS{set.FS, cancel}))
	p, why := core.ResolveEmbeddedCtx(ctx, "bd")
	if p != "" || why == "" {
		t.Fatalf("%q %q", p, why)
	}
	dir := filepath.Join(root, set.Manifest.Hash())
	for _, n := range mustReadDir(t, dir) {
		if strings.HasPrefix(n, ".tmp-") || n == "bd" {
			t.Errorf("an interrupted extraction left %s", n)
		}
	}
	// An interruption is not remembered as a failure.
	t.Cleanup(core.SetEmbeddedForTest(set.FS))
	if p, why := core.ResolveEmbedded("bd"); p == "" {
		t.Fatalf("%s", why)
	}
}

func TestAKilledExtractionsLeftoversAreReapedByTheNextOne(t *testing.T) {
	set, root := embeddedIn(t)
	defer core.SetTmpMaxAgeForTest(time.Minute)()
	dir := filepath.Join(root, set.Manifest.Hash())
	os.MkdirAll(dir, 0o700)
	stale := filepath.Join(dir, ".tmp-bd-123456")
	fresh := filepath.Join(dir, ".tmp-lambe-654321")
	os.WriteFile(stale, bytes.Repeat([]byte("x"), 1<<20), 0o600)
	os.WriteFile(fresh, []byte("x"), 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(stale, old, old)
	if p, why := core.ResolveEmbedded("bd"); p == "" {
		t.Fatal(why)
	}
	if exists(stale) {
		t.Fatal("a stale .tmp- leftover must be reaped under the lock before extracting")
	}
	if !exists(fresh) {
		t.Fatal("a recent .tmp- file may belong to a live extraction and is left alone")
	}
}

func TestExtractionIsSerializedByTheLockFile(t *testing.T) {
	set, _ := embeddedIn(t)
	var active, peak atomic.Int32
	slow := slowFS{set.FS, &active, &peak}
	t.Cleanup(core.SetEmbeddedForTest(slow))
	var wg sync.WaitGroup
	for _, tool := range core.KnownTools {
		wg.Add(1)
		go func() { defer wg.Done(); core.ResolveEmbedded(tool) }()
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("%d extractions ran at the same time: the lock does not serialize them", peak.Load())
	}
}

type slowFS struct {
	fs.FS
	active, peak *atomic.Int32
}

type slowFile struct {
	fs.File
	s    slowFS
	once sync.Once
	done sync.Once
}

func (s slowFS) Open(name string) (fs.File, error) {
	f, err := s.FS.Open(name)
	if err == nil && strings.HasSuffix(name, ".zst") {
		return &slowFile{File: f, s: s}, nil
	}
	return f, err
}

func (f *slowFile) Read(p []byte) (int, error) {
	f.once.Do(func() {
		n := f.s.active.Add(1)
		for {
			pk := f.s.peak.Load()
			if n <= pk || f.s.peak.CompareAndSwap(pk, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	})
	n, err := f.File.Read(p)
	if err != nil {
		f.done.Do(func() { f.s.active.Add(-1) })
	}
	return n, err
}

func TestWaitingForAnotherAbacusTimesOutNamingTheLockFile(t *testing.T) {
	set, root := embeddedIn(t)
	defer core.SetLockTimeoutForTest(300 * time.Millisecond)()
	os.MkdirAll(root, 0o700)
	lock := filepath.Join(root, set.Manifest.Hash()+".lock")
	flockExclusive(t, lock)
	start := time.Now()
	p, why := core.ResolveEmbedded("bd")
	if p != "" || !strings.Contains(why, lock) || !strings.Contains(why, "delete that lock file") || time.Since(start) > 5*time.Second {
		t.Fatalf("%q after %v", why, time.Since(start))
	}
}

func TestACacheRootOthersCanWriteIsRefused(t *testing.T) {
	_, root := embeddedIn(t)
	os.MkdirAll(root, 0o700)
	os.Chmod(root, 0o777)
	if p, why := core.ResolveEmbedded("bd"); p != "" || !strings.Contains(why, "can be written by other users") {
		t.Fatalf("%q %q", p, why)
	}
}

func TestAnAncestorOthersCouldSwapIsRefused(t *testing.T) {
	cases := map[string]func(t *testing.T, parent string){
		"world-writable ancestor":              func(t *testing.T, parent string) { os.Chmod(parent, 0o777) },
		"group-writable ancestor (custom dir)": func(t *testing.T, parent string) { os.Chmod(parent, 0o770) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			embeddedIn(t)
			parent := filepath.Join(t.TempDir(), "shared")
			os.MkdirAll(parent, 0o700)
			mk(t, parent)
			t.Setenv(core.ToolsDirEnv, filepath.Join(parent, "tools"))
			if p, why := core.ResolveEmbedded("bd"); p != "" || !strings.Contains(why, "can be written by other users") || !strings.Contains(why, parent) {
				t.Fatalf("%q %q", p, why)
			}
		})
	}
	t.Run("sticky world-writable ancestor is fine", func(t *testing.T) {
		embeddedIn(t)
		parent := filepath.Join(t.TempDir(), "sticky")
		os.MkdirAll(parent, 0o700)
		os.Chmod(parent, 0o777|os.ModeSticky)
		t.Setenv(core.ToolsDirEnv, filepath.Join(parent, "tools"))
		if p, why := core.ResolveEmbedded("bd"); p == "" {
			t.Fatal(why)
		}
	})
	t.Run("ancestor owned by another user", func(t *testing.T) {
		embeddedIn(t)
		t.Cleanup(core.SetCurrentUIDForTest(func() int { return os.Getuid() + 1 }))
		if p, why := core.ResolveEmbedded("bd"); p != "" || !strings.Contains(why, "belongs to another user") {
			t.Fatalf("%q %q", p, why)
		}
	})
}

func TestBuiltInExecProblemSaysWhatToDo(t *testing.T) {
	embeddedIn(t)
	p, _ := core.ResolveEmbedded("bd")
	pathErr := &fs.PathError{Op: "fork/exec", Path: p, Err: syscall.EACCES}
	cases := map[string]struct {
		exit int
		err  error
	}{
		"EACCES (noexec mount)":     {-1, pathErr},
		"operation not permitted":   {-1, errors.New("fork/exec " + p + ": operation not permitted")},
		"killed by a signal":        {-1, nil},
		"permission denied (macOS)": {-1, errors.New("fork/exec " + p + ": permission denied")},
	}
	for name, c := range cases {
		got := core.BuiltInExecProblem(p, c.exit, c.err)
		if !strings.Contains(got, filepath.Dir(p)) || !strings.Contains(got, "ABACUS_TOOLS_DIR=<folder>") || strings.Contains(strings.ToLower(got), "install") {
			t.Errorf("%s: %q", name, got)
		}
		if runtime.GOOS == "darwin" && !strings.Contains(got, "xattr -dr com.apple.quarantine "+filepath.Dir(p)) {
			t.Errorf("%s: no quarantine remedy on macOS: %q", name, got)
		}
	}
	for name, c := range map[string]struct {
		path string
		exit int
		err  error
	}{
		"not a built-in path": {"/usr/bin/true", -1, pathErr},
		"a normal exit":       {p, 3, nil},
		"a timeout":           {p, -1, core.ErrTimedOut},
		"a cancel":            {p, -1, core.ErrCanceled},
		"an unrelated error":  {p, -1, errors.New("boom")},
	} {
		if got := core.BuiltInExecProblem(c.path, c.exit, c.err); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// TestRealSizeBlobStreamsAndExtracts runs a roughly 100 MB executable through the real zstd path
// (set ABACUS_REAL_SIZE=1; the final verification runs it and reports the numbers).
func TestRealSizeBlobStreamsAndExtracts(t *testing.T) {
	if os.Getenv("ABACUS_REAL_SIZE") == "" {
		t.Skip("set ABACUS_REAL_SIZE=1 to run the 100 MB extraction test")
	}
	block := make([]byte, 1<<20)
	x := uint64(88172645463325252)
	for i := range block {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		block[i] = byte(x) & 0x3f // compressible, not trivially
	}
	var body bytes.Buffer
	body.WriteString("#!/bin/sh\necho big\n# ")
	for body.Len() < 100<<20 {
		body.Write(block)
		block[0]++
	}
	set := embedtest.New("n", nil, embedtest.Tool{Name: "bd", Version: "1", Body: body.String()})
	t.Cleanup(core.SetEmbeddedForTest(set.FS))
	_, root := embeddedIn2(t, set)
	start := time.Now()
	p, why := core.ResolveEmbedded("bd")
	took := time.Since(start)
	if p == "" {
		t.Fatal(why)
	}
	fi, _ := os.Stat(p)
	start = time.Now()
	core.SetEmbeddedForTest(set.FS)
	p2, _ := core.ResolveEmbedded("bd")
	warm := time.Since(start)
	t.Logf("REAL-SIZE: raw %d bytes, compressed %d bytes, extraction %v, warm start (stat only) %v, on disk %d bytes under %s", fi.Size(), len(set.FS["bd.zst"].Data), took.Round(time.Millisecond), warm.Round(time.Microsecond), fi.Size(), root)
	if p2 != p || fi.Size() != int64(body.Len()) {
		t.Fatal("size mismatch")
	}
	if out, err := exec.Command(p).Output(); err != nil || !strings.Contains(string(out), "big") {
		t.Fatalf("%q %v", out, err)
	}
}

func embeddedIn2(t *testing.T, set *embedtest.Set) (*embedtest.Set, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "tools")
	t.Setenv(core.ToolsDirEnv, root)
	return set, root
}

// TestEveryRefusalIsPlainWords reproduces each way the built-in tools can be refused or fail and
// checks the words the user would see: they say what failed and offer the one user-level fix, and
// never tell anyone to put anything on the PATH or to fetch a tool.
func TestEveryRefusalIsPlainWords(t *testing.T) {
	banned := regexpMust(`(?i)install|dev-bundle|make bundle|npm i|npx|scripts/|re-extract`)
	bannedPATH := regexpMust(`\bPATH\b`)
	noRoot := func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("running as root: permission refusals cannot be reproduced")
		}
	}
	type scenario struct {
		name  string
		setup func(t *testing.T, set *embedtest.Set, root string)
		want  string // a substring that must appear (the one fix or the cause)
	}
	fresh := func(t *testing.T) { t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS)) }
	scenarios := []scenario{
		{"cache owned by another uid", func(t *testing.T, set *embedtest.Set, root string) {
			core.ResolveEmbedded("bd")
			t.Cleanup(core.SetCurrentUIDForTest(func() int { return os.Getuid() + 1 }))
			fresh(t)
		}, "belongs to another user"},
		{"a link in place of the directory", func(t *testing.T, set *embedtest.Set, root string) {
			os.MkdirAll(root, 0o700)
			os.Symlink(t.TempDir(), filepath.Join(root, set.Manifest.Hash()))
		}, "not a plain folder"},
		{"a link in place of the tool", func(t *testing.T, set *embedtest.Set, root string) {
			p, _ := core.ResolveEmbedded("bd")
			os.Remove(p)
			os.Symlink("/bin/sh", p)
			fresh(t)
		}, "not a plain file"},
		{"a directory open to the group", func(t *testing.T, set *embedtest.Set, root string) {
			p, _ := core.ResolveEmbedded("bd")
			os.Chmod(filepath.Dir(p), 0o770)
			fresh(t)
		}, "open to other users"},
		{"a file the group can write", func(t *testing.T, set *embedtest.Set, root string) {
			p, _ := core.ResolveEmbedded("bd")
			os.Chmod(p, 0o660)
			fresh(t)
		}, "unsafe permissions"},
		{"a file that is not executable", func(t *testing.T, set *embedtest.Set, root string) {
			p, _ := core.ResolveEmbedded("bd")
			os.Chmod(p, 0o600)
			fresh(t)
		}, "unsafe permissions"},
		{"a read-only root", func(t *testing.T, set *embedtest.Set, root string) {
			noRoot(t)
			os.MkdirAll(root, 0o500)
		}, "permission denied"},
		{"a full disk", func(t *testing.T, set *embedtest.Set, root string) {
			t.Cleanup(core.SetExtractWriterForTest(func(w io.Writer) io.Writer { return &failAfter{w, 5} }))
		}, "disk full"},
		{"a lock that never frees", func(t *testing.T, set *embedtest.Set, root string) {
			t.Cleanup(core.SetLockTimeoutForTest(200 * time.Millisecond))
			os.MkdirAll(root, 0o700)
			flockExclusive(t, filepath.Join(root, set.Manifest.Hash()+".lock"))
		}, "delete that lock file"},
		{"a damaged blob", func(t *testing.T, set *embedtest.Set, root string) {
			set.FS["bd.zst"].Data = set.FS["bd.zst"].Data[:12]
			core.SetEmbeddedForTest(set.FS)
		}, "download it again"},
		{"a relative folder setting", func(t *testing.T, set *embedtest.Set, root string) {
			t.Setenv(core.ToolsDirEnv, "relative/dir")
		}, "absolute"},
		{"a world-writable ancestor", func(t *testing.T, set *embedtest.Set, root string) {
			parent := filepath.Join(t.TempDir(), "open")
			os.MkdirAll(parent, 0o700)
			os.Chmod(parent, 0o777)
			t.Setenv(core.ToolsDirEnv, filepath.Join(parent, "tools"))
		}, "can be written by other users"},
	}
	for _, sc := range scenarios {
		t.Run(strings.ReplaceAll(sc.name, " ", "_"), func(t *testing.T) {
			set, root := embeddedIn(t)
			sc.setup(t, set, root)
			core.ClearEmbeddedFailures()
			f, detail := core.LocateFound("bd", "", func(string) (string, error) { t.Fatal("PATH consulted"); return "", nil })
			if f.Bin != "" {
				t.Fatalf("expected a refusal, got %+v", f)
			}
			msg := core.AbsentSentence("bd", detail)
			if !strings.Contains(msg, sc.want) {
				t.Errorf("want %q in %q", sc.want, msg)
			}
			if m := banned.FindString(strings.ReplaceAll(msg, root, "")); m != "" || bannedPATH.MatchString(msg) {
				t.Errorf("advice %q in %q", m, msg)
			}
		})
	}
	t.Run("exec_failure", func(t *testing.T) {
		embeddedIn(t)
		p, _ := core.ResolveEmbedded("bd")
		msg := core.BuiltInExecProblem(p, -1, &fs.PathError{Op: "fork/exec", Path: p, Err: syscall.EACCES})
		if msg == "" || banned.MatchString(strings.ReplaceAll(msg, filepath.Dir(p), "")) || bannedPATH.MatchString(msg) {
			t.Errorf("%q", msg)
		}
	})
}

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }
