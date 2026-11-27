package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// copilotPathMissTTL keeps a cwd with no session from re-walking every session directory
	// on every tick.
	copilotPathMissTTL = 2 * time.Second
	// maxCopilotPaths bounds the cwd-to-transcript hit/miss cache.
	maxCopilotPaths = 256
	// maxCopilotCWDs bounds the transcript-to-project cache, like the other layout caches.
	maxCopilotCWDs = 1024
	// maxCopilotSidecars bounds the parsed workspace.yaml cache.
	maxCopilotSidecars = 256
)

// CopilotSessionsPath is the directory Copilot CLI groups session histories under.
// COPILOT_HOME relocates the whole configuration tree, so it is honoured before the
// home-relative default.
func CopilotSessionsPath(home string) string {
	return CopilotSessionsPathFrom(home, os.Getenv)
}

// CopilotSessionsPathFrom is CopilotSessionsPath with the environment supplied by the caller,
// so the sessions join can keep its environment edge injectable.
func CopilotSessionsPathFrom(home string, getenv func(string) string) string {
	if home == "" {
		return ""
	}
	if getenv != nil {
		if copilotHome := strings.TrimSpace(getenv("COPILOT_HOME")); copilotHome != "" {
			return filepath.Join(copilotHome, "session-state")
		}
	}
	return filepath.Join(home, ".copilot", "session-state")
}

// CopilotStore reads GitHub Copilot CLI's session event logs.
//
// A session is a directory under <session-state> holding events.jsonl and workspace.yaml;
// installs older than Copilot CLI 1.0.11 kept one flat <id>.jsonl file in the same directory.
// The event log is the transcript, while workspace.yaml carries the project directory and the
// optional display name. Each line is {type, data, timestamp, parentId}: messages are
// user.message/assistant.message, tools are tool.execution_start/tool.execution_complete, and
// token totals arrive on assistant.usage (per call) or session.shutdown (per-model cumulative).
//
// The record shapes are Copilot CLI's own event stream; the CLI publishes no formal schema for
// events.jsonl, so this reader treats every field as optional. It follows the observed shapes
// documented by the Copilot SDK's streaming-events reference and by token-accounting readers
// (session.start, user.message, assistant.message, assistant.usage, session.shutdown,
// tool.execution_*).
//
// A CopilotStore is not safe for concurrent use; keep it on the goroutine that owns the tab.
type CopilotStore struct {
	// Root is the session-state directory, normally CopilotSessionsPath(home). Transcript only
	// needs a transcript path; Root is used by PathFor.
	Root string
	jsonlFiles

	paths      map[string]copilotPath
	cwds       map[string]copilotCWD
	workspaces map[string]copilotSidecar
}

// copilotPath remembers a resolved transcript, or a recent miss, for one cwd.
type copilotPath struct {
	sinceUsed    time.Time
	path         string
	rootModAtHit time.Time
	checkedAt    time.Time
}

// copilotCWD caches a transcript's project. A hit is kept forever because a session's directory
// does not change; a miss expires so a session still being written is picked up next tick.
type copilotCWD struct {
	cwd     string
	checked time.Time
}

// copilotSidecar is the parsed workspace.yaml and the file stamp it was read at. The stamp makes
// an unchanged sidecar one stat per tick rather than a read.
type copilotSidecar struct {
	workspace copilotWorkspace
	ok        bool
	exists    bool
	size      int64
	mod       time.Time
	checked   time.Time
}

// copilotWorkspace is the part of workspace.yaml a session read uses. The file is machine
// written with scalar values only, so it is read as key/value lines rather than parsed as YAML.
type copilotWorkspace struct {
	cwd     string
	name    string
	updated time.Time
}

// copilotEvent is the common envelope of every events.jsonl record. Data is a superset of the
// payloads the session reader handles; an event type that omits a field simply leaves it zero.
type copilotEvent struct {
	Type      string      `json:"type"`
	Timestamp string      `json:"timestamp"`
	Data      copilotData `json:"data"`
}

