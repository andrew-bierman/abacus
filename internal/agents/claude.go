package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// claudePathMissTTL keeps a cwd with no transcript from re-walking every project directory on
// every tick.
const claudePathMissTTL = 2 * time.Second

// ClaudeStore reads Claude Code's JSONL transcripts.
//
// Unlike opencode, Claude keeps no session list: a session exists because a live process
// writes one transcript for its working directory. So the caller supplies the path it already
// resolved from the process table, and the store only remembers how far it has read.
//
// A ClaudeStore is not safe for concurrent use; keep it on the goroutine that owns the tab.
type ClaudeStore struct {
	// Root is the projects directory, normally ClaudeProjectsPath(home). Transcript only needs
	// a path; Root is used by PathFor.
	Root string
	jsonlFiles
	paths map[string]claudePath
}

// ClaudeProjectsPath is the default directory of Claude Code transcripts for a home.
func ClaudeProjectsPath(home string) string {
	if home == "" {
		return ""
	}
	return strings.TrimSuffix(home, "/") + "/.claude/projects"
}

var errClaudePathEmpty = errors.New("agents: claude transcript path is empty")

// Transcript returns the accumulated facts for the transcript at path, reading only the bytes
// appended since the previous call. A vanished or unreadable file returns an error; a corrupt
// record inside it is skipped.
func (s *ClaudeStore) Transcript(path string) (StoredSession, error) {
	return s.load(path, errClaudePathEmpty, func() jsonlAccumulator { return &claudeFile{} })
}

// PathFor returns the transcript a session working in cwd is writing.
//
// The encoded-cwd directory is the normal home. The fallback covers worktree sessions, whose
// directory is keyed by the branch instead, so it accepts the newest transcript written at or
// after since; a zero since accepts any. The fallback is a heuristic: since should be the
// session's start time, which keeps it from adopting an older transcript from another project.
func (s *ClaudeStore) PathFor(cwd string, since time.Time) (string, bool) {
	if s.Root == "" || cwd == "" {
		return "", false
	}
	if cached, ok := s.paths[cwd]; ok && cached.since.Equal(since) {
		switch {
		case cached.path == "":
			if time.Since(cached.checked) < claudePathMissTTL {
				return "", false
			}
		default:
			// A session can start a new transcript in the same directory (/clear, or a second
			// session here), so the hit is only good while it is still the newest there.
			if latest, ok := newestTranscript(filepath.Dir(cached.path), since); ok && latest.path == cached.path {
				return cached.path, true
			}
		}
	}
	ref, ok := s.resolvePath(cwd, since)
	if s.paths == nil {
		s.paths = make(map[string]claudePath)
	}
	s.paths[cwd] = claudePath{since: since, path: ref.path, checked: time.Now()}
	return ref.path, ok
}

// claudePath remembers a resolved transcript, or a recent miss, so neither re-walks the whole
// projects directory on every tick.
type claudePath struct {
	since   time.Time
	path    string
	checked time.Time
}

func (s *ClaudeStore) resolvePath(cwd string, since time.Time) (transcriptRef, bool) {
	encoded := encodeClaudePath(cwd)
	if ref, ok := newestTranscript(filepath.Join(s.Root, encoded), since); ok {
		return ref, true
	}

	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return transcriptRef{}, false
	}
	var best transcriptRef
	for _, e := range entries {
		if !e.IsDir() || e.Name() == encoded {
			continue
		}
		if ref, ok := newestTranscript(filepath.Join(s.Root, e.Name()), since); ok && ref.newerThan(best) {
			best = ref
		}
	}
	return best, best.path != ""
}

// transcriptRef is a transcript with the mtime used to choose between candidates.
type transcriptRef struct {
	path string
	mod  time.Time
}

// newerThan keeps the choice deterministic when transcripts share an mtime, which a fast copy
// or a coarse filesystem clock can produce.
func (t transcriptRef) newerThan(o transcriptRef) bool {
	if o.path == "" {
		return true
	}
	if c := t.mod.Compare(o.mod); c != 0 {
		return c > 0
	}
	return t.path > o.path
}

