package agents

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Grok's session directory layout is documented in its bundled user guide (17-sessions.md):
// summary.json is the index entry, updates.jsonl is an ACP session/update stream, and usage.json
// carries one usage row per completed turn. Fixtures here follow that documented envelope, and
// every id, path and prompt is invented.

const grokTestCWD = "/home/dev/shop"

func grokJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func grokUpdateLine(t *testing.T, seconds int64, update map[string]any) string {
	t.Helper()
	return grokJSON(t, map[string]any{
		"timestamp": seconds,
		"method":    "session/update",
		"params": map[string]any{
			"sessionId": "ses_fixture",
			"update":    update,
			"_meta": map[string]any{
				"eventId":          "ses_fixture-" + string(rune('a'+seconds)),
				"agentTimestampMs": seconds * 1000,
				"promptId":         "prompt-1",
			},
		},
	})
}

func grokChunk(t *testing.T, seconds int64, kind, text string) string {
	t.Helper()
	return grokUpdateLine(t, seconds, map[string]any{
		"sessionUpdate": kind,
		"content":       map[string]any{"type": "text", "text": text},
	})
}

func grokToolCall(t *testing.T, seconds int64, status string) string {
	t.Helper()
	return grokToolCallID(t, seconds, "call_1", status)
}

func grokToolCallID(t *testing.T, seconds int64, id, status string) string {
	t.Helper()
	return grokUpdateLine(t, seconds, map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Run the tests",
		"status":        status,
	})
}

func grokToolUpdate(t *testing.T, seconds int64, status string) string {
	t.Helper()
	return grokToolUpdateID(t, seconds, "call_1", status)
}

func grokToolUpdateID(t *testing.T, seconds int64, id, status string) string {
	t.Helper()
	return grokUpdateLine(t, seconds, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        status,
	})
}

func grokSummaryJSON(t *testing.T, id, cwd, title, model, updated string) string {
	t.Helper()
	lastActive := updated
	if at, err := time.Parse(time.RFC3339, updated); err == nil {
		lastActive = at.Add(time.Minute).Format(time.RFC3339)
	}
	return grokJSON(t, map[string]any{
		"info":                map[string]any{"id": id, "cwd": cwd},
		"generated_title":     title,
		"current_model_id":    model,
		"updated_at":          updated,
		"last_active_at":      lastActive,
		"parent_session_id":   "ses_parent",
		"num_chat_messages":   2,
		"chat_format_version": 1,
	})
}

func grokUsage(t *testing.T, turns ...map[string]any) string {
	t.Helper()
	return grokJSON(t, map[string]any{
		"sessionId": "ses_fixture",
		"updatedAt": "2026-11-24T10:05:00Z",
		"session":   map[string]any{"inputTokens": 500, "outputTokens": 80},
		"turns":     turns,
	})
}

func grokTurn(t *testing.T, number int, in, out, cacheRead, cacheWrite, total int64) map[string]any {
	t.Helper()
	return map[string]any{
		"turnNumber":          number,
		"endedAt":             "2026-11-24T10:05:00Z",
		"inputTokens":         in,
		"outputTokens":        out,
		"cachedReadTokens":    cacheRead,
		"cacheCreationTokens": cacheWrite,
		"reasoningTokens":     0,
		"totalTokens":         total,
		"modelCalls":          1,
	}
}

func writeGrokFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeGrokSession(t *testing.T, root, cwd, id string, summary string, updates []string, usage string) string {
	t.Helper()
	dir := filepath.Join(root, url.PathEscape(cwd), id)
	writeGrokFile(t, filepath.Join(dir, "summary.json"), summary)
	if len(updates) > 0 {
		writeGrokFile(t, filepath.Join(dir, "updates.jsonl"), strings.Join(updates, "\n")+"\n")
	}
	if usage != "" {
		writeGrokFile(t, filepath.Join(dir, "usage.json"), usage)
	}
	return dir
}

