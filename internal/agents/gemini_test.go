package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Gemini CLI's session file is documented by its ChatRecordingService
// (packages/core/src/services/chatRecordingService.ts and chatRecordingTypes.ts): one JSONL file
// per session under <tmp>/<project>/chats, a metadata header line, message records, and "$set"
// metadata updates. Fixtures here follow that record shape; every id, path and prompt is invented.

const geminiTestCWD = "/home/dev/atelier"

func geminiJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func geminiHeader(t *testing.T, sessionID string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(geminiTestCWD))
	return geminiJSON(t, map[string]any{
		"sessionId":   sessionID,
		"projectHash": hex.EncodeToString(sum[:]),
		"startTime":   "2026-11-24T10:00:00Z",
		"lastUpdated": "2026-11-24T10:00:00Z",
		"kind":        "main",
	})
}

func geminiUser(t *testing.T, id, timestamp, text string) string {
	t.Helper()
	return geminiJSON(t, map[string]any{
		"id":        id,
		"timestamp": timestamp,
		"type":      "user",
		"content":   text,
	})
}

func geminiResponse(t *testing.T, id, timestamp, text, model string, tokens map[string]any) string {
	t.Helper()
	record := map[string]any{
		"id":        id,
		"timestamp": timestamp,
		"type":      "gemini",
		"content":   []any{map[string]any{"text": text}},
		"model":     model,
	}
	if tokens != nil {
		record["tokens"] = tokens
	}
	return geminiJSON(t, record)
}

func geminiSummary(t *testing.T, summary string) string {
	t.Helper()
	return geminiJSON(t, map[string]any{
		"$set": map[string]any{
			"summary":     summary,
			"lastUpdated": "2026-11-24T10:00:06Z",
		},
	})
}

func geminiTokens(in, out, cached int) map[string]any {
	return map[string]any{
		"input": in, "output": out, "cached": cached,
		"thoughts": 0, "tool": 0, "total": in + out,
	}
}

func writeGeminiFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func geminiSetMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func writeGeminiProject(t *testing.T, root, id, cwd string) string {
	t.Helper()
	project := filepath.Join(root, id)
	writeGeminiFile(t, filepath.Join(project, ".project_root"), cwd+"\n")
	return project
}

func writeGeminiSession(t *testing.T, project, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(project, "chats", name)
	writeGeminiFile(t, path, strings.Join(lines, "\n")+"\n")
	return path
}

func readGemini(t *testing.T, store *GeminiStore, path string) StoredSession {
	t.Helper()
	got, err := store.Transcript(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestGeminiTranscriptReadsMessagesAndRewrites(t *testing.T) {
	root := t.TempDir()
	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl",
		geminiHeader(t, "ses_atelier"),
		geminiUser(t, "msg_linen", "2026-11-24T10:00:01Z", "tidy the atelier"),
		geminiResponse(t, "msg_kiln", "2026-11-24T10:00:02Z", "Checking the shelves.", "fixture-labs/kiln-1", nil),
		// The same response is written again as its usage and then its tool calls arrive.
		geminiResponse(t, "msg_kiln", "2026-11-24T10:00:03Z", "Checking the shelves.", "fixture-labs/kiln-1", geminiTokens(120, 30, 20)),
		geminiResponse(t, "msg_kiln", "2026-11-24T10:00:04Z", "Checking the shelves.", "fixture-labs/kiln-1", geminiTokens(120, 30, 20)),
		geminiResponse(t, "msg_loom", "2026-11-24T10:00:05Z", "Shelves tidied.", "fixture-labs/kiln-2", geminiTokens(50, 8, 10)),
		geminiSummary(t, "Atelier tidy-up"),
	)
	geminiSetMtime(t, path, time.Date(2026, 11, 24, 10, 0, 6, 0, time.UTC))

	got := readGemini(t, &GeminiStore{Root: root}, path)

	if got.ID != "ses_atelier" || got.Dir != geminiTestCWD || got.ParentID != "" {
		t.Errorf("identity = %q/%q/%q", got.ID, got.Dir, got.ParentID)
	}
	if got.Title != "Atelier tidy-up" {
		t.Errorf("title = %q, want the $set summary", got.Title)
	}
	if got.Model != "fixture-labs/kiln-2" {
		t.Errorf("model = %q, want the latest response model", got.Model)
	}
	// input includes cached, so the adapter subtracts it once.
	if want := (Tokens{Input: 140, Output: 38, CacheRead: 30}); got.Tokens != want {
		t.Errorf("tokens = %+v, want each response rewritten, not summed %+v", got.Tokens, want)
	}
	if want := []int64{150, 58}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want one point per response %v", got.TokenHistory, want)
	}
	if want := (Context{Used: 50}); got.Context != want {
		t.Errorf("context = %+v, want the latest response prompt %+v", got.Context, want)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after the response", got.State)
	}
	if want := time.Date(2026, 11, 24, 10, 0, 5, 0, time.UTC); !got.Updated.Equal(want) {
		t.Errorf("updated = %v, want the last message %v", got.Updated, want)
	}
}

