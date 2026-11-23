//go:build unix

package core_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// A held lock never hangs the UI: Add gives up after the wait and says the history was not saved.
func TestSearchHistoryLockIsBounded(t *testing.T) {
	h, p := histAt(t)
	if err := h.Add(core.HistoryGlobal, "first"); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	old := core.HistoryLockWait
	core.HistoryLockWait = 150 * time.Millisecond
	defer func() { core.HistoryLockWait = old }()
	start := time.Now()
	err = h.Add(core.HistoryGlobal, "second")
	if !errors.Is(err, core.ErrHistoryBusy) {
		t.Fatalf("err = %v, want ErrHistoryBusy", err)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Errorf("waited %v", d)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := h.Add(core.HistoryGlobal, "second"); err != nil {
		t.Errorf("after the lock was released: %v", err)
	}
}

// A symlink into a tracker directory is a tracker directory.
func TestSearchHistoryRefusesASymlinkedTrackerDir(t *testing.T) {
	root := t.TempDir()
	tracker := filepath.Join(root, "proj", ".beads")
	if err := os.MkdirAll(tracker, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent")
	if err := os.Symlink(tracker, link); err != nil {
		t.Fatal(err)
	}
	h := core.SearchHistory{Path: filepath.Join(link, "state", "search-history.json")}
	if h.Add(core.HistoryGlobal, "x") == nil {
		t.Error("history must not be written through a symlink into .beads")
	}
	if ents, _ := os.ReadDir(tracker); len(ents) != 0 {
		t.Errorf("the tracker directory was written to: %v", ents)
	}
}