func readGrok(t *testing.T, store *GrokStore, dir string) StoredSession {
	t.Helper()
	got, err := store.Transcript(dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestGrokTranscriptReadsSummaryUpdatesAndUsage(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "Fixture title", "fixture-model-2", "2026-11-24T10:05:00Z"),
		[]string{
			grokChunk(t, 1, "user_message_chunk", "Trim the fixture list"),
			grokChunk(t, 2, "agent_message_chunk", "On it."),
		},
		grokUsage(t,
			grokTurn(t, 1, 300, 40, 100, 20, 340),
			grokTurn(t, 2, 200, 40, 80, 10, 330),
		))

	got := readGrok(t, &GrokStore{}, dir)

	if got.ID != "ses_fixture" || got.Dir != grokTestCWD {
		t.Errorf("id/dir = %q/%q", got.ID, got.Dir)
	}
	if got.Title != "Fixture title" || got.Model != "fixture-model-2" {
		t.Errorf("title/model = %q/%q", got.Title, got.Model)
	}
	if got.ParentID != "ses_parent" {
		t.Errorf("parent = %q", got.ParentID)
	}
	if want := (Tokens{Input: 380, Output: 80, CacheRead: 180, CacheCreate: 30}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if want := (Context{Used: 290}); got.Context != want {
		t.Errorf("context = %+v, want %+v", got.Context, want)
	}
	if want := []int64{340, 330}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after the response", got.State)
	}
	if want := time.Date(2026, 11, 24, 10, 6, 0, 0, time.UTC); !got.Updated.Equal(want) {
		t.Errorf("updated = %v, want %v", got.Updated, want)
	}
}

func TestGrokTranscriptSummaryOnly(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture", grokJSON(t, map[string]any{
		"info":              map[string]any{"id": "ses_fixture", "cwd": grokTestCWD},
		"session_summary":   "Summary fixture",
		"current_model_id":  "fixture-model-2",
		"updated_at":        "2026-11-24T10:05:00Z",
		"parent_session_id": "ses_parent",
	}), nil, "")

	got := readGrok(t, &GrokStore{}, dir)

	if got.ID != "ses_fixture" || got.Title != "Summary fixture" || got.Model != "fixture-model-2" {
		t.Errorf("session = %+v", got)
	}
	if got.Dir != grokTestCWD || got.State != StateWaiting {
		t.Errorf("dir/state = %q/%q", got.Dir, got.State)
	}
	if want := time.Date(2026, 11, 24, 10, 5, 0, 0, time.UTC); !got.Updated.Equal(want) {
		t.Errorf("updated = %v, want the updated_at fallback %v", got.Updated, want)
	}
	if got.Tokens != (Tokens{}) || got.Context != (Context{}) {
		t.Errorf("tokens/context = %+v/%+v, want empty", got.Tokens, got.Context)
	}
}

func TestGrokTranscriptStateFollowsTheUpdateStream(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "", "", "2026-11-24T10:05:00Z"),
		[]string{
			grokChunk(t, 1, "user_message_chunk", "Tidy the scripts"),
			grokChunk(t, 2, "agent_message_chunk", "Reading."),
			grokToolCall(t, 3, "in_progress"),
		}, "")

	if got := readGrok(t, &GrokStore{}, dir); got.State != StateExecuting {
		t.Errorf("state = %q, want executing while a tool call is in progress", got.State)
	}
	if got := readGrok(t, &GrokStore{}, dir); got.Title != "Tidy the scripts" {
		t.Errorf("title = %q, want the first prompt as the fallback", got.Title)
	}

	appendJSONLSpecFile(t, filepath.Join(dir, "updates.jsonl"), grokToolUpdate(t, 4, "completed"))
	if got := readGrok(t, &GrokStore{}, dir); got.State != StateThinking {
		t.Errorf("state = %q, want thinking after the tool result", got.State)
	}

	appendJSONLSpecFile(t, filepath.Join(dir, "updates.jsonl"),
		grokUpdateLine(t, 5, map[string]any{"sessionUpdate": "turn_completed", "stop_reason": "end_turn"}))
	if got := readGrok(t, &GrokStore{}, dir); got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after turn_completed", got.State)
	}

	appendJSONLSpecFile(t, filepath.Join(dir, "updates.jsonl"),
		grokChunk(t, 6, "agent_thought_chunk", "Checking the fixture."))
	if got := readGrok(t, &GrokStore{}, dir); got.State != StateThinking {
		t.Errorf("state = %q, want thinking while a thought is generating", got.State)
	}

	appendJSONLSpecFile(t, filepath.Join(dir, "updates.jsonl"),
		grokUpdateLine(t, 7, map[string]any{"sessionUpdate": "session_info_update", "title": "Renamed fixture"}))
	if got := readGrok(t, &GrokStore{}, dir); got.Title != "Renamed fixture" {
		t.Errorf("title = %q, want the session_info_update title", got.Title)
	}
}