type copilotData struct {
	copilotUsage
	SessionID     string `json:"sessionId"`
	Content       string `json:"content"`
	Summary       string `json:"summary"`
	Model         string `json:"model"`
	NewModel      string `json:"newModel"`
	SelectedModel string `json:"selectedModel"`
	CurrentModel  string `json:"currentModel"`
	ToolCallID    string `json:"toolCallId"`

	CurrentTokens int64 `json:"currentTokens"`
	TokenLimit    int64 `json:"tokenLimit"`

	Context struct {
		CWD string `json:"cwd"`
	} `json:"context"`

	ModelMetrics map[string]copilotModelMetrics `json:"modelMetrics"`
}

// copilotModelMetrics is one model's envelope inside session.shutdown.data.modelMetrics.
type copilotModelMetrics struct {
	Usage copilotUsage `json:"usage"`
}

// copilotUsage is the token breakdown Copilot reports for one call or one shutdown total.
// inputTokens includes cache reads and writes, so the adapter subtracts them once.
type copilotUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

var errCopilotPathEmpty = errors.New("agents: copilot transcript path is empty")

// Transcript returns the accumulated facts for the event log at path. The record grammar is
// Copilot's; workspace.yaml supplies the project directory and any display name.
func (s *CopilotStore) Transcript(path string) (StoredSession, error) {
	st, err := s.load(path, errCopilotPathEmpty, func() jsonlAccumulator { return &copilotFile{} })
	if err != nil {
		return StoredSession{}, err
	}
	if ws, ok := s.workspace(filepath.Dir(path)); ok {
		if st.Dir == "" && ws.cwd != "" {
			st.Dir = ws.cwd
		}
		if ws.name != "" {
			st.Title = ws.name
		}
		if st.Updated.IsZero() && !ws.updated.IsZero() {
			st.Updated = ws.updated
		}
	}
	if st.ID == "" {
		st.ID = copilotSessionID(path)
	}
	if st.Dir == "" {
		st.Dir = s.transcriptCWD(path)
	}
	return st, nil
}

// copilotSessionID names a session whose start record did not carry one.
func copilotSessionID(path string) string {
	base := filepath.Base(path)
	if base == "events.jsonl" {
		return filepath.Base(filepath.Dir(path))
	}
	return strings.TrimSuffix(base, ".jsonl")
}

// PathFor returns the event log a live process working in cwd is writing. The newest session
// at or after since wins, so an older session in the same project is not adopted.
func (s *CopilotStore) PathFor(cwd string, since time.Time) (string, bool) {
	if s.Root == "" || cwd == "" {
		return "", false
	}
	rootMod := copilotModTime(s.Root)
	if cached, ok := s.paths[cwd]; ok && cached.sinceUsed.Equal(since) && cached.rootModAtHit.Equal(rootMod) {
		if cached.path == "" {
			if time.Since(cached.checkedAt) < copilotPathMissTTL {
				return "", false
			}
		} else if _, err := os.Stat(cached.path); err == nil {
			return cached.path, true
		}
	}
	path, ok := s.resolvePath(cwd, since)
	if s.paths == nil {
		s.paths = make(map[string]copilotPath)
	}
	s.paths[cwd] = copilotPath{sinceUsed: since, path: path, rootModAtHit: rootMod, checkedAt: time.Now()}
	s.trimPaths(cwd)
	return path, ok
}

func (s *CopilotStore) resolvePath(cwd string, since time.Time) (string, bool) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return "", false
	}
	var best transcriptRef
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		var (
			path string
			info os.FileInfo
		)
		if e.IsDir() {
			path = filepath.Join(s.Root, e.Name(), "events.jsonl")
			info, err = os.Stat(path)
			if err != nil {
				continue
			}
		} else {
			if !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			path = filepath.Join(s.Root, e.Name())
			info, err = e.Info()
			if err != nil {
				continue
			}
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}
		ref := transcriptRef{path: path, mod: info.ModTime()}
		if !ref.newerThan(best) {
			continue
		}
		if !sameCopilotDir(s.transcriptCWD(path), cwd) {
			continue
		}
		best = ref
	}
	return best.path, best.path != ""
}

