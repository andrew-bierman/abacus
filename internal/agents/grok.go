package agents

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// grokPathMissTTL keeps a cwd with no session directory from re-walking every group on
	// every tick.
	grokPathMissTTL = 2 * time.Second
	// maxGrokSidecars bounds the parsed summary/usage cache, like the transcript cursor map.
	maxGrokSidecars = 256
	// maxGrokPaths bounds the cwd-to-directory hit/miss cache in PathFor.
	maxGrokPaths = 256
	// maxGrokGroups bounds the per-cwd-group newest-session cache.
	maxGrokGroups = 256
)

// GrokStore reads Grok's session directories.
//
// A Grok session is a directory, not a single transcript: summary.json is the index entry,
// updates.jsonl is the ACP conversation stream, and usage.json carries one usage row per
// completed turn. The reader keeps the same incremental cursor for updates.jsonl as the other
// JSONL stores, and caches the small summary and usage files until either file changes.
//
// A GrokStore is not safe for concurrent use; keep it on the goroutine that owns the tab.
type GrokStore struct {
	// Root is the sessions directory, normally GrokSessionsPath(home). Transcript only needs a
	// session directory; Root is used by PathFor.
	Root string
	jsonlFiles

	paths    map[string]grokPath
	sidecars map[string]grokSidecar
	groups   map[string]grokGroupBest
}

// grokPath remembers a resolved session directory, or a recent miss, for one cwd.
type grokPath struct {
	since   time.Time
	dir     string
	checked time.Time
}

// grokGroupBest caches the newest session for one cwd group, invalidated by the group's mtime.
type grokGroupBest struct {
	dir      string
	at       time.Time
	groupMod time.Time
	checked  time.Time
}

// grokStamp identifies a sidecar version. Size covers the common case where a growing usage
// file was rewritten within the filesystem's mtime granularity.
type grokStamp struct {
	size int64
	mod  time.Time
}

// grokSidecar is the parsed pair of small files beside updates.jsonl.
type grokSidecar struct {
	summary      grokSummary
	summaryOK    bool
	summaryStamp grokStamp

	usage      grokUsageFile
	usageOK    bool
	usageStamp grokStamp

	// usage totals are computed once per usage file version; rebuilding the bounded history on
	// every tick would allocate for a file that did not move.
	usageApplied bool
	tokens       Tokens
	history      []int64
	context      int64
}

// GrokSessionsPath is the default Grok sessions directory for a home. GROK_HOME relocates the
// whole tree, so it is honoured before the home-relative default.
func GrokSessionsPath(home string) string {
	return GrokSessionsPathFrom(home, os.Getenv)
}

// GrokSessionsPathFrom is GrokSessionsPath with the environment supplied by the caller, so the
// sessions join can keep its environment edge injectable.
func GrokSessionsPathFrom(home string, getenv func(string) string) string {
	if home == "" {
		return ""
	}
	if getenv != nil {
		if grokHome := strings.TrimSpace(getenv("GROK_HOME")); grokHome != "" {
			return filepath.Join(grokHome, "sessions")
		}
	}
	return filepath.Join(home, ".grok", "sessions")
}

var errGrokPathEmpty = errors.New("agents: grok session path is empty")

// Transcript returns the accumulated facts for the session directory at dir. A session with
// no updates.jsonl is still readable: Grok writes summary.json at creation, and an empty
// conversation is a session with no turns.
func (s *GrokStore) Transcript(dir string) (StoredSession, error) {
	if dir == "" {
		return StoredSession{}, errGrokPathEmpty
	}
	side := s.sidecar(dir)
	updates := filepath.Join(dir, "updates.jsonl")
	st, err := s.load(updates, errGrokPathEmpty, func() jsonlAccumulator { return &grokFile{} })
	hasUpdates := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return StoredSession{}, err
	}
	if !hasUpdates && !side.summaryOK {
		return StoredSession{}, err
	}
	if side.summaryOK {
		applyGrokSummary(&st, side.summary)
	}
	if st.ID == "" {
		st.ID = filepath.Base(dir)
	}
	if st.State == "" {
		st.State = StateWaiting
	}
	if side.usageApplied {
		st.Tokens = side.tokens
		st.TokenHistory = slices.Clone(side.history)
		st.Context.Used = side.context
	}
	return st, nil
}