func TestGrokTranscriptFallsBackToTurnUsageInUpdates(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "", "", "2026-11-24T10:05:00Z"),
		[]string{
			grokChunk(t, 1, "user_message_chunk", "Measure this"),
			grokUpdateLine(t, 2, map[string]any{
				"sessionUpdate": "turn_completed",
				"usage": map[string]any{
					"inputTokens": 300, "outputTokens": 40,
					"cachedReadTokens": 100, "cacheCreationTokens": 20,
					"reasoningTokens": 5, "totalTokens": 340,
				},
			}),
		}, "")

	got := readGrok(t, &GrokStore{}, dir)

	if want := (Tokens{Input: 180, Output: 40, CacheRead: 100, CacheCreate: 20}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if want := []int64{340}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
	if got.Context.Used != 300 {
		t.Errorf("context = %+v, want the turn's whole input side", got.Context)
	}
}

func TestGrokTranscriptSkipsMalformedLines(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "Still readable", "fixture-model-2", "2026-11-24T10:05:00Z"),
		[]string{
			`not json`,
			`{"method":"session/update","params":{"update":{}}}`,
			grokUpdateLine(t, 3, map[string]any{
				"sessionUpdate": "turn_completed",
				"usage": map[string]any{
					"inputTokens": 100, "outputTokens": 10, "totalTokens": 110,
				},
			}),
		}, "")

	got := readGrok(t, &GrokStore{}, dir)

	if got.ID != "ses_fixture" || got.Title != "Still readable" {
		t.Errorf("session = %+v, want the summary to survive bad updates", got)
	}
	if want := (Tokens{Input: 100, Output: 10}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the valid record after the malformed ones", got.Tokens)
	}
}

func TestGrokPathForFindsTheNewestSessionForADirectory(t *testing.T) {
	root := t.TempDir()
	writeGrokSession(t, root, grokTestCWD, "ses_old",
		grokSummaryJSON(t, "ses_old", grokTestCWD, "Old", "fixture-model-1", "2026-11-24T10:00:00Z"), nil, "")
	newer := writeGrokSession(t, root, grokTestCWD, "ses_new",
		grokSummaryJSON(t, "ses_new", grokTestCWD, "New", "fixture-model-2", "2026-11-24T10:05:00Z"), nil, "")

	store := &GrokStore{Root: root}
	since := time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)
	got, ok := store.PathFor(grokTestCWD, since)
	if !ok || got != newer {
		t.Fatalf("PathFor = %q, %v; want %q", got, ok, newer)
	}

	after := time.Date(2026, 11, 24, 10, 4, 0, 0, time.UTC)
	if got, ok := store.PathFor(grokTestCWD, after); !ok || got != newer {
		t.Fatalf("PathFor after old = %q, %v; want %q", got, ok, newer)
	}
	// The summary's last_active_at is one minute after updated_at, so a process that started at
	// updated_at still finds this session.
	atUpdated := time.Date(2026, 11, 24, 10, 5, 0, 0, time.UTC)
	if got, ok := store.PathFor(grokTestCWD, atUpdated); !ok || got != newer {
		t.Fatalf("PathFor at updated_at = %q, %v; want %q", got, ok, newer)
	}
	tooLate := time.Date(2026, 11, 24, 10, 7, 0, 0, time.UTC)
	if got, ok := store.PathFor(grokTestCWD, tooLate); ok {
		t.Fatalf("PathFor = %q, true; want no session written before the process started", got)
	}
	if got, ok := store.PathFor("/home/dev/other", since); ok {
		t.Fatalf("PathFor other cwd = %q, true; want none", got)
	}
}

