package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newState(t *testing.T) (*TreeState, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state", "tasks-tree.json")
	return NewTreeState(p), p
}

func TestTreeStateRoundTripPerTracker(t *testing.T) {
	s, p := newState(t)
	if got := s.Load("/trk/a"); len(got.Folded) != 0 || got.View != "" {
		t.Fatalf("nothing saved yet: %+v", got)
	}
	if err := s.Save("/trk/a", TreeLayout{Folded: []string{"ac-9", "ac-1", "ac-9"}, View: "flat"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("/trk/b", TreeLayout{Folded: []string{"x-1"}}); err != nil {
		t.Fatal(err)
	}
	a, b := s.Load("/trk/a"), s.Load("/trk/b")
	if !slices.Equal(a.Folded, []string{"ac-1", "ac-9"}) || a.View != "flat" {
		t.Errorf("a: %+v (sorted, deduplicated)", a)
	}
	if !slices.Equal(b.Folded, []string{"x-1"}) || b.View != "" {
		t.Errorf("b: %+v (another tracker's choices must not leak)", b)
	}
	// A new store over the same file sees it: the state survives the process.
	if got := NewTreeState(p).Load("/trk/a"); !slices.Equal(got.Folded, a.Folded) {
		t.Errorf("reload: %+v", got)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("state file mode: %v %v", st, err)
	}
}

func TestTreeStateIsAbacusStateNotTheTracker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	p, err := DefaultTreeStatePath()
	if err != nil || p != filepath.Join(dir, "abacus", "tasks-tree.json") {
		t.Fatalf("path %q err %v", p, err)
	}
	t.Setenv("XDG_STATE_HOME", "relative/dir")
	t.Setenv("HOME", dir)
	if p, _ := DefaultTreeStatePath(); !filepath.IsAbs(p) || filepath.Dir(p) != filepath.Join(dir, ".local", "state", "abacus") {
		t.Errorf("a relative XDG_STATE_HOME is ignored: %q", p)
	}
}

func TestTreeStateSavingEmptyClearsTheTracker(t *testing.T) {
	s, p := newState(t)
	_ = s.Save("/t", TreeLayout{Folded: []string{"a"}, View: "tree"})
	_ = s.Save("/t", TreeLayout{})
	b, _ := os.ReadFile(p)
	var f struct {
		Trackers map[string]json.RawMessage `json:"trackers"`
	}
	_ = json.Unmarshal(b, &f)
	if _, ok := f.Trackers["/t"]; ok {
		t.Errorf("an empty layout is the default, not an entry: %s", b)
	}
}

func TestTreeStateBadFilesAreEmptyAndOverwritable(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":       "\x00\x01 not json",
		"wrong version": `{"schema_version":2,"trackers":{"/t":{"folded":["a"]}}}`,
		"wrong types":   `{"schema_version":1,"trackers":{"/t":{"folded":"a","view":7}}}`,
		"empty":         ``,
		"array":         `[1,2]`,
	} {
		s, p := newState(t)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte(body), 0o600)
		if got := s.Load("/t"); len(got.Folded) != 0 || got.View != "" {
			t.Errorf("%s: loaded %+v", name, got)
		}
		if err := s.Save("/t", TreeLayout{Folded: []string{"a"}}); err != nil {
			t.Errorf("%s: save over a bad file: %v", name, err)
		}
		if got := s.Load("/t"); !slices.Equal(got.Folded, []string{"a"}) {
			t.Errorf("%s: after save %+v", name, got)
		}
	}
}

func TestTreeStateHostileAndBounded(t *testing.T) {
	s, p := newState(t)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	hostile := map[string]any{"schema_version": 1, "trackers": map[string]any{"/t": map[string]any{"folded": []string{"ok", "bad\x1b[2Jid", "", strings.Repeat("a", 500)}, "view": "<script>"}}}
	b, _ := json.Marshal(hostile)
	_ = os.WriteFile(p, b, 0o600)
	got := s.Load("/t")
	if !slices.Equal(got.Folded, []string{"ok", "bad[2Jid"}) {
		t.Errorf("ids are sanitized, empty and oversize ones dropped: %q", got.Folded)
	}
	if got.View != "" {
		t.Errorf("an unknown view is the default: %q", got.View)
	}
	var many []string
	for i := 0; i < maxFolded+50; i++ {
		many = append(many, fmt.Sprintf("id-%05d", i))
	}
	if err := s.Save("/big", TreeLayout{Folded: many}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Load("/big").Folded); n != maxFolded {
		t.Errorf("folded ids per tracker are capped: %d", n)
	}
	big := make([]byte, maxStateFile+10)
	_ = os.WriteFile(p, big, 0o600)
	if got := s.Load("/big"); len(got.Folded) != 0 {
		t.Errorf("an oversized file is not read: %d", len(got.Folded))
	}
}

func TestTreeStateKeepsTheNewestTrackers(t *testing.T) {
	s, _ := newState(t)
	clock := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	for i := 0; i < maxTrackers+5; i++ {
		_ = s.Save(fmt.Sprintf("/t%02d", i), TreeLayout{Folded: []string{"a"}})
	}
	if got := s.Load("/t00"); len(got.Folded) != 0 {
		t.Error("the oldest tracker was dropped")
	}
	if got := s.Load(fmt.Sprintf("/t%02d", maxTrackers+4)); len(got.Folded) != 1 {
		t.Error("the newest tracker was kept")
	}
}

func TestTreeStateNilAndRelativeAreSafe(t *testing.T) {
	var s *TreeState
	if got := s.Load("/t"); len(got.Folded) != 0 {
		t.Error("nil store loads nothing")
	}
	if err := s.Save("/t", TreeLayout{Folded: []string{"a"}}); err != nil {
		t.Errorf("nil store: saving is a no-op, got %v", err)
	}
	if err := NewTreeState("rel/path.json").Save("/t", TreeLayout{Folded: []string{"a"}}); err == nil {
		t.Error("a relative path is refused, never written under the working directory")
	}
}
