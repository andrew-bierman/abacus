package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// geminiPathMissTTL keeps a cwd with no project directory from re-walking the whole temp tree
// on every tick.
const geminiPathMissTTL = 2 * time.Second

// maxGeminiPaths bounds the cwd-to-session cache, like the other per-cwd stores.
const maxGeminiPaths = 256

// GeminiSessionsPath is the directory Gemini CLI groups project chats under. GEMINI_CLI_HOME
// relocates the whole tree, so it is honoured before the home-relative default.
func GeminiSessionsPath(home string) string {
	return GeminiSessionsPathFrom(home, os.Getenv)
}

// GeminiSessionsPathFrom is GeminiSessionsPath with the environment supplied by the caller, so
// the sessions join can keep its environment edge injectable.
func GeminiSessionsPathFrom(home string, getenv func(string) string) string {
	if home == "" {
		return ""
	}
	if getenv != nil {
		if geminiHome := strings.TrimSpace(getenv("GEMINI_CLI_HOME")); geminiHome != "" {
			home = geminiHome
		}
	}
	return filepath.Join(home, ".gemini", "tmp")
}

// GeminiStore reads Gemini CLI's session JSONL files.
//
// A session is <tmp>/<project>/chats/session-<timestamp>-<shortId>.jsonl. The first line is a
// metadata header; later lines are messages (type user, gemini, info, ...), and "$set" records
// update the header after it was written. The project directory is a short slug from
// <gemini>/projects.json, with older stores keyed by the project root's SHA-256. Subagent
// sessions nest under chats/<parentSessionId>/<sessionId>.jsonl.
//
// The records are read by the generic JSONL adapter; this type adds the layout and the path
// join a live process needs. The layout above is from Gemini CLI's ChatRecordingService
// (packages/core/src/services/chatRecordingService.ts), storage.ts and paths.ts.
//
// A GeminiStore is not safe for concurrent use; keep it on the goroutine that owns the tab.
type GeminiStore struct {
	// Root is the tmp directory, normally GeminiSessionsPath(home). Transcript only needs a
	// path; Root is used by PathFor.
	Root string

	JSONLStore
	paths map[string]geminiPath

	// markers is the ownership-marker index, rebuilt when the temp tree's mtime changes so a
	// cwd miss does not re-read every project directory on every tick.
	markers   map[string]string
	markerMod time.Time
}

// geminiPath remembers a resolved transcript, or a recent miss, for one cwd.
type geminiPath struct {
	since   time.Time
	path    string
	checked time.Time
}

// geminiMapping is Gemini CLI's record grammar: the ChatRecordingService writes a message
// record per line and rewrites one when its token counts or tool calls arrive. Usage is
// per-message (additive), promptTokenCount already includes cached content, and the summary
// arrives as a "$set" metadata update.
func geminiMapping() JSONLMapping {
	return JSONLMapping{
		Timestamp: "timestamp",
		Role:      "type",
		Roles: map[string]JSONLRole{
			"user":   JSONLRoleUser,
			"gemini": JSONLRoleAssistant,
		},
		Text:               "content",
		Model:              "model",
		ID:                 "sessionId",
		Title:              "$set.summary",
		Input:              "tokens.input",
		Output:             "tokens.output",
		CacheRead:          "tokens.cached",
		InputIncludesCache: true,
		Usage:              JSONLUsageAdditive,
		UsageID:            "id",
	}
}

// Transcript returns the accumulated facts for the session at path. The record grammar is the
// generic adapter's; the project root and, for a subagent file, the parent session id come from
// the layout around the file.
func (s *GeminiStore) Transcript(path string) (StoredSession, error) {
	if s.Mapping.Role == "" {
		s.Mapping = geminiMapping()
	}
	st, err := s.JSONLStore.Transcript(path)
	if err != nil {
		return StoredSession{}, err
	}
	geminiLayout(&st, path)
	return st, nil
}

// geminiLayout fills the facts a session record cannot carry: its working directory (from the
// project directory's ownership marker) and its parent (the directory a subagent file sits in).
func geminiLayout(st *StoredSession, path string) {
	chats := filepath.Dir(path)
	if filepath.Base(chats) != "chats" {
		// A subagent file lives one level deeper, in a directory named for its parent session.
		if st.ParentID == "" {
			st.ParentID = filepath.Base(chats)
		}
		chats = filepath.Dir(chats)
	}
	if st.Dir == "" {
		if root, ok := geminiProjectRoot(filepath.Dir(chats)); ok {
			st.Dir = root
		}
	}
}

