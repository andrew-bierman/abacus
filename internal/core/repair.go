package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/brand"
)

// WorkspaceHealth is what a look at the managed workspace folder found.
type WorkspaceHealth string

const (
	WorkspaceClean     WorkspaceHealth = "clean"      // nothing there (or only earlier repair leftovers): a normal setup applies
	WorkspaceReady     WorkspaceHealth = "ready"      // it is a tracker
	WorkspaceHalfBuilt WorkspaceHealth = "half-built" // a failed init: a .beads folder with no tracker in it, nothing else of the user's
	WorkspaceUnsafe    WorkspaceHealth = "unsafe"     // something is there that a repair must not touch
)

// repairedPrefix names a folder an earlier repair moved aside. It is never counted as the user's.
const repairedPrefix = ".beads.incomplete-"

// maxRepairWalk bounds the look inside .beads: a failed init leaves a handful of files.
const maxRepairWalk = 2000

// InspectWorkspace classifies dir without changing it. It is the one guard behind the repair:
// a repair is only ever offered for WorkspaceHalfBuilt, and only that. Reason says why for
// WorkspaceUnsafe.
//
// Half-built means: dir is a real folder; its own .beads is a real folder with no metadata.json;
// the only other entries are a real .git folder, the files init itself leaves (.gitignore, the gate lock) and folders an earlier repair moved aside; and
// inside .beads there are no links, no special files and no non-empty .jsonl (issue exports),
// so nothing that looks like someone's tasks is ever set aside.
func InspectWorkspace(dir string) (WorkspaceHealth, string) {
	if dir == "" {
		return WorkspaceClean, ""
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return WorkspaceClean, ""
	case err != nil:
		return WorkspaceUnsafe, "cannot look at " + dir + ": " + Sanitize(err.Error())
	case !fi.IsDir():
		return WorkspaceUnsafe, dir + " is not a plain folder"
	}
	if HasOwnTracker(dir) {
		return WorkspaceReady, ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return WorkspaceUnsafe, "cannot read " + dir + ": " + Sanitize(err.Error())
	}
	hasBeads := false
	for _, e := range entries {
		switch name := e.Name(); {
		case name == ".beads":
			hasBeads = true
			if !e.IsDir() { // DirEntry.IsDir is false for a symlink
				return WorkspaceUnsafe, ".beads in " + dir + " is not a plain folder"
			}
		case name == ".git":
			if !e.IsDir() {
				return WorkspaceUnsafe, ".git in " + dir + " is not a plain folder"
			}
		case strings.HasPrefix(name, repairedPrefix):
		case name == ".gitignore" || name == ".beads.gate.lock" || name == ".beads.gate.lock.info":
			// files bd init itself leaves in the folder (seen in a real init); never a user's work
			if !e.Type().IsRegular() {
				return WorkspaceUnsafe, name + " in " + dir + " is not a plain file"
			}
		default:
			return WorkspaceUnsafe, dir + " holds other files (" + Sanitize(name) + "), so it is not an unfinished workspace"
		}
	}
	if !hasBeads {
		return WorkspaceClean, ""
	}
	if reason := unsafeInsideBeads(filepath.Join(dir, ".beads")); reason != "" {
		return WorkspaceUnsafe, reason
	}
	return WorkspaceHalfBuilt, ""
}

func unsafeInsideBeads(root string) string {
	n, reason := 0, ""
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			reason = "cannot read inside .beads: " + Sanitize(err.Error())
			return fs.SkipAll
		}
		if n++; n > maxRepairWalk {
			reason = ".beads holds too many files to be a failed start"
			return fs.SkipAll
		}
		t := d.Type()
		switch {
		case t&fs.ModeSymlink != 0 || (!t.IsRegular() && !t.IsDir()):
			reason = ".beads holds a link or special file (" + Sanitize(d.Name()) + ")"
		case t.IsRegular() && strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl"):
			if info, err := d.Info(); err != nil || info.Size() > 0 {
				reason = ".beads holds task data (" + Sanitize(d.Name()) + ")"
			}
		}
		if reason != "" {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && reason == "" {
		reason = "cannot read inside .beads: " + Sanitize(err.Error())
	}
	return reason
}

// planRepair builds the repair plan: set the unfinished .beads aside, then create the workspace
// again through the Tasks write gate. Nothing is deleted.
func (s *Setup) planRepair() (SetupPlan, error) {
	d, ok := s.writer.(InitDirer)
	if s.writer == nil || !ok || d.InitDir() == "" {
		return SetupPlan{}, errors.New("there is no managed workspace to repair")
	}
	dir := d.InitDir()
	switch h, why := InspectWorkspace(dir); h {
	case WorkspaceHalfBuilt:
	case WorkspaceUnsafe:
		return SetupPlan{}, fmt.Errorf("refusing to repair: %s; nothing was changed", why)
	default:
		return SetupPlan{}, errors.New("the workspace is not half-created: nothing to repair")
	}
	wp, err := PlanTaskChange(s.writer, TaskChange{Op: TaskInit, Dir: dir})
	if err != nil {
		return SetupPlan{}, err
	}
	now := time.Now
	if s.o.Now != nil {
		now = s.o.Now
	}
	aside := filepath.Join(dir, repairedPrefix+now().UTC().Format("20060102T150405Z"))
	return SetupPlan{id: setupSeq.Add(1), Step: StepRepair, Domain: DomainTasks, wp: wp, dir: dir, aside: aside,
		Argv: []string{brand.Name, "setup", "--repair"}, Lines: []string{
			"repair the half-created fleet workspace",
			"folder: " + dir,
			"1. move " + filepath.Join(dir, ".beads") + " aside to " + aside + " (nothing is deleted)",
			"2. create the workspace again: " + DisplayArgv(wp.Argv),
			"refused instead if the folder holds anything else of yours",
		}}, nil
}

// applyRepair re-checks the folder, moves the unfinished .beads aside (audited, single use) and
// then creates the workspace through the normal Tasks write gate.
func (s *Setup) applyRepair(ctx context.Context, p SetupPlan) (string, error) {
	e := AuditEntry{Domain: string(p.Domain), Verb: "setup-repair", Argv: p.Argv, Tracker: p.dir}
	err := s.gate.do(p.id, e, func() error {
		if h, why := InspectWorkspace(p.dir); h != WorkspaceHalfBuilt {
			if why == "" {
				why = "it is no longer half-created"
			}
			return changed(why)
		}
		if _, err := os.Lstat(p.aside); err == nil {
			return changed(p.aside + " already exists")
		}
		return os.Rename(filepath.Join(p.dir, ".beads"), p.aside)
	})
	if err != nil {
		return "", err
	}
	if _, err := s.applier.Apply(ctx, p.wp.Confirm()); err != nil {
		return "", fmt.Errorf("the unfinished files were moved aside to %s, but creating the workspace failed: %w", p.aside, err)
	}
	return "repaired your fleet workspace (the unfinished files are kept in " + p.aside + ")", nil
}
