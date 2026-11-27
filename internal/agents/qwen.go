package agents

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// maxQwenHeads bounds the first-record cache: one entry per session file whose project was
// compared, so a long-lived monitor does not reread heads it already matched.
const maxQwenHeads = 256

// QwenSessionsPath is the directory Qwen Code groups project sessions under. QWEN_RUNTIME_DIR
// relocates the runtime output base, and QWEN_HOME relocates the configuration tree that
// supplies the default base, so both are honoured before the home-relative default.
func QwenSessionsPath(home string) string {
	return QwenSessionsPathFrom(home, os.Getenv)
}

// QwenSessionsPathFrom is QwenSessionsPath with the environment supplied by the caller, so the
// sessions join can keep its environment edge injectable.
func QwenSessionsPathFrom(home string, getenv func(string) string) string {
	if home == "" {
		return ""
	}
	if getenv != nil {
		if runtimeDir := strings.TrimSpace(getenv("QWEN_RUNTIME_DIR")); runtimeDir != "" {
			return filepath.Join(runtimeDir, "projects")
		}
		if qwenHome := strings.TrimSpace(getenv("QWEN_HOME")); qwenHome != "" {
			return filepath.Join(qwenHome, "projects")
		}
	}
	return filepath.Join(home, ".qwen", "projects")
}

// QwenStore reads Qwen Code's session JSONL files.
//
// A session is <projects>/<sanitized cwd>/chats/<sessionId>.jsonl, where the directory name is
// the project root with every non-alphanumeric rune replaced by a hyphen. The transcript is
// append-only: each line is a ChatRecord carrying type, timestamp, cwd and sessionId, user
// records hold message.parts, and assistant records add model, usageMetadata and
// contextWindowSize. A custom title and a parent session arrive as system records with their
// value in systemPayload. Archived sessions move under chats/archive and are not live.
//
// The file layout and record shapes are Qwen Code's SessionService, Storage.getProjectDir,
// sanitizeCwd and ChatRecordingService (packages/core/src/services/sessionService.ts,
// chatRecordingService.ts and config/storage.ts). Qwen is a Gemini CLI fork, but its record
// model (type user/assistant/tool_result/system plus provenance) differs enough from the
// Gemini mapping that this reader keeps its own grammar. In particular, Qwen writes
// notifications, cron prompts and agent messages as type user; they must not start a turn or
// become a session title.
//
// A QwenStore is not safe for concurrent use; keep it on the goroutine that owns the tab.
type QwenStore struct {
	// Root is the projects directory, normally QwenSessionsPath(home). Transcript only needs a
	// transcript path; Root is used by PathFor.
	Root  string
	heads map[string]qwenHead
	jsonlFiles
}

// qwenHead is a session file's first-record working directory and the file identity it was
// read at. Appends keep the identity and only grow the file, so a live session's head is read
// once rather than on every tick.
type qwenHead struct {
	cwd     string
	info    os.FileInfo
	checked time.Time
}

// qwenRecord is the part of one ChatRecord this reader uses. Every field is optional: a store
// writes records for features this reader does not model, and they contribute nothing.
type qwenRecord struct {
	Type              string    `json:"type"`
	Subtype           string    `json:"subtype"`
	Provenance        string    `json:"provenance"`
	Timestamp         string    `json:"timestamp"`
	SessionID         string    `json:"sessionId"`
	CWD               string    `json:"cwd"`
	Model             string    `json:"model"`
	ContextWindowSize int64     `json:"contextWindowSize"`
	Message           qwenBody  `json:"message"`
	Usage             qwenUsage `json:"usageMetadata"`
	SystemPayload     struct {
		// DisplayText is the prompt projection Qwen's list reader prefers; it is a
		// pointer so an explicitly empty projection is distinct from an absent one.
		DisplayText     *string `json:"displayText"`
		CustomTitle     string  `json:"customTitle"`
		ParentSessionID string  `json:"parentSessionId"`
	} `json:"systemPayload"`
}

