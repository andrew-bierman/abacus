package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/embedtools"
	"github.com/klauspost/compress/zstd"
)

// The tools abacus needs (bd, lambe, beads-lease and the Vercel Board CLI) are built into the
// binary, compressed. This file turns them into runnable files: the first run extracts each into a
// private per-user cache, `<tools dir>/<content hash>/<name>`, and every run checks it again.
//
// What a run checks, and why not more: file type, owner, mode and size every start (a stat), and
// the sha256 only at extraction or when the file's size or mtime no longer matches the marker
// written beside it at the last verification (a 100 MB hash on every start would add about a
// quarter second to each one). The cache is 0700 and owned by the user, so changing a file without
// changing its size and mtime takes the user's own account, which could change abacus itself. The
// hashes come from the manifest inside the binary; no environment variable can replace them.

// ToolsDirEnv overrides where built-in tools are extracted.
const ToolsDirEnv = "ABACUS_TOOLS_DIR"

// damagedMark is in the reason when the built-in bytes do not match their own manifest, which
// only a damaged download causes; BundledProblem words that case differently from a folder problem.
const damagedMark = "does not match what was built into abacus"

type embeddedSet struct {
	fsys fs.FS
	man  *embedtools.Manifest
	hash string
	bad  string // the manifest could not be read: every tool is unusable

	mu      sync.Mutex
	paths   map[string]string          // tool -> verified path, for this process (re-checked on every use)
	byPath  map[string]embedtools.Tool // verified path -> tool
	failed  map[string]string          // tool -> why it could not be set up, until the next refresh
	noticed bool                       // the "preparing" notice was shown
	touched bool                       // the last-used stamp was refreshed
}

var (
	embeddedMu  sync.RWMutex
	embeddedCur *embeddedSet
)

func init() { setEmbedded(embedtools.FS) }

func setEmbedded(fsys fs.FS) {
	var s *embeddedSet
	if fsys != nil {
		s = &embeddedSet{fsys: fsys, paths: map[string]string{}, byPath: map[string]embedtools.Tool{}, failed: map[string]string{}}
		b, err := fs.ReadFile(fsys, embedtools.ManifestFile)
		if err == nil {
			s.man, err = embedtools.Parse(b)
		}
		if err != nil {
			s.bad = "the tool list inside this copy of abacus is unreadable (" + err.Error() + "): it " + damagedMark
		} else {
			s.hash = s.man.Hash()
		}
	}
	embeddedMu.Lock()
	embeddedCur = s
	embeddedMu.Unlock()
}

// SetEmbeddedForTest replaces the embedded file set (nil for a development build) and returns a
// restore func. Only tests call it; production reads embedtools.FS.
func SetEmbeddedForTest(fsys fs.FS) (restore func()) {
	embeddedMu.RLock()
	old := embeddedCur
	embeddedMu.RUnlock()
	setEmbedded(fsys)
	return func() { embeddedMu.Lock(); embeddedCur = old; embeddedMu.Unlock() }
}

func embeddedSetNow() *embeddedSet {
	embeddedMu.RLock()
	defer embeddedMu.RUnlock()
	return embeddedCur
}

// EmbeddedEnabled reports whether this binary carries its tools (built with -tags embedtools).
func EmbeddedEnabled() bool { return embeddedSetNow() != nil }

// EmbeddedVersions is tool -> the version built in (empty in a development build).
func EmbeddedVersions() map[string]string {
	out := map[string]string{}
	if s := embeddedSetNow(); s != nil && s.man != nil {
		for _, t := range s.man.Tools {
			out[t.Name] = t.Version
		}
	}
	return out
}

// EmbeddedFS is the embedded file set (notices, licenses) and its manifest; nil in a development build.
func EmbeddedFS() (fs.FS, *embedtools.Manifest) {
	if s := embeddedSetNow(); s != nil && s.man != nil {
		return s.fsys, s.man
	}
	return nil, nil
}

// ToolsDir is the extraction root: ABACUS_TOOLS_DIR, else $XDG_DATA_HOME/abacus/tools, else
// ~/.local/share/abacus/tools. A relative path is refused (it would depend on the working directory).
func ToolsDir() (string, error) {
	if d := os.Getenv(ToolsDirEnv); d != "" {
		if !filepath.IsAbs(d) {
			return "", fmt.Errorf("%s must be an absolute folder path", ToolsDirEnv)
		}
		return filepath.Clean(d), nil
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return "", errors.New("there is no home folder to keep the tools in")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, brand.Name, "tools"), nil
}

