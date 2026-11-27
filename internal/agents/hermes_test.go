package agents

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// hermesFixture is the subset of Hermes Agent's state.db this reader depends on, built with
// invented rows. The schema follows persistence/sqlite/_schema_ddl.py upstream; no real
// transcript is ever committed.
type hermesFixture struct {
	path string
}

func newHermesDB(t *testing.T) *hermesFixture {
	t.Helper()
	return newHermesDBAt(t, filepath.Join(t.TempDir(), "state.db"))
}

func newHermesDBAt(t *testing.T, path string) *hermesFixture {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			source TEXT NOT NULL,
			model TEXT,
			parent_session_id TEXT,
			started_at REAL NOT NULL,
			ended_at REAL,
			message_count INTEGER DEFAULT 0,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			cache_write_tokens INTEGER DEFAULT 0,
			reasoning_tokens INTEGER DEFAULT 0,
			cwd TEXT,
			estimated_cost_usd REAL,
			actual_cost_usd REAL,
			title TEXT,
			last_activity_at REAL,
			last_activity_description TEXT
		);
		CREATE TABLE messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES sessions(id),
			role TEXT NOT NULL,
			content TEXT,
			tool_calls TEXT,
			timestamp REAL NOT NULL,
			token_count INTEGER,
			active INTEGER NOT NULL DEFAULT 1
		);
		CREATE INDEX idx_sessions_effective_activity
			ON sessions(COALESCE(last_activity_at, started_at) DESC, started_at DESC);
		CREATE INDEX idx_messages_session ON messages(session_id, timestamp);
		CREATE INDEX idx_messages_session_active ON messages(session_id, active, timestamp);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return &hermesFixture{path: path}
}

type hermesSessionRow struct {
	id, source, model, parent, cwd, title, activity string
	started, ended, updated                         float64
	input, output, cacheRead, cacheWrite            int64
	estimated, actual                               float64
}

func (f *hermesFixture) session(t *testing.T, r hermesSessionRow) {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		INSERT INTO sessions (id, source, model, parent_session_id, started_at, ended_at,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cwd,
			estimated_cost_usd, actual_cost_usd, title, last_activity_at, last_activity_description)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.id, r.source, r.model, nullableText(r.parent), r.started, nullableFloat(r.ended),
		r.input, r.output, r.cacheRead, r.cacheWrite, r.cwd,
		nullableFloat(r.estimated), nullableFloat(r.actual), nullableText(r.title),
		nullableFloat(r.updated), nullableText(r.activity))
	if err != nil {
		t.Fatal(err)
	}
}

func (f *hermesFixture) message(t *testing.T, session, role, content, toolCalls string, at float64) {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, timestamp)
		VALUES (?,?,?,?,?)`, session, role, content, nullableText(toolCalls), at); err != nil {
		t.Fatal(err)
	}
}

// inactiveMessage writes a message Hermes deactivated during context compression: the row is
// still on disk, but the live conversation no longer includes it.
func (f *hermesFixture) inactiveMessage(t *testing.T, session, role, content, toolCalls string, at float64) {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, timestamp, active)
		VALUES (?,?,?,?,?,0)`, session, role, nullableText(content), nullableText(toolCalls), at); err != nil {
		t.Fatal(err)
	}
}

