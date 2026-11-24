package agents

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// PiStore reads pi's session JSONL files.
//
// Pi keeps no session list either: a session is a JSONL file for a working directory, so the
// caller supplies the path it resolved from the process table and the store only remembers how
// far it has read. Pi's own shell tools receive PI_SESSION_FILE, but the pi process carries only
// process markers, so the cwd is the reliable join.
//
// A PiStore is not safe for concurrent use; keep it on the goroutine that owns the tab.
type PiStore struct {
	// Root is the sessions directory, normally PiSessionsPath(home). A configured sessionDir is
	// passed here by the caller; Transcript only needs a path.
	Root string
	jsonlFiles
}

// PiSessionsPath is the default directory of pi sessions for a home.
func PiSessionsPath(home string) string {
	if home == "" {
		return ""
	}
	return strings.TrimSuffix(home, "/") + "/.pi/agent/sessions"
}

var errPiPathEmpty = errors.New("agents: pi session path is empty")

// Transcript returns the accumulated facts for the session at path, reading only the bytes
// appended since the previous call. A vanished or unreadable file returns an error; a corrupt
// record inside it is skipped.
func (s *PiStore) Transcript(path string) (StoredSession, error) {
	return s.load(path, errPiPathEmpty, func() jsonlAccumulator { return &piFile{} })
}

// PathFor returns the session a live process working in cwd is writing.
//
// The default root groups sessions one directory per cwd; a configured sessionDir is flat and
// carries every cwd in that one directory, so both layouts are tried. A zero since accepts any.
// Two live pi sessions in the same directory cannot be told apart here; that attribution belongs
// to the core wiring.
func (s *PiStore) PathFor(cwd string, since time.Time) (string, bool) {
	if s.Root == "" || cwd == "" {
		return "", false
	}
	if ref, ok := newestTranscript(filepath.Join(s.Root, encodePiPath(cwd)), since); ok {
		return ref.path, true
	}
	return newestFlatTranscript(s.Root, cwd, since)
}

// newestFlatTranscript finds a session in a configured sessionDir, where pi drops the per-cwd
// subdirectory and identifies each file by its header's cwd. It mirrors pi's own lookup: newest
// first, header read only until the cwd matches.
func newestFlatTranscript(dir, cwd string, since time.Time) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var refs []transcriptRef
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
		refs = append(refs, transcriptRef{path: filepath.Join(dir, e.Name()), mod: info.ModTime()})
	}
	slices.SortFunc(refs, func(a, b transcriptRef) int {
		if c := b.mod.Compare(a.mod); c != 0 {
			return c
		}
		return strings.Compare(a.path, b.path)
	})
	for _, ref := range refs {
		if fileCWD, ok := piHeaderCWD(ref.path); ok && filepath.Clean(fileCWD) == filepath.Clean(cwd) {
			return ref.path, true
		}
	}
	return "", false
}

// piHeaderCWD reads just the header of a session file.
func piHeaderCWD(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	line, _, ok, err := readJSONLine(bufio.NewReaderSize(f, 64<<10))
	if !ok || err != nil {
		return "", false
	}
	var e piEntry
	if err := json.Unmarshal(line, &e); err != nil || e.Type != "session" || e.CWD == "" {
		return "", false
	}
	return e.CWD, true
}

// encodePiPath mirrors pi's session directory naming: one leading separator is dropped, and
// every separator or colon becomes a hyphen inside a `--…--` wrapper. Dots and underscores stay,
// unlike Claude's encoding.
func encodePiPath(cwd string) string {
	if len(cwd) > 0 && (cwd[0] == '/' || cwd[0] == '\\') {
		cwd = cwd[1:]
	}
	mapped := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '-'
		}
		return r
	}, cwd)
	return "--" + mapped + "--"
}

type piFile struct {
	jsonlCursor

	id      string
	dir     string
	model   string
	title   string
	prompt  string
	tokens  Tokens
	history []int64

	contextUsed int64
	sawResponse bool

	openTurn     bool
	pendingCalls map[string]struct{}
}

func (p *piFile) add(line []byte) {
	var e piEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	switch e.Type {
	case "session":
		if p.id == "" {
			p.id = e.ID
		}
		if p.dir == "" {
			p.dir = e.CWD
		}
	case "model_change":
		if p.model == "" {
			p.model = piModelName(e.Provider, e.ModelID)
		}
	case "session_info":
		// The latest name wins, including an empty one: pi treats that as a cleared name and
		// falls back to the first prompt.
		p.title = strings.TrimSpace(e.Name)
	case "usage", "compaction", "branch_summary":
		// These record model work that is not a conversation response: cache warming, a
		// compaction summary, a branch summary. They count toward the session's tokens but
		// must not move the context fill or add a sparkline point.
		p.addTokens(e.Usage)
	case "message":
		p.message(e.Message)
	}
}

