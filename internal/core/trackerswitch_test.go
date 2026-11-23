package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// mkTracker makes a directory that looks like a tracker (.beads/metadata.json): no tool runs.
func mkNamedTracker(t *testing.T, root, name string) string {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(d, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, ".beads", "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

type fakeTarget struct {
	dir string
	err error
}

func (f *fakeTarget) SetTrackerDir(d string) error {
	if f.err == nil {
		f.dir = d
	}
	return f.err
}

type switchEnv struct {
	root          string
	a, b          string
	sw            *core.Switcher
	target        *fakeTarget
	audit         *memAudit
	cfg           core.ConfigStore
	recents       core.RecentTrackers
	current       *string
	untouchedFile string
}

func newSwitchEnv(t *testing.T) *switchEnv {
	t.Helper()
	root := t.TempDir()
	e := &switchEnv{root: root, a: mkNamedTracker(t, root, "alpha"), b: mkNamedTracker(t, root, "beta"), target: &fakeTarget{}, audit: &memAudit{}}
	e.cfg = core.ConfigStore{Path: filepath.Join(root, "cfg", "config.json")}
	e.recents = core.RecentTrackers{Path: filepath.Join(root, "state", "recent-trackers.json")}
	cur := e.a
	e.current = &cur
	e.sw = core.NewSwitcher(e.cfg, e.recents, e.target, func() string { return *e.current }, e.audit)
	e.untouchedFile = filepath.Join(e.b, ".beads", "metadata.json")
	return e
}

func TestSwitchPlanRefusesNonTrackers(t *testing.T) {
	e := newSwitchEnv(t)
	plain := filepath.Join(e.root, "plain")
	_ = os.MkdirAll(plain, 0o700)
	bareBeads := filepath.Join(e.root, "bare")
	_ = os.MkdirAll(filepath.Join(bareBeads, ".beads"), 0o700) // .beads without metadata.json
	file := filepath.Join(e.root, "afile")
	_ = os.WriteFile(file, nil, 0o600)
	below := filepath.Join(e.a, "sub")
	_ = os.MkdirAll(below, 0o700)
	for name, dir := range map[string]string{
		"plain dir":         plain,
		"bare .beads":       bareBeads,
		"the .beads dir":    filepath.Join(e.b, ".beads"),
		"a file":            file,
		"missing":           filepath.Join(e.root, "nope"),
		"empty":             "",
		"relative":          "beta",
		"below a tracker":   below,
		"control character": e.b + "\x1b[2J",
	} {
		if _, err := e.sw.Plan(dir); err == nil {
			t.Errorf("%s: planned", name)
		}
	}
	if _, err := e.sw.Plan(e.a); err == nil {
		t.Error("switching to the tracker already in use is refused")
	}
	if _, err := e.sw.Plan(e.b); err != nil {
		t.Errorf("a real tracker is accepted: %v", err)
	}
}

func TestSwitchApplyIsGatedAuditedAndWritesOnlyAbacusConfig(t *testing.T) {
	e := newSwitchEnv(t)
	p, err := e.sw.Plan(e.b)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Argv, " "); got != "abacus config set tracker_dir "+e.b {
		t.Errorf("argv shown = %q", got)
	}
	if err := e.sw.Apply(context.Background(), p); !errors.Is(err, core.ErrNotConfirmed) {
		t.Fatalf("an unconfirmed plan must not apply: %v", err)
	}
	if e.target.dir != "" || len(e.audit.lines) != 0 {
		t.Fatal("nothing may happen before the confirm")
	}
	before, _ := os.ReadFile(e.untouchedFile)
	if err := e.sw.Apply(context.Background(), p.Confirm()); err != nil {
		t.Fatal(err)
	}
	if e.target.dir != e.b {
		t.Errorf("the modules were pointed at %q", e.target.dir)
	}
	if c, _ := e.cfg.Load(); c.TrackerDir != e.b {
		t.Errorf("tracker_dir = %q", c.TrackerDir)
	}
	if l := e.recents.List(); len(l) != 1 || l[0].Dir != e.b {
		t.Errorf("recents = %+v", l)
	}
	if len(e.audit.lines) != 2 || e.audit.lines[0].Phase != "attempt" || e.audit.lines[1].Phase != "ok" || e.audit.lines[0].Verb != "switch-tracker" || e.audit.lines[0].Tracker != e.b {
		t.Errorf("audit = %+v (an attempt line, then an ok line)", e.audit.lines)
	}
	if after, _ := os.ReadFile(e.untouchedFile); string(after) != string(before) {
		t.Error("a tracker file changed")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.b, ".beads")); len(ents) != 1 {
		t.Errorf("something was written into the tracker: %v", ents)
	}
	if err := e.sw.Apply(context.Background(), p.Confirm()); !errors.Is(err, core.ErrAlreadyApplied) {
		t.Errorf("a plan applies once: %v", err)
	}
}

