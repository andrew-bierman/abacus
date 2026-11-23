package board

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
)

// asFake serves the fixtures whatever path the built-in copy was unpacked to.
type asFake struct{ f *fakeRun }

func (a asFake) RunSplit(ctx context.Context, bin string, args ...string) ([]byte, []byte, int, error) {
	return a.f.RunSplit(ctx, "/fake/board", args...)
}

// The built-in Board CLI is the biggest and the optional tool: Detect reports it as built in
// without unpacking it, and the first read (or Doctor) unpacks and identifies it.
func TestTheBuiltInBoardIsUnpackedOnFirstUseNotAtDetection(t *testing.T) {
	set := embedtest.Standard()
	t.Cleanup(core.SetEmbeddedForTest(set.FS))
	root := filepath.Join(t.TempDir(), "tools")
	t.Setenv(core.ToolsDirEnv, root)
	f := boardRuns(t)
	m := New(Options{Runner: asFake{f}})
	d := m.Detect(context.Background())
	if d.State != core.Ready || d.Version != "1.0.0" {
		t.Fatalf("%+v", d)
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("detecting the Board must not unpack anything")
	}
	if len(f.calls) != 0 {
		t.Fatalf("detecting the Board must not run it: %v", f.calls)
	}
	if _, err := m.Board(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, set.Manifest.Hash(), "board")); err != nil {
		t.Fatalf("the first read must unpack the Board CLI: %v", err)
	}
	if len(f.calls) == 0 || !strings.Contains(f.calls[0], "--help") {
		t.Fatalf("the unpacked binary is identified before it is used: %v", f.calls)
	}
}

func TestADeferredBoardThatCannotBeSetUpSaysSoOnFirstUse(t *testing.T) {
	set := embedtest.Standard()
	delete(set.FS, "board.zst")
	t.Cleanup(core.SetEmbeddedForTest(set.FS))
	t.Setenv(core.ToolsDirEnv, filepath.Join(t.TempDir(), "tools"))
	m := New(Options{Runner: asFake{boardRuns(t)}})
	m.Detect(context.Background())
	_, err := m.Board(context.Background())
	if err == nil || !strings.Contains(err.Error(), "built-in board") || !strings.Contains(err.Error(), "download it again") {
		t.Fatalf("%v", err)
	}
}