// sidecar returns the parsed summary and usage files for one session directory. Each file is
// decoded again only when its size or mtime changes, so a monitor tick and a one-shot drain
// pass reuse the same small-file parse.
func (s *GrokStore) sidecar(dir string) grokSidecar {
	if s.sidecars == nil {
		s.sidecars = make(map[string]grokSidecar)
	}
	side := s.sidecars[dir]
	changed := false

	summaryPath := filepath.Join(dir, "summary.json")
	if stamp, ok := grokFileStamp(summaryPath); ok {
		if !side.summaryOK || side.summaryStamp != stamp {
			side.summary, side.summaryOK = readGrokSummary(summaryPath)
			side.summaryStamp = stamp
			changed = true
		}
	} else if side.summaryOK || side.summaryStamp != (grokStamp{}) {
		side.summary, side.summaryOK, side.summaryStamp = grokSummary{}, false, grokStamp{}
		changed = true
	}

	usagePath := filepath.Join(dir, "usage.json")
	if stamp, ok := grokFileStamp(usagePath); ok {
		if !side.usageOK || side.usageStamp != stamp {
			side.usage, side.usageOK = readGrokUsage(usagePath)
			side.usageStamp = stamp
			side.usageApplied = false
			if side.usageOK {
				side.tokens, side.history, side.context, side.usageApplied = grokUsageTotals(side.usage)
			}
			changed = true
		}
	} else if side.usageOK || side.usageStamp != (grokStamp{}) {
		side.usage, side.usageOK, side.usageStamp = grokUsageFile{}, false, grokStamp{}
		side.tokens, side.history, side.context, side.usageApplied = Tokens{}, nil, 0, false
		changed = true
	}

	if changed {
		s.sidecars[dir] = side
		s.trimSidecars(dir)
	}
	return side
}

func grokFileStamp(path string) (grokStamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return grokStamp{}, false
	}
	return grokStamp{size: info.Size(), mod: info.ModTime()}, true
}

func (s *GrokStore) trimSidecars(keep string) {
	if len(s.sidecars) <= maxGrokSidecars {
		return
	}
	var oldest string
	var oldestMod time.Time
	for dir, side := range s.sidecars {
		if dir == keep {
			continue
		}
		mod := side.summaryStamp.mod
		if side.usageStamp.mod.After(mod) {
			mod = side.usageStamp.mod
		}
		if oldest == "" || mod.Before(oldestMod) {
			oldest, oldestMod = dir, mod
		}
	}
	if oldest != "" {
		delete(s.sidecars, oldest)
	}
}

func (s *GrokStore) trimPaths(keep string) {
	if len(s.paths) <= maxGrokPaths {
		return
	}
	var oldest string
	var oldestChecked time.Time
	for cwd, p := range s.paths {
		if cwd == keep {
			continue
		}
		if oldest == "" || p.checked.Before(oldestChecked) {
			oldest, oldestChecked = cwd, p.checked
		}
	}
	if oldest != "" {
		delete(s.paths, oldest)
	}
}

// PathFor returns the session directory a live process working in cwd is writing.
//
// Grok groups sessions under the URL-encoded cwd, and a path too long for one directory uses a
// slug plus a `.cwd` file instead. Both layouts are tried, and a zero since accepts any session.
// Two live Grok sessions in one directory cannot be told apart here; that attribution belongs
// to the core wiring.
func (s *GrokStore) PathFor(cwd string, since time.Time) (string, bool) {
	if s.Root == "" || cwd == "" {
		return "", false
	}
	if cached, ok := s.paths[cwd]; ok && cached.since.Equal(since) {
		if cached.dir == "" {
			if time.Since(cached.checked) < grokPathMissTTL {
				return "", false
			}
		} else if _, err := os.Stat(cached.dir); err == nil {
			// A new session in the same directory must still win, so verify the cached hit
			// remains the group's newest before reusing it.
			if latest, ok := s.newestGrokSession(filepath.Dir(cached.dir), cwd, since); ok && latest == cached.dir {
				return cached.dir, true
			}
		}
	}
	dir, ok := s.resolvePath(cwd, since)
	if s.paths == nil {
		s.paths = make(map[string]grokPath)
	}
	s.paths[cwd] = grokPath{since: since, dir: dir, checked: time.Now()}
	s.trimPaths(cwd)
	return dir, ok
}

func (s *GrokStore) resolvePath(cwd string, since time.Time) (string, bool) {
	encoded := url.PathEscape(cwd)
	if dir, ok := s.newestGrokSession(filepath.Join(s.Root, encoded), cwd, since); ok {
		return dir, true
	}

	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || e.Name() == encoded {
			continue
		}
		group := filepath.Join(s.Root, e.Name())
		decoded, err := url.PathUnescape(e.Name())
		if err == nil && filepath.Clean(decoded) == filepath.Clean(cwd) {
			if dir, ok := s.newestGrokSession(group, cwd, since); ok {
				return dir, true
			}
			continue
		}
		// A path too long for one directory is named by a slug plus a `.cwd` file instead.
		groupCWD, ok := grokGroupCWD(group)
		if !ok || filepath.Clean(groupCWD) != filepath.Clean(cwd) {
			continue
		}
		if dir, ok := s.newestGrokSession(group, cwd, since); ok {
			return dir, true
		}
	}
	return "", false
}

