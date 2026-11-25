package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Codex's rollout format has grown two vocabularies, a per-event one and the newer
// item_completed wrapper, so these fixtures are built line by line from the fields the reader
// uses. Names, ids and prompts are invented.

const codexTestCWD = "/home/dev/shop"

func codexJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func codexLine(t *testing.T, eventType string, payload map[string]any) string {
	t.Helper()
	return codexLineAt(t, "2026-11-24T10:00:00Z", eventType, payload)
}

func codexLineAt(t *testing.T, timestamp, eventType string, payload map[string]any) string {
	t.Helper()
	return codexJSON(t, map[string]any{
		"timestamp": timestamp,
		"type":      eventType,
		"payload":   payload,
	})
}

func codexMeta(t *testing.T, id, cwd string) string {
	t.Helper()
	return codexLine(t, "session_meta", map[string]any{
		"id": id, "session_id": id, "cwd": cwd,
		"timestamp": "2026-11-24T10:00:00Z",
	})
}

func codexTurnContext(t *testing.T, model string) string {
	t.Helper()
	return codexLine(t, "turn_context", map[string]any{"model": model, "cwd": codexTestCWD})
}

func codexEvent(t *testing.T, subtype string, extra map[string]any) string {
	t.Helper()
	payload := map[string]any{"type": subtype}
	for k, v := range extra {
		payload[k] = v
	}
	return codexLine(t, "event_msg", payload)
}