// Tunables, variables so tests can shorten them.
var (
	lockTimeout = 30 * time.Second    // how long a start waits for another abacus preparing the tools
	tmpMaxAge   = 10 * time.Minute    // a .tmp- file older than this is the leftover of a killed extraction
	pruneWindow = 14 * 24 * time.Hour // another version unused for this long may be removed
	keepPrev    = 2                   // the newest other versions are always kept
	// PrepareNotice, when set, is called once (before the first extraction of a process) so the
	// front end can tell the user why the first start takes a few seconds.
	PrepareNotice func()
	// extractWrap lets tests fail the destination writer (a full disk).
	extractWrap = func(w io.Writer) io.Writer { return w }
)

// SetExtractWriterForTest wraps the destination writer of every extraction and returns a restore func.
func SetExtractWriterForTest(f func(io.Writer) io.Writer) (restore func()) {
	old := extractWrap
	extractWrap = f
	return func() { extractWrap = old }
}

// ClearEmbeddedFailures forgets the remembered failures, so the next use tries again. A refresh
// (every detection, every Doctor run) calls it; within one refresh a failure is not retried.
func ClearEmbeddedFailures() {
	if s := embeddedSetNow(); s != nil {
		s.mu.Lock()
		s.failed = map[string]string{}
		s.mu.Unlock()
	}
}

// ResolveEmbedded returns the path of the built-in tool, extracting it first if needed, or why it
// cannot be used ("" path). The path is a verified regular file in a private directory. A Ctrl-C
// during extraction removes the partial file and then ends the process as the signal would have.
func ResolveEmbedded(tool string) (path, why string) {
	ctx, done := interruptContext(context.Background())
	path, why = ResolveEmbeddedCtx(ctx, tool)
	done()
	return path, why
}

// ResolveEmbeddedCtx is ResolveEmbedded with an explicit cancellation.
func ResolveEmbeddedCtx(ctx context.Context, tool string) (path, why string) {
	s := embeddedSetNow()
	switch {
	case s == nil:
		return "", "this build has no built-in tools"
	case s.bad != "":
		return "", s.bad
	}
	t, ok := s.man.Tool(tool)
	if !ok {
		return "", "this copy of abacus does not include " + tool
	}
	s.mu.Lock()
	if p, ok := s.paths[tool]; ok {
		if cheapVerify(p, t) == nil {
			s.mu.Unlock()
			return p, ""
		}
		delete(s.paths, tool) // gone or changed since it was verified: verify again from scratch
		delete(s.byPath, p)
	}
	if w, ok := s.failed[tool]; ok {
		s.mu.Unlock()
		return "", w
	}
	s.mu.Unlock()
	root, err := ToolsDir()
	if err == nil {
		var p string
		if p, err = s.ensure(ctx, root, t); err == nil {
			s.mu.Lock()
			s.paths[tool], s.byPath[p] = p, t
			s.mu.Unlock()
			return p, ""
		}
	}
	if ctx.Err() == nil { // an interruption is not a failure to remember
		s.mu.Lock()
		s.failed[tool] = err.Error()
		s.mu.Unlock()
	}
	return "", err.Error()
}

