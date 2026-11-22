package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestConfigRoundTripsAtomicallyWithPrivateModes(t *testing.T) {
	st := core.ConfigStore{Path: filepath.Join(t.TempDir(), "c", "abacus", "config.json")}
	if c, err := st.Load(); err != nil || c != (core.Config{}) {
		t.Fatalf("a missing file is an empty config: %+v %v", c, err)
	}
	if err := st.Update(func(c *core.Config) error { return c.Set("actor", "me-laptop") }); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *core.Config) error { return c.Set("tracker_dir", "/data/ws") }); err != nil {
		t.Fatal(err)
	}
	c, err := st.Load()
	if err != nil || c.Actor != "me-laptop" || c.TrackerDir != "/data/ws" {
		t.Fatalf("%+v %v", c, err)
	}
	fi, _ := os.Stat(st.Path)
	di, _ := os.Stat(filepath.Dir(st.Path))
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("modes %v %v", fi.Mode().Perm(), di.Mode().Perm())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(st.Path), "*.tmp*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestConfigRefusesBadValuesAndUnknownKeys(t *testing.T) {
	var c core.Config
	for _, bad := range [][2]string{
		{"actor", ""}, {"actor", "has space"}, {"actor", "-lead"}, {"actor", strings.Repeat("a", 65)},
		{"tracker_dir", "relative/dir"}, {"lambe_config", "rel.json"}, {"nope", "x"},
	} {
		if err := c.Set(bad[0], bad[1]); err == nil {
			t.Errorf("Set(%q, %q) should fail", bad[0], bad[1])
		}
	}
	if c != (core.Config{}) {
		t.Fatalf("a refused set changed the config: %+v", c)
	}
}

func TestConfigLoadFailsOnAnUnreadableFileRatherThanGuessing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (core.ConfigStore{Path: p}).Load(); err == nil {
		t.Fatal("want an error")
	}
}

func TestDefaultPathsFollowXDGAndNeverRelative(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/c")
	t.Setenv("XDG_DATA_HOME", "/x/d")
	if p, _ := core.DefaultConfigPath(); p != "/x/c/abacus/config.json" {
		t.Errorf("config path %q", p)
	}
	if p, _ := core.DefaultWorkspaceDir(); p != "/x/d/abacus/workspace" {
		t.Errorf("workspace %q", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/h")
	if p, _ := core.DefaultConfigPath(); p != "/h/.config/abacus/config.json" {
		t.Errorf("config path %q", p)
	}
	if p, _ := core.DefaultWorkspaceDir(); p != "/h/.local/share/abacus/workspace" {
		t.Errorf("workspace %q", p)
	}
}

func TestUpdateKeepsACorruptFileAsDotCorruptAndStartsFromEmpty(t *testing.T) {
	dir := t.TempDir()
	st := core.ConfigStore{Path: filepath.Join(dir, "config.json")}
	bad := []byte("{not json, and precious")
	if err := os.WriteFile(st.Path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); err == nil {
		t.Fatal("Load must report the corrupt file")
	}
	note, err := st.UpdateNote(func(c *core.Config) error { return c.Set("actor", "me-1") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "config.json.corrupt") {
		t.Fatalf("the step result must say where the old file went: %q", note)
	}
	kept, err := os.ReadFile(st.Path + ".corrupt")
	if err != nil || string(kept) != string(bad) {
		t.Fatalf("the unreadable bytes must be preserved: %q %v", kept, err)
	}
	if fi, _ := os.Stat(st.Path + ".corrupt"); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if c, err := st.Load(); err != nil || c.Actor != "me-1" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestAFailedUpdateOfACorruptFileLeavesItWhereItIs(t *testing.T) {
	st := core.ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	if err := os.WriteFile(st.Path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *core.Config) error { return c.Set("actor", "bad name") }); err == nil {
		t.Fatal("want the validation error")
	}
	if b, _ := os.ReadFile(st.Path); string(b) != "{oops" {
		t.Fatalf("a refused change must not touch the file: %q", b)
	}
}

func TestConcurrentUpdatesDoNotLoseEachOther(t *testing.T) {
	st := core.ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	var wg sync.WaitGroup
	for _, kv := range [][2]string{{"actor", "me-1"}, {"tracker_dir", "/t/ws"}, {"lambe_config", "/t/l.yaml"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.Update(func(c *core.Config) error {
				time.Sleep(30 * time.Millisecond) // a wide read-modify-write window
				return c.Set(kv[0], kv[1])
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	c, err := st.Load()
	if err != nil || c.Actor != "me-1" || c.TrackerDir != "/t/ws" || c.LambeConfig != "/t/l.yaml" {
		t.Fatalf("an update was lost: %+v %v", c, err)
	}
}

func TestSettingAPathToEmptyClearsIt(t *testing.T) {
	c := core.Config{TrackerDir: "/x", LambeConfig: "/y"}
	if err := c.Set("tracker_dir", ""); err != nil || c.TrackerDir != "" || c.LambeConfig != "/y" {
		t.Fatalf("%+v %v", c, err)
	}
	if err := c.Set("actor", ""); err == nil {
		t.Fatal("an empty actor is still invalid")
	}
}

func TestConfigCheckModuleReportsAnUnreadableFileInDoctor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	m := core.NewConfigCheck(core.ConfigStore{Path: p})
	if c := m.Doctor(context.Background()); c[0].Status != core.StatusOK {
		t.Fatalf("%+v", c)
	}
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := m.Doctor(context.Background())
	if c[0].Status != core.StatusWarn || !strings.Contains(c[0].Detail, "not valid JSON") || !strings.Contains(c[0].Fix, ".corrupt") {
		t.Fatalf("%+v", c)
	}
}
