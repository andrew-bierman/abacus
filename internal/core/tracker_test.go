package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func mkTracker(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHasTrackerWalksUpLikeBdDoes(t *testing.T) {
	root := mkTracker(t, t.TempDir())
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if !core.HasTracker(sub) || core.HasTracker(t.TempDir()) {
		t.Fatal("HasTracker should find an ancestor's .beads and nothing else")
	}
	bare := t.TempDir() // a .beads with only an events cache is not a tracker
	if err := os.MkdirAll(filepath.Join(bare, ".beads", "eventsData"), 0o755); err != nil {
		t.Fatal(err)
	}
	if core.HasTracker(bare) {
		t.Fatal("a bare .beads must not count")
	}
}

func TestResolveTrackerPrecedence(t *testing.T) {
	here := mkTracker(t, t.TempDir())
	cfgWS := mkTracker(t, t.TempDir())
	defWS := filepath.Join(t.TempDir(), "workspace")
	empty := t.TempDir()

	cases := []struct {
		name          string
		env           string
		cfg           core.Config
		cwd           string
		ws            string
		wantDir, want string // dir ("" = the working directory) and source
		wantOffer     string
	}{
		{"env wins over everything", "/env/dir", core.Config{TrackerDir: cfgWS}, here, defWS, "/env/dir", "env", ""},
		{"a tracker in the cwd beats the config", "", core.Config{TrackerDir: cfgWS}, here, defWS, "", "here", ""},
		{"config tracker when the cwd has none", "", core.Config{TrackerDir: cfgWS}, empty, defWS, cfgWS, "config", ""},
		{"a stale config path is not used", "", core.Config{TrackerDir: "/gone/ws"}, empty, defWS, "", "none", defWS},
		{"nothing found offers the managed workspace", "", core.Config{}, empty, defWS, "", "none", defWS},
	}
	for _, c := range cases {
		got := core.ResolveTracker(c.env, c.cfg, c.cwd, c.ws)
		if got.Dir != c.wantDir || got.Source != c.want || got.Offer != c.wantOffer {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	// A workspace created earlier is adopted even if the config forgot it.
	mkTracker(t, defWS)
	if got := core.ResolveTracker("", core.Config{}, empty, defWS); got.Dir != defWS || got.Source != "workspace" || got.Offer != "" {
		t.Errorf("existing managed workspace: %+v", got)
	}
}

func TestTrackerRefIsShared(t *testing.T) {
	r := core.NewTrackerRef("/a")
	r.Set("/b")
	if r.Dir() != "/b" {
		t.Fatal(r.Dir())
	}
}