// transcriptCWD resolves the project a transcript belongs to, caching it because a session's
// directory is fixed for its lifetime and PathFor may see the same transcript on every scan.
func (s *CopilotStore) transcriptCWD(path string) string {
	if ws, ok := s.workspace(filepath.Dir(path)); ok && ws.cwd != "" {
		return ws.cwd
	}
	if cached, ok := s.cwds[path]; ok {
		if cached.cwd != "" || time.Since(cached.checked) < copilotPathMissTTL {
			return cached.cwd
		}
	}
	cwd := readCopilotStartCWD(path)
	if s.cwds == nil {
		s.cwds = make(map[string]copilotCWD)
	}
	s.cwds[path] = copilotCWD{cwd: cwd, checked: time.Now()}
	s.trimCWDs(path)
	return cwd
}

// workspace returns the parsed workspace.yaml for a session directory, re-reading it only when
// the file's size or mtime changes. A missing file is cached too, so a legacy transcript does
// not pay a failing open on every tick.
func (s *CopilotStore) workspace(dir string) (copilotWorkspace, bool) {
	path := filepath.Join(dir, "workspace.yaml")
	if s.workspaces == nil {
		s.workspaces = make(map[string]copilotSidecar)
	}
	cached, hit := s.workspaces[path]
	var (
		size   int64
		mod    time.Time
		exists bool
	)
	if info, err := os.Stat(path); err == nil {
		size, mod, exists = info.Size(), info.ModTime(), true
	}
	if hit && cached.exists == exists && cached.size == size && cached.mod.Equal(mod) {
		return cached.workspace, cached.ok
	}
	side := copilotSidecar{exists: exists, size: size, mod: mod, checked: time.Now()}
	if exists {
		side.workspace, side.ok = readCopilotWorkspace(path)
	}
	s.workspaces[path] = side
	s.trimWorkspaces(path)
	return side.workspace, side.ok
}

func (s *CopilotStore) trimPaths(keep string) {
	trimCopilotOldest(s.paths, maxCopilotPaths, keep, func(p copilotPath) time.Time { return p.checkedAt })
}

func (s *CopilotStore) trimCWDs(keep string) {
	trimCopilotOldest(s.cwds, maxCopilotCWDs, keep, func(c copilotCWD) time.Time { return c.checked })
}

func (s *CopilotStore) trimWorkspaces(keep string) {
	trimCopilotOldest(s.workspaces, maxCopilotSidecars, keep, func(c copilotSidecar) time.Time { return c.checked })
}

// trimCopilotOldest drops the least recently checked entry once a cache exceeds its bound. The
// entry being kept is never the one dropped, so a live session cannot evict its own state.
func trimCopilotOldest[V any](m map[string]V, limit int, keep string, checked func(V) time.Time) {
	if len(m) <= limit {
		return
	}
	var oldest string
	var oldestChecked time.Time
	for key, value := range m {
		if key == keep {
			continue
		}
		if at := checked(value); oldest == "" || at.Before(oldestChecked) {
			oldest, oldestChecked = key, at
		}
	}
	if oldest != "" {
		delete(m, oldest)
	}
}

func copilotModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func sameCopilotDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// readCopilotWorkspace reads the scalar keys this reader needs. A missing or malformed file is
// not an error: the event stream still carries the session identity and project directory.
func readCopilotWorkspace(path string) (copilotWorkspace, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return copilotWorkspace{}, false
	}
	var ws copilotWorkspace
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), copilotYAMLScalar(strings.TrimSpace(value))
		switch key {
		case "cwd":
			ws.cwd = value
		case "name":
			ws.name = value
		case "updated_at":
			if t, ok := jsonlTime(value); ok {
				ws.updated = t
			}
		}
	}
	return ws, ws.cwd != "" || ws.name != "" || !ws.updated.IsZero()
}

