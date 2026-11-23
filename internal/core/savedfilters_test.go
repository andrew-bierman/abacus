package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func filtersAt(t *testing.T) (core.SavedFilters, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config", "abacus", "saved-filters.json")
	return core.SavedFilters{Path: p}, p
}

func TestSavedFiltersRoundTripSortedPrivateAndReplace(t *testing.T) {
	f, p := filtersAt(t)
	for _, c := range [][2]string{{"ui work", "label:ui"}, {"blocked", "status:blocked"}, {"ui work", "label:ui !status:closed"}} {
		if err := f.Save(c[0], c[1]); err != nil {
			t.Fatal(err)
		}
	}
	got := f.List()
	if len(got) != 2 || got[0].Name != "blocked" || got[1].Name != "ui work" || got[1].Query != "label:ui !status:closed" {
		t.Fatalf("list = %+v (sorted by name, a repeat name replaces)", got)
	}
	if s, ok := f.Get("blocked"); !ok || s.Query != "status:blocked" {
		t.Errorf("get = %+v %v", s, ok)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v %v, want 0600", st, err)
	}
	if err := f.Delete("blocked"); err != nil || len(f.List()) != 1 {
		t.Errorf("delete: %v %v", err, f.List())
	}
	if f.Delete("blocked") == nil {
		t.Error("deleting a missing filter is an error, not silence")
	}
}

func TestSavedFiltersValidation(t *testing.T) {
	f, _ := filtersAt(t)
	for name, c := range map[string][2]string{
		"empty name":        {"", "label:ui"},
		"leading space":     {" a", "label:ui"},
		"control char":      {"a\x1b[2J", "label:ui"},
		"punctuation first": {"-a", "label:ui"},
		"too long name":     {strings.Repeat("n", core.MaxSavedFilterName+1), "label:ui"},
		"empty query":       {"a", "  "},
		"unparseable":       {"a", "nofield:value"},
		"control query":     {"a", "label:ui\x1b"},
		"huge query":        {"a", "label:" + strings.Repeat("x", 3000)},
	} {
		if err := f.Save(c[0], c[1]); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	if n := len(f.List()); n != 0 {
		t.Errorf("%d filters after refusals", n)
	}
}

func TestSavedFiltersAreBounded(t *testing.T) {
	f, _ := filtersAt(t)
	for i := 0; i < core.MaxSavedFilters; i++ {
		if err := f.Save(fmt.Sprintf("f%02d", i), "label:ui"); err != nil {
			t.Fatal(err)
		}
	}
	if f.Save("one-too-many", "label:ui") == nil {
		t.Error("a save past the bound must be refused")
	}
	if f.Save("f07", "label:db") != nil {
		t.Error("replacing within the bound is fine")
	}
	if len(f.List()) != core.MaxSavedFilters {
		t.Errorf("%d kept", len(f.List()))
	}
}

func TestSavedFiltersCorruptFileIsKeptAside(t *testing.T) {
	f, p := filtersAt(t)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(f.List()) != 0 {
		t.Error("a corrupt file reads as empty")
	}
	if err := f.Save("a", "label:ui"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p + ".corrupt"); err != nil || string(b) != "{not json" {
		t.Errorf("the unreadable file must be kept: %q %v", b, err)
	}
	if len(f.List()) != 1 {
		t.Error("the save started from empty")
	}
}

func TestSavedFiltersIgnoreHostileStoredEntries(t *testing.T) {
	f, p := filtersAt(t)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	raw := `{"schema_version":1,"filters":[{"name":"ok","query":"label:ui"},{"name":"bad\u001b[2J","query":"x"},{"name":"ok","query":"dup"},{"name":"ctl","query":"a\u001bb"}]}`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if l := f.List(); len(l) != 1 || l[0].Name != "ok" || l[0].Query != "label:ui" {
		t.Errorf("list = %+v", l)
	}
}

func TestSavedFiltersNeverWriteIntoATrackerDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj", ".beads", "state")
	f := core.SavedFilters{Path: filepath.Join(dir, "saved-filters.json")}
	if f.Save("a", "label:ui") == nil {
		t.Error("saved inside .beads")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the tracker directory was touched")
	}
	if (core.SavedFilters{Path: "relative.json"}).Save("a", "label:ui") == nil {
		t.Error("a relative path must be refused")
	}
	if (core.SavedFilters{}).Save("a", "label:ui") == nil {
		t.Error("a store with no path keeps nothing, and says so")
	}
}

// Atomic: a save that cannot complete leaves the old file whole. The directory is made read-only,
// so only a write that goes straight to the file (not temp file + rename) could change it.
func TestSavedFiltersWriteIsAtomic(t *testing.T) {
	f, p := filtersAt(t)
	if err := f.Save("keep", "label:ui"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	if err := os.Chmod(filepath.Dir(p), 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Dir(p), 0o700)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	// The lock file already exists, so the lock still works and only the replace can fail.
	if f.Save("new", "label:db") == nil {
		t.Fatal("expected the save to fail in a read-only directory")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Errorf("the file changed although the save failed:\n%s\n---\n%s", before, after)
	}
}

// Readers during a burst of writers always see a whole, parseable file, and concurrent writers
// (here goroutines opening the store independently, as separate abacus processes would) lose nothing.
func TestSavedFiltersConcurrentWritersUnderLockLoseNothing(t *testing.T) {
	f, p := filtersAt(t)
	const n = 20
	var wg sync.WaitGroup
	stop := make(chan struct{})
	bad := make(chan string, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(p); err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
				select {
				case bad <- string(b):
				default:
				}
			}
		}
	}()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := (core.SavedFilters{Path: p}).Save(fmt.Sprintf("w%02d", i), "label:ui"); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	select {
	case b := <-bad:
		t.Fatalf("a reader saw a partial file: %q", b)
	default:
	}
	if got := len(f.List()); got != n {
		t.Errorf("%d of %d concurrent saves survived", got, n)
	}
}