func newestTranscript(dir string, since time.Time) (transcriptRef, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return transcriptRef{}, false
	}
	var best transcriptRef
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}
		candidate := transcriptRef{path: filepath.Join(dir, e.Name()), mod: info.ModTime()}
		if candidate.newerThan(best) {
			best = candidate
		}
	}
	return best, best.path != ""
}

// encodeClaudePath mirrors Claude Code's project directory naming: every separator, colon,
// underscore and dot becomes a hyphen.
func encodeClaudePath(cwd string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '_', '.':
			return '-'
		}
		return r
	}, cwd)
}

// claudeFile is the accumulation for one transcript. It survives between reads so only new
// bytes are parsed; it is replaced wholesale when the file behind a path changes.
type claudeFile struct {
	jsonlCursor

	id      string
	dir     string
	model   string
	title   string
	prompt  string
	tokens  Tokens
	history []int64

	contextUsed int64
	maxContext  int64
	sawUsage    bool

	lastMsgID   string
	lastUsage   Tokens
	lastCounted bool

	hasTool  bool
	openTurn bool
}

func (c *claudeFile) add(line []byte) {
	var e claudeEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	if e.SessionID != "" {
		c.id = e.SessionID
	}
	if e.CWD != "" {
		c.dir = e.CWD
	}
	switch e.Type {
	case "assistant":
		c.assistant(e)
	case "user":
		c.user(e)
	case "ai-title":
		if title := strings.TrimSpace(e.AITitle); title != "" {
			c.title = title
		}
	case "summary":
		// Older Claude Code wrote one summary record per transcript instead of ai-title.
		if title := strings.TrimSpace(e.Summary); title != "" {
			c.title = title
		}
	}
}

func (c *claudeFile) assistant(e claudeEntry) {
	c.openTurn = false
	msg := e.Message
	if msg == nil {
		return
	}
	// Claude Code marks local, non-model records (a permission notice, "no response
	// requested") with a bracketed model name, which must not replace the real one.
	if msg.Model != "" && !strings.HasPrefix(msg.Model, "<") {
		c.model = msg.Model
	}
	hasTool := claudeHasToolUse(msg.Content)
	sameResponse := msg.ID != "" && msg.ID == c.lastMsgID
	if sameResponse {
		// One response is written as several records, one per content block, so the tool call
		// may arrive on a different line from the text.
		c.hasTool = c.hasTool || hasTool
	} else {
		c.lastMsgID = msg.ID
		c.hasTool = hasTool
		c.lastUsage = Tokens{}
		c.lastCounted = false
	}
	u := msg.Usage
	if u == nil {
		return
	}
	t := Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheCreate: u.CacheCreate}
	if t == (Tokens{}) {
		// Zero usage means no model call happened: counting it would add an empty turn and
		// blank the context the previous response established.
		return
	}
	if sameResponse && c.lastCounted {
		// Same response again: replace its contribution rather than add a second one. This is
		// what keeps a split response from doubling the totals, and it also accepts a usage
		// snapshot that grew while the response streamed.
		c.tokens = c.tokens.sub(c.lastUsage)
		if len(c.history) > 0 {
			c.history = c.history[:len(c.history)-1]
		}
	}
	c.tokens = c.tokens.add(t)
	c.lastUsage, c.lastCounted = t, true
	c.sawUsage = true

	// All three are prompt tokens: input excludes cached content, so the prompt the model saw
	// is their sum.
	used := t.prompt()
	c.contextUsed = used
	if used > c.maxContext {
		c.maxContext = used
	}
	c.history = appendHistoryPoint(c.history, t.sum())
}

