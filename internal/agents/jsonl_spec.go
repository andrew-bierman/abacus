package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"time"
)

// JSONLRole is a record's conversation role after a mapping translates the store's own
// vocabulary. A role outside these three still contributes metadata, but never a turn: most
// stores write notices and bookkeeping records into the same transcript.
type JSONLRole string

const (
	JSONLRoleUser      JSONLRole = "user"
	JSONLRoleAssistant JSONLRole = "assistant"
	JSONLRoleTool      JSONLRole = "tool"
)

// JSONLUsage is how a record's token counters relate to the session totals.
type JSONLUsage string

const (
	// JSONLUsageAdditive means every record carries a delta, so records are summed.
	JSONLUsageAdditive JSONLUsage = "additive"
	// JSONLUsageCumulative means every record carries the session total, so the latest level
	// wins and per-turn history is the difference between consecutive levels.
	JSONLUsageCumulative JSONLUsage = "cumulative"
)

// JSONLMapping locates the fields one line-oriented agent writes.
//
// Paths are dotted and walk objects and array indexes. A missing path is not an error: a store
// that has not written a field yet simply contributes nothing. Text may be a string or an array
// of blocks carrying "text" (or nested "content"), which covers the two content shapes these
// stores use.
type JSONLMapping struct {
	// Timestamp is the record time. It is parsed as RFC 3339 or a Unix timestamp and becomes
	// StoredSession.Updated; without it the file's mtime is used.
	Timestamp string
	// Role is the field a record's kind or role is read from. Roles translates that store's
	// vocabulary into the three roles below; without Roles, the values are read literally.
	Role  string
	Roles map[string]JSONLRole
	// Text is the record's visible text. The first user text becomes the title when Title is
	// empty.
	Text string
	// Model is the latest model that ran, and ID, Dir and ParentID identify the session.
	Model    string
	ID       string
	Dir      string
	ParentID string
	// Title is an explicit session name. The latest non-empty value wins.
	Title string
	// Input, Output, CacheRead and CacheCreate are the four token counters.
	Input       string
	Output      string
	CacheRead   string
	CacheCreate string
	// InputIncludesCache says Input already covers CacheRead, as some providers report. The
	// adapter subtracts it once so Tokens and the derived fill use the same split.
	InputIncludesCache bool
	// Usage says whether those counters are deltas or running totals. The zero value is
	// additive.
	Usage JSONLUsage
	// ContextUsed and ContextLimit are optional. Without ContextUsed the fill is the last
	// assistant response's prompt tokens: input plus both cache counters.
	ContextUsed  string
	ContextLimit string
}

var errJSONLPathEmpty = errors.New("agents: jsonl transcript path is empty")

// JSONLStore reads a line-oriented transcript described by a JSONLMapping. It shares the
// incremental cursor, byte budget, rotation handling and drain lifecycle with the other JSONL
// readers.
//
// A JSONLStore is not safe for concurrent use; keep it on the goroutine that owns the tab.
type JSONLStore struct {
	// Mapping must be set before the first Transcript call: the store compiles it once and
	// keeps that reading for every file, so a later change cannot make two files disagree.
	Mapping  JSONLMapping
	compiled *jsonlPaths
	jsonlFiles
}

// Transcript returns the accumulated facts for the transcript at path, reading only the bytes
// appended since the previous call. A vanished or unreadable file returns an error; a corrupt
// record inside it is skipped.
func (s *JSONLStore) Transcript(path string) (StoredSession, error) {
	if err := s.Mapping.validate(); err != nil {
		return StoredSession{}, err
	}
	paths := s.paths()
	return s.load(path, errJSONLPathEmpty, func() jsonlAccumulator {
		return &jsonlMappedFile{paths: paths}
	})
}

// validate rejects a mapping typo on first read. An unknown usage value would otherwise fall
// through to additive and sum running totals, and an unknown role would silently drop turns.
func (m JSONLMapping) validate() error {
	switch m.Usage {
	case "", JSONLUsageAdditive, JSONLUsageCumulative:
	default:
		return fmt.Errorf("agents: unknown jsonl usage %q", m.Usage)
	}
	for raw, role := range m.Roles {
		switch role {
		case JSONLRoleUser, JSONLRoleAssistant, JSONLRoleTool:
		default:
			return fmt.Errorf("agents: unknown jsonl role %q for %q", role, raw)
		}
	}
	return nil
}

func (s *JSONLStore) paths() *jsonlPaths {
	if s.compiled == nil {
		compiled := compileJSONLMapping(s.Mapping)
		s.compiled = &compiled
	}
	return s.compiled
}