func codexUser(t *testing.T, text string) string {
	t.Helper()
	return codexEvent(t, "item_completed", map[string]any{
		"item": map[string]any{
			"type":    "UserMessage",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
}

func codexAgentMessage(t *testing.T, phase string) string {
	t.Helper()
	return codexEvent(t, "item_completed", map[string]any{
		"item": map[string]any{
			"type":    "AgentMessage",
			"phase":   phase,
			"content": []any{map[string]any{"type": "text", "text": "Working on it."}},
		},
	})
}

func codexToolCall(t *testing.T, callID string) string {
	t.Helper()
	return codexLine(t, "response_item", map[string]any{
		"type": "custom_tool_call", "call_id": callID, "name": "exec",
		"input": "await tools.exec_command({cmd: 'go test ./...'})",
	})
}

func codexToolOutput(t *testing.T, callID string) string {
	t.Helper()
	return codexLine(t, "response_item", map[string]any{
		"type": "custom_tool_call_output", "call_id": callID, "output": "ok",
	})
}

func codexFunctionCall(t *testing.T, callID string) string {
	t.Helper()
	return codexLine(t, "response_item", map[string]any{
		"type": "function_call", "call_id": callID, "name": "sleep", "arguments": "{}",
	})
}

func codexFunctionOutput(t *testing.T, callID string) string {
	t.Helper()
	return codexLine(t, "response_item", map[string]any{
		"type": "function_call_output", "call_id": callID, "output": "ok",
	})
}

// codexCounts is one usage record in a fixture. Codex counts cached input inside input_tokens and keeps
// cache writes in a counter of their own.
type codexCounts struct {
	input, cached, write, output int64
}

func codexTokenCount(t *testing.T, total, last codexCounts, window int64) string {
	t.Helper()
	return codexEvent(t, "token_count", map[string]any{
		"info": map[string]any{
			"total_token_usage": map[string]any{
				"input_tokens": total.input, "cached_input_tokens": total.cached,
				"cache_write_input_tokens": total.write, "output_tokens": total.output,
			},
			"last_token_usage": map[string]any{
				"input_tokens": last.input, "cached_input_tokens": last.cached,
				"cache_write_input_tokens": last.write, "output_tokens": last.output,
			},
			"model_context_window": window,
		},
	})
}

func writeCodexFile(t *testing.T, path string, lines ...string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCodexRollout(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	return writeCodexFile(t, filepath.Join(dir, "rollout-test.jsonl"), lines...)
}

func appendCodexRollout(t *testing.T, path, raw string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
}

func readCodex(t *testing.T, store *CodexStore, path string) StoredSession {
	t.Helper()
	got, err := store.Rollout(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCodexRolloutReadsMetaModelAndWindow(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexTurnContext(t, "gpt-6.1-sol"),
		codexTokenCount(t,
			codexCounts{input: 22_488, cached: 12_416, output: 166},
			codexCounts{input: 22_488, cached: 12_416, output: 166}, 475_000),
	)

	got := readCodex(t, &CodexStore{}, path)

	if got.ID != "ses_a" || got.Dir != codexTestCWD {
		t.Errorf("id/dir = %q/%q", got.ID, got.Dir)
	}
	if got.Model != "gpt-6.1-sol" {
		t.Errorf("model = %q", got.Model)
	}
	// Codex counts the cached read inside input_tokens, so it is split out, not added again.
	if want := (Tokens{Input: 10_072, Output: 166, CacheRead: 12_416}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if want := (Context{Used: 22_488, Limit: 475_000}); got.Context != want {
		t.Errorf("context = %+v, want %+v", got.Context, want)
	}
	if got.Updated.IsZero() {
		t.Error("updated is zero, want the rollout's mtime")
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting: no turn was opened", got.State)
	}
}

func TestCodexTokenCountsReplaceInsteadOfAccumulate(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexUser(t, "Measure this"),
		codexTokenCount(t,
			codexCounts{input: 1000, cached: 400, write: 50, output: 50},
			codexCounts{input: 1000, cached: 400, output: 50}, 200_000),
		codexTokenCount(t,
			codexCounts{input: 1800, cached: 900, write: 50, output: 80},
			codexCounts{input: 800, cached: 500, write: 50, output: 30}, 200_000),
	)

	got := readCodex(t, &CodexStore{}, path)

	// Totals are a level for the whole session: the second event replaces the first.
	if want := (Tokens{Input: 900, Output: 80, CacheRead: 900}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	// The cache write is deliberately dropped: adding it to a total that may include it would
	// double count, and it is zero in every rollout this machine has written.
	if got.Tokens.CacheCreate != 0 || got.Tokens.Total() != 980 {
		t.Errorf("cache write leaked into the total: %+v (total %d)", got.Tokens, got.Tokens.Total())
	}
	if want := (Context{Used: 800, Limit: 200_000}); got.Context != want {
		t.Errorf("context = %+v, want the last turn's fill %+v", got.Context, want)
	}
	if want := []int64{1050, 830}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
}

func TestCodexCompactionDoesNotBlankContext(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexTokenCount(t,
			codexCounts{input: 1000, cached: 400, output: 50},
			codexCounts{input: 1000, cached: 400, output: 50}, 200_000),
		// Compaction writes a token_count whose last usage is zero but whose cumulative total
		// grew. It is a marker, not a response, so neither the fill nor the history may move.
		codexTokenCount(t,
			codexCounts{input: 2000, cached: 900, output: 80},
			codexCounts{}, 200_000),
	)

	got := readCodex(t, &CodexStore{}, path)
	if want := (Context{Used: 1000, Limit: 200_000}); got.Context != want {
		t.Errorf("context = %+v, want the previous fill %+v", got.Context, want)
	}
	if want := []int64{1050}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
}

func TestCodexStateFollowsTheOpenTurn(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexTurnContext(t, "gpt-6.1-sol"),
		codexEvent(t, "task_started", map[string]any{"model_context_window": 300_000}),
	)
	store := &CodexStore{}

	got := readCodex(t, store, path)
	if got.State != StateThinking {
		t.Fatalf("state after task_started = %q, want thinking", got.State)
	}
	if got.Context != (Context{}) {
		t.Errorf("context = %+v, want empty until usage is recorded", got.Context)
	}

	appendCodexRollout(t, path, codexToolCall(t, "call_1")+"\n")
	if got := readCodex(t, store, path); got.State != StateExecuting {
		t.Fatalf("state after a tool call = %q, want executing", got.State)
	}

	// A prompt is a turn boundary: a call still open belongs to the turn that just ended and
	// must not pin the next one in executing.
	appendCodexRollout(t, path, codexToolCall(t, "call_2")+"\n")
	appendCodexRollout(t, path, codexUser(t, "never mind")+"\n")
	if got := readCodex(t, store, path); got.State != StateThinking {
		t.Fatalf("state after a prompt closed an open call = %q, want thinking", got.State)
	}

	appendCodexRollout(t, path, codexToolCall(t, "call_3")+"\n")
	appendCodexRollout(t, path, codexToolOutput(t, "call_3")+"\n")
	if got := readCodex(t, store, path); got.State != StateThinking {
		t.Fatalf("state after the tool output = %q, want thinking", got.State)
	}

	// A commentary message is progress inside the turn, not its end.
	appendCodexRollout(t, path, codexAgentMessage(t, "commentary")+"\n")
	if got := readCodex(t, store, path); got.State != StateThinking {
		t.Fatalf("state after commentary = %q, want thinking", got.State)
	}

	appendCodexRollout(t, path, codexEvent(t, "task_complete", nil)+"\n")
	if got := readCodex(t, store, path); got.State != StateWaiting {
		t.Fatalf("state after task_complete = %q, want waiting", got.State)
	}

	appendCodexRollout(t, path, codexUser(t, "carry on")+"\n")
	if got := readCodex(t, store, path); got.State != StateThinking {
		t.Fatalf("state after a new prompt = %q, want thinking", got.State)
	}

	appendCodexRollout(t, path, codexAgentMessage(t, "final_answer")+"\n")
	if got := readCodex(t, store, path); got.State != StateWaiting {
		t.Fatalf("state after final_answer = %q, want waiting", got.State)
	}

	appendCodexRollout(t, path, codexEvent(t, "task_started", nil)+"\n")
	appendCodexRollout(t, path, codexEvent(t, "turn_aborted", nil)+"\n")
	if got := readCodex(t, store, path); got.State != StateWaiting {
		t.Fatalf("state after turn_aborted = %q, want waiting", got.State)
	}
}

func TestCodexOldSchemaUserMessageOpensTheTurn(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexEvent(t, "user_message", map[string]any{"message": "Fix the build"}),
	)

	got := readCodex(t, &CodexStore{}, path)
	if got.State != StateThinking {
		t.Errorf("state = %q, want thinking", got.State)
	}
	if got.Title != "Fix the build" {
		t.Errorf("title = %q, want the prompt", got.Title)
	}
}

func TestCodexForkedRolloutKeepsItsOwnIdentity(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_fork", codexTestCWD),
		// A fork replays its parent's history, session_meta included.
		codexMeta(t, "ses_parent", "/home/dev/parent"),
		codexTurnContext(t, "gpt-6.1-sol"),
	)

	got := readCodex(t, &CodexStore{}, path)
	if got.ID != "ses_fork" {
		t.Errorf("id = %q, want the file's own id", got.ID)
	}
	if got.Dir != codexTestCWD {
		t.Errorf("dir = %q, want the fork's cwd, not the replayed parent's", got.Dir)
	}
}

func TestCodexTitleComesFromTheFirstPromptOnly(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexUser(t, "Add a health check"),
		codexUser(t, "now fix the failing test"),
	)

	got := readCodex(t, &CodexStore{}, path)
	if got.Title != "Add a health check" {
		t.Errorf("title = %q, want the first prompt", got.Title)
	}
}

func TestCodexRolloutReadsOnlyAppendedLines(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexTokenCount(t,
			codexCounts{input: 100, output: 10},
			codexCounts{input: 100, output: 10}, 200_000),
	)
	store := &CodexStore{}

	if got := readCodex(t, store, path); got.Tokens.Input != 100 {
		t.Fatalf("first read input = %d, want 100", got.Tokens.Input)
	}

	appendCodexRollout(t, path, codexTokenCount(t,
		codexCounts{input: 300, cached: 100, output: 25},
		codexCounts{input: 200, cached: 100, output: 15}, 200_000)+"\n")
	got := readCodex(t, store, path)
	if want := (Tokens{Input: 200, Output: 25, CacheRead: 100}); got.Tokens != want {
		t.Fatalf("second read = %+v, want %+v", got.Tokens, want)
	}
	if want := []int64{110, 215}; !slices.Equal(got.TokenHistory, want) {
		t.Fatalf("history = %v, want %v", got.TokenHistory, want)
	}

	partial := codexTokenCount(t,
		codexCounts{input: 600, cached: 200, output: 40},
		codexCounts{input: 300, cached: 200, output: 15}, 200_000)
	appendCodexRollout(t, path, partial[:len(partial)/2])
	if got := readCodex(t, store, path); got.Tokens.Input != 200 || len(got.TokenHistory) != 2 {
		t.Fatalf("partial line counted: tokens %+v, history %v", got.Tokens, got.TokenHistory)
	}
	appendCodexRollout(t, path, partial[len(partial)/2:]+"\n")
	got = readCodex(t, store, path)
	if want := (Tokens{Input: 400, Output: 40, CacheRead: 200}); got.Tokens != want {
		t.Fatalf("completed line = %+v, want %+v", got.Tokens, want)
	}
	if want := []int64{110, 215, 315}; !slices.Equal(got.TokenHistory, want) {
		t.Fatalf("history = %v, want %v", got.TokenHistory, want)
	}

	// A read with nothing appended returns the same snapshot, and the history is a copy the
	// caller may keep.
	again := readCodex(t, store, path)
	if again.Tokens != got.Tokens || again.State != got.State || again.Title != got.Title {
		t.Errorf("no-change read = %+v, want %+v", again, got)
	}
	want := got.TokenHistory[0]
	again.TokenHistory[0] = -1
	if fresh := readCodex(t, store, path); fresh.TokenHistory[0] != want {
		t.Errorf("history aliased the cached slice: %v", fresh.TokenHistory)
	}
}

func TestCodexRolloutResetsWhenTheFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	path := writeCodexRollout(t, dir,
		codexMeta(t, "ses_a", codexTestCWD),
		codexTokenCount(t,
			codexCounts{input: 100, output: 50},
			codexCounts{input: 100, output: 50}, 200_000),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			quotaLimits(quotaSlot(9, 300, 1774686045), nil)),
	)
	store := &CodexStore{}
	if got := readCodex(t, store, path); got.Tokens.Input != 100 {
		t.Fatalf("first read input = %d", got.Tokens.Input)
	}

	// Replace the file, inode and all, with a longer rollout that starts over. Size alone
	// would not catch this: the new file is bigger than the old offset.
	replacement := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_b", codexTestCWD),
		codexUser(t, "New work"),
		codexTokenCount(t,
			codexCounts{input: 7, output: 3},
			codexCounts{input: 7, output: 3}, 200_000),
		codexTokenCount(t,
			codexCounts{input: 9, output: 4},
			codexCounts{input: 2, output: 1}, 200_000),
		quotaTokenCountLine(t, "2026-11-24T11:00:00Z",
			quotaLimits(quotaSlot(70, 300, 1774689700), nil)),
	)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	got := readCodex(t, store, path)
	if got.ID != "ses_b" || got.Title != "New work" {
		t.Errorf("id/title = %q/%q, want the replacement", got.ID, got.Title)
	}
	if want := (Tokens{Input: 9, Output: 4}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the replacement's %+v", got.Tokens, want)
	}
	if len(got.TokenHistory) != 2 {
		t.Errorf("history = %v, want only the replacement's points", got.TokenHistory)
	}
	if got.Quota == nil || got.Quota.Short == nil || got.Quota.Short.UsedPercent != 70 {
		t.Errorf("quota = %+v, want the replacement's 70%%", got.Quota)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rollout(path); err == nil {
		t.Fatal("a vanished rollout returned no error")
	}
	if _, ok := store.files[path]; ok {
		t.Error("state for a vanished rollout was kept")
	}
}