type qwenBody struct {
	Parts []qwenPart `json:"parts"`
}

type qwenPart struct {
	Text string `json:"text"`
}

// qwenUsage is Gemini's usageMetadata shape: promptTokenCount already includes cached content,
// so the adapter subtracts it once and keeps the cache read visible.
type qwenUsage struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
}

func (u qwenUsage) tokens() Tokens {
	return Tokens{
		Input:     max(0, u.PromptTokenCount-u.CachedContentTokenCount),
		Output:    u.CandidatesTokenCount,
		CacheRead: u.CachedContentTokenCount,
	}
}

var errQwenPathEmpty = errors.New("agents: qwen transcript path is empty")

// Transcript returns the accumulated facts for the session at path, reading only the bytes
// appended since the previous call.
func (s *QwenStore) Transcript(path string) (StoredSession, error) {
	st, err := s.load(path, errQwenPathEmpty, func() jsonlAccumulator { return &qwenFile{} })
	if err != nil {
		return StoredSession{}, err
	}
	if st.ID == "" {
		st.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	return st, nil
}

// PathFor returns the session a live process working in cwd is writing. The sanitized project
// directory is deterministic, so this resolves one directory; the newest session at or after
// since wins. Two different directories can sanitize to the same name, so a candidate whose
// first record names another project is skipped rather than adopted. A candidate whose head is
// unreadable is kept: it is the newest file in this directory and may be a session that has not
// flushed its first record yet, and rejecting it could adopt an older, stale session instead.
func (s *QwenStore) PathFor(cwd string, since time.Time) (string, bool) {
	if s.Root == "" || cwd == "" {
		return "", false
	}
	chats := filepath.Join(s.Root, qwenProjectID(cwd), "chats")
	entries, err := os.ReadDir(chats)
	if err != nil {
		return "", false
	}
	candidates := make([]qwenCandidate, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !isQwenSessionFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}
		candidates = append(candidates, qwenCandidate{
			path: filepath.Join(chats, e.Name()),
			mod:  info.ModTime(),
			info: info,
		})
	}
	slices.SortFunc(candidates, func(a, b qwenCandidate) int {
		if c := b.mod.Compare(a.mod); c != 0 {
			return c
		}
		return strings.Compare(b.path, a.path)
	})
	for _, candidate := range candidates {
		if head, ok := s.headCWD(candidate.path, candidate.info); ok && head != "" && !sameQwenDir(head, cwd) {
			continue
		}
		return candidate.path, true
	}
	return "", false
}

// qwenCandidate is one session file in a project's chats directory, with the entry's own
// FileInfo so resolving its head does not stat the file a second time.
type qwenCandidate struct {
	path string
	mod  time.Time
	info os.FileInfo
}