// ResolvedEmbedded is the path of a built-in tool that has already been set up in this process, or
// "" (it extracts nothing: used where a tool is only wanted if it is already there).
func ResolvedEmbedded(tool string) string {
	s := embeddedSetNow()
	if s == nil || s.man == nil {
		return ""
	}
	t, ok := s.man.Tool(tool)
	if !ok {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.paths[tool]; ok && cheapVerify(p, t) == nil {
		return p
	}
	return ""
}

// CheckBuiltIn is the cheap re-check run immediately before a built-in tool is executed (type,
// owner, mode and size, one stat): a file replaced since it was verified is not run. A path that is
// not one of the built-in copies is not this function's business.
func CheckBuiltIn(path string) error {
	s := embeddedSetNow()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	t, ok := s.byPath[path]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	if err := cheapVerify(path, t); err != nil {
		s.mu.Lock()
		delete(s.paths, t.Name)
		delete(s.byPath, path)
		s.mu.Unlock()
		return fmt.Errorf("the built-in %s changed after abacus checked it (%s): not running it; start abacus again", t.Name, err)
	}
	return nil
}

// IsBuiltInPath reports whether path is a built-in copy resolved in this process.
func IsBuiltInPath(path string) bool {
	s := embeddedSetNow()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byPath[path]
	return ok
}

// cheapVerify is the per-use check: a regular file of the user's, private, executable, and of the
// expected size. No hashing.
func cheapVerify(path string, t embedtools.Tool) error {
	fi, err := os.Lstat(path)
	switch {
	case err != nil:
		return errors.New(plainWhy(err))
	case !fi.Mode().IsRegular():
		return errors.New("not a plain file")
	case !isMine(fi):
		return errors.New("belongs to another user")
	case fi.Mode().Perm()&0o077 != 0 || fi.Mode().Perm()&0o100 == 0:
		return errors.New("unsafe permissions")
	case fi.Size() != t.Size:
		return errors.New("its size changed")
	}
	return nil
}

func (s *embeddedSet) notice() {
	s.mu.Lock()
	first := !s.noticed
	s.noticed = true
	s.mu.Unlock()
	if first && PrepareNotice != nil {
		PrepareNotice()
	}
}

// touch refreshes the last-used stamp of the directory a process runs tools from (once per process)
// and keeps a shared "in use" lock on it for the life of the process, so another abacus never
// removes it from under us.
func (s *embeddedSet) touch(root string) {
	s.mu.Lock()
	first := !s.touched
	s.touched = true
	s.mu.Unlock()
	if !first {
		return
	}
	dir := filepath.Join(root, s.hash)
	stamp := filepath.Join(dir, usedStamp)
	now := time.Now()
	if err := os.Chtimes(stamp, now, now); err != nil {
		_ = os.WriteFile(stamp, nil, 0o600)
	}
	holdInUse(filepath.Join(root, s.hash) + ".use")
}

const usedStamp = ".used"

func (s *embeddedSet) ensure(ctx context.Context, root string, t embedtools.Tool) (string, error) {
	dir := filepath.Join(root, s.hash)
	if err := checkAncestors(root); err != nil {
		return "", err
	}
	if p, err := verifyExtracted(dir, t, false); err == nil {
		s.touch(root)
		return p, nil
	} else if !errors.Is(err, errNotThere) && !errors.Is(err, errRepair) {
		return "", err
	}
	s.notice()
	if err := ensureRoot(root); err != nil {
		return "", err
	}
	lockPath := filepath.Join(root, s.hash) + ".lock" // one extraction at a time
	unlock, err := lockWait(lockPath, lockTimeout)
	if err != nil {
		return "", err
	}
	defer unlock()
	if p, err := verifyExtracted(dir, t, false); err == nil { // another abacus finished while we waited
		s.touch(root)
		return p, nil
	} else if !errors.Is(err, errNotThere) && !errors.Is(err, errRepair) {
		return "", err
	}
	if err := ensureDir(dir); err != nil {
		return "", err
	}
	reapTmp(dir)
	p, err := s.extract(ctx, dir, t)
	if err != nil {
		return "", err
	}
	s.touch(root)
	s.pruneOld(root)
	return p, nil
}

// reapTmp removes the leftovers of an extraction that was killed: .tmp- files old enough that no
// live extraction can still own them (the caller holds the extraction lock, so none is running).
func reapTmp(dir string) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range es {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(dir, e.Name())); err == nil && fi.Mode().IsRegular() && time.Since(fi.ModTime()) > tmpMaxAge {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

var (
	errNotThere = errors.New("not extracted yet")
	errRepair   = errors.New("extracted copy needs replacing")
)

func isMine(fi os.FileInfo) bool {
	uid, ok := ownerUID(fi)
	return !ok || uid == currentUID()
}

// checkAncestors refuses a tools folder below a directory someone else could swap: each existing
// ancestor must belong to the user or root and must not be writable by others (a sticky directory
// such as /tmp is fine). For a folder chosen with ABACUS_TOOLS_DIR the group may not write either:
// that folder must be private. The default folder under the user's data directory is checked for
// the first two only (a home directory with a shared group is common).
func checkAncestors(root string) error {
	strict := os.Getenv(ToolsDirEnv) != ""
	for p := filepath.Dir(root); ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err == nil {
			uid, ok := ownerUID(fi)
			switch {
			case ok && uid != 0 && uid != currentUID():
				return fmt.Errorf("%s belongs to another user, so tools kept below it could be replaced: choose a private folder with %s=<folder>", p, ToolsDirEnv)
			case fi.Mode().Perm()&0o002 != 0 && fi.Mode()&os.ModeSticky == 0,
				strict && fi.Mode().Perm()&0o020 != 0 && fi.Mode()&os.ModeSticky == 0:
				return fmt.Errorf("%s can be written by other users, so tools kept below it could be replaced: choose a private folder with %s=<folder>", p, ToolsDirEnv)
			}
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

// ensureRoot creates the extraction root (private, 0700) when it is missing, and refuses one that
// is not a directory of the user's, or that others can write to.
func ensureRoot(root string) error {
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return fmt.Errorf("could not create %s: %s", filepath.Dir(root), plainWhy(err))
	}
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("could not create %s: %s", root, plainWhy(err))
	}
	if err := checkAncestors(root); err != nil {
		return err
	}
	fi, err := os.Stat(root) // the root itself may be a link (a dotfiles setup); nothing below it may be
	switch {
	case err != nil:
		return fmt.Errorf("could not read %s: %s", root, plainWhy(err))
	case !fi.IsDir():
		return fmt.Errorf("%s is not a folder", root)
	case !isMine(fi):
		return fmt.Errorf("%s belongs to another user", root)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s can be written by other users", root)
	}
	return nil
}

// ensureDir creates or checks the per-content directory: a real directory (never a link), the
// user's own, closed to group and others.
func ensureDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("could not create %s: %s", dir, plainWhy(err))
	}
	fi, err := os.Lstat(dir)
	switch {
	case err != nil:
		return fmt.Errorf("could not read %s: %s", dir, plainWhy(err))
	case !fi.IsDir():
		return fmt.Errorf("%s is not a plain folder (a link or a file is in its place)", dir)
	case !isMine(fi):
		return fmt.Errorf("%s belongs to another user", dir)
	case fi.Mode().Perm()&0o077 != 0:
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%s is open to other users and could not be closed: %s", dir, plainWhy(err))
		}
	}
	return nil
}

