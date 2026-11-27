package agents

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// errNoHermesStore means the database has no sessions table. That is an uninitialised file or
// another agent's database, both of which read as "nothing here", never as a failure of the tab.
var errNoHermesStore = errors.New("agents: no sessions table (not a hermes store)")

// HermesDBPaths is where Hermes Agent keeps its session databases for a home.
func HermesDBPaths(home string) []string {
	return HermesDBPathsFrom(home, os.Getenv)
}

// HermesDBPathsFrom is HermesDBPaths with the environment supplied by the caller, so the
// sessions join can keep its environment edge injectable.
//
// Hermes resolves its root through HERMES_HOME (a named profile sets it to the profile's
// directory) and otherwise uses ~/.hermes. Named profiles that live under the default root each
// keep their own state.db, so every profile is read: a process started with `--profile coder`
// writes a session the default database never sees.
func HermesDBPathsFrom(home string, getenv func(string) string) []string {
	paths, _ := HermesDBPathsAndErr(home, getenv)
	return paths
}

// HermesDBPathsAndErr is HermesDBPathsFrom plus the profiles directory's listing error.
//
// filepath.Glob returns only ErrBadPattern, so a profiles directory that exists but cannot be
// listed (permission denied, or a file where the directory belongs) used to read as "no
// profiles" and hide every named profile's sessions with no notice. The default profile's path
// is still returned when only the listing failed, so its sessions keep joining.
func HermesDBPathsAndErr(home string, getenv func(string) string) ([]string, error) {
	if home == "" {
		return nil, nil
	}
	root := filepath.Join(home, ".hermes")
	if getenv != nil {
		if relocated := strings.TrimSpace(getenv("HERMES_HOME")); relocated != "" {
			root = relocated
		}
	}
	paths := []string{filepath.Join(root, "state.db")}
	// A missing profiles directory is not an error: most installs never create one.
	entries, err := os.ReadDir(filepath.Join(root, "profiles"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return paths, nil
		}
		return paths, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			paths = append(paths, filepath.Join(root, "profiles", entry.Name(), "state.db"))
		}
	}
	return paths, nil
}

// HermesStore reads Hermes Agent's SQLite state databases.
//
// Hermes keeps one database per profile: <root>/state.db for the default profile and
// <root>/profiles/<name>/state.db for a named one. That database is the session list: `sessions`
// carries the ids, model, working directory, title, parent link, cumulative token counters and
// cost, while `messages` carries the transcript. It is the writer's own schema, so this reader
// trusts the stored counters rather than re-adding messages; Hermes documents `token_count` on a
// message as sparse, which makes it unsuitable for a per-turn series.
//
// The schema read here is persistence/sqlite/_schema_ddl.py in the Hermes Agent repository:
// `sessions` (started_at, last_activity_at, input_tokens, output_tokens, cache_*_tokens, cwd,
// title, parent_session_id) and `messages` (session_id, role, content, tool_calls, timestamp,
// active). The state database is a singleton the gateway writes; this reader opens it read-only
// and in WAL-aware mode, because `immutable=1` would miss a live write-ahead log.
type HermesStore struct {
	// Paths are the state.db files, normally HermesDBPaths(home).
	Paths []string
}

