package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

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

	mu    sync.Mutex
	paths map[string]string // tool -> verified path, for this process
}

var (
	embeddedMu  sync.RWMutex
	embeddedCur *embeddedSet
)

func init() { setEmbedded(embedtools.FS) }

func setEmbedded(fsys fs.FS) {
	var s *embeddedSet
	if fsys != nil {
		s = &embeddedSet{fsys: fsys, paths: map[string]string{}}
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

// ResolveEmbedded returns the path of the built-in tool, extracting it first if needed, or why it
// cannot be used ("" path). The path is a verified regular file in a private directory.
func ResolveEmbedded(tool string) (path, why string) {
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
		s.mu.Unlock()
		return p, ""
	}
	s.mu.Unlock()
	root, err := ToolsDir()
	if err != nil {
		return "", err.Error()
	}
	p, err := s.ensure(root, t)
	if err != nil {
		return "", err.Error()
	}
	s.mu.Lock()
	s.paths[tool] = p
	s.mu.Unlock()
	return p, ""
}

func (s *embeddedSet) ensure(root string, t embedtools.Tool) (string, error) {
	dir := filepath.Join(root, s.hash)
	if p, err := verifyExtracted(dir, t, false); err == nil {
		return p, nil
	} else if !errors.Is(err, errNotThere) && !errors.Is(err, errRepair) {
		return "", err
	}
	if err := ensureRoot(root); err != nil {
		return "", err
	}
	unlock, err := lockConfig(filepath.Join(root, s.hash)) // <root>/<hash>.lock: one extraction at a time
	if err != nil {
		return "", fmt.Errorf("could not lock %s: %s", root, plainWhy(err))
	}
	defer unlock()
	if p, err := verifyExtracted(dir, t, false); err == nil { // another abacus finished while we waited
		return p, nil
	} else if !errors.Is(err, errNotThere) && !errors.Is(err, errRepair) {
		return "", err
	}
	if err := ensureDir(dir); err != nil {
		return "", err
	}
	p, err := s.extract(dir, t)
	if err != nil {
		return "", err
	}
	s.pruneOld(root)
	return p, nil
}

var (
	errNotThere = errors.New("not extracted yet")
	errRepair   = errors.New("extracted copy needs replacing")
)

func isMine(fi os.FileInfo) bool {
	uid, ok := ownerUID(fi)
	return !ok || uid == currentUID()
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

// extract decompresses the embedded blob into dir, checking length and sha256 against the manifest
// before the file gets its final name: a truncated or altered blob fails closed and leaves nothing.
func (s *embeddedSet) extract(dir string, t embedtools.Tool) (string, error) {
	path := filepath.Join(dir, t.Name)
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a plain file (a link or a folder is in its place)", path)
	}
	src, err := s.fsys.Open(t.File)
	if err != nil {
		return "", fmt.Errorf("the built-in %s is missing from this copy of abacus (%s), which %s", t.Name, err, damagedMark)
	}
	defer src.Close()
	dec, err := zstd.NewReader(src)
	if err != nil {
		return "", fmt.Errorf("the built-in %s cannot be read: %s", t.Name, plainWhy(err))
	}
	defer dec.Close()
	tmp, err := os.CreateTemp(dir, ".tmp-"+t.Name+"-")
	if err != nil {
		return "", fmt.Errorf("could not write into %s: %s", dir, plainWhy(err))
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) { tmp.Close(); os.Remove(tmpName); return "", err }
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(dec, t.Size+1)) // never more than declared
	switch {
	case err != nil:
		return fail(fmt.Errorf("the built-in %s is damaged inside this copy of abacus (%v), which %s", t.Name, err, damagedMark))
	case n != t.Size || hex.EncodeToString(h.Sum(nil)) != t.SHA256:
		return fail(fmt.Errorf("the built-in %s %s (its sha256 or length is different)", t.Name, damagedMark))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("could not write %s: %s", tmpName, plainWhy(err)))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("could not write %s: %s", tmpName, plainWhy(err))
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

var hashDirRE = regexp.MustCompile(`^[0-9a-f]{16}(\.lock)?$`)

// pruneOld removes extraction directories of other versions, but only once every tool of the
// current one is in place (the previous version stays until the new one is complete), and never one
// newer than ours (another abacus version running at the same time keeps its own).
func (s *embeddedSet) pruneOld(root string) {
	cur := filepath.Join(root, s.hash)
	ci, err := os.Lstat(cur)
	if err != nil {
		return
	}
	for _, t := range s.man.Tools {
		if _, err := verifyExtracted(cur, t, false); err != nil {
			return
		}
	}
	es, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range es {
		name := e.Name()
		if !hashDirRE.MatchString(name) || strings.TrimSuffix(name, ".lock") == s.hash {
			continue
		}
		p := filepath.Join(root, name)
		fi, err := os.Lstat(p)
		if err != nil || !isMine(fi) {
			continue
		}
		if strings.HasSuffix(name, ".lock") {
			if fi.Mode().IsRegular() {
				os.Remove(p)
			}
			continue
		}
		if fi.IsDir() && fi.ModTime().Before(ci.ModTime()) {
			os.RemoveAll(p)
		}
	}
}

// embeddedAbsentDetail words a failed built-in tool for LocateFound's detail.
func embeddedAbsentDetail(tool, why string) string {
	return fmt.Sprintf("%s is not available %s%s)", tool, refusedMarker, why)
}