func grokGroupCWD(group string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(group, ".cwd"))
	if err != nil {
		return "", false
	}
	cwd := strings.TrimSpace(string(b))
	return cwd, cwd != ""
}

func (s *GrokStore) newestGrokSession(group, cwd string, since time.Time) (string, bool) {
	if s.groups == nil {
		s.groups = make(map[string]grokGroupBest)
	}
	groupMod := grokModTime(group)
	if cached, ok := s.groups[group]; ok && cached.dir != "" && cached.groupMod.Equal(groupMod) {
		if _, err := os.Stat(cached.dir); err == nil {
			if since.IsZero() || !cached.at.Before(since) {
				cached.checked = time.Now()
				s.groups[group] = cached
				return cached.dir, true
			}
		}
	}

	entries, err := os.ReadDir(group)
	if err != nil {
		return "", false
	}
	var best transcriptRef
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(group, e.Name())
		summary, summaryOK := readGrokSummary(filepath.Join(dir, "summary.json"))
		var at time.Time
		if summaryOK {
			at = summary.updated()
		}
		if at.IsZero() {
			info, err := e.Info()
			if err != nil {
				continue
			}
			at = info.ModTime()
		}
		if !since.IsZero() && at.Before(since) {
			continue
		}
		if summaryOK && summary.Info.CWD != "" && filepath.Clean(summary.Info.CWD) != filepath.Clean(cwd) {
			continue
		}
		ref := transcriptRef{path: dir, mod: at}
		if ref.newerThan(best) {
			best = ref
		}
	}
	if best.path != "" {
		s.groups[group] = grokGroupBest{dir: best.path, at: best.mod, groupMod: groupMod, checked: time.Now()}
		s.trimGroups(group)
	}
	return best.path, best.path != ""
}

func grokModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (s *GrokStore) trimGroups(keep string) {
	if len(s.groups) <= maxGrokGroups {
		return
	}
	var oldest string
	var oldestChecked time.Time
	for group, g := range s.groups {
		if group == keep {
			continue
		}
		if oldest == "" || g.checked.Before(oldestChecked) {
			oldest, oldestChecked = group, g.checked
		}
	}
	if oldest != "" {
		delete(s.groups, oldest)
	}
}