func TestCodexRolloutSkipsAnOverlongLine(t *testing.T) {
	// The oversized record is a prompt: a reader that kept it whole would let it win the title.
	// The size is fixed rather than tied to the cap so the fixture cannot grow with the guard.
	huge := codexUser(t, strings.Repeat("x", 11<<20))
	path := writeCodexRollout(t, t.TempDir(),
		huge,
		codexMeta(t, "ses_a", codexTestCWD),
		codexUser(t, "Real prompt"),
		codexTokenCount(t,
			codexCounts{input: 4, output: 2},
			codexCounts{input: 4, output: 2}, 200_000),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			quotaLimits(quotaSlot(6, 300, 1774686045), nil)),
	)

	store := &CodexStore{}
	// The oversized line alone exceeds one read's byte budget, and a read that starts with the
	// budget spent still makes one line of progress, so several calls finish the file.
	for i := 0; i < 4; i++ {
		readCodex(t, store, path)
	}
	got := readCodex(t, store, path)
	if got.ID != "ses_a" || got.Tokens.Input != 4 {
		t.Errorf("id/tokens = %q/%+v, want the lines after the oversized one", got.ID, got.Tokens)
	}
	if got.Title != "Real prompt" {
		t.Errorf("title = %q, want the prompt after the oversized line", got.Title)
	}
	if got.Quota == nil || got.Quota.Short == nil || got.Quota.Short.UsedPercent != 6 {
		t.Errorf("quota = %+v, want the record after the oversized line", got.Quota)
	}
}

func TestCodexRolloutSkipsMalformedLines(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		"not json at all",
		codexTokenCount(t,
			codexCounts{input: 5, output: 3},
			codexCounts{input: 5, output: 3}, 200_000),
	)

	got := readCodex(t, &CodexStore{}, path)
	if got.Tokens.Input != 5 || got.Tokens.Output != 3 {
		t.Errorf("tokens = %+v, want the valid record after the malformed one", got.Tokens)
	}
}