// PathFor returns the session a live process working in cwd is writing. The newest session in
// the project's chats directory wins; since should be the process's start time so an older
// session in the same project is not adopted.
func (s *GeminiStore) PathFor(cwd string, since time.Time) (string, bool) {
	if s.Root == "" || cwd == "" {
		return "", false
	}
	if cached, ok := s.paths[cwd]; ok && cached.since.Equal(since) {
		if cached.path == "" {
			if time.Since(cached.checked) < geminiPathMissTTL {
				return "", false
			}
		} else {
			// A new session can start in the same project, so a hit is good only while it is
			// still the newest session there.
			if latest, ok := newestGeminiSession(filepath.Dir(cached.path), since); ok && latest == cached.path {
				return cached.path, true
			}
		}
	}
	path, ok := s.resolvePath(cwd, since)
	if s.paths == nil {
		s.paths = make(map[string]geminiPath)
	}
	s.paths[cwd] = geminiPath{since: since, path: path, checked: time.Now()}
	s.trimPaths(cwd)
	return path, ok
}

func (s *GeminiStore) trimPaths(keep string) {
	if len(s.paths) <= maxGeminiPaths {
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

func (s *GeminiStore) resolvePath(cwd string, since time.Time) (string, bool) {
	// Candidate project directories, in precedence order: the registered slug, the legacy
	// SHA-256 directory, then any ownership marker. Sorting the matching slugs keeps the pick
	// deterministic if a stale registry repeats one cwd.
	var ids []string
	for root, id := range geminiRegistry(s.Root) {
		if sameGeminiDir(root, cwd) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	projects := make([]string, 0, len(ids)+2)
	for _, id := range slices.Compact(ids) {
		projects = append(projects, filepath.Join(s.Root, id))
	}
	sum := sha256.Sum256([]byte(cwd))
	projects = append(projects, filepath.Join(s.Root, hex.EncodeToString(sum[:])))
	if project, ok := s.markerProject(cwd); ok {
		projects = append(projects, project)
	}

	for _, project := range projects {
		if path, ok := newestGeminiSession(filepath.Join(project, "chats"), since); ok {
			return path, true
		}
	}
	return "", false
}

// markerProject resolves a cwd from the ownership markers under Root. The index is cached until
// the root directory changes, which is when a project directory is added or removed.
func (s *GeminiStore) markerProject(cwd string) (string, bool) {
	mod := geminiTreeMod(s.Root)
	if s.markers == nil || !s.markerMod.Equal(mod) {
		markers := make(map[string]string)
		if entries, err := os.ReadDir(s.Root); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				project := filepath.Join(s.Root, e.Name())
				if root, ok := geminiProjectRoot(project); ok {
					markers[filepath.Clean(root)] = project
				}
			}
		}
		s.markers, s.markerMod = markers, mod
	}
	project, ok := s.markers[filepath.Clean(cwd)]
	return project, ok
}

func geminiTreeMod(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// geminiRegistry reads the project path to slug map beside the temp dir. A missing or malformed
// registry is not an error: the legacy hash and the ownership markers still find the project.
func geminiRegistry(root string) map[string]string {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(root), "projects.json"))
	if err != nil {
		return nil
	}
	var data struct {
		Projects map[string]string `json:"projects"`
	}
	if json.Unmarshal(b, &data) != nil {
		return nil
	}
	return data.Projects
}

func geminiProjectRoot(project string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(project, ".project_root"))
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(b))
	return root, root != ""
}

func sameGeminiDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// newestGeminiSession finds the newest main session file in one chats directory. A subagent
// file lives in a subdirectory and is not the session the live process is running.
func newestGeminiSession(chats string, since time.Time) (string, bool) {
	ref, ok := newestTranscriptMatching(chats, since, func(name string) bool {
		return strings.HasPrefix(name, "session-") && strings.HasSuffix(name, ".jsonl")
	})
	return ref.path, ok
}
