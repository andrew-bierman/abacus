package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/andrew-bierman/abacus/internal/brand"
)

// TrackerSwitch is the one gated action of the tracker switcher: point Tasks at another tracker. It
// writes abacus's own config (tracker_dir) and nothing else: no tool command runs and nothing is
// written into either tracker. Like every setup step it is planned first, needs an explicit
// Confirm, can be applied once, and is audited before and after through the shared gate.

// SwitchPlan is a validated switch with the text the confirm screen shows.
type SwitchPlan struct {
	id        uint64
	Dir       string   // the validated target
	From      string   // the tracker in use now ("" when none)
	Lines     []string // what applying does, plain sentences
	Argv      []string // the equivalent config command, shown and audited; never run as a command
	confirmed bool
}

// Confirm marks the plan as explicitly approved.
func (p SwitchPlan) Confirm() SwitchPlan { p.confirmed = true; return p }

var switchSeq atomic.Uint64

// Switcher plans and applies tracker switches.
type Switcher struct {
	Config  ConfigStore
	Recents RecentTrackers
	Audit   Audit
	Target  TrackerSwitcher // re-points the running modules; nil refuses (nothing to switch)
	Current func() string   // the tracker directory in use now
	gate    *gate
}

// NewSwitcher builds a switcher; a nil audit means the default file audit.
func NewSwitcher(cfg ConfigStore, recents RecentTrackers, target TrackerSwitcher, current func() string, a Audit) *Switcher {
	if a == nil {
		a = DefaultAudit()
	}
	return &Switcher{Config: cfg, Recents: recents, Audit: a, Target: target, Current: current, gate: newGate(a)}
}

// Candidates is the trackers the user may switch to: the one in use, the remembered ones and the
// configured tracker_dir, each only while it still holds a tracker, without repeats. current is
// the tracker in use (its root, "" when unknown). Nothing on disk is listed or scanned.
func (s *Switcher) Candidates() (list []RecentTracker, current string) {
	if s.Current != nil {
		current = TrackerRoot(s.Current())
	}
	add := func(t RecentTracker) {
		for _, x := range list {
			if x.Dir == t.Dir {
				return
			}
		}
		list = append(list, t)
	}
	if current != "" {
		add(RecentTracker{Dir: current})
	}
	for _, t := range s.Recents.List() {
		add(t)
	}
	if c, err := s.Config.Load(); err == nil && c.TrackerDir != "" && HasOwnTracker(c.TrackerDir) {
		add(RecentTracker{Dir: filepath.Clean(c.TrackerDir)})
	}
	return list, current
}

// Plan validates dir and describes the switch. It changes nothing.
func (s *Switcher) Plan(dir string) (SwitchPlan, error) {
	if s.Target == nil {
		return SwitchPlan{}, errors.New("switching tracker is not available here")
	}
	if s.Config.Path == "" {
		return SwitchPlan{}, errors.New("no config file to keep the choice in (no home directory?)")
	}
	dir, err := ValidateTrackerDir(dir)
	if err != nil {
		return SwitchPlan{}, err
	}
	from := ""
	if s.Current != nil {
		from = TrackerRoot(s.Current())
	}
	if from == dir {
		return SwitchPlan{}, errors.New("that tracker is already the one in use")
	}
	lines := []string{
		"Tasks will read this tracker from now on.",
		"This saves tracker_dir in the " + brand.Name + " settings and applies now.",
		"No tracker is changed and no tool command runs.",
		"At the next start, ABACUS_BD_DIR or a tracker at or above the working directory still wins.",
	}
	return SwitchPlan{id: switchSeq.Add(1), Dir: dir, From: from, Lines: lines,
		Argv: []string{"abacus", "config", "set", "tracker_dir", dir}}, nil
}

// Apply runs a confirmed plan once: audit the attempt (a failed audit blocks it), save tracker_dir,
// re-point the running modules, remember the tracker, audit the outcome.
func (s *Switcher) Apply(_ context.Context, p SwitchPlan) error {
	if !p.confirmed {
		return ErrNotConfirmed
	}
	e := AuditEntry{Domain: string(DomainTasks), Verb: "switch-tracker", Argv: p.Argv, Tracker: p.Dir}
	return s.gate.do(p.id, e, func() error {
		if _, err := ValidateTrackerDir(p.Dir); err != nil { // it may have gone since the plan
			return err
		}
		if err := s.Config.Update(func(c *Config) error { return c.Set("tracker_dir", p.Dir) }); err != nil {
			return fmt.Errorf("could not save tracker_dir: %w", err)
		}
		if err := s.Target.SetTrackerDir(p.Dir); err != nil {
			return err
		}
		_ = s.Recents.Touch(p.Dir, time.Now()) // a list that cannot be saved must not fail the switch
		return nil
	})
}
