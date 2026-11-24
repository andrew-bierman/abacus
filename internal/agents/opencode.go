package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go (BSD-3-Clause): no cgo, so abacus stays one file and users install nothing
)

// opencodeSession is one row of OpenCode's local session database, as far as this build reads it.
type opencodeSession struct {
	ID, ParentID, Title, Model, Dir string
	Input, Output, CacheWrite       int64
	Cost                            *float64
	Updated                         time.Time
	Busy, Idle, Compacting          int64 // epoch milliseconds of the last transition into each state
}

// openCodeReader reads the sessions of one database file updated at or after since, newest first,
// at most limit. It is injected so tests never open a real database.
type openCodeReader func(ctx context.Context, dbPath string, since time.Time, limit int) ([]opencodeSession, error)

// errNoOpenCodeSessions means the file is not a session database this build reads (an older layout,
// or another file): it reads as "nothing here", never as a failure.
var errNoOpenCodeSessions = errors.New("no session table in the OpenCode database")

// openCodeCols are the columns this reader may use. The table must carry the first three; every other
// column is optional, and an absent one is simply unknown, so a layout change costs a field, not the read.
var (
	openCodeRequired = []string{"id", "directory", "time_updated"}
	openCodeOptional = map[string]string{ // column -> the value used when it is absent or null
		"parent_id": "''", "title": "''", "model": "''",
		"tokens_input": "0", "tokens_output": "0", "tokens_cache_write": "0",
		"time_busy": "0", "time_idle": "0", "time_compacting": "0",
	}
	openCodeOrder = []string{"parent_id", "title", "model", "tokens_input", "tokens_output", "tokens_cache_write", "time_busy", "time_idle", "time_compacting"}
)

// readOpenCodeDB opens the database read-only and immutable: no lock is taken, no -shm or -wal file
// is created and nothing is written to it. The cost is that rows still waiting in the write-ahead log
// are not seen until OpenCode checkpoints, so a live session can lag; its state is then reported
// through the age rules like any other guess. The caller has already checked that the path is a
// regular file.
func readOpenCodeDB(ctx context.Context, dbPath string, since time.Time, limit int) ([]opencodeSession, error) {
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&immutable=1&_pragma=query_only(1)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	have, err := tableColumns(ctx, db, "session_v2")
	if err != nil {
		return nil, err
	}
	for _, c := range openCodeRequired {
		if !have[c] {
			return nil, errNoOpenCodeSessions
		}
	}
	sel := []string{"id", "directory", "time_updated"}
	for _, c := range openCodeOrder {
		if have[c] {
			sel = append(sel, "COALESCE("+c+", "+openCodeOptional[c]+")")
		} else {
			sel = append(sel, openCodeOptional[c])
		}
	}
	cost := "NULL"
	if have["cost"] {
		cost = "cost"
	}
	// Every name interpolated above comes from the fixed lists in this file, never from the database.
	rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(sel, ", ")+", "+cost+" FROM session_v2 WHERE time_updated >= ? ORDER BY time_updated DESC LIMIT ?", since.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []opencodeSession
	for rows.Next() {
		var s opencodeSession
		var updated int64
		var model string
		var c sql.NullFloat64
		var dir sql.NullString
		var id sql.NullString
		if err := rows.Scan(&id, &dir, &updated, &s.ParentID, &s.Title, &model, &s.Input, &s.Output, &s.CacheWrite, &s.Busy, &s.Idle, &s.Compacting, &c); err != nil {
			return nil, err
		}
		s.ID, s.Dir, s.Updated, s.Model = id.String, dir.String, time.UnixMilli(updated), openCodeModel(model)
		if c.Valid {
			v := c.Float64
			s.Cost = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info('"+table+"')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		have[n] = true
	}
	if len(have) == 0 {
		return nil, errNoOpenCodeSessions
	}
	return have, rows.Err()
}

// openCodeModel is "provider/model" from the column's JSON object ({"id","providerID"}); text that is
// not that shape is shown as it is.
func openCodeModel(raw string) string {
	raw = strings.TrimSpace(raw)
	var m struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
	}
	if raw == "" || json.Unmarshal([]byte(raw), &m) != nil || m.ID == "" {
		return raw
	}
	if m.ProviderID != "" {
		return m.ProviderID + "/" + m.ID
	}
	return m.ID
}

// asSession maps a row to what the scan matches on. The harness's own busy and idle stamps say which
// way it last moved; they are read like a transcript's last record (age decides how far to trust them).
func (o opencodeSession) asSession() session {
	s := session{ID: o.ID, Cwd: o.Dir, Model: o.Model, Task: o.Title, ModTime: o.Updated, Cost: o.Cost}
	if t := o.Input + o.Output + o.CacheWrite; t > 0 {
		s.Tokens = &t
	}
	if o.Compacting > 0 || o.Busy > o.Idle {
		s.End = endPrompt
	} else {
		s.End = endDone
	}
	return s
}
