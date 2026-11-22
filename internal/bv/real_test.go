package bv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Runs the real bv, only with --robot-* modes, against an invented project in a temp dir with
// isolated HOME/XDG. Skips when bv is not installed.
func TestRealBvAgainstAnIsolatedTempProject(t *testing.T) {
	if _, err := exec.LookPath("bv"); err != nil {
		t.Skip("bv is not installed")
	}
	home, proj := t.TempDir(), t.TempDir()
	for k, v := range map[string]string{"HOME": home, "XDG_STATE_HOME": home + "/s", "XDG_CONFIG_HOME": home + "/c", "XDG_CACHE_HOME": home + "/k", "BEADS_DIR": proj + "/.beads", "BV_OUTPUT_FORMAT": "toon"} {
		t.Setenv(k, v)
	}
	if err := os.Mkdir(filepath.Join(proj, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	jl := `{"id":"a-1","title":"base","status":"open","priority":1,"issue_type":"task","created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-01T10:00:00Z"}
{"id":"a-2","title":"next","status":"open","priority":2,"issue_type":"task","created_at":"2026-11-02T10:00:00Z","updated_at":"2026-11-02T10:00:00Z","dependencies":[{"issue_id":"a-2","depends_on_id":"a-1","type":"blocks"}]}
`
	if err := os.WriteFile(filepath.Join(proj, ".beads", "issues.jsonl"), []byte(jl), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Dir: proj})
	if d := m.Detect(context.Background()); d.State != core.Ready {
		t.Fatal(d)
	}
	r, err := m.Insights(context.Background())
	if err != nil || r.Reason != "" || len(r.TopPicks) == 0 || r.TopPicks[0].ID != "a-1" || r.GraphNodes != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}
