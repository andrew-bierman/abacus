package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, so abacus stays one file
)

// ErrNoOpenCodeV2 means the database has no session_v2 table. That is either an older
// opencode that wrote the v1 tables or a file that is not an opencode store at all; both
// read as "nothing here", never as a failure of the tab.
var ErrNoOpenCodeV2 = errors.New("agents: no session_v2 table (opencode v1 or not a store)")

// StoredSession is one session as its harness recorded it, independent of whether a process
// is running right now. A store is the session list for harnesses that have one; the process
// table only says which of them are live.
type StoredSession struct {
	ID       string
	Title    string
	Model    string
	Dir      string
	ParentID string
	Tokens   Tokens
	CostUSD  float64
	Updated  time.Time
	State    State
}

// OpenCodeStore reads opencode's local SQLite database.
//
// opencode has shipped two shapes of this database. The v2 one is the current install here:
// `session_v2` carries the counters, the model and the parent link, and `session_message`
// carries the turns. `ferry` and `moirai` both still read the v1 tables (`session`,
// `message`, `part`), which is why this reader is ours.
type OpenCodeStore struct {
	// Path is the database file. Empty means the default location for this user.
	Path string
}

// OpenCodeDBPath is the default opencode database location.
func OpenCodeDBPath(home string) string {
	if home == "" {
		return ""
	}
	return strings.TrimSuffix(home, "/") + "/.local/share/opencode/opencode.db"
}

// Sessions returns the sessions updated at or after since, newest first.
//
// The window is what keeps this cheap on a machine with a long history: a monitor wants the
// live and recent sessions, not every session ever run.
func (s OpenCodeStore) Sessions(ctx context.Context, since time.Time, limit int) ([]StoredSession, error) {
	path := s.Path
	if path == "" {
		return nil, errors.New("agents: opencode database path is empty")
	}
	if limit <= 0 {
		limit = 50
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT id,
		       COALESCE(parent_id, ''), COALESCE(title, ''), COALESCE(model, ''),
		       COALESCE(directory, ''), COALESCE(tokens_input, 0), COALESCE(tokens_output, 0),
		       COALESCE(tokens_cache_read, 0), COALESCE(tokens_cache_write, 0),
		       COALESCE(cost, 0), COALESCE(time_updated, 0),
		       COALESCE(time_busy, 0), COALESCE(time_idle, 0), COALESCE(time_compacting, 0)
		FROM session_v2
		WHERE time_updated >= ?
		ORDER BY time_updated DESC
		LIMIT ?`, since.UnixMilli(), limit)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, ErrNoOpenCodeV2
		}
		return nil, err
	}
	defer rows.Close()

	var out []StoredSession
	for rows.Next() {
		var (
			s          StoredSession
			model      string
			cost       float64
			updated    int64
			busy       int64
			idle       int64
			compacting int64
		)
		if err := rows.Scan(&s.ID, &s.ParentID, &s.Title, &model, &s.Dir,
			&s.Tokens.Input, &s.Tokens.Output, &s.Tokens.CacheRead, &s.Tokens.CacheCreate,
			&cost, &updated, &busy, &idle, &compacting); err != nil {
			return nil, err
		}
		s.Model = openCodeModel(model)
		s.CostUSD = cost
		s.Updated = time.UnixMilli(updated)
		s.State = openCodeState(busy, idle, compacting)
		out = append(out, s)
	}
	return out, rows.Err()
}

// openCodeState reads opencode's own busy/idle stamps rather than guessing from file times.
// Both are epoch milliseconds of the last transition, so the later one says which it is in.
func openCodeState(busy, idle, compacting int64) State {
	switch {
	case compacting > 0:
		return StateThinking
	case busy > idle:
		return StateThinking
	default:
		return StateWaiting
	}
}

// openCodeModel renders opencode's model column, which is a JSON object:
// {"id":"…","providerID":"…","variant":"…"}. The provider is kept because the same model id
// exists under several providers and a user choosing between them cares which.
func openCodeModel(raw string) string {
	if raw == "" {
		return ""
	}
	var m struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
		Variant    string `json:"variant"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return strings.TrimSpace(raw)
	}
	name := m.ID
	if m.ProviderID != "" && m.ID != "" {
		name = m.ProviderID + "/" + m.ID
	}
	if m.Variant != "" {
		name += "#" + m.Variant
	}
	return name
}

func isNoSuchTable(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no such table")
}

// String is for logs and doctor output, not for the UI.
func (s StoredSession) String() string {
	return fmt.Sprintf("%s %s %s", s.ID, s.State, s.Title)
}
