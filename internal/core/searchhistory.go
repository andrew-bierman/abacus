package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/query"
)

// Search history is the last queries a person typed, per list, kept in abacus's own state
// directory (never in a tracker directory, which abacus does not write into).
const (
	// HistoryGlobal is the global search box; HistoryTasksFilter is the Tasks `/` filter.
	HistoryGlobal      = "global"
	HistoryTasksFilter = "tasks-filter"
	// MaxSearchHistory is how many queries each list keeps.
	MaxSearchHistory = 100
)

// HistoryLockWait is how long Add waits for another writer before giving up. It runs on the
// UI's goroutine, so it is short.
var HistoryLockWait = 500 * time.Millisecond

// ErrHistoryBusy is a history file another process holds: the query is not saved.
var ErrHistoryBusy = errors.New("search history is busy, so this query was not saved")

var errLockBusy = errors.New("lock busy")

// SearchHistory reads and writes the history file at Path. The zero value (no path) keeps
// nothing: Add does nothing and Load is empty, as in a demo session.
type SearchHistory struct{ Path string }

// DefaultSearchHistoryPath is $XDG_STATE_HOME/abacus/search-history.json or
// ~/.local/state/abacus/search-history.json.
func DefaultSearchHistoryPath() (string, error) {
	d, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return "", errors.New("search history: no home directory to keep it in")
	}
	return filepath.Join(d, "search-history.json"), nil
}

// DefaultSearchHistory is the history at the default path; with no usable home it keeps nothing.
func DefaultSearchHistory() SearchHistory {
	p, err := DefaultSearchHistoryPath()
	if err != nil {
		return SearchHistory{}
	}
	return SearchHistory{Path: p}
}

type historyFile map[string][]string

func (h SearchHistory) read() historyFile {
	f := historyFile{}
	if h.Path == "" {
		return f
	}
	b, err := os.ReadFile(h.Path)
	if err != nil || json.Unmarshal(b, &f) != nil || f == nil {
		return historyFile{} // missing or corrupt: start empty
	}
	return f
}

// Load is one list, oldest first.
func (h SearchHistory) Load(kind string) []string { return slices.Clone(h.read()[kind]) }

func validKind(kind string) bool { return kind == HistoryGlobal || kind == HistoryTasksFilter }

// Add appends a query to a list: blank queries, queries over the length cap and a repeat of the
// newest entry are dropped, and only the last MaxSearchHistory are kept. The file is updated under
// a lock and written atomically (temp file, fsync, rename, directory fsync) with mode 0600.
func (h SearchHistory) Add(kind, q string) error {
	if !validKind(kind) {
		return fmt.Errorf("search history: unknown list %q", kind)
	}
	q = strings.TrimSpace(Sanitize(q))
	if q == "" || utf8.RuneCountInString(q) > query.MaxLen || h.Path == "" {
		return nil
	}
	if !filepath.IsAbs(h.Path) {
		return errors.New("search history: the path must be absolute")
	}
	if underTrackerDir(h.Path) {
		return errors.New("search history: abacus does not write into a tracker directory")
	}
	dir := filepath.Dir(h.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("search history: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // a directory made earlier, or by another tool, may be looser
		return fmt.Errorf("search history: %w", err)
	}
	unlock, err := lockWithin(h.Path, HistoryLockWait)
	if errors.Is(err, errLockBusy) {
		return ErrHistoryBusy
	}
	if err != nil {
		return fmt.Errorf("search history: %w", err)
	}
	defer unlock()
	f := h.read()
	list := f[kind]
	if n := len(list); n > 0 && list[n-1] == q {
		return nil
	}
	list = append(list, q)
	if len(list) > MaxSearchHistory {
		list = list[len(list)-MaxSearchHistory:]
	}
	f[kind] = list
	return writeJSONAtomic(h.Path, f)
}

// underTrackerDir is whether any component of path, as written or with symlinks resolved, is a
// tracker directory (.beads, in any case).
func underTrackerDir(path string) bool {
	for _, p := range []string{path, resolveExisting(path)} {
		for _, c := range strings.Split(filepath.ToSlash(p), "/") {
			if strings.EqualFold(c, ".beads") {
				return true
			}
		}
	}
	return false
}

// resolveExisting resolves the symlinks of the deepest part of path that exists and appends the rest.
func resolveExisting(path string) string {
	rest, cur := "", path
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		rest, cur = filepath.Join(filepath.Base(cur), rest), parent
	}
}

// writeJSONAtomic replaces path with v's JSON: a temp file in the same directory, fsynced, then
// renamed over the target and the directory fsynced, so a reader never sees a torn file.
func writeJSONAtomic(path string, v any) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("search history: %w", err)
	}
	defer os.Remove(tmp.Name())
	b, _ := json.MarshalIndent(v, "", "  ")
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("search history: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("search history: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("search history: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("search history: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("search history: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
