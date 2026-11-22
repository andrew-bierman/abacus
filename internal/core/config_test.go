package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
