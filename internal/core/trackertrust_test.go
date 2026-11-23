//go:build unix

package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestSwitchRefusesATrackerOthersCanWrite(t *testing.T) {
	e := newSwitchEnv(t)
	for name, dir := range map[string]string{"project dir": e.b, ".beads": filepath.Join(e.b, ".beads")} {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		_, err := e.sw.Plan(e.b)
		if err == nil || !strings.Contains(err.Error(), "writable by other users") {
			t.Errorf("%s 0777: %v", name, err)
		}
		_ = os.Chmod(dir, 0o700)
		if _, err := e.sw.Plan(e.b); err != nil {
			t.Errorf("%s 0700 again: %v", name, err)
		}
	}
	if err := os.Chmod(e.b, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sw.Plan(e.b); err == nil {
		t.Error("group-writable is refused too")
	}
	_ = os.Chmod(e.b, 0o755) // readable by others, writable by none: fine
	if _, err := e.sw.Plan(e.b); err != nil {
		t.Errorf("0755: %v", err)
	}
}

func TestTrackerOwnership(t *testing.T) {
	fi, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getuid()
	if why := core.UntrustedDir(fi, me); why != "" {
		t.Errorf("my own directory: %q", why)
	}
	if why := core.UntrustedDir(fi, me+1); !strings.Contains(why, "another user") {
		t.Errorf("a directory of another user: %q", why)
	}
}

// The switch repoints the live modules first and saves tracker_dir only once that worked; if the save
// fails the repoint is rolled back, so the screen and the settings never disagree.
func TestSwitchRollsBackWhenTheConfigCannotBeSaved(t *testing.T) {
	e := newSwitchEnv(t)
	e.target.dir = e.a
	blocker := filepath.Join(e.root, "blocker")
	_ = os.WriteFile(blocker, nil, 0o600)
	cfg := core.ConfigStore{Path: filepath.Join(blocker, "config.json")} // its directory is a file
	sw := core.NewSwitcher(cfg, e.recents, e.target, func() string { return e.a }, e.audit)
	p, err := sw.Plan(e.b)
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Apply(context.Background(), p.Confirm()); err == nil {
		t.Fatal("the save cannot work")
	}
	if e.target.dir != e.a {
		t.Errorf("the repoint was not rolled back: %q", e.target.dir)
	}
	if last := e.audit.lines[len(e.audit.lines)-1]; last.Phase != "error" {
		t.Errorf("audit %+v", last)
	}
}

type failTarget struct{ dir string }

func (f *failTarget) SetTrackerDir(string) error { return errors.New("cannot repoint") }

func TestSwitchWritesNoConfigWhenTheRepointFails(t *testing.T) {
	e := newSwitchEnv(t)
	sw := core.NewSwitcher(e.cfg, e.recents, &failTarget{}, func() string { return e.a }, e.audit)
	p, _ := sw.Plan(e.b)
	if err := sw.Apply(context.Background(), p.Confirm()); err == nil {
		t.Fatal("the repoint failed")
	}
	if c, _ := e.cfg.Load(); c.TrackerDir != "" {
		t.Errorf("tracker_dir %q was saved although nothing was repointed", c.TrackerDir)
	}
	if _, err := os.Stat(e.cfg.Path); err == nil {
		t.Error("the config file was created")
	}
}

// The config write is atomic: after a switch the file is whole JSON with mode 0600 and no temp files
// are left; a write that cannot complete leaves the old content.
func TestSwitchConfigWriteIsAtomic(t *testing.T) {
	e := newSwitchEnv(t)
	_ = e.cfg.Update(func(c *core.Config) error { return c.Set("actor", "ada") })
	p, _ := e.sw.Plan(e.b)
	if err := e.sw.Apply(context.Background(), p.Confirm()); err != nil {
		t.Fatal(err)
	}
	c, err := e.cfg.Load()
	if err != nil || c.TrackerDir != e.b || c.Actor != "ada" {
		t.Fatalf("%+v %v", c, err)
	}
	if st, _ := os.Stat(e.cfg.Path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if ents, _ := os.ReadDir(filepath.Dir(e.cfg.Path)); len(ents) != 2 { // config.json and its lock
		t.Errorf("leftovers: %v", ents)
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	before, _ := os.ReadFile(e.cfg.Path)
	_ = os.Chmod(filepath.Dir(e.cfg.Path), 0o500)
	defer os.Chmod(filepath.Dir(e.cfg.Path), 0o700)
	if err := e.cfg.Update(func(c *core.Config) error { return c.Set("actor", "bob") }); err == nil {
		t.Fatal("expected the write to fail")
	}
	after, _ := os.ReadFile(e.cfg.Path)
	if string(before) != string(after) {
		t.Errorf("a failed write changed the file:\n%s\n--\n%s", before, after)
	}
}

// Save and Delete run on the UI goroutine: a lock held by another process makes them give up quickly.
func TestSavedFilterLockWaitIsShort(t *testing.T) {
	f, p := filtersAt(t)
	if err := f.Save("a", "label:ui"); err != nil {
		t.Fatal(err)
	}
	lf, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]func() error{
		"save":   func() error { return f.Save("b", "label:db") },
		"delete": func() error { return f.Delete("a") },
	} {
		start := time.Now()
		if err := op(); !errors.Is(err, core.ErrFiltersBusy) {
			t.Errorf("%s: %v", name, err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s waited %v for a held lock", name, d)
		}
	}
}

func TestSavedFilterNameLengthBoundary(t *testing.T) {
	f, _ := filtersAt(t)
	if err := f.Save(strings.Repeat("n", core.MaxSavedFilterName), "label:ui"); err != nil {
		t.Errorf("exactly %d characters is allowed: %v", core.MaxSavedFilterName, err)
	}
	if err := f.Save(strings.Repeat("n", core.MaxSavedFilterName+1), "label:ui"); err == nil {
		t.Errorf("%d characters is refused", core.MaxSavedFilterName+1)
	}
	if err := f.Save(strings.Repeat("é", core.MaxSavedFilterName), "label:ui"); err == nil {
		t.Error("a name is ASCII: letters and digits")
	}
}
