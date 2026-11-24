package agents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// maxClaudeLine bounds one transcript record. Attachments can embed large payloads, and a
	// malformed writer must not make the tab allocate without bound.
	maxClaudeLine = 10 << 20
	// maxClaudeFiles bounds the per-path accumulations. A monitor runs for days across many
	// short sessions, so the oldest state is dropped rather than kept forever; a dropped file
	// is simply reparsed from the start if it is read again.
	maxClaudeFiles = 256
	// maxClaudeBytesPerRead bounds how much one tick parses across every transcript, so the
	// first sight of long histories cannot stall the tab. The offset is the resume point, so
	// the totals are the same either way.
	maxClaudeBytesPerRead = 8 << 20
	// claudeBudgetWindow is the tick it assumes: a caller reading many transcripts back to
	// back shares one budget until this much wall time has passed.
	claudeBudgetWindow = 200 * time.Millisecond
	// claudePathMissTTL keeps a cwd with no transcript from re-walking every project directory
	// on every tick.
	claudePathMissTTL = 2 * time.Second
)

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
	Root   string
	files  map[string]*claudeFile
	paths  map[string]claudePath
	budget int64
	window time.Time
}

// ClaudeProjectsPath is the default directory of Claude Code transcripts for a home.
func ClaudeProjectsPath(home string) string {
	if home == "" {
		return ""
	}
	return strings.TrimSuffix(home, "/") + "/.claude/projects"
}

// Transcript returns the accumulated facts for the transcript at path, reading only the bytes
// appended since the previous call. A vanished or unreadable file returns an error; a corrupt
// record inside it is skipped.
func (s *ClaudeStore) Transcript(path string) (StoredSession, error) {
	if path == "" {
		return StoredSession{}, errors.New("agents: claude transcript path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			delete(s.files, path)
		}
		return StoredSession{}, err
	}
	// A caught-up transcript needs no descriptor: the size check alone decides there is
	// nothing to read, and this is the common case on every tick.
	if st, ok := s.files[path]; ok && !st.replaced(info) && info.Size() <= st.offset {
		return st.session(), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return StoredSession{}, err
	}
	defer f.Close()
	if info, err = f.Stat(); err != nil {
		return StoredSession{}, err
	}

	if s.files == nil {
		s.files = make(map[string]*claudeFile)
	}
	st, ok := s.files[path]
	if !ok || st.replaced(info) {
		st = &claudeFile{}
		s.files[path] = st
	}
	err = st.read(f, info, s.readBudget())
	s.trim()
	return st.session(), err
}

// readBudget hands out the bytes this tick may parse. A caller that reads one transcript per
// live session shares the same budget instead of paying the cap for each of them.
func (s *ClaudeStore) readBudget() *int64 {
	if time.Since(s.window) > claudeBudgetWindow {
		s.budget, s.window = maxClaudeBytesPerRead, time.Now()
	}
	return &s.budget
}

// trim drops the least recently written state once the map is full. Re-reading a dropped file
// reparses it from the start, which is correct and rare.
func (s *ClaudeStore) trim() {
	if len(s.files) <= maxClaudeFiles {
		return
	}
	var oldestPath string
	var oldest time.Time
	for path, st := range s.files {
		mod := st.modTime()
		if oldestPath == "" || mod.Before(oldest) {
			oldestPath, oldest = path, mod
		}
	}
	delete(s.files, oldestPath)
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
	info   os.FileInfo
	offset int64
	reader *bufio.Reader

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

func (c *claudeFile) modTime() time.Time {
	if c.info == nil {
		return time.Time{}
	}
	return c.info.ModTime()
}

func (c *claudeFile) replaced(info os.FileInfo) bool {
	if c.info == nil {
		return true
	}
	return !os.SameFile(c.info, info) || info.Size() < c.offset
}

func (c *claudeFile) read(f *os.File, info os.FileInfo, budget *int64) error {
	c.info = info
	if info.Size() <= c.offset {
		return nil
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return err
	}
	if c.reader == nil {
		c.reader = bufio.NewReaderSize(f, 64<<10)
	} else {
		c.reader.Reset(f)
	}
	for {
		line, n, ok, err := readClaudeLine(c.reader, maxClaudeLine)
		if !ok {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		c.offset += int64(n)
		*budget -= int64(n)
		if len(line) > 0 {
			c.add(line)
		}
		if *budget <= 0 {
			return nil
		}
	}
}

func readClaudeLine(r *bufio.Reader, limit int) ([]byte, int, bool, error) {
	chunk, err := r.ReadSlice('\n')
	if err == nil && len(chunk) <= limit {
		return trimClaudeEOL(chunk), len(chunk), true, nil
	}
	var (
		line []byte
		read int
	)
	for {
		read += len(chunk)
		if room := limit - len(line); room > 0 {
			line = append(line, chunk[:min(room, len(chunk))]...)
		}
		if err == nil {
			return trimClaudeEOL(line), read, true, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, 0, false, err
		}
		chunk, err = r.ReadSlice('\n')
	}
}

func trimClaudeEOL(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
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
	used := u.Input + u.CacheRead + u.CacheCreate
	c.contextUsed = used
	if used > c.maxContext {
		c.maxContext = used
	}
	c.history = append(c.history, t.Input+t.Output+t.CacheRead+t.CacheCreate)
	if len(c.history) > maxTokenHistory {
		c.history = c.history[len(c.history)-maxTokenHistory:]
	}
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
		c.prompt = claudePrompt(text)
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
		TokenHistory: append([]int64(nil), c.history...),
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

// claudePrompt turns the first real prompt into a session title, dropping the markdown and
// image markers a pasted prompt carries.
func claudePrompt(text string) string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") {
			continue
		}
		parts = append(parts, line)
	}
	clean := strings.TrimSpace(claudeDropImages(strings.Join(parts, " ")))
	return truncateRunes(clean, 50)
}

func claudeDropImages(s string) string {
	for {
		start := strings.Index(s, "[Image")
		if start < 0 {
			return s
		}
		rest := s[start:]
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return s[:start]
		}
		s = strings.TrimLeft(s[:start]+rest[end+1:], " ")
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