func TestCodexRolloutRejectsAnEmptyPath(t *testing.T) {
	if _, err := (&CodexStore{}).Rollout(""); !errors.Is(err, errCodexPathEmpty) {
		t.Fatalf("Rollout(\"\") error = %v, want the empty-path guard", err)
	}
}

func TestCodexRolloutBoundsHistory(t *testing.T) {
	lines := []string{codexMeta(t, "ses_a", codexTestCWD)}
	for i := 0; i < maxTokenHistory+5; i++ {
		lines = append(lines, codexTokenCount(t,
			codexCounts{input: int64(i + 1), output: 1},
			codexCounts{input: int64(i + 1), output: 1}, 200_000))
	}

	got := readCodex(t, &CodexStore{}, writeCodexRollout(t, t.TempDir(), lines...))
	if len(got.TokenHistory) != maxTokenHistory {
		t.Fatalf("history length = %d, want %d", len(got.TokenHistory), maxTokenHistory)
	}
	if want := int64(7); got.TokenHistory[0] != want {
		t.Errorf("oldest kept point = %d, want %d", got.TokenHistory[0], want)
	}
}

func TestCodexStoreDropsTheOldestAccumulation(t *testing.T) {
	dir := t.TempDir()
	store := &CodexStore{}
	base := time.Now().Add(-time.Hour)
	paths := make([]string, 0, maxJSONLFiles+1)
	for i := 0; i <= maxJSONLFiles; i++ {
		path := filepath.Join(dir, fmt.Sprintf("rollout_%03d.jsonl", i))
		writeCodexFile(t, path, codexMeta(t, "ses_a", codexTestCWD))
		when := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		readCodex(t, store, path)
		paths = append(paths, path)
	}

	if len(store.files) != maxJSONLFiles {
		t.Fatalf("tracked files = %d, want %d", len(store.files), maxJSONLFiles)
	}
	if _, ok := store.files[paths[0]]; ok {
		t.Error("the oldest accumulation was kept")
	}
	if _, ok := store.files[paths[len(paths)-1]]; !ok {
		t.Error("the newest accumulation was dropped")
	}

	// Re-reading the oldest file must keep it: evicting the accumulator just created would
	// restart it from byte 0 on every read instead of letting it catch up.
	readCodex(t, store, paths[0])
	if _, ok := store.files[paths[0]]; !ok {
		t.Error("the accumulation just read was evicted")
	}
	if len(store.files) != maxJSONLFiles {
		t.Errorf("tracked files = %d, want %d", len(store.files), maxJSONLFiles)
	}
}

func TestCodexTitleJoinsItemContentBlocks(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexLine(t, "event_msg", map[string]any{
			"type": "item_completed",
			"item": map[string]any{
				"type": "UserMessage",
				"content": []any{
					map[string]any{"type": "text", "text": "Add a"},
					map[string]any{"type": "input_image", "image_url": "https://example.test/shot.png"},
					map[string]any{"type": "text", "text": "health check"},
				},
			},
		}),
	)

	got := readCodex(t, &CodexStore{}, path)
	if got.Title != "Add a health check" {
		t.Errorf("title = %q, want the text blocks joined around the non-text part", got.Title)
	}
}

func TestCodexTitleIgnoresUnusableItemContent(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexEvent(t, "item_completed", map[string]any{
			"item": map[string]any{"type": "UserMessage"},
		}),
		codexEvent(t, "item_completed", map[string]any{
			"item": map[string]any{"type": "UserMessage", "content": "not a block array"},
		}),
		codexUser(t, "Real prompt"),
	)

	got := readCodex(t, &CodexStore{}, path)
	if got.Title != "Real prompt" {
		t.Errorf("title = %q, want the first usable prompt", got.Title)
	}
}

func TestCodexIgnoresACallWithoutAnID(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexEvent(t, "task_started", nil),
		codexLine(t, "response_item", map[string]any{"type": "custom_tool_call", "name": "exec"}),
	)
	store := &CodexStore{}

	if got := readCodex(t, store, path); got.State != StateThinking {
		t.Fatalf("state after an id-less call = %q, want thinking", got.State)
	}
	appendCodexRollout(t, path, codexLine(t, "response_item", map[string]any{
		"type": "custom_tool_call_output", "output": "ok",
	})+"\n")
	if got := readCodex(t, store, path); got.State != StateThinking {
		t.Fatalf("state after an id-less output = %q, want thinking", got.State)
	}
}