// jsonlPaths is a mapping with every dotted path split once, so a record's hot path does no
// string parsing or allocation.
type jsonlPaths struct {
	usage              JSONLUsage
	roles              map[string]JSONLRole
	inputIncludesCache bool
	meta               jsonlMetaPaths
	text               []string
	role               []string
	tokens             jsonlTokenPaths
	fill               jsonlFillPaths
}

type jsonlMetaPaths struct {
	id, dir, parent, model, title, timestamp []string
}

type jsonlTokenPaths struct {
	input, output, cacheRead, cacheCreate []string
}

type jsonlFillPaths struct {
	used, limit []string
}

func compileJSONLMapping(m JSONLMapping) jsonlPaths {
	return jsonlPaths{
		usage:              m.Usage,
		roles:              maps.Clone(m.Roles),
		inputIncludesCache: m.InputIncludesCache,
		meta: jsonlMetaPaths{
			id:        splitJSONLPath(m.ID),
			dir:       splitJSONLPath(m.Dir),
			parent:    splitJSONLPath(m.ParentID),
			model:     splitJSONLPath(m.Model),
			title:     splitJSONLPath(m.Title),
			timestamp: splitJSONLPath(m.Timestamp),
		},
		text: splitJSONLPath(m.Text),
		role: splitJSONLPath(m.Role),
		tokens: jsonlTokenPaths{
			input:       splitJSONLPath(m.Input),
			output:      splitJSONLPath(m.Output),
			cacheRead:   splitJSONLPath(m.CacheRead),
			cacheCreate: splitJSONLPath(m.CacheCreate),
		},
		fill: jsonlFillPaths{
			used:  splitJSONLPath(m.ContextUsed),
			limit: splitJSONLPath(m.ContextLimit),
		},
	}
}

// jsonlMappedFile is the accumulation for one mapped transcript.
type jsonlMappedFile struct {
	jsonlCursor
	jsonlSession
	paths *jsonlPaths

	openTurn  bool
	executing bool
}

func (f *jsonlMappedFile) add(line []byte) {
	record, ok := jsonlObject(line)
	if !ok {
		return
	}
	f.meta(record)
	role := f.paths.recordRole(record)
	f.applyRole(record, role)
	if tokens, ok := f.paths.tokensFor(record); ok {
		f.usage(tokens, role)
	}
	f.contextFields(record)
}

// meta folds the session facts a record may carry. A forked or resumed transcript can repeat its
// parent's header, and the first record is this file's own identity.
func (f *jsonlMappedFile) meta(record map[string]any) {
	// The identity paths cannot change after the first record, so a later record skips their
	// path walks instead of paying for a discarded value.
	if f.id == "" {
		if v, ok := jsonlLookup(record, f.paths.meta.id); ok {
			f.setIDIfEmpty(jsonlString(v))
		}
	}
	if f.dir == "" {
		if v, ok := jsonlLookup(record, f.paths.meta.dir); ok {
			f.setDirIfEmpty(jsonlString(v))
		}
	}
	if f.parentID == "" {
		if v, ok := jsonlLookup(record, f.paths.meta.parent); ok {
			f.setParentIfEmpty(jsonlString(v))
		}
	}
	if v, ok := jsonlLookup(record, f.paths.meta.model); ok {
		f.setModel(jsonlString(v))
	}
	if v, ok := jsonlLookup(record, f.paths.meta.title); ok {
		f.setTitle(jsonlString(v))
	}
	if v, ok := jsonlLookup(record, f.paths.meta.timestamp); ok {
		if ts, ok := jsonlTime(v); ok && !ts.IsZero() {
			f.updated = ts
		}
	}
}

func (f *jsonlMappedFile) applyRole(record map[string]any, role JSONLRole) {
	switch role {
	case JSONLRoleUser:
		f.openTurn = true
		f.executing = false
		f.setPrompt(jsonlText(record, f.paths.text))
	case JSONLRoleAssistant:
		f.openTurn = false
		f.executing = false
	case JSONLRoleTool:
		// A mapping carries no call ids, so a tool record means work is running until the next
		// assistant or user record says the turn moved on.
		f.executing = true
	}
}

func (f *jsonlMappedFile) usage(t Tokens, role JSONLRole) {
	if t == (Tokens{}) {
		return
	}
	// delta is what this record added: the record itself in additive mode and after a reset,
	// and the difference from the previous level in cumulative mode.
	delta := t
	if f.paths.usage == JSONLUsageCumulative {
		current := t.sum()
		point := current
		if f.sawUsage {
			previous := f.tokens.sum()
			if current >= previous {
				point = current - previous
				delta = t.sub(f.tokens)
			}
		}
		f.tokens = t
		f.sawUsage = true
		if role == JSONLRoleAssistant && point > 0 {
			f.addHistory(point)
		}
	} else if role == JSONLRoleAssistant {
		f.addResponseTokens(t)
	} else {
		f.addTokens(t)
	}
	if len(f.paths.fill.used) == 0 && role == JSONLRoleAssistant {
		// The fill is the last response's own prompt tokens, not the session total a
		// cumulative store reports.
		if used := delta.prompt(); used > 0 {
			f.useContext(used)
		}
	}
}

