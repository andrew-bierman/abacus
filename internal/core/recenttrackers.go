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
)

// Recent trackers are the project directories the user opened or configured, kept in abacus's own
// state directory for the Tasks tracker switcher. abacus never scans folders to fill this list.

const (
	// MaxRecentTrackers bounds the list; the least recently used are dropped.
	MaxRecentTrackers = 20
	maxTrackersFile   = 1 << 20
)

// RecentTracker is one remembered tracker: the directory that holds its .beads.
type RecentTracker struct {
	Dir    string    `json:"dir"`
	UsedAt time.Time `json:"used_at"`
}

// RecentTrackers reads and writes the file at Path. The zero value keeps nothing.
type RecentTrackers struct{ Path string }

type trackersFile struct {
	SchemaVersion int             `json:"schema_version"`
	Trackers      []RecentTracker `json:"trackers"`
}

// DefaultRecentTrackersPath is $XDG_STATE_HOME/abacus/recent-trackers.json or
// ~/.local/state/abacus/recent-trackers.json.
func DefaultRecentTrackersPath() (string, error) {
	d, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return "", errors.New("recent trackers: no home directory to keep them in")
	}
	return filepath.Join(d, "recent-trackers.json"), nil
}

// DefaultRecentTrackers is the store at the default path; without a usable home it keeps nothing.
func DefaultRecentTrackers() RecentTrackers {
	p, err := DefaultRecentTrackersPath()
	if err != nil {
		return RecentTrackers{}
	}
	return RecentTrackers{Path: p}
}

// ValidateTrackerDir is the one check a directory must pass to be a tracker abacus will switch to:
// an absolute, clean path without control characters whose own .beads holds metadata.json. A
// directory merely below a tracker, a bare .beads folder and a plain file are all refused. It
// returns the cleaned directory.
func ValidateTrackerDir(dir string) (string, error) {
	switch {
	case dir == "" || !filepath.IsAbs(dir) || Sanitize(dir) != dir:
		return "", errors.New("a tracker is an absolute directory path")
	case filepath.Base(filepath.Clean(dir)) == ".beads":
		return "", errors.New("give the project directory that holds .beads, not .beads itself")
	}
	dir = filepath.Clean(dir)
	if !HasOwnTracker(dir) {
		return "", fmt.Errorf("%s is not a tracker: no .beads/metadata.json in it", Sanitize(dir))
	}
	for _, d := range []string{dir, filepath.Join(dir, ".beads")} {
		name := filepath.Join(filepath.Base(filepath.Dir(d)), filepath.Base(d)) // short: a notice is one line
		if d == dir {
			name = filepath.Base(d)
		}
		if fi, err := os.Stat(d); err != nil {
			return "", fmt.Errorf("%s cannot be read: %s", Sanitize(name), Sanitize(err.Error()))
		} else if why := UntrustedDir(fi, os.Getuid()); why != "" {
			return "", fmt.Errorf("not switching: %s %s", Sanitize(name), why)
		}
	}
	return dir, nil
}

func (r RecentTrackers) read() []RecentTracker {
	if r.Path == "" {
		return nil
	}
	fi, err := os.Stat(r.Path)
	if err != nil || fi.Size() > maxTrackersFile {
		return nil
	}
	b, err := os.ReadFile(r.Path)
	var f trackersFile
	if err != nil || json.Unmarshal(b, &f) != nil {
		return nil
	}
	var out []RecentTracker
	for _, t := range f.Trackers { // an entry that is not a clean absolute path is never offered
		if filepath.IsAbs(t.Dir) && filepath.Clean(t.Dir) == t.Dir && Sanitize(t.Dir) == t.Dir && !slices.ContainsFunc(out, func(x RecentTracker) bool { return x.Dir == t.Dir }) {
			out = append(out, t)
		}
	}
	slices.SortStableFunc(out, func(a, b RecentTracker) int { return b.UsedAt.Compare(a.UsedAt) })
	return out[:min(len(out), MaxRecentTrackers)]
}

// List is the remembered trackers that still hold a tracker, most recently used first. A folder
// that has gone, or is no longer a tracker, is left out (never offered, never listed on disk).
func (r RecentTrackers) List() []RecentTracker {
	var out []RecentTracker
	for _, t := range r.read() {
		if HasOwnTracker(t.Dir) {
			out = append(out, t)
		}
	}
	return out
}

// Touch remembers dir as used now. A directory that is not a tracker is refused, so the list only
// ever holds trackers the user really opened.
func (r RecentTrackers) Touch(dir string, now time.Time) error {
	dir, err := ValidateTrackerDir(dir)
	if err != nil {
		return fmt.Errorf("recent trackers: %w", err)
	}
	if r.Path == "" {
		return nil
	}
	if !filepath.IsAbs(r.Path) || underTrackerDir(r.Path) {
		return errors.New("recent trackers: abacus does not write into a tracker directory")
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o700); err != nil {
		return fmt.Errorf("recent trackers: %w", err)
	}
	unlock, err := lockWithin(r.Path, 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("recent trackers: %w", err)
	}
	defer unlock()
	list := slices.DeleteFunc(r.read(), func(t RecentTracker) bool { return t.Dir == dir })
	list = append([]RecentTracker{{Dir: dir, UsedAt: now.UTC()}}, list...)
	if len(list) > MaxRecentTrackers {
		list = list[:MaxRecentTrackers]
	}
	if err := writeJSONAtomic(r.Path, trackersFile{SchemaVersion: 1, Trackers: list}); err != nil {
		return errors.New(strings.Replace(err.Error(), "search history:", "recent trackers:", 1))
	}
	return nil
}