func TestGrokPathForPrefersANewSessionAfterACachedHit(t *testing.T) {
	root := t.TempDir()
	first := writeGrokSession(t, root, grokTestCWD, "ses_first",
		grokSummaryJSON(t, "ses_first", grokTestCWD, "First", "fixture-model-1", "2026-11-24T10:00:00Z"), nil, "")
	store := &GrokStore{Root: root}
	since := time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)
	if got, ok := store.PathFor(grokTestCWD, since); !ok || got != first {
		t.Fatalf("PathFor = %q, %v; want %q", got, ok, first)
	}

	second := writeGrokSession(t, root, grokTestCWD, "ses_second",
		grokSummaryJSON(t, "ses_second", grokTestCWD, "Second", "fixture-model-2", "2026-11-24T10:10:00Z"), nil, "")
	if got, ok := store.PathFor(grokTestCWD, since); !ok || got != second {
		t.Fatalf("PathFor after a new session = %q, %v; want %q", got, ok, second)
	}
}

func TestGrokPathForReadsTheCwdFileForALongGroupName(t *testing.T) {
	root := t.TempDir()
	cwd := "/home/dev/a-directory-name-too-long-for-a-group"
	dir := writeGrokSession(t, root, cwd, "ses_long",
		grokSummaryJSON(t, "ses_long", cwd, "Long", "fixture-model-2", "2026-11-24T10:05:00Z"), nil, "")
	group := filepath.Dir(dir)
	if err := os.Rename(group, filepath.Join(root, "slug-and-hash")); err != nil {
		t.Fatal(err)
	}
	writeGrokFile(t, filepath.Join(root, "slug-and-hash", ".cwd"), cwd+"\n")

	got, ok := (&GrokStore{Root: root}).PathFor(cwd, time.Time{})
	if !ok || got != filepath.Join(root, "slug-and-hash", "ses_long") {
		t.Fatalf("PathFor = %q, %v; want the session under the .cwd group", got, ok)
	}
}

func TestGrokTranscriptRefreshesChangedSidecars(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "First title", "fixture-model-2", "2026-11-24T10:05:00Z"),
		nil, grokUsage(t, grokTurn(t, 1, 300, 40, 100, 20, 340)))

	store := &GrokStore{}
	if got := readGrok(t, store, dir); got.Title != "First title" || len(got.TokenHistory) != 1 {
		t.Fatalf("first read = %+v", got)
	}

	writeGrokFile(t, filepath.Join(dir, "summary.json"),
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "A longer second title", "fixture-model-2", "2026-11-24T10:06:00Z"))
	writeGrokFile(t, filepath.Join(dir, "usage.json"), grokUsage(t,
		grokTurn(t, 1, 300, 40, 100, 20, 340),
		grokTurn(t, 2, 200, 40, 80, 10, 240),
	))

	got := readGrok(t, store, dir)
	if got.Title != "A longer second title" {
		t.Errorf("title = %q, want the changed summary", got.Title)
	}
	if want := []int64{340, 240}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want the changed usage file %v", got.TokenHistory, want)
	}
}