type grokSummary struct {
	Info struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"info"`
	SessionSummary  string `json:"session_summary"`
	GeneratedTitle  string `json:"generated_title"`
	CurrentModelID  string `json:"current_model_id"`
	UpdatedAt       string `json:"updated_at"`
	LastActiveAt    string `json:"last_active_at"`
	ParentSessionID string `json:"parent_session_id"`
}

func (s grokSummary) updated() time.Time {
	if t, ok := jsonlTime(s.LastActiveAt); ok && !t.IsZero() {
		return t
	}
	t, _ := jsonlTime(s.UpdatedAt)
	return t
}

func applyGrokSummary(st *StoredSession, s grokSummary) {
	if s.Info.ID != "" {
		st.ID = s.Info.ID
	}
	if s.Info.CWD != "" {
		st.Dir = s.Info.CWD
	}
	title := s.GeneratedTitle
	if strings.TrimSpace(title) == "" {
		title = s.SessionSummary
	}
	if t := strings.TrimSpace(title); t != "" {
		st.Title = t
	}
	if st.Model == "" && s.CurrentModelID != "" {
		st.Model = s.CurrentModelID
	}
	if s.ParentSessionID != "" {
		st.ParentID = s.ParentSessionID
	}
	if t := s.updated(); !t.IsZero() {
		st.Updated = t
	}
}

// grokFile is the accumulation for one updates.jsonl stream. The file is an ACP session/update
// stream, so a chunk opens or closes a turn and the usage file, when present, carries the
// authoritative token totals.
type grokFile struct {
	jsonlCursor
	jsonlSession

	openTurn     bool
	pendingCalls map[string]struct{}
}

func (f *grokFile) add(line []byte) {
	var e grokLine
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	if ms, err := e.Params.Meta.AgentTimestampMs.Int64(); err == nil && ms > 0 {
		f.updated = time.UnixMilli(ms)
	} else if ts, ok := jsonlTime(e.Timestamp); ok && !ts.IsZero() {
		f.updated = ts
	}

	u := e.Params.Update
	if u.Meta.ModelID != "" {
		f.setModel(u.Meta.ModelID)
	}
	switch u.SessionUpdate {
	case "user_message_chunk":
		f.openTurn = true
		if text := grokText(u.Content); strings.TrimSpace(text) != "" {
			f.setPrompt(text)
		}
	case "agent_message_chunk":
		f.openTurn = false
		f.pendingCalls = nil
	case "agent_thought_chunk":
		f.openTurn = true
	case "tool_call":
		if f.pendingCalls == nil {
			f.pendingCalls = make(map[string]struct{})
		}
		f.pendingCalls[u.ToolCallID] = struct{}{}
	case "tool_call_update":
		switch u.Status {
		case "completed", "failed":
			delete(f.pendingCalls, u.ToolCallID)
			f.openTurn = true
		}
	case "turn_completed":
		f.openTurn = false
		f.pendingCalls = nil
		if u.Usage != nil {
			f.addTurnUsage(*u.Usage)
		}
	case "usage_update":
		if u.Usage != nil {
			f.addTurnUsage(*u.Usage)
		}
	case "session_info_update":
		f.setTitle(u.Title)
	case "model_changed", "model_auto_switched":
		if u.NewModelID != "" {
			f.setModel(u.NewModelID)
		}
	case "served_model":
		if u.ModelID != "" {
			f.setModel(u.ModelID)
		}
	}
	if e.Params.Meta.TotalTokens > 0 {
		f.useContext(e.Params.Meta.TotalTokens)
	}
}

func (f *grokFile) addTurnUsage(u grokUsageSummary) {
	t := grokTokens(u)
	if t == (Tokens{}) {
		return
	}
	f.addTokens(t)
	f.addHistory(grokUsagePoint(u, t))
	f.useContext(t.prompt())
}

func (f *grokFile) session() StoredSession {
	state := StateWaiting
	switch {
	case len(f.pendingCalls) > 0:
		state = StateExecuting
	case f.openTurn:
		state = StateThinking
	}
	return f.jsonlSession.session(state, f.modTime())
}

func grokText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return jsonlValueText(v)
}

type grokLine struct {
	Timestamp json.Number `json:"timestamp"`
	Params    struct {
		Update grokUpdate `json:"update"`
		Meta   struct {
			AgentTimestampMs json.Number `json:"agentTimestampMs"`
			TotalTokens      int64       `json:"totalTokens"`
		} `json:"_meta"`
	} `json:"params"`
}

type grokUpdate struct {
	SessionUpdate string            `json:"sessionUpdate"`
	Content       json.RawMessage   `json:"content"`
	Status        string            `json:"status"`
	Title         string            `json:"title"`
	Usage         *grokUsageSummary `json:"usage"`
	ToolCallID    string            `json:"toolCallId"`
	NewModelID    string            `json:"new_model_id"`
	ModelID       string            `json:"modelId"`
	Meta          struct {
		ModelID string `json:"modelId"`
	} `json:"_meta"`
}

type grokUsageFile struct {
	Turns []grokUsageSummary `json:"turns"`
}

type grokUsageSummary struct {
	InputTokens         int64 `json:"inputTokens"`
	OutputTokens        int64 `json:"outputTokens"`
	CachedReadTokens    int64 `json:"cachedReadTokens"`
	CacheCreationTokens int64 `json:"cacheCreationTokens"`
	ReasoningTokens     int64 `json:"reasoningTokens"`
	TotalTokens         int64 `json:"totalTokens"`
}

func readGrokJSON[T any](path string) (T, bool) {
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		return v, false
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, false
	}
	return v, true
}

func readGrokSummary(path string) (grokSummary, bool) {
	return readGrokJSON[grokSummary](path)
}

func readGrokUsage(path string) (grokUsageFile, bool) {
	return readGrokJSON[grokUsageFile](path)
}

func grokUsageTotals(u grokUsageFile) (Tokens, []int64, int64, bool) {
	var (
		tokens  Tokens
		history []int64
		context int64
	)
	for _, turn := range u.Turns {
		t := grokTokens(turn)
		if t == (Tokens{}) {
			continue
		}
		tokens = tokens.add(t)
		history = appendHistoryPoint(history, grokUsagePoint(turn, t))
		context = t.prompt()
	}
	return tokens, history, context, len(history) > 0
}

func grokUsagePoint(u grokUsageSummary, t Tokens) int64 {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return t.sum()
}

func grokTokens(u grokUsageSummary) Tokens {
	input := u.InputTokens
	cacheApart := u.TotalTokens > 0 &&
		(u.TotalTokens == u.InputTokens+u.CachedReadTokens+u.CacheCreationTokens+u.OutputTokens ||
			u.TotalTokens == u.InputTokens+u.CachedReadTokens+u.CacheCreationTokens+u.OutputTokens+u.ReasoningTokens) &&
		u.CachedReadTokens+u.CacheCreationTokens > 0
	if !cacheApart {
		input = max(0, input-(u.CachedReadTokens+u.CacheCreationTokens))
	}
	return Tokens{
		Input:       input,
		Output:      u.OutputTokens,
		CacheRead:   u.CachedReadTokens,
		CacheCreate: u.CacheCreationTokens,
	}
}
