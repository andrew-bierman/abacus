package agents

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// CodexStore reads Codex's rollout files, one JSONL per session.
//
// Codex keeps no session list that carries a working directory either: a live session is a
// process holding a rollout open. So, like Claude, the caller supplies the path it already
// resolved from the process table and the store only remembers how far it has read.
//
// A CodexStore is not safe for concurrent use; keep it on the goroutine that owns the tab.
type CodexStore struct {
	jsonlFiles
}

// CodexSessionsPath is the default root of Codex rollouts for a home. A rollout sits under
// <root>/<YYYY>/<MM>/<DD>/rollout-*.jsonl.
func CodexSessionsPath(home string) string {
	if home == "" {
		return ""
	}
	return strings.TrimSuffix(home, "/") + "/.codex/sessions"
}

var errCodexPathEmpty = errors.New("agents: codex rollout path is empty")

// Rollout returns the accumulated facts for the rollout at path, reading only the bytes
// appended since the previous call. A vanished or unreadable file returns an error; a corrupt
// record inside it is skipped.
func (s *CodexStore) Rollout(path string) (StoredSession, error) {
	return s.load(path, errCodexPathEmpty, func() jsonlAccumulator { return &codexFile{} })
}

// codexFile is the accumulation for one rollout.
type codexFile struct {
	jsonlCursor

	id    string
	dir   string
	model string
	title string

	tokens  Tokens
	context Context
	history []int64
	// sawUsage keeps an unread window out of the report: a limit without a fill would draw a
	// 0% bar rather than "this harness has not recorded one yet".
	sawUsage bool

	openTurn     bool
	pendingCalls map[string]struct{}
}

func (c *codexFile) add(line []byte) {
	var e codexEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	switch e.Type {
	case "session_meta":
		c.meta(e.Payload)
	case "turn_context":
		if e.Payload.Model != "" {
			c.model = e.Payload.Model
		}
		c.setWindow(e.Payload.ContextWindow)
	case "event_msg":
		c.event(e.Payload)
	case "response_item":
		c.response(e.Payload)
	}
}

func (c *codexFile) meta(p codexPayload) {
	// A forked rollout replays its parent's history, including the parent's session_meta. The
	// first record is this file's identity; a later one describes the replayed parent.
	id := p.ID
	if id == "" {
		id = p.SessionID
	}
	if c.id == "" {
		c.id = id
	}
	if c.dir == "" {
		c.dir = p.CWD
	}
}

func (c *codexFile) event(p codexPayload) {
	switch p.Type {
	case "task_started", "user_message":
		// The per-event vocabulary is what older rollouts write; the newer one lands as
		// item_completed below. Both open a turn, and only the first prompt is a title.
		c.startTurn()
		c.setWindow(p.ContextWindow)
		if c.title == "" {
			c.setTitle(p.Message)
		}
	case "token_count":
		c.usage(p.Info)
	case "task_complete", "turn_aborted":
		c.endTurn()
	case "item_completed":
		c.item(p.Item)
	}
}

func (c *codexFile) item(item *codexItem) {
	if item == nil {
		return
	}
	switch item.Type {
	case "UserMessage":
		c.startTurn()
		if c.title == "" {
			c.setTitle(codexItemText(item))
		}
	case "AgentMessage":
		// Commentary is progress inside the turn; only a final answer ends it.
		if item.Phase == "final_answer" {
			c.endTurn()
		}
	}
}

func (c *codexFile) response(p codexPayload) {
	switch p.Type {
	case "function_call", "custom_tool_call":
		if p.CallID == "" {
			return // without an id its output could never close it
		}
		if c.pendingCalls == nil {
			c.pendingCalls = make(map[string]struct{})
		}
		c.pendingCalls[p.CallID] = struct{}{}
	case "function_call_output", "custom_tool_call_output":
		delete(c.pendingCalls, p.CallID)
	}
}

func (c *codexFile) startTurn() {
	c.openTurn = true
	c.pendingCalls = nil
}

func (c *codexFile) endTurn() {
	c.openTurn = false
	c.pendingCalls = nil
}

func (c *codexFile) setTitle(text string) {
	if text == "" {
		return
	}
	c.title = promptTitle(text)
}

func (c *codexFile) setWindow(limit int64) {
	if limit > 0 {
		c.context.Limit = limit
	}
}

func (c *codexFile) usage(info *codexTokenInfo) {
	if info == nil {
		return
	}
	c.setWindow(info.ContextWindow)
	if t := info.Total; t != nil {
		// Cumulative totals are a level, not a delta: each event replaces the previous one.
		// Codex counts cached input inside input_tokens, so the cache read is split out rather
		// than summed twice. cache_write_input_tokens is dropped: it is zero in every rollout
		// seen here, and adding it to a total that may already include it risks double counting.
		c.tokens = Tokens{
			Input:     max(0, t.Input-t.Cached),
			Output:    t.Output,
			CacheRead: t.Cached,
		}
	}
	last := info.Last
	if last == nil || last.Input+last.Output == 0 {
		// Compaction writes a token_count with a zero last usage. It is a marker, not a
		// response, so it must not blank the fill or add an empty sparkline point.
		return
	}
	c.sawUsage = true
	c.context.Used = last.Input
	c.history = appendHistoryPoint(c.history, last.Input+last.Output)
}

func (c *codexFile) session() StoredSession {
	s := StoredSession{
		ID:           c.id,
		Dir:          c.dir,
		Title:        c.title,
		Model:        c.model,
		Tokens:       c.tokens,
		TokenHistory: slices.Clone(c.history),
		Updated:      c.modTime(),
		State:        StateWaiting,
	}
	switch {
	case len(c.pendingCalls) > 0:
		s.State = StateExecuting
	case c.openTurn:
		s.State = StateThinking
	}
	if c.sawUsage {
		s.Context = c.context
	}
	return s
}

// codexEntry is the envelope every rollout line carries. Unknown types leave Type empty and
// are ignored, which is what keeps format additions from breaking the read.
type codexEntry struct {
	Type    string       `json:"type"`
	Payload codexPayload `json:"payload"`
}

type codexPayload struct {
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	Model         string          `json:"model"`
	ContextWindow int64           `json:"model_context_window"`
	Message       string          `json:"message"`
	CallID        string          `json:"call_id"`
	Info          *codexTokenInfo `json:"info"`
	Item          *codexItem      `json:"item"`
}

type codexItem struct {
	Type    string          `json:"type"`
	Phase   string          `json:"phase"`
	Content json.RawMessage `json:"content"`
}

type codexText struct {
	Text string `json:"text"`
}

type codexTokenInfo struct {
	Total         *codexUsage `json:"total_token_usage"`
	Last          *codexUsage `json:"last_token_usage"`
	ContextWindow int64       `json:"model_context_window"`
}

type codexUsage struct {
	Input  int64 `json:"input_tokens"`
	Cached int64 `json:"cached_input_tokens"`
	Output int64 `json:"output_tokens"`
}

// codexItemText joins an item's text blocks. The schema is inconsistent about the casing of
// other fields, but text blocks carry "text" consistently. The content is decoded here rather
// than with the envelope so a full assistant answer is never materialized just to read its
// item type.
func codexItemText(item *codexItem) string {
	if len(item.Content) == 0 {
		return ""
	}
	var parts []codexText
	if err := json.Unmarshal(item.Content, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(part.Text)
	}
	return b.String()
}