func (f *jsonlMappedFile) contextFields(record map[string]any) {
	if v, ok := jsonlLookup(record, f.paths.fill.used); ok {
		if n, ok := jsonlInt(v); ok && n >= 0 {
			f.useContext(n)
		}
	}
	if v, ok := jsonlLookup(record, f.paths.fill.limit); ok {
		if n, ok := jsonlInt(v); ok {
			f.setContextLimit(n)
		}
	}
}

func (f *jsonlMappedFile) session() StoredSession {
	state := StateWaiting
	switch {
	case f.executing:
		state = StateExecuting
	case f.openTurn:
		state = StateThinking
	}
	return f.jsonlSession.session(state, f.modTime())
}

func (p *jsonlPaths) recordRole(record map[string]any) JSONLRole {
	v, ok := jsonlLookup(record, p.role)
	if !ok {
		return ""
	}
	raw := jsonlString(v)
	if role, ok := p.roles[raw]; ok {
		return role
	}
	return JSONLRole(raw)
}

// tokensFor reports whether the mapping declares any token path, which is different from a
// declared path that is absent on this record.
func (p *jsonlPaths) tokensFor(record map[string]any) (Tokens, bool) {
	var t Tokens
	declared := false
	set := func(path []string, dst *int64) {
		if len(path) == 0 {
			return
		}
		declared = true
		if v, ok := jsonlLookup(record, path); ok {
			if n, ok := jsonlInt(v); ok && n > 0 {
				*dst = n
			}
		}
	}
	set(p.tokens.input, &t.Input)
	set(p.tokens.output, &t.Output)
	set(p.tokens.cacheRead, &t.CacheRead)
	set(p.tokens.cacheCreate, &t.CacheCreate)
	if p.inputIncludesCache {
		t.Input = max(0, t.Input-t.CacheRead)
	}
	return t, declared
}

func jsonlObject(line []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false
	}
	obj, ok := v.(map[string]any)
	return obj, ok
}

// jsonlLookup walks one compiled path through objects and arrays.
func jsonlLookup(v any, parts []string) (any, bool) {
	if len(parts) == 0 {
		return nil, false
	}
	cur := v
	for _, part := range parts {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// splitJSONLPath splits a dotted path once. A backslash escapes a dot so a store whose field
// name contains one is still expressible.
func splitJSONLPath(path string) []string {
	if path == "" {
		return nil
	}
	var (
		parts   []string
		part    strings.Builder
		escaped bool
	)
	for _, r := range path {
		switch {
		case escaped:
			part.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '.':
			parts = append(parts, part.String())
			part.Reset()
		default:
			part.WriteRune(r)
		}
	}
	if escaped {
		part.WriteRune('\\')
	}
	return append(parts, part.String())
}

func jsonlString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

func jsonlText(record map[string]any, path []string) string {
	v, ok := jsonlLookup(record, path)
	if !ok {
		return ""
	}
	return jsonlValueText(v)
}

// jsonlValueText joins the text blocks of an array-shaped message, recursing through "text" and
// "content" so both a bare content array and a nested message body work.
func jsonlValueText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if text := jsonlValueText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		for _, key := range []string{"text", "content"} {
			if inner, ok := x[key]; ok {
				if text := jsonlValueText(inner); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func jsonlInt(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil {
			return int64(f), true
		}
	case string:
		s := strings.TrimSpace(x)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), true
		}
	}
	return 0, false
}

func jsonlTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t, true
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return jsonlUnixTime(n), true
		}
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return jsonlUnixTime(n), true
		}
		if f, err := x.Float64(); err == nil {
			return jsonlUnixTime(int64(f)), true
		}
	}
	return time.Time{}, false
}

// jsonlUnixTime picks the unit from magnitude. Stores in the wild write seconds, milliseconds,
// microseconds and nanoseconds, and a wrong guess would put a session decades away.
func jsonlUnixTime(n int64) time.Time {
	switch {
	case n <= 0:
		return time.Time{}
	case n < 1e11:
		return time.Unix(n, 0)
	case n < 1e14:
		return time.UnixMilli(n)
	case n < 1e17:
		return time.UnixMicro(n)
	default:
		return time.Unix(0, n)
	}
}