func TestGeminiTranscriptFallsBackToTheFirstPrompt(t *testing.T) {
	root := t.TempDir()
	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl",
		geminiHeader(t, "ses_atelier"),
		geminiUser(t, "msg_linen", "2026-11-24T10:00:01Z", "dust the shelves"),
		geminiResponse(t, "msg_kiln", "2026-11-24T10:00:02Z", "Done.", "fixture-labs/kiln-1", geminiTokens(10, 2, 0)),
	)

	got := readGemini(t, &GeminiStore{Root: root}, path)

	if got.Title != "dust the shelves" {
		t.Errorf("title = %q, want the first prompt without a summary", got.Title)
	}
}

func TestGeminiTranscriptFoldsAppendedRecords(t *testing.T) {
	root := t.TempDir()
	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl",
		geminiHeader(t, "ses_atelier"),
		geminiUser(t, "msg_linen", "2026-11-24T10:00:01Z", "tidy the atelier"),
	)
	store := &GeminiStore{Root: root}

	first := readGemini(t, store, path)
	if first.State != StateThinking {
		t.Fatalf("state after a prompt = %q, want thinking", first.State)
	}
	if first.Tokens != (Tokens{}) {
		t.Fatalf("tokens = %+v, want none before a response", first.Tokens)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join([]string{
		geminiResponse(t, "msg_kiln", "2026-11-24T10:00:02Z", "Working.", "fixture-labs/kiln-1", geminiTokens(20, 4, 0)),
		geminiResponse(t, "msg_kiln", "2026-11-24T10:00:03Z", "Working.", "fixture-labs/kiln-1", geminiTokens(30, 6, 0)),
	}, "\n") + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	second := readGemini(t, store, path)
	if want := (Tokens{Input: 30, Output: 6}); second.Tokens != want {
		t.Fatalf("tokens = %+v, want the appended rewrite only %+v", second.Tokens, want)
	}
	if want := []int64{36}; !slices.Equal(second.TokenHistory, want) {
		t.Fatalf("history = %v, want the rewritten response once %v", second.TokenHistory, want)
	}
}

func TestGeminiTranscriptSubagentParentFromLayout(t *testing.T) {
	root := t.TempDir()
	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	path := filepath.Join(project, "chats", "ses_parent", "ses_child.jsonl")
	writeGeminiFile(t, path, strings.Join([]string{
		geminiHeader(t, "ses_child"),
		geminiUser(t, "msg_child", "2026-11-24T10:00:01Z", "count the shelves"),
	}, "\n")+"\n")

	got := readGemini(t, &GeminiStore{Root: root}, path)

	if got.ParentID != "ses_parent" {
		t.Errorf("parent = %q, want the containing session directory", got.ParentID)
	}
	if got.Dir != geminiTestCWD {
		t.Errorf("dir = %q, want the project root marker", got.Dir)
	}
}

func TestGeminiPathForUsesTheProjectRegistry(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".gemini", "tmp")
	writeGeminiFile(t, filepath.Join(home, ".gemini", "projects.json"),
		`{"projects":{"`+geminiTestCWD+`":"atelier"}}`)
	project := filepath.Join(root, "atelier")
	old := writeGeminiSession(t, project, "session-2026-11-24T09-00-old11111.jsonl", geminiHeader(t, "ses_old"))
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl", geminiHeader(t, "ses_atelier"))
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	geminiSetMtime(t, old, started.Add(-time.Hour))
	geminiSetMtime(t, path, started.Add(time.Minute))

	store := &GeminiStore{Root: root}
	if got, ok := store.PathFor(geminiTestCWD, started); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want %q, true", got, ok, path)
	}
	if _, ok := store.PathFor(geminiTestCWD, started.Add(time.Hour)); ok {
		t.Errorf("PathFor adopted a transcript written before the window")
	}
	if _, ok := store.PathFor("/home/dev/other", started); ok {
		t.Errorf("PathFor found a session for another project")
	}
}

func TestGeminiPathForFallsBackToTheLegacyHash(t *testing.T) {
	root := t.TempDir()
	sum := sha256.Sum256([]byte(geminiTestCWD))
	project := filepath.Join(root, hex.EncodeToString(sum[:]))
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl", geminiHeader(t, "ses_atelier"))
	geminiSetMtime(t, path, time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC))

	store := &GeminiStore{Root: root}
	if got, ok := store.PathFor(geminiTestCWD, time.Time{}); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want the legacy-hash session %q", got, ok, path)
	}
}

func TestGeminiPathForUsesTheOwnershipMarkerWithoutARegistry(t *testing.T) {
	root := t.TempDir()
	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl", geminiHeader(t, "ses_atelier"))
	geminiSetMtime(t, path, time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC))

	store := &GeminiStore{Root: root}
	if got, ok := store.PathFor(geminiTestCWD, time.Time{}); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want the marker-owned session %q", got, ok, path)
	}
}

