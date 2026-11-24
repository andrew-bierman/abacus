package agents

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// newOpenCodeDB builds the subset of opencode's v2 schema this reader depends on, with
// invented rows. No real session data is ever committed: the fixtures here are synthetic.
func newOpenCodeDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE session_v2 (
			id text PRIMARY KEY,
			parent_id text,
			title text,
			model text,
			directory text NOT NULL,
			cost real DEFAULT 0 NOT NULL,
			tokens_input integer DEFAULT 0 NOT NULL,
			tokens_output integer DEFAULT 0 NOT NULL,
			tokens_reasoning integer DEFAULT 0 NOT NULL,
			tokens_cache_read integer DEFAULT 0 NOT NULL,
			tokens_cache_write integer DEFAULT 0 NOT NULL,
			time_busy integer,
			time_idle integer,
			time_compacting integer,
			time_updated integer NOT NULL
		)`)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func insertSession(t *testing.T, path, id, parent, title, model, dir string, in, out, cacheRead, cacheWrite int64, cost float64, busy, idle, compacting, updated int64) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		INSERT INTO session_v2 (id, parent_id, title, model, directory, cost,
			tokens_input, tokens_output, tokens_cache_read, tokens_cache_write,
			time_busy, time_idle, time_compacting, time_updated)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, parent, title, model, dir, cost, in, out, cacheRead, cacheWrite, busy, idle, compacting, updated)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeSessionsReadsTheV2Store(t *testing.T) {
	path := newOpenCodeDB(t)
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC).UnixMilli()
	insertSession(t, path, "ses_a", "", "Payment refactor",
		`{"id":"glm-5.3-flash","providerID":"acme"}`, "/home/dev/shop",
		1000, 200, 5000, 10, 0.0123, base, base, 0, base)
	insertSession(t, path, "ses_b", "ses_a", "Subagent: tests",
		`{"id":"glm-5.3","providerID":"acme","variant":"low"}`, "/home/dev/shop",
		50, 5, 0, 0, 0, base-1, base, 0, base-1000)

	got, err := OpenCodeStore{Path: path}.Sessions(context.Background(), time.UnixMilli(base-5000), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	// Newest first.
	if got[0].ID != "ses_a" {
		t.Fatalf("first session = %q, want ses_a", got[0].ID)
	}
	a := got[0]
	if a.Title != "Payment refactor" || a.Dir != "/home/dev/shop" {
		t.Errorf("wrong row fields: %+v", a)
	}
	if a.Model != "acme/glm-5.3-flash" {
		t.Errorf("model = %q, want provider-qualified id", a.Model)
	}
	if a.Tokens != (Tokens{Input: 1000, Output: 200, CacheRead: 5000, CacheCreate: 10}) {
		t.Errorf("tokens = %+v", a.Tokens)
	}
	if a.CostUSD != 0.0123 {
		t.Errorf("cost = %v", a.CostUSD)
	}
	// busy == idle means it last went idle, so it is not working.
	if a.State != StateWaiting {
		t.Errorf("state = %q, want waiting", a.State)
	}
	if got[1].ParentID != "ses_a" {
		t.Errorf("parent = %q, want ses_a", got[1].ParentID)
	}
	if got[1].Model != "acme/glm-5.3#low" {
		t.Errorf("model = %q, want variant suffix", got[1].Model)
	}
}

func TestOpenCodeSessionsReportsBusyAndCompacting(t *testing.T) {
	path := newOpenCodeDB(t)
	base := time.Now().UnixMilli()
	insertSession(t, path, "ses_busy", "", "Busy", "", "/w", 0, 0, 0, 0, 0, base, base-1000, 0, base)
	insertSession(t, path, "ses_compact", "", "Compacting", "", "/w", 0, 0, 0, 0, 0, base-1000, base, base, base)

	got, err := OpenCodeStore{Path: path}.Sessions(context.Background(), time.UnixMilli(base-5000), 10)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]State{}
	for _, s := range got {
		states[s.ID] = s.State
	}
	if states["ses_busy"] != StateThinking {
		t.Errorf("busy session = %q, want thinking", states["ses_busy"])
	}
	if states["ses_compact"] != StateThinking {
		t.Errorf("compacting session = %q, want thinking", states["ses_compact"])
	}
}

func TestOpenCodeSessionsHonoursTheWindow(t *testing.T) {
	path := newOpenCodeDB(t)
	now := time.Now()
	insertSession(t, path, "ses_old", "", "Old", "", "/w", 0, 0, 0, 0, 0, 0, 0, 0, now.Add(-48*time.Hour).UnixMilli())
	insertSession(t, path, "ses_new", "", "New", "", "/w", 0, 0, 0, 0, 0, 0, 0, 0, now.UnixMilli())

	got, err := OpenCodeStore{Path: path}.Sessions(context.Background(), now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "ses_new" {
		t.Fatalf("got %+v, want only ses_new", got)
	}
}

func TestOpenCodeSessionsReportsAV1Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session (id text)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := (OpenCodeStore{Path: path}).Sessions(context.Background(), time.Time{}, 10); !errors.Is(err, ErrNoOpenCodeV2) {
		t.Fatalf("err = %v, want ErrNoOpenCodeV2", err)
	}
}

func TestOpenCodeModelParsing(t *testing.T) {
	cases := map[string]string{
		"":                            "",
		`{"id":"x"}`:                  "x",
		`{"id":"x","providerID":"p"}`: "p/x",
		`{"id":"x","providerID":"p","variant":"high"}`: "p/x#high",
		"not json": "not json",
	}
	for raw, want := range cases {
		if got := openCodeModel(raw); got != want {
			t.Errorf("openCodeModel(%q) = %q, want %q", raw, got, want)
		}
	}
}