func TestGrokTranscriptPrefersUsageFileOverStreamUsage(t *testing.T) {
	stream := grokUpdateLine(t, 2, map[string]any{
		"sessionUpdate": "turn_completed",
		"usage": map[string]any{
			"inputTokens": 100, "outputTokens": 10, "totalTokens": 110,
		},
	})

	root := t.TempDir()
	withFile := writeGrokSession(t, root, grokTestCWD, "ses_file",
		grokSummaryJSON(t, "ses_file", grokTestCWD, "File", "fixture-model-2", "2026-11-24T10:05:00Z"),
		[]string{stream}, grokUsage(t, grokTurn(t, 1, 300, 40, 100, 20, 340)))
	if got := readGrok(t, &GrokStore{}, withFile); got.Tokens != (Tokens{Input: 180, Output: 40, CacheRead: 100, CacheCreate: 20}) {
		t.Errorf("tokens = %+v, want the usage file to win", got.Tokens)
	}

	emptyFile := writeGrokSession(t, root, grokTestCWD, "ses_empty",
		grokSummaryJSON(t, "ses_empty", grokTestCWD, "Empty", "fixture-model-2", "2026-11-24T10:05:00Z"),
		[]string{stream}, `{"turns":[]}`)
	if got := readGrok(t, &GrokStore{}, emptyFile); got.Tokens != (Tokens{Input: 100, Output: 10}) {
		t.Errorf("tokens = %+v, want the stream usage to survive an empty usage file", got.Tokens)
	}
}

func TestGrokTokensNormalization(t *testing.T) {
	for _, test := range []struct {
		name string
		in   grokUsageSummary
		want Tokens
	}{
		{
			name: "input already excludes cache",
			in:   grokUsageSummary{InputTokens: 300, OutputTokens: 40, CachedReadTokens: 100, CacheCreationTokens: 20, TotalTokens: 460},
			want: Tokens{Input: 300, Output: 40, CacheRead: 100, CacheCreate: 20},
		},
		{
			name: "input includes cache",
			in:   grokUsageSummary{InputTokens: 300, OutputTokens: 40, CachedReadTokens: 100, CacheCreationTokens: 20, TotalTokens: 340},
			want: Tokens{Input: 180, Output: 40, CacheRead: 100, CacheCreate: 20},
		},
		{
			name: "reasoning stays inside output",
			in:   grokUsageSummary{InputTokens: 300, OutputTokens: 40, CachedReadTokens: 100, CacheCreationTokens: 20, ReasoningTokens: 5, TotalTokens: 465},
			want: Tokens{Input: 300, Output: 40, CacheRead: 100, CacheCreate: 20},
		},
		{
			name: "no total falls back to the included-cache shape",
			in:   grokUsageSummary{InputTokens: 300, OutputTokens: 40, CachedReadTokens: 100, CacheCreationTokens: 20},
			want: Tokens{Input: 180, Output: 40, CacheRead: 100, CacheCreate: 20},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := grokTokens(test.in); got != test.want {
				t.Errorf("grokTokens = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestGrokTranscriptUsageUpdate(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "", "fixture-model-2", "2026-11-24T10:05:00Z"),
		[]string{
			grokChunk(t, 1, "user_message_chunk", "Measure this"),
			grokUpdateLine(t, 2, map[string]any{
				"sessionUpdate": "usage_update",
				"usage": map[string]any{
					"inputTokens": 200, "outputTokens": 30,
					"cachedReadTokens": 50, "cacheCreationTokens": 10,
					"totalTokens": 240,
				},
			}),
		}, "")

	got := readGrok(t, &GrokStore{}, dir)
	if want := (Tokens{Input: 140, Output: 30, CacheRead: 50, CacheCreate: 10}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if want := []int64{240}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
}

func TestGrokTranscriptParallelToolCalls(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "", "fixture-model-2", "2026-11-24T10:05:00Z"),
		[]string{
			grokChunk(t, 1, "user_message_chunk", "Call both"),
			grokToolCallID(t, 2, "call_a", "in_progress"),
			grokToolCallID(t, 3, "call_b", "in_progress"),
		}, "")

	if got := readGrok(t, &GrokStore{}, dir); got.State != StateExecuting {
		t.Fatalf("state = %q, want executing with two pending calls", got.State)
	}

	appendJSONLSpecFile(t, filepath.Join(dir, "updates.jsonl"), grokToolUpdateID(t, 4, "call_a", "completed"))
	if got := readGrok(t, &GrokStore{}, dir); got.State != StateExecuting {
		t.Fatalf("state = %q, want executing while call_b still pending", got.State)
	}

	appendJSONLSpecFile(t, filepath.Join(dir, "updates.jsonl"), grokToolUpdateID(t, 5, "call_b", "completed"))
	if got := readGrok(t, &GrokStore{}, dir); got.State != StateThinking {
		t.Fatalf("state = %q, want thinking after both calls complete", got.State)
	}
}

func TestGrokTranscriptModelChanged(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "", "fixture-model-2", "2026-11-24T10:05:00Z"),
		[]string{
			grokChunk(t, 1, "user_message_chunk", "Switch model"),
			grokUpdateLine(t, 2, map[string]any{
				"sessionUpdate": "model_changed",
				"new_model_id":  "fixture-model-3",
			}),
			grokUpdateLine(t, 3, map[string]any{
				"sessionUpdate": "served_model",
				"modelId":       "fixture-model-4",
			}),
		}, "")

	got := readGrok(t, &GrokStore{}, dir)
	if got.Model != "fixture-model-4" {
		t.Errorf("model = %q, want the latest served_model", got.Model)
	}
}

