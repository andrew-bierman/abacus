package core

import (
	"os"
	"path/filepath"
	"sync"
)

// HasTracker reports whether dir (the working directory when empty) is inside a Beads tracker:
// a .beads directory holding metadata.json at or above it, which is how bd itself finds one. A
// bare .beads (bd leaves an events cache in odd places, even in a home directory) is not one.
func HasTracker(dir string) bool {
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return false
		}
	}
	dir, _ = filepath.Abs(dir)
	for {
		if fi, err := os.Stat(filepath.Join(dir, ".beads", "metadata.json")); err == nil && fi.Mode().IsRegular() {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// TrackerChoice is which tracker Tasks reads. Dir is the directory for `bd -C` ("" means the
// working directory); Source is env, here, config, workspace or none. When there is none,
// Offer is the managed workspace a confirmed setup step may create.
type TrackerChoice struct {
	Dir, Source, Offer string
}

// ResolveTracker picks the tracker: ABACUS_BD_DIR, then a tracker at or above the working
// directory, then the configured tracker directory, then an existing managed workspace. With
// none of those it offers the managed workspace (and never creates it).
func ResolveTracker(env string, cfg Config, cwd, workspace string) TrackerChoice {
	switch {
	case env != "":
		return TrackerChoice{Dir: env, Source: "env"}
	case HasTracker(cwd):
		return TrackerChoice{Source: "here"}
	case cfg.TrackerDir != "" && HasTracker(cfg.TrackerDir):
		return TrackerChoice{Dir: cfg.TrackerDir, Source: "config"}
	case workspace != "" && HasTracker(workspace):
		return TrackerChoice{Dir: workspace, Source: "workspace"}
	}
	return TrackerChoice{Source: "none", Offer: workspace}
}

// TrackerRef is the tracker directory shared by every module that reads it (bd and bv), so a
// workspace created while the TUI is open is picked up without a restart.
type TrackerRef struct {
	mu  sync.RWMutex
	dir string
}

func NewTrackerRef(dir string) *TrackerRef { return &TrackerRef{dir: dir} }

func (r *TrackerRef) Dir() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dir
}

func (r *TrackerRef) Set(dir string) {
	r.mu.Lock()
	r.dir = dir
	r.mu.Unlock()
}