func TestSwitchAuditFailureBlocksTheSwitch(t *testing.T) {
	e := newSwitchEnv(t)
	e.audit.err = errors.New("disk full")
	p, _ := e.sw.Plan(e.b)
	if err := e.sw.Apply(context.Background(), p.Confirm()); err == nil {
		t.Fatal("applied although the audit failed")
	}
	if c, _ := e.cfg.Load(); c.TrackerDir != "" || e.target.dir != "" {
		t.Error("the audit is fail-closed: nothing may change")
	}
}

func TestSwitchRevalidatesAtApplyTime(t *testing.T) {
	e := newSwitchEnv(t)
	p, _ := e.sw.Plan(e.b)
	_ = os.RemoveAll(e.b)
	err := e.sw.Apply(context.Background(), p.Confirm())
	if err == nil || e.target.dir != "" {
		t.Fatalf("a tracker that vanished after the plan must not be switched to: %v", err)
	}
	if last := e.audit.lines[len(e.audit.lines)-1]; last.Phase != "error" {
		t.Errorf("the failure is audited: %+v", last)
	}
}

func TestSwitchCandidatesAreOnlyKnownTrackers(t *testing.T) {
	e := newSwitchEnv(t)
	stray := mkNamedTracker(t, e.root, "never-opened") // exists on disk, but the user never opened it
	gone := mkNamedTracker(t, e.root, "gone")
	_ = e.recents.Touch(gone, time.Now().Add(-time.Hour))
	_ = e.recents.Touch(e.b, time.Now())
	_ = os.RemoveAll(gone)
	list, cur := e.sw.Candidates()
	var dirs []string
	for _, t := range list {
		dirs = append(dirs, t.Dir)
	}
	if cur != e.a {
		t.Errorf("current = %q", cur)
	}
	if got := strings.Join(dirs, "|"); got != e.a+"|"+e.b {
		t.Errorf("candidates = %q: the one in use, then recents that still exist; never %s", got, stray)
	}
	// the configured tracker_dir counts as a tracker the user chose
	_ = e.cfg.Update(func(c *core.Config) error { return c.Set("tracker_dir", stray) })
	list, _ = e.sw.Candidates()
	if len(list) != 3 || list[2].Dir != stray {
		t.Errorf("configured tracker missing: %+v", list)
	}
}

func TestRecentTrackersBoundOrderAndRefusal(t *testing.T) {
	root := t.TempDir()
	r := core.RecentTrackers{Path: filepath.Join(root, "s", "recent.json")}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var dirs []string
	for i := 0; i < core.MaxRecentTrackers+5; i++ {
		d := mkNamedTracker(t, root, "t"+string(rune('a'+i)))
		dirs = append(dirs, d)
		if err := r.Touch(d, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got := r.List()
	if len(got) != core.MaxRecentTrackers || got[0].Dir != dirs[len(dirs)-1] {
		t.Errorf("%d kept, newest first expected", len(got))
	}
	_ = r.Touch(dirs[10], base.Add(time.Hour)) // touching moves it to the front without duplicating
	if g := r.List(); g[0].Dir != dirs[10] || len(g) != core.MaxRecentTrackers {
		t.Errorf("touch: %+v", g[0])
	}
	if r.Touch(filepath.Join(root, "plain"), base) == nil {
		t.Error("a non-tracker is never remembered")
	}
	if (core.RecentTrackers{Path: filepath.Join(root, "x", ".beads", "r.json")}).Touch(dirs[0], base) == nil {
		t.Error("never written into a tracker directory")
	}
}