func TestGrokPathForRejectsMismatchedCWD(t *testing.T) {
	root := t.TempDir()
	other := "/home/dev/other"
	writeGrokSession(t, root, grokTestCWD, "ses_mismatch",
		grokSummaryJSON(t, "ses_mismatch", other, "Mismatch", "fixture-model-2", "2026-11-24T10:05:00Z"), nil, "")
	match := writeGrokSession(t, root, grokTestCWD, "ses_match",
		grokSummaryJSON(t, "ses_match", grokTestCWD, "Match", "fixture-model-2", "2026-11-24T10:06:00Z"), nil, "")

	store := &GrokStore{Root: root}
	since := time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)
	got, ok := store.PathFor(grokTestCWD, since)
	if !ok || got != match {
		t.Fatalf("PathFor = %q, %v; want %q", got, ok, match)
	}
}

func TestGrokTranscriptIncrementalRead(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, grokTestCWD, "ses_fixture",
		grokSummaryJSON(t, "ses_fixture", grokTestCWD, "", "fixture-model-2", "2026-11-24T10:05:00Z"),
		[]string{
			grokChunk(t, 1, "user_message_chunk", "First"),
			grokUpdateLine(t, 2, map[string]any{
				"sessionUpdate": "turn_completed",
				"usage": map[string]any{
					"inputTokens": 100, "outputTokens": 10, "totalTokens": 110,
				},
			}),
		}, "")

	store := &GrokStore{}
	if got := readGrok(t, store, dir); len(got.TokenHistory) != 1 {
		t.Fatalf("first read history = %v, want one point", got.TokenHistory)
	}

	appendJSONLSpecFile(t, filepath.Join(dir, "updates.jsonl"),
		grokUpdateLine(t, 3, map[string]any{
			"sessionUpdate": "turn_completed",
			"usage": map[string]any{
				"inputTokens": 200, "outputTokens": 20, "totalTokens": 220,
			},
		}))

	got := readGrok(t, store, dir)
	if want := []int64{110, 220}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
	if got := readGrok(t, store, dir); len(got.TokenHistory) != 2 {
		t.Errorf("re-read history = %v, want still two points", got.TokenHistory)
	}
}

func TestGrokSessionsPathHonoursGrokHome(t *testing.T) {
	withHome := func(key string) string {
		if key == "GROK_HOME" {
			return "/tmp/fixture-grok-home"
		}
		return ""
	}
	if got, want := GrokSessionsPathFrom("/home/dev", withHome), "/tmp/fixture-grok-home/sessions"; got != want {
		t.Errorf("GrokSessionsPathFrom = %q, want %q", got, want)
	}
	if got, want := GrokSessionsPathFrom("/home/dev", func(string) string { return "" }), "/home/dev/.grok/sessions"; got != want {
		t.Errorf("GrokSessionsPathFrom without GROK_HOME = %q, want %q", got, want)
	}
	if got := GrokSessionsPathFrom("", withHome); got != "" {
		t.Errorf("GrokSessionsPathFrom empty home = %q, want empty", got)
	}
}