func copilotYAMLScalar(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if s, err := strconv.Unquote(value); err == nil {
			return s
		}
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1]
	}
	return value
}

// readCopilotStartCWD finds the project in a transcript without workspace.yaml, as a flat
// legacy session or a session whose sidecar was removed.
func readCopilotStartCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for i := 0; i < 64; i++ {
		var e copilotEvent
		if err := dec.Decode(&e); err != nil {
			return ""
		}
		if e.Type == "session.start" && e.Data.Context.CWD != "" {
			return e.Data.Context.CWD
		}
	}
	return ""
}

// copilotFile is the accumulation for one event log. Copilot reports usage in two shapes that
// describe the same tokens: assistant.usage is per call and additive, while session.shutdown is
// a per-model cumulative rollup written when the CLI exits. They are kept apart and the more
// complete source is chosen at read time rather than summed, so a session that records both is
// not billed twice.
type copilotFile struct {
	jsonlCursor
	jsonlSession

	openTurn bool
	pending  map[string]struct{}
	anonTool int

	callTokens   Tokens
	callHistory  []int64
	sawCallUsage bool

	shutdownTokens  Tokens
	shutdownHistory []int64
	shutdownSeen    bool
	shutdownPrev    map[string]Tokens

	messageTokens   Tokens
	messageHistory  []int64
	sawMessageUsage bool
}

func (f *copilotFile) add(line []byte) {
	var e copilotEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	if ts, ok := jsonlTime(e.Timestamp); ok && !ts.IsZero() {
		f.updated = ts
	}
	f.identity(e)
	switch e.Type {
	case "user.message":
		f.openTurn = true
		f.clearTools()
		f.setPrompt(e.Data.Content)
	case "assistant.turn_start":
		f.openTurn = true
	case "assistant.message":
		f.openTurn = false
		if e.Data.OutputTokens > 0 {
			f.addMessageOutput(e.Data.OutputTokens)
		}
	case "assistant.usage":
		f.addCallUsage(e.Data)
	case "tool.execution_start":
		if f.pending == nil {
			f.pending = make(map[string]struct{})
		}
		if e.Data.ToolCallID == "" {
			f.anonTool++
		} else {
			f.pending[e.Data.ToolCallID] = struct{}{}
		}
		f.openTurn = false
	case "tool.execution_complete":
		if e.Data.ToolCallID == "" {
			if f.anonTool > 0 {
				f.anonTool--
			}
		} else {
			delete(f.pending, e.Data.ToolCallID)
		}
		f.openTurn = true
	case "assistant.turn_end":
		f.openTurn = false
		f.clearTools()
	case "session.task_complete":
		f.openTurn = false
		f.clearTools()
		f.setTitle(e.Data.Summary)
	case "session.shutdown":
		f.openTurn = false
		f.clearTools()
		f.addShutdown(e.Data)
	case "session.usage_info":
		if e.Data.TokenLimit > 0 {
			f.setContextLimit(e.Data.TokenLimit)
		}
		if e.Data.CurrentTokens > 0 {
			f.useContext(e.Data.CurrentTokens)
		}
	}
}

// clearTools drops any tool call the store never reported complete, so an interrupted turn
// cannot leave the session executing forever. A turn boundary is the point where nothing from
// the previous turn can still be running.
func (f *copilotFile) clearTools() {
	f.pending = nil
	f.anonTool = 0
}

// identity folds the session facts any record may carry. session.start names the session and
// its project; a model can also arrive on a turn, a message or a model change.
func (f *copilotFile) identity(e copilotEvent) {
	f.setIDIfEmpty(e.Data.SessionID)
	if e.Data.Context.CWD != "" {
		f.setDirIfEmpty(e.Data.Context.CWD)
	}
	switch {
	case e.Data.SelectedModel != "":
		f.setModel(e.Data.SelectedModel)
	case e.Data.NewModel != "":
		f.setModel(e.Data.NewModel)
	case e.Data.Model != "":
		f.setModel(e.Data.Model)
	case e.Data.CurrentModel != "":
		f.setModel(e.Data.CurrentModel)
	}
}