// zeroActivity stores last_activity_at = 0 explicitly. The fixture's session() writes a zero
// through nullableFloat as NULL, so this reaches the schema's "unset" value rather than no value.
func (f *hermesFixture) zeroActivity(t *testing.T, id string) {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE sessions SET last_activity_at = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

func hermesSession(t *testing.T, store HermesStore, since time.Time, limit int) []StoredSession {
	t.Helper()
	got, err := store.Sessions(context.Background(), since, limit)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestHermesSessionsReadsTheStateDB(t *testing.T) {
	f := newHermesDB(t)
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) float64 { return float64(base.Add(offset).Unix()) }

	f.session(t, hermesSessionRow{
		id: "ses_shop", source: "cli", model: "anthropic/claude-sonnet-4.6",
		cwd: "/home/dev/shop", title: "Payment refactor",
		started: at(-time.Hour), updated: at(0),
		input: 900, output: 100, cacheRead: 4000, cacheWrite: 50,
		estimated: 0.02, actual: 0.015,
	})
	f.message(t, "ses_shop", "user", "trim the invoice list", "", at(-2*time.Minute))
	f.message(t, "ses_shop", "assistant", "on it", `[{"id":"call_1"}]`, at(-time.Minute))

	f.session(t, hermesSessionRow{
		id: "ses_old", source: "cli", model: "openai/gpt-5.5", parent: "ses_shop",
		cwd: "/home/dev/shop", started: at(-2 * time.Hour), updated: at(-20 * time.Minute),
		input: 10, output: 2,
	})
	f.message(t, "ses_old", "user", "# Heading\n\nfix the flaky test", "", at(-25*time.Minute))

	f.session(t, hermesSessionRow{
		id: "ses_blog", source: "cli", cwd: "/home/dev/blog",
		started: at(-3 * time.Hour), updated: at(-30 * time.Minute),
	})

	got := hermesSession(t, HermesStore{Paths: []string{f.path}}, base.Add(-10*time.Hour), 10)
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3: %+v", len(got), got)
	}
	if got[0].ID != "ses_shop" {
		t.Fatalf("first session = %q, want ses_shop", got[0].ID)
	}
	shop := got[0]
	if shop.Title != "Payment refactor" || shop.Dir != "/home/dev/shop" || shop.Model != "anthropic/claude-sonnet-4.6" {
		t.Errorf("wrong row fields: %+v", shop)
	}
	if shop.Tokens != (Tokens{Input: 900, Output: 100, CacheRead: 4000, CacheCreate: 50}) {
		t.Errorf("tokens = %+v", shop.Tokens)
	}
	if shop.CostUSD != 0.015 {
		t.Errorf("cost = %v, want the actual cost when Hermes recorded one", shop.CostUSD)
	}
	if shop.State != StateExecuting {
		t.Errorf("state = %q, want executing from the active tool call", shop.State)
	}
	if want := base; !shop.Updated.Equal(want) {
		t.Errorf("updated = %v, want %v", shop.Updated, want)
	}
	// Hermes persists cumulative session totals and a sparse per-message token_count, not a
	// per-turn series or a context window, so those fields stay at their does-not-record value.
	if shop.Context != (Context{}) || shop.TokenHistory != nil {
		t.Errorf("context/history = %+v/%v, want the does-not-record sentinel", shop.Context, shop.TokenHistory)
	}

	old := got[1]
	if old.Title != "fix the flaky test" {
		t.Errorf("title = %q, want the first prompt with its markdown dropped", old.Title)
	}
	if old.ParentID != "ses_shop" {
		t.Errorf("parent = %q, want ses_shop", old.ParentID)
	}
	if old.State != StateThinking {
		t.Errorf("state = %q, want thinking while the last turn is the user's", old.State)
	}
	if old.CostUSD != 0 {
		t.Errorf("cost = %v, want zero when no cost was recorded", old.CostUSD)
	}
}

func TestHermesSessionsHonoursTheWindowAndLimit(t *testing.T) {
	f := newHermesDB(t)
	base := time.Now()
	at := func(offset time.Duration) float64 { return float64(base.Add(offset).Unix()) }
	f.session(t, hermesSessionRow{id: "ses_new", source: "cli", started: at(-time.Hour), updated: at(0)})
	f.session(t, hermesSessionRow{id: "ses_old", source: "cli", started: at(-3 * time.Hour), updated: at(-2 * time.Hour)})

	if got := hermesSession(t, HermesStore{Paths: []string{f.path}}, base.Add(-time.Hour), 10); len(got) != 1 || got[0].ID != "ses_new" {
		t.Errorf("window = %+v, want only the session active since the cutoff", got)
	}
	if got := hermesSession(t, HermesStore{Paths: []string{f.path}}, base.Add(-4*time.Hour), 1); len(got) != 1 || got[0].ID != "ses_new" {
		t.Errorf("limit = %+v, want the newest session only", got)
	}
}