func TestCodexTracksNonCustomFunctionCalls(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexEvent(t, "task_started", nil),
	)
	store := &CodexStore{}

	appendCodexRollout(t, path, codexFunctionCall(t, "call_f")+"\n")
	if got := readCodex(t, store, path); got.State != StateExecuting {
		t.Fatalf("state after function_call = %q, want executing", got.State)
	}
	appendCodexRollout(t, path, codexFunctionOutput(t, "call_f")+"\n")
	if got := readCodex(t, store, path); got.State != StateThinking {
		t.Fatalf("state after function_call_output = %q, want thinking", got.State)
	}
}

func TestCodexTurnWithoutAWindowKeepsTheRecordedLimit(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexTokenCount(t,
			codexCounts{input: 1000, output: 50},
			codexCounts{input: 1000, output: 50}, 200_000),
		// Older events carry no model_context_window; a zero must not erase the recorded one.
		codexEvent(t, "user_message", map[string]any{"message": "keep going"}),
	)

	if got := readCodex(t, &CodexStore{}, path); got.Context.Limit != 200_000 {
		t.Errorf("context limit = %d, want the recorded 200k window", got.Context.Limit)
	}
}

func TestCodexSessionMetaFallsBackToSessionID(t *testing.T) {
	cases := map[string]struct {
		meta map[string]any
		want string
	}{
		"id wins when both are present": {map[string]any{"id": "ses_new", "session_id": "ses_old"}, "ses_new"},
		"legacy session_id only":        {map[string]any{"session_id": "ses_legacy"}, "ses_legacy"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			meta := map[string]any{"cwd": codexTestCWD}
			for k, v := range c.meta {
				meta[k] = v
			}
			path := writeCodexRollout(t, t.TempDir(), codexLine(t, "session_meta", meta))
			if got := readCodex(t, &CodexStore{}, path); got.ID != c.want {
				t.Errorf("id = %q, want %q", got.ID, c.want)
			}
		})
	}
}

func TestCodexSkipsRecordsWithNothingToRead(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexLine(t, "future_event", map[string]any{"anything": true}),
		codexEvent(t, "item_completed", nil),
		codexEvent(t, "token_count", nil),
		codexTokenCount(t,
			codexCounts{input: 5, output: 3},
			codexCounts{input: 5, output: 3}, 200_000),
	)

	got := readCodex(t, &CodexStore{}, path)
	if got.Tokens.Input != 5 || got.Tokens.Output != 3 {
		t.Errorf("tokens = %+v, want the valid record after the empty ones", got.Tokens)
	}
}

func TestCodexRolloutResetsWhenTheFileIsTruncatedInPlace(t *testing.T) {
	path := writeCodexRollout(t, t.TempDir(),
		codexMeta(t, "ses_a", codexTestCWD),
		codexUser(t, strings.Repeat("x", 2000)),
		codexTokenCount(t,
			codexCounts{input: 100, output: 50},
			codexCounts{input: 100, output: 50}, 200_000),
		quotaTokenCountLine(t, "2026-11-24T10:00:00Z",
			quotaLimits(quotaSlot(9, 300, 1774686045), nil)),
	)
	store := &CodexStore{}
	if got := readCodex(t, store, path); got.Tokens.Input != 100 {
		t.Fatalf("first read input = %d", got.Tokens.Input)
	}

	// Same inode, shorter file: the size check is the only signal that the accumulation is
	// stale, and a live writer truncating a rollout is the common case.
	writeCodexFile(t, path,
		codexMeta(t, "ses_a", codexTestCWD),
		codexUser(t, "Fresh work"),
		codexTokenCount(t,
			codexCounts{input: 7, output: 3},
			codexCounts{input: 7, output: 3}, 200_000),
		quotaTokenCountLine(t, "2026-11-24T11:00:00Z",
			quotaLimits(quotaSlot(55, 300, 1774689700), nil)),
	)

	got := readCodex(t, store, path)
	if want := (Tokens{Input: 7, Output: 3}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the truncated file's %+v", got.Tokens, want)
	}
	if len(got.TokenHistory) != 1 || got.TokenHistory[0] != 10 {
		t.Errorf("history = %v, want only the truncated file's point", got.TokenHistory)
	}
	if got.Title != "Fresh work" {
		t.Errorf("title = %q, want the truncated file's prompt", got.Title)
	}
	if got.Quota == nil || got.Quota.Short == nil || got.Quota.Short.UsedPercent != 55 {
		t.Errorf("quota = %+v, want the truncated file's 55%%", got.Quota)
	}
}