func (f *copilotFile) addCallUsage(d copilotData) {
	raw := copilotRawTokens(d.copilotUsage)
	net := copilotNetTokens(raw)
	if net == (Tokens{}) {
		return
	}
	f.callTokens = f.callTokens.add(net)
	f.callHistory = appendHistoryPoint(f.callHistory, net.sum())
	if d.InputTokens > 0 {
		f.useContext(d.InputTokens)
	}
	f.sawCallUsage = true
}

func (f *copilotFile) addMessageOutput(output int64) {
	t := Tokens{Output: output}
	f.messageTokens = f.messageTokens.add(t)
	f.messageHistory = appendHistoryPoint(f.messageHistory, t.sum())
	f.sawMessageUsage = true
}

// addShutdown folds one shutdown's per-model totals. A resumed session appends another
// shutdown with cumulative totals, so the contribution is the delta since the same model's
// previous snapshot.
func (f *copilotFile) addShutdown(d copilotData) {
	if f.shutdownPrev == nil {
		f.shutdownPrev = make(map[string]Tokens)
	}
	if d.CurrentTokens > 0 {
		f.useContext(d.CurrentTokens)
	}
	var delta Tokens
	seen := false
	for model, metrics := range d.ModelMetrics {
		raw := copilotRawTokens(metrics.Usage)
		if raw == (Tokens{}) {
			continue
		}
		net := copilotNetTokens(raw)
		seen = true
		previous, ok := f.shutdownPrev[model]
		step := net
		if ok && !copilotTokensDecreased(net, previous) {
			step = net.sub(previous)
		}
		f.shutdownPrev[model] = net
		delta = delta.add(step)
	}
	if !seen || delta == (Tokens{}) {
		return
	}
	f.shutdownTokens = f.shutdownTokens.add(delta)
	f.shutdownHistory = appendHistoryPoint(f.shutdownHistory, delta.sum())
	f.shutdownSeen = true
}

func (f *copilotFile) session() StoredSession {
	state := StateWaiting
	switch {
	case len(f.pending) > 0 || f.anonTool > 0:
		state = StateExecuting
	case f.openTurn:
		state = StateThinking
	}
	st := f.jsonlSession.session(state, f.modTime())
	// A usage-info record can carry a limit without a current fill, so the limit is copied even
	// when the shared assembler saw no fill.
	st.Context.Limit = f.context.Limit
	switch {
	case f.shutdownSeen && f.sawCallUsage && f.callTokens.sum() > f.shutdownTokens.sum():
		// A resumed session can append per-call rows after its previous shutdown rollup.
		// Both describe the same lifetime, so the larger one is the more complete read.
		st.Tokens = f.callTokens
		st.TokenHistory = slices.Clone(f.callHistory)
	case f.shutdownSeen:
		st.Tokens = f.shutdownTokens
		st.TokenHistory = slices.Clone(f.shutdownHistory)
	case f.sawCallUsage:
		st.Tokens = f.callTokens
		st.TokenHistory = slices.Clone(f.callHistory)
	case f.sawMessageUsage:
		st.Tokens = f.messageTokens
		st.TokenHistory = slices.Clone(f.messageHistory)
	}
	return st
}

func copilotRawTokens(u copilotUsage) Tokens {
	return Tokens{
		Input:       u.InputTokens,
		Output:      u.OutputTokens,
		CacheRead:   u.CacheReadTokens,
		CacheCreate: u.CacheWriteTokens,
	}
}

func copilotNetTokens(t Tokens) Tokens {
	t.Input = max(0, t.Input-t.CacheRead-t.CacheCreate)
	return t
}

func copilotTokensDecreased(a, b Tokens) bool {
	return a.Input < b.Input || a.Output < b.Output ||
		a.CacheRead < b.CacheRead || a.CacheCreate < b.CacheCreate
}