func TestHermesSessionsSkipsMissingAndForeignStores(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	foreign := filepath.Join(dir, "foreign.db")
	if err := os.WriteFile(foreign, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	noSessions := filepath.Join(dir, "empty.db")
	db, err := sql.Open("sqlite", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// A missing store is simply not there, and a database without a sessions table is
	// "nothing here" rather than an unreadable store.
	if got := hermesSession(t, HermesStore{Paths: []string{missing, noSessions}}, time.Time{}, 10); len(got) != 0 {
		t.Errorf("sessions = %+v, want none", got)
	}
	if _, err := (HermesStore{Paths: []string{foreign}}).Sessions(context.Background(), time.Time{}, 10); err == nil {
		t.Error("a corrupt store must report an error")
	}
}

func TestHermesDBPathsIncludesProfilesAndTheEnvironmentOverride(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".hermes")
	profiles := []string{
		filepath.Join(base, "profiles", "coder", "state.db"),
		filepath.Join(base, "profiles", "writer", "state.db"),
	}
	for _, path := range profiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := HermesDBPathsFrom(home, func(string) string { return "" })
	want := append([]string{filepath.Join(base, "state.db")}, profiles...)
	if !slices.Equal(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}

	relocated := t.TempDir()
	got = HermesDBPathsFrom(home, func(key string) string {
		if key == "HERMES_HOME" {
			return relocated
		}
		return ""
	})
	if want := []string{filepath.Join(relocated, "state.db")}; !slices.Equal(got, want) {
		t.Errorf("relocated paths = %v, want %v", got, want)
	}
}

func TestHermesSessionsReadsEveryProfile(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".hermes")
	defaultDB := newHermesDBAt(t, filepath.Join(base, "state.db"))
	profileDB := newHermesDBAt(t, filepath.Join(base, "profiles", "coder", "state.db"))
	now := float64(time.Now().Unix())
	defaultDB.session(t, hermesSessionRow{id: "ses_default", source: "cli", cwd: "/home/dev/shop", started: now, updated: now})
	profileDB.session(t, hermesSessionRow{id: "ses_profile", source: "cli", cwd: "/home/dev/blog", started: now, updated: now})

	got := hermesSession(t, HermesStore{Paths: HermesDBPathsFrom(home, func(string) string { return "" })}, time.Now().Add(-time.Hour), 10)
	if len(got) != 2 {
		t.Fatalf("sessions = %+v, want one per profile", got)
	}
	ids := []string{got[0].ID, got[1].ID}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"ses_default", "ses_profile"}) {
		t.Errorf("ids = %v, want both profiles", ids)
	}
}

func TestHermesSessionsIgnoresInactiveMessages(t *testing.T) {
	f := newHermesDB(t)
	base := time.Date(2026, 11, 24, 11, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) float64 { return float64(base.Add(offset).Unix()) }

	f.session(t, hermesSessionRow{
		id: "ses_compressed", source: "cli", cwd: "/home/dev/shop",
		started: at(-time.Hour), updated: at(0),
	})
	// A compressed prompt and an assistant tool call are still on disk but inactive. Without the
	// active filter the title would come from the stale prompt and the last message would read
	// as an executing tool call.
	f.inactiveMessage(t, "ses_compressed", "user", "stale compressed prompt", "", at(-3*time.Minute))
	f.message(t, "ses_compressed", "user", "live prompt", "", at(-2*time.Minute))
	f.inactiveMessage(t, "ses_compressed", "assistant", "old tool call", `[{"id":"call_0"}]`, at(-time.Minute))

	got := hermesSession(t, HermesStore{Paths: []string{f.path}}, base.Add(-time.Hour), 10)
	if len(got) != 1 {
		t.Fatalf("sessions = %+v, want one", got)
	}
	if got[0].Title != "live prompt" {
		t.Errorf("title = %q, want the active user prompt", got[0].Title)
	}
	if got[0].State != StateThinking {
		t.Errorf("state = %q, want thinking while the last active turn is the user's", got[0].State)
	}
}

