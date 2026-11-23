package core

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// The tree view's fold state is the user's own choice about how to look at a tracker, so it lives
// in abacus's state directory, keyed by tracker, and never inside the tracker's directory.

const (
	treeStateVersion = 1
	maxStateFile     = 4 << 20
	maxTrackers      = 64   // the least recently saved are dropped beyond this
	maxFolded        = 5000 // per tracker
	maxIDLen         = 200
)

// TreeLayout is one tracker's tree choices: which parents are folded, and whether the user chose the
// tree or the flat list ("" means the default: the tree when the tracker has parent-child links).
type TreeLayout struct {
	Folded []string
	View   string // "", "tree" or "flat"
}

func (l TreeLayout) empty() bool { return len(l.Folded) == 0 && l.View == "" }

// TreeState reads and writes the layouts. A nil *TreeState keeps nothing (a demo session, a test).
type TreeState struct {
	path string
	now  func() time.Time
}

// NewTreeState keeps layouts in the JSON file at path.
func NewTreeState(path string) *TreeState { return &TreeState{path: path, now: time.Now} }

// DefaultTreeStatePath is $XDG_STATE_HOME/abacus/tasks-tree.json (only when absolute) or
// ~/.local/state/abacus/tasks-tree.json.
func DefaultTreeStatePath() (string, error) {
	d, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return "", errors.New("tree state: no home directory to keep it in")
	}
	return filepath.Join(d, "tasks-tree.json"), nil
}

// DefaultTreeState is the store at DefaultTreeStatePath; without a usable home it keeps nothing.
func DefaultTreeState() *TreeState {
	p, err := DefaultTreeStatePath()
	if err != nil {
		return nil
	}
	return NewTreeState(p)
}

type treeEntry struct {
	Folded  []string `json:"folded,omitempty"`
	View    string   `json:"view,omitempty"`
	SavedAt string   `json:"saved_at,omitempty"`
}

type treeFile struct {
	SchemaVersion int                  `json:"schema_version"`
	Trackers      map[string]treeEntry `json:"trackers"`
}

// read is the file's content, or an empty file for anything unusable: a missing, oversized,
// unreadable or wrong-version file is the same as no choices, and the next save replaces it.
func (s *TreeState) read() treeFile {
	f := treeFile{SchemaVersion: treeStateVersion, Trackers: map[string]treeEntry{}}
	if s == nil || !filepath.IsAbs(s.path) {
		return f
	}
	fh, err := os.Open(s.path)
	if err != nil {
		return f
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, maxStateFile+1))
	if err != nil || len(b) > maxStateFile {
		return f
	}
	var in treeFile
	if json.Unmarshal(b, &in) != nil || in.SchemaVersion != treeStateVersion {
		return f
	}
	if in.Trackers != nil {
		f.Trackers = in.Trackers
	}
	return f
}

func cleanIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = Sanitize(id)
		if id != "" && len(id) <= maxIDLen {
			out = append(out, id)
		}
	}
	return out
}

// Load is the saved layout of a tracker; the zero layout when there is none.
func (s *TreeState) Load(tracker string) TreeLayout {
	e := s.read().Trackers[tracker]
	l := TreeLayout{Folded: cleanIDs(e.Folded)}
	if e.View == "tree" || e.View == "flat" {
		l.View = e.View
	}
	if len(l.Folded) > maxFolded {
		l.Folded = l.Folded[:maxFolded]
	}
	if len(l.Folded) == 0 {
		l.Folded = nil
	}
	return l
}

// Save records a tracker's layout (an empty one removes the entry), keeping the other trackers'
// entries from a fresh read and replacing the file atomically.
func (s *TreeState) Save(tracker string, l TreeLayout) error {
	if s == nil {
		return nil
	}
	if !filepath.IsAbs(s.path) {
		return errors.New("tree state: path is empty or not absolute")
	}
	f := s.read()
	ids := cleanIDs(l.Folded)
	sort.Strings(ids)
	ids = slices.Compact(ids)
	if len(ids) > maxFolded {
		ids = ids[:maxFolded]
	}
	l.Folded = ids
	if l.View != "tree" && l.View != "flat" {
		l.View = ""
	}
	if l.empty() {
		delete(f.Trackers, tracker)
	} else {
		f.Trackers[tracker] = treeEntry{Folded: l.Folded, View: l.View, SavedAt: s.now().UTC().Format(time.RFC3339)}
	}
	for len(f.Trackers) > maxTrackers {
		var oldest string
		for k, e := range f.Trackers {
			if oldest == "" || e.SavedAt < f.Trackers[oldest].SavedAt || (e.SavedAt == f.Trackers[oldest].SavedAt && k < oldest) {
				oldest = k
			}
		}
		delete(f.Trackers, oldest)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tasks-tree-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil { // CreateTemp makes the file 0600
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