// headCWD reads the first record's project, caching it while the file keeps growing in place.
// A missing or incomplete first record returns ok false; PathFor decides what an unverified
// candidate means.
func (s *QwenStore) headCWD(path string, info os.FileInfo) (string, bool) {
	if h, ok := s.heads[path]; ok && qwenHeadCurrent(h.info, info) {
		h.checked = time.Now()
		s.heads[path] = h
		return h.cwd, true
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	line, _, ok, err := readJSONLine(bufio.NewReaderSize(f, 64<<10))
	if err != nil || !ok {
		return "", false
	}
	var record struct {
		CWD string `json:"cwd"`
	}
	if json.Unmarshal(line, &record) != nil {
		return "", false
	}
	if s.heads == nil {
		s.heads = make(map[string]qwenHead)
	}
	s.heads[path] = qwenHead{cwd: record.CWD, info: info, checked: time.Now()}
	s.trimHeads(path)
	return record.CWD, true
}

func (s *QwenStore) trimHeads(keep string) {
	trimCopilotOldest(s.heads, maxQwenHeads, keep, func(h qwenHead) time.Time { return h.checked })
}

// qwenHeadCurrent reports whether a cached head still describes the same file: the store is
// append-only, so an identity match with no shrink keeps the first record intact.
func qwenHeadCurrent(prev, info os.FileInfo) bool {
	return prev != nil && info != nil && os.SameFile(prev, info) && info.Size() >= prev.Size()
}

// qwenFile is the accumulation for one transcript. Qwen appends one record per turn step and
// never rewrites a record, so usage is additive. A tool result closes the tool window and the
// model is generating again, which is why it opens the turn.
type qwenFile struct {
	jsonlCursor
	jsonlSession

	openTurn bool
}

func (f *qwenFile) add(line []byte) {
	var record qwenRecord
	if json.Unmarshal(line, &record) != nil {
		return
	}
	if ts, ok := jsonlTime(record.Timestamp); ok && !ts.IsZero() {
		f.updated = ts
	}
	f.setIDIfEmpty(record.SessionID)
	f.setDirIfEmpty(record.CWD)
	if record.SystemPayload.ParentSessionID != "" {
		f.setParentIfEmpty(record.SystemPayload.ParentSessionID)
	}
	if record.SystemPayload.CustomTitle != "" {
		f.setTitle(record.SystemPayload.CustomTitle)
	}
	switch record.Type {
	case "user":
		if !qwenPrompt(record) {
			return
		}
		f.openTurn = true
		// Qwen's list reader labels a session from the first user record with no subtype,
		// preferring its display projection over the raw parts. A mid-turn message has a
		// subtype and so never supplies the name.
		if record.Subtype == "" {
			if record.SystemPayload.DisplayText != nil {
				f.setPrompt(*record.SystemPayload.DisplayText)
			} else {
				f.setPrompt(qwenText(record.Message.Parts))
			}
		}
	case "assistant":
		f.openTurn = false
		f.setModel(record.Model)
		if record.ContextWindowSize > 0 {
			f.setContextLimit(record.ContextWindowSize)
		}
		if tokens := record.Usage.tokens(); tokens != (Tokens{}) {
			f.addResponseTokens(tokens)
			if used := tokens.prompt(); used > 0 {
				f.useContext(used)
			}
		}
	case "tool_result":
		f.openTurn = true
	case "system":
		// Qwen settles every admitted prompt with a system record of subtype turn_result, and
		// writes it even when the turn ends before any assistant record exists: a pre-send
		// gate that refuses the request, or an abort before the first token. The turn is over
		// in every state (completed, cancelled, error), so leaving it open would pin an
		// interrupted tab in thinking until the next prompt.
		if record.Subtype == "turn_result" {
			f.openTurn = false
		}
	}
}

func (f *qwenFile) session() StoredSession {
	state := StateWaiting
	if f.openTurn {
		state = StateThinking
	}
	return f.jsonlSession.session(state, f.modTime())
}

// qwenPrompt reports whether a user-typed record is a real turn. Qwen writes background
// notifications and agent messages as type user; their subtypes keep them out, and provenance
// is the authority when a store records it.
func qwenPrompt(record qwenRecord) bool {
	switch record.Subtype {
	case "", "mid_turn_user_message":
	default:
		return false
	}
	switch record.Provenance {
	case "", "real_user", "mod_prompt":
		return true
	}
	return false
}

func qwenText(parts []qwenPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// qwenProjectID mirrors Qwen's sanitizeCwd: every non-alphanumeric rune becomes a hyphen, and
// Windows folds the path to lower case because its directories are case-insensitive.
func qwenProjectID(cwd string) string {
	if runtime.GOOS == "windows" {
		cwd = strings.ToLower(cwd)
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, cwd)
}

// isQwenSessionFile matches the session names SessionService writes: a UUID-like id and
// ".jsonl". Sidecars live beside it as <id>.ledger.jsonl and <id>.pr.json and must not be
// mistaken for transcripts.
func isQwenSessionFile(name string) bool {
	base, ok := strings.CutSuffix(name, ".jsonl")
	if !ok || len(base) < 32 || len(base) > 36 {
		return false
	}
	for _, r := range base {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F', r == '-':
		default:
			return false
		}
	}
	return true
}

func sameQwenDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