// Sessions returns the sessions updated at or after since, newest first.
//
// One unreadable profile does not hide the others: a corrupt database is skipped while another
// profile still answers, and the readable sessions are returned alongside the first error so the
// join can index them and still report the failed profile.
func (s HermesStore) Sessions(ctx context.Context, since time.Time, limit int) ([]StoredSession, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		out      []StoredSession
		firstErr error
	)
	seen := make(map[string]bool, len(s.Paths))
	for _, path := range s.Paths {
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Stat(path); err != nil {
			if !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		sessions, err := readHermesDB(ctx, path, since, limit)
		if err != nil {
			if !errors.Is(err, errNoHermesStore) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, sessions...)
	}
	slices.SortFunc(out, func(a, b StoredSession) int {
		if !a.Updated.Equal(b.Updated) {
			if a.Updated.After(b.Updated) {
				return -1
			}
			return 1
		}
		return strings.Compare(b.ID, a.ID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, firstErr
}

// readHermesDB reads one profile's database. The window keeps a long history cheap; a monitor
// wants the live and recent sessions, not every session ever run.
//
// last_activity_at = 0 is the schema's "unset", not 1970 (hermesTime applies the same rule after
// the read), so the window NULLIFs it into the fallback chain instead of comparing 0 to since.
func readHermesDB(ctx context.Context, path string, since time.Time, limit int) ([]StoredSession, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT s.id,
		       COALESCE(s.parent_session_id, ''),
		       COALESCE(s.title, ''),
		       CASE WHEN TRIM(COALESCE(s.title, '')) = '' THEN COALESCE((
		           SELECT SUBSTR(m.content, 1, 500) FROM messages m
		           WHERE m.session_id = s.id AND m.role = 'user' AND m.active = 1
		             AND m.content IS NOT NULL AND TRIM(m.content) <> ''
		           ORDER BY m.timestamp, m.id LIMIT 1
		       ), '') ELSE '' END,
		       COALESCE(s.model, ''),
		       COALESCE(s.cwd, ''),
		       COALESCE(s.input_tokens, 0), COALESCE(s.output_tokens, 0),
		       COALESCE(s.cache_read_tokens, 0), COALESCE(s.cache_write_tokens, 0),
		       COALESCE(s.actual_cost_usd, s.estimated_cost_usd, 0),
		       COALESCE(NULLIF(s.last_activity_at, 0),
		                (SELECT MAX(m.timestamp) FROM messages m WHERE m.session_id = s.id),
		                s.started_at),
		       COALESCE(s.last_activity_description, ''),
		       COALESCE(lm.role, ''),
		       COALESCE((lm.tool_calls IS NOT NULL AND lm.tool_calls <> ''), 0),
		       (s.ended_at IS NOT NULL)
		FROM sessions s
		LEFT JOIN messages lm ON lm.id = (
		    SELECT m.id FROM messages m
		    WHERE m.session_id = s.id AND m.active = 1
		    ORDER BY m.timestamp DESC, m.id DESC LIMIT 1
		)
		WHERE COALESCE(NULLIF(s.last_activity_at, 0), s.started_at) >= ?
		ORDER BY COALESCE(NULLIF(s.last_activity_at, 0), s.started_at) DESC, s.id DESC
		LIMIT ?`, since.Unix(), limit)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, errNoHermesStore
		}
		return nil, err
	}
	defer rows.Close()

	out := make([]StoredSession, 0, limit)
	for rows.Next() {
		var (
			st       StoredSession
			title    string
			preview  string
			activity string
			lastRole string
			lastTool int64
			ended    int64
			cost     float64
			updated  float64
		)
		if err := rows.Scan(&st.ID, &st.ParentID, &title, &preview, &st.Model, &st.Dir,
			&st.Tokens.Input, &st.Tokens.Output, &st.Tokens.CacheRead, &st.Tokens.CacheCreate,
			&cost, &updated, &activity, &lastRole, &lastTool, &ended); err != nil {
			return nil, err
		}
		st.Title = strings.TrimSpace(title)
		if st.Title == "" {
			st.Title = promptTitle(preview)
		}
		st.CostUSD = cost
		st.Updated = hermesTime(updated)
		st.State = hermesState(ended != 0, activity, lastRole, lastTool != 0)
		out = append(out, st)
	}
	return out, rows.Err()
}

// hermesTime converts the epoch-second REAL Hermes stores. A zero or negative stamp is the
// schema's "unset", not 1970, so it stays a zero time for the join to ignore.
func hermesTime(seconds float64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(seconds * 1000))
}

// Hermes state is not a single column. During a turn it stamps a free-form activity label
// ("executing tool", "context compression in progress") and clears it when the turn becomes
// idle, so a non-empty label is the freshest signal. Without one, the last live message says
// whether the agent owes the user a reply (user or tool result) or already gave it (assistant).
func hermesState(ended bool, activity, lastRole string, lastToolCall bool) State {
	if ended {
		return StateWaiting
	}
	switch label := strings.ToLower(activity); {
	case strings.Contains(label, "tool"):
		return StateExecuting
	case label != "":
		return StateThinking
	}
	switch lastRole {
	case "user":
		return StateThinking
	case "assistant":
		if lastToolCall {
			return StateExecuting
		}
	case "tool":
		return StateThinking
	}
	return StateWaiting
}