func (c *claudeFile) user(e claudeEntry) {
	msg := e.Message
	if msg == nil {
		return
	}
	text, allToolResults := claudeContent(msg.Content)
	// Claude Code records an interrupt as a user line; the model is not generating.
	if strings.HasPrefix(strings.TrimSpace(text), "[Request interrupted by user") {
		c.openTurn = false
		c.hasTool = false
		return
	}
	if e.IsMeta || allToolResults || claudeLocalCommand(text) {
		// Claude Code also writes tool results, slash-command output and bash dumps as
		// user-role records. A tool result reopens the turn, because the model is being asked
		// to answer it; the rest are local operations that never reach the model, and treating
		// them as prompts would pin the session in thinking.
		c.hasTool = false
		if allToolResults {
			c.openTurn = true
		}
		return
	}
	c.openTurn = true
	c.hasTool = false
	if c.prompt == "" {
		c.prompt = promptTitle(text)
	}
}

func (c *claudeFile) session() StoredSession {
	title := c.title
	if title == "" {
		title = c.prompt
	}
	s := StoredSession{
		ID:           c.id,
		Dir:          c.dir,
		Title:        title,
		Model:        c.model,
		Tokens:       c.tokens,
		TokenHistory: slices.Clone(c.history),
		Updated:      c.modTime(),
		State:        StateWaiting,
	}
	switch {
	case c.openTurn:
		s.State = StateThinking
	case c.hasTool:
		s.State = StateExecuting
	}
	if c.sawUsage {
		s.Context = Context{Used: c.contextUsed, Limit: claudeContextLimit(c.model, c.maxContext)}
	}
	return s
}

// claudeContextLimit infers the window, which the transcript does not record: a model marked
// [1m], or a session already seen above 200k, is a 1M window; everything else is 200k.
func claudeContextLimit(model string, maxSeen int64) int64 {
	if strings.Contains(model, "[1m]") || maxSeen > 200_000 {
		return 1_000_000
	}
	return 200_000
}

// claudeEntry is the subset of a transcript record this reader uses. Claude Code's format has
// many record types; unknown ones unmarshal to an empty Type and are ignored.
type claudeEntry struct {
	Type      string         `json:"type"`
	SessionID string         `json:"sessionId"`
	CWD       string         `json:"cwd"`
	IsMeta    bool           `json:"isMeta"`
	AITitle   string         `json:"aiTitle"`
	Summary   string         `json:"summary"`
	Message   *claudeMessage `json:"message"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *claudeUsage    `json:"usage"`
}

type claudeUsage struct {
	Input       int64 `json:"input_tokens"`
	Output      int64 `json:"output_tokens"`
	CacheRead   int64 `json:"cache_read_input_tokens"`
	CacheCreate int64 `json:"cache_creation_input_tokens"`
}

type claudeBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// claudeContent splits a message body, which is either a bare string or an array of blocks,
// into its visible text and whether it is nothing but tool results. A mixed block message is
// treated as text so user input is never swallowed.
func claudeContent(raw json.RawMessage) (text string, allToolResults bool) {
	if len(raw) == 0 {
		return "", false
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		return s, false
	case '[':
		var blocks []claudeBlock
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return "", false
		}
		var b strings.Builder
		allToolResults = len(blocks) > 0
		for _, block := range blocks {
			if block.Type == "text" && block.Text != "" {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(block.Text)
			}
			if block.Type != "tool_result" {
				allToolResults = false
			}
		}
		return b.String(), allToolResults
	}
	return "", false
}

func claudeHasToolUse(raw json.RawMessage) bool {
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return false
	}
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

// claudeLocalCommand reports whether a user record is a local operation Claude Code serializes
// with user role: a slash command, its output, or a bash invocation.
func claudeLocalCommand(text string) bool {
	t := strings.TrimSpace(text)
	for _, tag := range []string{
		"<local-command-stdout>", "<local-command-stderr>", "<local-command-caveat>",
		"<command-name>", "<bash-input>", "<bash-stdout>", "<bash-stderr>",
	} {
		if strings.HasPrefix(t, tag) {
			return true
		}
	}
	return false
}
