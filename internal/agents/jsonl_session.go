package agents

import (
	"slices"
	"strings"
	"time"
)

// jsonlSession is the accumulated session state every line-oriented reader fills in.
//
// The record grammar stays with each harness: Claude, Codex and pi name turns differently and
// only their own reader can tell a tool call from a status marker. Identity, token totals,
// per-turn history, the context fill and the final StoredSession are the same for all of them,
// so they live here once. A fix to the title fallback or the history bound therefore cannot
// land in one reader and miss another.
type jsonlSession struct {
	id       string
	dir      string
	parentID string
	model    string
	title    string
	prompt   string
	updated  time.Time

	tokens  Tokens
	history []int64
	context Context
	// sawUsage and sawContext keep a harness that has recorded no usage out of the report. A
	// cumulative mapping that records only a limit can still report an empty fill against it.
	sawUsage   bool
	sawContext bool
}

func (s *jsonlSession) setID(id string) {
	if id != "" {
		s.id = id
	}
}

// setIDIfEmpty keeps the first identity. A forked or resumed transcript may replay its
// parent's header, and the first record is this file's own.
func (s *jsonlSession) setIDIfEmpty(id string) {
	if s.id == "" && id != "" {
		s.id = id
	}
}

func (s *jsonlSession) setDir(dir string) {
	if dir != "" {
		s.dir = dir
	}
}

func (s *jsonlSession) setDirIfEmpty(dir string) {
	if s.dir == "" && dir != "" {
		s.dir = dir
	}
}

func (s *jsonlSession) setParentIfEmpty(id string) {
	if s.parentID == "" && id != "" {
		s.parentID = id
	}
}

func (s *jsonlSession) setModel(model string) {
	if model != "" {
		s.model = model
	}
}

// setTitle takes the latest non-empty name. A store whose name record is authoritative clears a
// name through replaceTitle instead.
func (s *jsonlSession) setTitle(title string) {
	if title = strings.TrimSpace(title); title != "" {
		s.title = title
	}
}

func (s *jsonlSession) replaceTitle(title string) {
	s.title = strings.TrimSpace(title)
}

// setPrompt keeps the first real prompt as the title fallback.
func (s *jsonlSession) setPrompt(text string) {
	if s.prompt == "" && text != "" {
		s.prompt = promptTitle(text)
	}
}

// addTokens folds an auxiliary record's counters into the session total. A cache warm or a
// compaction summary adds tokens without pretending to be a turn, so it draws no sparkline point.
func (s *jsonlSession) addTokens(t Tokens) {
	if t == (Tokens{}) {
		return
	}
	s.tokens = s.tokens.add(t)
	s.sawUsage = true
}

// addResponseTokens folds one model response into the total and draws its sparkline point.
func (s *jsonlSession) addResponseTokens(t Tokens) {
	if t == (Tokens{}) {
		return
	}
	s.addTokens(t)
	s.addHistory(t.sum())
}

func (s *jsonlSession) addHistory(point int64) {
	s.history = appendHistoryPoint(s.history, point)
}

// discardLastResponse reverses the previous contribution of a response written in several
// records, keeping the total and the sparkline in step.
func (s *jsonlSession) discardLastResponse(prev Tokens) {
	s.tokens = s.tokens.sub(prev)
	if len(s.history) > 0 {
		s.history = s.history[:len(s.history)-1]
	}
}

// useContext records the prompt the last response saw. A zero is left to the reader, because
// only it can tell "no response yet" from "a response that used no prompt".
func (s *jsonlSession) useContext(used int64) {
	s.context.Used = used
	s.sawContext = true
}

func (s *jsonlSession) setContextLimit(limit int64) {
	if limit > 0 {
		s.context.Limit = limit
	}
}

func (s *jsonlSession) session(state State, fallbackUpdated time.Time) StoredSession {
	title := s.title
	if title == "" {
		title = s.prompt
	}
	updated := s.updated
	if updated.IsZero() {
		updated = fallbackUpdated
	}
	out := StoredSession{
		ID:           s.id,
		Dir:          s.dir,
		ParentID:     s.parentID,
		Title:        title,
		Model:        s.model,
		Tokens:       s.tokens,
		TokenHistory: slices.Clone(s.history),
		Updated:      updated,
		State:        state,
	}
	if s.sawUsage || s.sawContext {
		out.Context = s.context
	}
	return out
}