func TestHermesSessionsUsesStartedAtWhenActivityIsUnset(t *testing.T) {
	f := newHermesDB(t)
	base := time.Date(2026, 11, 24, 11, 0, 0, 0, time.UTC)
	f.session(t, hermesSessionRow{
		id: "ses_unset_activity", source: "cli", cwd: "/home/dev/shop",
		started: float64(base.Add(-30 * time.Minute).Unix()), updated: float64(base.Unix()),
	})
	f.zeroActivity(t, "ses_unset_activity")

	got := hermesSession(t, HermesStore{Paths: []string{f.path}}, base.Add(-time.Hour), 10)
	if len(got) != 1 {
		t.Fatalf("sessions = %+v, want the stored zero stamp to fall back to started_at", got)
	}
	if want := base.Add(-30 * time.Minute); !got[0].Updated.Equal(want) {
		t.Errorf("updated = %v, want started_at %v", got[0].Updated, want)
	}
}

func TestHermesSessionsReturnsReadableProfilesWithTheFailure(t *testing.T) {
	dir := t.TempDir()
	good := newHermesDBAt(t, filepath.Join(dir, "good", "state.db"))
	corrupt := filepath.Join(dir, "bad", "state.db")
	if err := os.MkdirAll(filepath.Dir(corrupt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	good.session(t, hermesSessionRow{id: "ses_good", source: "cli", cwd: "/home/dev/shop", started: now, updated: now})

	got, err := (HermesStore{Paths: []string{good.path, corrupt}}).Sessions(context.Background(), time.Now().Add(-time.Hour), 10)
	if err == nil {
		t.Error("a corrupt profile must be reported")
	}
	if len(got) != 1 || got[0].ID != "ses_good" {
		t.Fatalf("sessions = %+v, want the readable profile's session alongside the error", got)
	}
}

func TestHermesDBPathsReportsAnUnlistableProfilesDirectory(t *testing.T) {
	home := t.TempDir()
	profiles := filepath.Join(home, ".hermes", "profiles")
	if err := os.MkdirAll(filepath.Dir(profiles), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profiles, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths, err := HermesDBPathsAndErr(home, func(string) string { return "" })
	if err == nil {
		t.Error("an unlistable profiles directory must be reported")
	}
	if want := []string{filepath.Join(home, ".hermes", "state.db")}; !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want the default store kept", paths)
	}
}

func TestHermesStateFollowsTheActiveTurn(t *testing.T) {
	cases := []struct {
		name     string
		ended    bool
		activity string
		role     string
		tools    bool
		want     State
	}{
		{name: "ended", ended: true, activity: "executing tool", role: "user", want: StateWaiting},
		{name: "tool activity", activity: "executing tool", role: "assistant", want: StateExecuting},
		{name: "compression", activity: "context compression in progress", want: StateThinking},
		{name: "other activity", activity: "waiting for model", want: StateThinking},
		{name: "user turn", role: "user", want: StateThinking},
		{name: "assistant reply", role: "assistant", want: StateWaiting},
		{name: "pending tool call", role: "assistant", tools: true, want: StateExecuting},
		{name: "tool result", role: "tool", want: StateThinking},
		{name: "empty", want: StateWaiting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hermesState(tc.ended, tc.activity, tc.role, tc.tools); got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
		})
	}
}