func (p *piFile) message(m *piMessage) {
	if m == nil {
		return
	}
	switch m.Role {
	case "user":
		text := piText(m.Content)
		if strings.TrimSpace(text) == "" {
			return
		}
		// A pending call stays pending: a steering prompt does not stop the tool that is
		// already running, and session() prefers executing while one is open.
		p.openTurn = true
		if p.prompt == "" {
			p.prompt = promptTitle(text)
		}
	case "assistant":
		p.assistant(m)
	case "toolResult":
		p.toolResult(m)
	}
}

func (p *piFile) assistant(m *piMessage) {
	if name := piModelName(m.Provider, m.Model); name != "" {
		p.model = name
	}
	// One assistant record is one response: pi writes the content blocks together, so a new
	// response also means the previous turn's calls are settled.
	p.openTurn = false
	p.pendingCalls = nil
	p.responseUsage(m.Usage)
	switch m.StopReason {
	case "toolUse":
		for _, id := range piToolCallIDs(m.Content) {
			if p.pendingCalls == nil {
				p.pendingCalls = make(map[string]struct{})
			}
			p.pendingCalls[id] = struct{}{}
		}
	case "deferred":
		// The provider side deferred the response and pi is waiting on it, so the turn is still
		// open even though no tool is running.
		p.openTurn = true
	}
}

func (p *piFile) toolResult(m *piMessage) {
	if m.ToolCallID != "" {
		delete(p.pendingCalls, m.ToolCallID)
	}
	p.addTokens(m.Usage)
	// The model is asked to answer the result, so the turn reopens. If another parallel call is
	// still pending, session() stays executing.
	p.openTurn = true
}

// responseUsage sets the fill and adds a history point. Nested tool work and auxiliary records
// go through addTokens instead, so they never pretend to be turns.
func (p *piFile) responseUsage(u *piUsage) {
	if u == nil {
		return
	}
	p.addTokens(u)
	// pi reports zero usage for a response that never reached the provider; counting it would add
	// an empty turn and blank the fill an earlier response established.
	if u.total() == 0 {
		return
	}
	p.sawResponse = true
	p.contextUsed = u.prompt()
	p.history = appendHistoryPoint(p.history, u.total())
}

func (p *piFile) addTokens(u *piUsage) {
	if u == nil {
		return
	}
	t := Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheCreate: u.CacheWrite}
	if t == (Tokens{}) {
		return
	}
	p.tokens = p.tokens.add(t)
}

func (p *piFile) session() StoredSession {
	title := p.title
	if title == "" {
		title = p.prompt
	}
	s := StoredSession{
		ID:           p.id,
		Dir:          p.dir,
		Title:        title,
		Model:        p.model,
		Tokens:       p.tokens,
		TokenHistory: slices.Clone(p.history),
		Updated:      p.modTime(),
		State:        StateWaiting,
	}
	switch {
	case len(p.pendingCalls) > 0:
		s.State = StateExecuting
	case p.openTurn:
		s.State = StateThinking
	}
	if p.sawResponse {
		// The session file does not name the window; Context.Limit stays zero, so a front-end
		// draws no bar rather than a made-up percentage.
		s.Context = Context{Used: p.contextUsed}
	}
	return s
}

// piEntry is the subset of a session record this reader uses. The file is a tree of entries via
// id/parentId, but it is written in append order and the last write is the current leaf, so the
// reader follows the line order; unknown types unmarshal to an empty Type and are ignored.
type piEntry struct {
	Type     string     `json:"type"`
	ID       string     `json:"id"`
	CWD      string     `json:"cwd"`
	Name     string     `json:"name"`
	Provider string     `json:"provider"`
	ModelID  string     `json:"modelId"`
	Message  *piMessage `json:"message"`
	Usage    *piUsage   `json:"usage"`
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	Usage      *piUsage        `json:"usage"`
	StopReason string          `json:"stopReason"`
	ToolCallID string          `json:"toolCallId"`
}

// piUsage is one provider call's counters. Reasoning is already inside output and cacheWrite1h
// inside cacheWrite, so neither is a field here.
type piUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

func (u piUsage) total() int64 {
	return u.Input + u.Output + u.CacheRead + u.CacheWrite
}

// prompt is what the model saw: input excludes cached content, so the cache counters are prompt
// tokens too.
func (u piUsage) prompt() int64 {
	return u.Input + u.CacheRead + u.CacheWrite
}

type piBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	ID   string `json:"id"`
}

// piText returns a message's visible text. User content is a bare string in older sessions and an
// array of blocks in current ones; images and other blocks carry no prompt.
func piText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return s
	case '[':
		var b strings.Builder
		for _, block := range piBlocks(raw) {
			if block.Type != "text" || block.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(block.Text)
		}
		return b.String()
	}
	return ""
}

func piToolCallIDs(raw json.RawMessage) []string {
	var ids []string
	for _, block := range piBlocks(raw) {
		if block.Type == "toolCall" && block.ID != "" {
			ids = append(ids, block.ID)
		}
	}
	return ids
}

func piBlocks(raw json.RawMessage) []piBlock {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var blocks []piBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// piModelName keeps the provider: the same model id can sit behind several providers, and pi
// records both on the response that actually ran.
func piModelName(provider, model string) string {
	if model == "" {
		return ""
	}
	if provider == "" {
		return model
	}
	return provider + "/" + model
}