// plainWhy is an error without the operation and path Go repeats in it ("permission denied").
func plainWhy(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func markerOf(path string) string { return path + ".ok" }

// verifyExtracted checks the extracted copy of t in dir. It returns errNotThere when nothing is
// there, errRepair when the copy is stale or damaged and may be replaced, and any other error when
// something is wrong that must not be repaired in place (a link, someone else's file, an open
// folder). verifyHash forces the sha256 even when the marker matches.
func verifyExtracted(dir string, t embedtools.Tool, verifyHash bool) (string, error) {
	di, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNotThere
	}
	switch {
	case err != nil:
		return "", fmt.Errorf("could not read %s: %s", dir, plainWhy(err))
	case !di.IsDir():
		return "", fmt.Errorf("%s is not a plain folder (a link or a file is in its place)", dir)
	case !isMine(di):
		return "", fmt.Errorf("%s belongs to another user", dir)
	case di.Mode().Perm()&0o077 != 0:
		return "", fmt.Errorf("%s is open to other users", dir)
	}
	path := filepath.Join(dir, t.Name)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNotThere
	}
	switch {
	case err != nil:
		return "", fmt.Errorf("could not read %s: %s", path, plainWhy(err))
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("%s is not a plain file (a link or a folder is in its place)", path)
	case !isMine(fi):
		return "", fmt.Errorf("%s belongs to another user", path)
	case fi.Mode().Perm()&0o077 != 0 || fi.Mode().Perm()&0o100 == 0:
		return "", fmt.Errorf("%s has unsafe permissions (%v)", path, fi.Mode().Perm())
	case fi.Size() != t.Size:
		return "", errRepair
	}
	want := fmt.Sprintf("v1 %s %d %d", t.SHA256, fi.Size(), fi.ModTime().UnixNano())
	if mb, err := os.ReadFile(markerOf(path)); err == nil && !verifyHash && strings.TrimSpace(string(mb)) == want {
		return path, nil
	}
	got, err := fileSHA256(path)
	if err != nil {
		return "", fmt.Errorf("could not read %s: %s", path, plainWhy(err))
	}
	if got != t.SHA256 {
		return "", errRepair
	}
	_ = writeAtomic(markerOf(path), []byte(want+"\n"), 0o600)
	return path, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeAtomic writes data to a temp file in the same directory, syncs it and renames it into place.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// ctxReader stops a copy when its context is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// recWriter remembers the first error of the destination, so a write failure (a full disk) is not
// mistaken for a damaged download when the copy reports it.
type recWriter struct {
	w   io.Writer
	err error
}

func (r *recWriter) Write(p []byte) (int, error) {
	n, err := r.w.Write(p)
	if err != nil && r.err == nil {
		r.err = err
	}
	return n, err
}

