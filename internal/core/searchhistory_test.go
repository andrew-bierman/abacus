package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func histAt(t *testing.T) (core.SearchHistory, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state", "abacus", "search-history.json")
	return core.SearchHistory{Path: p}, p
}

func TestSearchHistoryRoundTripKeepsListsApartAndPrivate(t *testing.T) {
	h, p := histAt(t)
	for _, q := range []string{"lease", "status:open", "renewal"} {
		if err := h.Add(core.HistoryGlobal, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Add(core.HistoryTasksFilter, "label:ui"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.Load(core.HistoryGlobal), "|"); got != "lease|status:open|renewal" {
		t.Errorf("global = %q (oldest first)", got)
	}
	if got := strings.Join(h.Load(core.HistoryTasksFilter), "|"); got != "label:ui" {
		t.Errorf("tasks-filter = %q", got)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v; want 0600", st, err)
	}
	if st, _ := os.Stat(filepath.Dir(p)); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", st.Mode().Perm())
	}
}

func TestSearchHistoryDropsEmptyConsecutiveDuplicatesAndKeepsLastHundred(t *testing.T) {
	h, _ := histAt(t)
	for _, q := range []string{"", "   ", "a", "a", " a ", "b", "a"} {
		_ = h.Add(core.HistoryGlobal, q)
	}
	if got := strings.Join(h.Load(core.HistoryGlobal), "|"); got != "a|b|a" {
		t.Errorf("got %q, want a|b|a (blank and consecutive duplicates dropped)", got)
	}
	for i := 0; i < 130; i++ {
		if err := h.Add(core.HistoryGlobal, fmt.Sprintf("q%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	got := h.Load(core.HistoryGlobal)
	if len(got) != core.MaxSearchHistory || got[0] != "q30" || got[len(got)-1] != "q129" {
		t.Errorf("len %d first %q last %q; want the last %d", len(got), got[0], got[len(got)-1], core.MaxSearchHistory)
	}
}

func TestSearchHistoryRefusesBadKindLongQueryAndTrackerPaths(t *testing.T) {
	h, _ := histAt(t)
	if h.Add("nope", "x") == nil {
		t.Error("an unknown list must be refused")
	}
	if err := h.Add(core.HistoryGlobal, strings.Repeat("x", 300)); err != nil || len(h.Load(core.HistoryGlobal)) != 0 {
		t.Errorf("a query over the cap is not kept: %v %v", err, h.Load(core.HistoryGlobal))
	}
	bad := core.SearchHistory{Path: filepath.Join(t.TempDir(), "proj", ".beads", "search-history.json")}
	if bad.Add(core.HistoryGlobal, "x") == nil {
		t.Error("abacus must never write history into a tracker directory")
	}
	if _, err := os.Stat(filepath.Dir(bad.Path)); err == nil {
		t.Error("the tracker directory was created")
	}
	if (core.SearchHistory{Path: "relative/h.json"}).Add(core.HistoryGlobal, "x") == nil {
		t.Error("a relative path must be refused")
	}
	if err := (core.SearchHistory{}).Add(core.HistoryGlobal, "x"); err != nil { // no path: in memory only, nothing written
		t.Errorf("an unset path is a no-op, got %v", err)
	}
}

func TestSearchHistoryDefaultPathIsInTheStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	p, err := core.DefaultSearchHistoryPath()
	if err != nil || p != filepath.Join(state, "abacus", "search-history.json") {
		t.Errorf("path = %q, %v", p, err)
	}
}

// Writes are atomic: concurrent writers never leave a torn or temp file, and a corrupt file is
// replaced rather than blocking the history.
func TestSearchHistoryWritesAreAtomicUnderConcurrency(t *testing.T) {
	// The default wait is tuned for people, not for 20 writers on a loaded machine: give the
	// test a generous wait so it checks atomicity, not scheduling.
	prev := core.HistoryLockWait
	core.HistoryLockWait = 30 * time.Second
	t.Cleanup(func() { core.HistoryLockWait = prev })
	h, p := histAt(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Add(core.HistoryGlobal, fmt.Sprintf("w%d", i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(h.Load(core.HistoryGlobal)); n != 20 {
		t.Errorf("%d entries, want 20 (a lost update)", n)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.Contains(e.Name(), "tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(h.Load(core.HistoryGlobal)) != 0 {
		t.Error("a corrupt file reads as empty")
	}
	if err := h.Add(core.HistoryGlobal, "fresh"); err != nil || strings.Join(h.Load(core.HistoryGlobal), "|") != "fresh" {
		t.Errorf("after a corrupt file: %v %v", err, h.Load(core.HistoryGlobal))
	}
}

func TestSearchHistoryRefusesTrackerDirsInAnyCase(t *testing.T) {
	for _, name := range []string{".beads", ".BEADS", ".Beads"} {
		h := core.SearchHistory{Path: filepath.Join(t.TempDir(), "proj", name, "sub", "search-history.json")}
		if h.Add(core.HistoryGlobal, "x") == nil {
			t.Errorf("%s accepted", name)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(h.Path), "..")); err == nil {
			t.Errorf("%s: a directory was created", name)
		}
	}
}

func TestSearchHistoryTightensALooseDirectory(t *testing.T) {
	h, p := histAt(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.Add(core.HistoryGlobal, "x"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Dir(p)); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", st.Mode().Perm())
	}
}