func TestGeminiSessionsPathHonoursTheCLIHome(t *testing.T) {
	getenv := func(key string) string {
		if key == "GEMINI_CLI_HOME" {
			return "/home/dev/gemini-home"
		}
		return ""
	}
	if got, want := GeminiSessionsPathFrom("/home/dev", getenv), "/home/dev/gemini-home/.gemini/tmp"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got := GeminiSessionsPathFrom("", func(string) string { return "" }); got != "" {
		t.Errorf("path for an unknown home = %q, want empty", got)
	}
	if got := GeminiSessionsPathFrom("", getenv); got != "" {
		t.Errorf("path for an unknown home with GEMINI_CLI_HOME = %q, want empty", got)
	}
}

func TestGeminiPathForRechecksTheNewestSessionOnACachedHit(t *testing.T) {
	root := t.TempDir()
	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	first := writeGeminiSession(t, project, "session-2026-11-24T09-00-old11111.jsonl", geminiHeader(t, "ses_old"))
	geminiSetMtime(t, first, started.Add(time.Minute))

	store := &GeminiStore{Root: root}
	if got, ok := store.PathFor(geminiTestCWD, started); !ok || got != first {
		t.Fatalf("PathFor = %q, %v; want the first session %q", got, ok, first)
	}

	newest := writeGeminiSession(t, project, "session-2026-11-24T09-05-new22222.jsonl", geminiHeader(t, "ses_new"))
	geminiSetMtime(t, newest, started.Add(2*time.Minute))
	if got, ok := store.PathFor(geminiTestCWD, started); !ok || got != newest {
		t.Errorf("PathFor after a cached hit = %q, %v; want the newer session %q", got, ok, newest)
	}
}

func TestGeminiPathForFindsAProjectCreatedAfterACachedMiss(t *testing.T) {
	root := t.TempDir()
	store := &GeminiStore{Root: root}
	if _, ok := store.PathFor("/home/dev/other", time.Time{}); ok {
		t.Fatal("PathFor found a session with no project")
	}

	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl", geminiHeader(t, "ses_atelier"))
	geminiSetMtime(t, path, time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC))

	if got, ok := store.PathFor(geminiTestCWD, time.Time{}); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want the project created after the cached miss %q", got, ok, path)
	}
}

func TestGeminiPathForFallsBackFromAMalformedRegistry(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".gemini", "tmp")
	writeGeminiFile(t, filepath.Join(home, ".gemini", "projects.json"), "{not json")
	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl", geminiHeader(t, "ses_atelier"))
	geminiSetMtime(t, path, time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC))

	store := &GeminiStore{Root: root}
	if got, ok := store.PathFor(geminiTestCWD, time.Time{}); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want the marker-owned session %q", got, ok, path)
	}
}

func TestGeminiTranscriptIgnoresNonConversationRecords(t *testing.T) {
	root := t.TempDir()
	project := writeGeminiProject(t, root, "atelier", geminiTestCWD)
	path := writeGeminiSession(t, project, "session-2026-11-24T10-00-abcdef12.jsonl",
		geminiHeader(t, "ses_atelier"),
		geminiUser(t, "msg_linen", "2026-11-24T10:00:01Z", "tidy the atelier"),
		geminiJSON(t, map[string]any{
			"id": "msg_notice", "timestamp": "2026-11-24T10:00:02Z",
			"type": "info", "content": "working",
		}),
		geminiResponse(t, "msg_kiln", "2026-11-24T10:00:03Z", "Done.", "fixture-labs/kiln-1", geminiTokens(10, 2, 0)),
		geminiJSON(t, map[string]any{
			"id": "msg_warning", "timestamp": "2026-11-24T10:00:04Z",
			"type": "warning", "content": "heads up",
		}),
	)

	got := readGemini(t, &GeminiStore{Root: root}, path)

	if got.Title != "tidy the atelier" || got.State != StateWaiting {
		t.Errorf("title/state = %q/%q, want the prompt and a closed turn", got.Title, got.State)
	}
	if want := (Tokens{Input: 10, Output: 2}); got.Tokens != want {
		t.Errorf("tokens = %+v, want only the conversation response %+v", got.Tokens, want)
	}
}

func TestGeminiTrimPathsEvictsTheOldest(t *testing.T) {
	store := &GeminiStore{paths: make(map[string]geminiPath)}
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	for i := 0; i <= maxGeminiPaths; i++ {
		store.paths[fmt.Sprintf("/project/%d", i)] = geminiPath{checked: base.Add(time.Duration(i) * time.Second)}
	}
	store.trimPaths("/project/0")
	if len(store.paths) != maxGeminiPaths {
		t.Fatalf("paths = %d, want %d", len(store.paths), maxGeminiPaths)
	}
	if _, ok := store.paths["/project/1"]; ok {
		t.Errorf("the oldest path survived eviction")
	}
}