func writeFailure(dir string, err error) error {
	hint := ""
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		hint = " (is the disk full?)"
	}
	return fmt.Errorf("could not write to %s: %s%s", dir, plainWhy(err), hint)
}

// extract decompresses the embedded blob into dir, checking length and sha256 against the manifest
// before the file gets its final name: a truncated or altered blob fails closed and leaves nothing.
func (s *embeddedSet) extract(ctx context.Context, dir string, t embedtools.Tool) (string, error) {
	path := filepath.Join(dir, t.Name)
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a plain file (a link or a folder is in its place)", path)
	}
	src, err := s.fsys.Open(t.File)
	if err != nil {
		return "", fmt.Errorf("the built-in %s is missing from this copy of abacus (%s), which %s", t.Name, err, damagedMark)
	}
	defer src.Close()
	dec, err := zstd.NewReader(ctxReader{ctx, src})
	if err != nil {
		return "", fmt.Errorf("the built-in %s cannot be read: %w", t.Name, err)
	}
	defer dec.Close()
	tmp, err := os.CreateTemp(dir, ".tmp-"+t.Name+"-")
	if err != nil {
		return "", writeFailure(dir, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) { tmp.Close(); os.Remove(tmpName); return "", err }
	h := sha256.New()
	rec := &recWriter{w: extractWrap(tmp)}
	n, err := io.Copy(io.MultiWriter(rec, h), io.LimitReader(dec, t.Size+1)) // never more than declared
	switch {
	case ctx.Err() != nil:
		return fail(errors.New("stopped before the built-in " + t.Name + " was ready"))
	case rec.err != nil:
		return fail(writeFailure(dir, rec.err))
	case err != nil:
		return fail(fmt.Errorf("the built-in %s is damaged inside this copy of abacus (%v), which %s", t.Name, err, damagedMark))
	case n != t.Size || hex.EncodeToString(h.Sum(nil)) != t.SHA256:
		return fail(fmt.Errorf("the built-in %s %s (its sha256 or length is different)", t.Name, damagedMark))
	}
	if err := tmp.Sync(); err != nil {
		return fail(writeFailure(dir, err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", writeFailure(dir, err)
	}
	if err := os.Chmod(tmpName, 0o700); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("could not place %s: %s", path, plainWhy(err))
	}
	if d, err := os.Open(dir); err == nil { // make the rename durable
		_ = d.Sync()
		d.Close()
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	_ = writeAtomic(markerOf(path), []byte(fmt.Sprintf("v1 %s %d %d\n", t.SHA256, fi.Size(), fi.ModTime().UnixNano())), 0o600)
	return path, nil
}

var hashDirRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// pruneOld removes extraction directories of other versions, but only when all of these hold:
// every tool of the current version is in place; the directory is not one of the newest keepPrev
// others; it has not been used (its .used stamp, else its own mtime) for pruneWindow; and the
// non-blocking exclusive locks on its extraction lock and its in-use lock are both free, so no
// abacus is preparing it or running from it. Lock files are never removed.
func (s *embeddedSet) pruneOld(root string) {
	cur := filepath.Join(root, s.hash)
	for _, t := range s.man.Tools {
		if _, err := verifyExtracted(cur, t, false); err != nil {
			return
		}
	}
	es, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type cand struct {
		name  string
		stamp time.Time
	}
	var cs []cand
	for _, e := range es {
		name := e.Name()
		if !hashDirRE.MatchString(name) || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".use") || name == s.hash {
			continue
		}
		fi, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !fi.IsDir() || !isMine(fi) {
			continue
		}
		stamp := fi.ModTime()
		if sf, err := os.Lstat(filepath.Join(root, name, usedStamp)); err == nil && sf.Mode().IsRegular() {
			stamp = sf.ModTime()
		}
		cs = append(cs, cand{name, stamp})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].stamp.After(cs[j].stamp) })
	for i, c := range cs {
		if i < keepPrev || time.Since(c.stamp) < pruneWindow {
			continue
		}
		base := filepath.Join(root, c.name)
		lk, ok, _ := tryFlock(base+".lock", true)
		if !ok {
			continue
		}
		uk, ok, _ := tryFlock(base+".use", true)
		if ok {
			os.RemoveAll(base)
			closeLock(uk)
		}
		closeLock(lk)
	}
}

// embeddedAbsentDetail words a failed built-in tool for LocateFound's detail.
func embeddedAbsentDetail(tool, why string) string {
	return fmt.Sprintf("%s is not available %s%s)", tool, refusedMarker, why)
}
