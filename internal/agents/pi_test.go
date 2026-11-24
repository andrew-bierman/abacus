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

// Pi's session format is documented (docs/session-format.md in the pi package) and its files form
// a tree, but this reader follows the linear append order the store writes. Fixtures are built line
// by line from the fields the reader uses; names, ids, providers and prompts are invented.

const piTestCWD = "/home/dev/shop"

func piJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func piHeader(t *testing.T, id, cwd string) string {
	t.Helper()
	return piJSON(t, map[string]any{
		"type": "session", "version": 3, "id": id,
		"timestamp": "2026-11-24T10:00:00.000Z", "cwd": cwd,
	})
}

func piModelChange(t *testing.T, provider, model string) string {
	t.Helper()
	return piJSON(t, map[string]any{
		"type": "model_change", "id": "m1", "parentId": nil,
		"timestamp": "2026-11-24T10:00:00.000Z",
		"provider":  provider, "modelId": model,
	})
}

func piSessionInfo(t *testing.T, name string) string {
	t.Helper()
	return piJSON(t, map[string]any{
		"type": "session_info", "id": "i1", "parentId": nil,
		"timestamp": "2026-11-24T10:00:00.000Z", "name": name,
	})
}

func piUser(t *testing.T, text string) string {
	t.Helper()
	return piJSON(t, map[string]any{
		"type": "message", "id": "u1", "parentId": nil,
		"timestamp": "2026-11-24T10:00:01.000Z",
		"message": map[string]any{
			"role":      "user",
			"content":   []any{map[string]any{"type": "text", "text": text}},
			"timestamp": 1_763_985_601_000,
		},
	})
}

func piUserString(t *testing.T, text string) string {
	t.Helper()
	return piJSON(t, map[string]any{
		"type": "message", "id": "u1", "parentId": nil,
		"timestamp": "2026-11-24T10:00:01.000Z",
		"message": map[string]any{
			"role": "user", "content": text, "timestamp": 1_763_985_601_000,
		},
	})
}

// piCounts is one usage record. Reasoning is a subset of output in pi's format.
type piCounts struct {
	input, output, cacheRead, cacheWrite, reasoning int64
}

func (c piCounts) usage() map[string]any {
	m := map[string]any{
		"input": c.input, "output": c.output,
		"cacheRead": c.cacheRead, "cacheWrite": c.cacheWrite,
		"totalTokens": c.input + c.output + c.cacheRead + c.cacheWrite,
	}
	if c.reasoning != 0 {
		m["reasoning"] = c.reasoning
	}
	return m
}

func piAssistant(t *testing.T, provider, model string, c piCounts, stop string, toolIDs ...string) string {
	t.Helper()
	content := []any{}
	if stop != "toolUse" {
		content = append(content, map[string]any{"type": "text", "text": "Working on it."})
	}
	for _, id := range toolIDs {
		content = append(content, map[string]any{
			"type": "toolCall", "id": id, "name": "bash",
			"arguments": map[string]any{"command": "go test ./..."},
		})
	}
	return piJSON(t, map[string]any{
		"type": "message", "id": "a1", "parentId": nil,
		"timestamp": "2026-11-24T10:00:02.000Z",
		"message": map[string]any{
			"role": "assistant", "content": content,
			"provider": provider, "model": model, "usage": c.usage(),
			"stopReason": stop, "timestamp": 1_763_985_602_000,
		},
	})
}

func piToolResult(t *testing.T, callID string, usage *piCounts) string {
	t.Helper()
	msg := map[string]any{
		"role": "toolResult", "toolCallId": callID, "toolName": "bash",
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"isError": false, "timestamp": 1_763_985_603_000,
	}
	if usage != nil {
		msg["usage"] = usage.usage()
	}
	return piJSON(t, map[string]any{
		"type": "message", "id": "t1", "parentId": nil,
		"timestamp": "2026-11-24T10:00:03.000Z", "message": msg,
	})
}

func piUsageEntry(t *testing.T, kind string, c piCounts) string {
	t.Helper()
	return piJSON(t, map[string]any{
		"type": "usage", "id": "g1", "parentId": nil,
		"timestamp": "2026-11-24T10:00:04.000Z", "kind": kind,
		"provider": "fixture-labs", "model": "shop-model-1", "usage": c.usage(),
	})
}

func piCompaction(t *testing.T, c piCounts) string {
	t.Helper()
	return piJSON(t, map[string]any{
		"type": "compaction", "id": "c1", "parentId": nil,
		"timestamp":        "2026-11-24T10:00:05.000Z",
		"summary":          "The user asked for a shop refactor.",
		"firstKeptEntryId": "u1", "tokensBefore": 900, "usage": c.usage(),
	})
}

func writePiFile(t *testing.T, path string, lines ...string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePiSession(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	return writePiFile(t, filepath.Join(dir, "2026-11-24T10-00-00-000Z_ses_a.jsonl"), lines...)
}

func appendPiFile(t *testing.T, path, raw string) {
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

func readPi(t *testing.T, store *PiStore, path string) StoredSession {
	t.Helper()
	got, err := store.Transcript(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPiTranscriptReadsHeaderAndResponse(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Fix the failing test"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 120, output: 30, cacheRead: 1000, cacheWrite: 5}, "stop"),
	)

	got := readPi(t, &PiStore{}, path)

	if got.ID != "ses_a" || got.Dir != piTestCWD {
		t.Errorf("id/dir = %q/%q", got.ID, got.Dir)
	}
	if got.Model != "fixture-labs/shop-model-1" {
		t.Errorf("model = %q", got.Model)
	}
	if want := (Tokens{Input: 120, Output: 30, CacheRead: 1000, CacheCreate: 5}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	// The prompt the model saw is every input counter: input excludes the cached part.
	if want := (Context{Used: 1125}); got.Context != want {
		t.Errorf("context = %+v, want %+v", got.Context, want)
	}
	if want := []int64{1155}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
	if got.Title != "Fix the failing test" {
		t.Errorf("title = %q", got.Title)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting", got.State)
	}
	if got.Updated.IsZero() {
		t.Error("updated is zero, want the session file's mtime")
	}
}

func TestPiResponsesAddTotalsWithoutCountingReasoningTwice(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Measure this"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 100, output: 10, reasoning: 8}, "stop"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 50, output: 5, reasoning: 3}, "stop"),
	)

	got := readPi(t, &PiStore{}, path)

	if want := (Tokens{Input: 150, Output: 15}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v: reasoning is already inside output", got.Tokens, want)
	}
	if got.Context.Used != 50 {
		t.Errorf("context used = %d, want the last response's 50", got.Context.Used)
	}
	if want := []int64{110, 55}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
}

func TestPiZeroUsageResponseDoesNotBlankTheFill(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Try it"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 100, output: 10}, "stop"),
		piAssistant(t, "fixture-labs", "shop-model-1", piCounts{}, "aborted"),
	)

	got := readPi(t, &PiStore{}, path)

	if want := (Tokens{Input: 100, Output: 10}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.Context.Used != 100 {
		t.Errorf("context used = %d, want the previous fill", got.Context.Used)
	}
	if want := []int64{110}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want only the response that ran", got.TokenHistory)
	}
}

func TestPiStateFollowsTheOpenTurn(t *testing.T) {
	path := writePiSession(t, t.TempDir(), piHeader(t, "ses_a", piTestCWD))
	store := &PiStore{}

	appendPiFile(t, path, piUser(t, "Run the suite")+"\n")
	if got := readPi(t, store, path); got.State != StateThinking {
		t.Fatalf("state after a prompt = %q, want thinking", got.State)
	}

	appendPiFile(t, path, piAssistant(t, "fixture-labs", "shop-model-1",
		piCounts{input: 40, output: 4}, "toolUse", "call_1")+"\n")
	if got := readPi(t, store, path); got.State != StateExecuting {
		t.Fatalf("state after a tool call = %q, want executing", got.State)
	}

	// A steering prompt does not stop the tool that is already running.
	appendPiFile(t, path, piUser(t, "Also check the logs")+"\n")
	if got := readPi(t, store, path); got.State != StateExecuting {
		t.Fatalf("state after a steering prompt = %q, want executing", got.State)
	}

	appendPiFile(t, path, piToolResult(t, "call_1", nil)+"\n")
	if got := readPi(t, store, path); got.State != StateThinking {
		t.Fatalf("state after a tool result = %q, want thinking", got.State)
	}

	appendPiFile(t, path, piAssistant(t, "fixture-labs", "shop-model-1",
		piCounts{input: 70, output: 9}, "stop")+"\n")
	if got := readPi(t, store, path); got.State != StateWaiting {
		t.Fatalf("state after the final response = %q, want waiting", got.State)
	}
}

func TestPiParallelCallsStayExecutingUntilEveryResultLands(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Run both"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 40, output: 4}, "toolUse", "call_1", "call_2"),
	)
	store := &PiStore{}
	if got := readPi(t, store, path); got.State != StateExecuting {
		t.Fatalf("state = %q, want executing with two calls pending", got.State)
	}

	appendPiFile(t, path, piToolResult(t, "call_1", nil)+"\n")
	if got := readPi(t, store, path); got.State != StateExecuting {
		t.Fatalf("state = %q, want executing while call_2 is pending", got.State)
	}

	appendPiFile(t, path, piToolResult(t, "call_2", nil)+"\n")
	if got := readPi(t, store, path); got.State != StateThinking {
		t.Fatalf("state = %q, want thinking once both results landed", got.State)
	}
}

func TestPiUnmatchedToolResultLeavesTheCallPending(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Run it"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 40, output: 4}, "toolUse", "call_1"),
		piToolResult(t, "call_other", nil),
	)

	if got := readPi(t, &PiStore{}, path); got.State != StateExecuting {
		t.Errorf("state = %q, want executing: only call_1's result may close it", got.State)
	}
}

func TestPiTerminalStopReasonsCloseTheTurn(t *testing.T) {
	for _, stop := range []string{"stop", "length", "error", "aborted"} {
		t.Run(stop, func(t *testing.T) {
			path := writePiSession(t, t.TempDir(),
				piHeader(t, "ses_a", piTestCWD),
				piUser(t, "Try it"),
				piAssistant(t, "fixture-labs", "shop-model-1",
					piCounts{input: 40, output: 4}, stop),
			)
			if got := readPi(t, &PiStore{}, path); got.State != StateWaiting {
				t.Errorf("state = %q, want waiting", got.State)
			}
		})
	}
}

func TestPiDeferredResponseKeepsTheTurnOpen(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Try it"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 40, output: 4}, "deferred"),
	)

	if got := readPi(t, &PiStore{}, path); got.State != StateThinking {
		t.Errorf("state = %q, want thinking while the provider result is deferred", got.State)
	}
}

func TestPiTerminalResponseWithoutResultClosesTheCall(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Run it"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 40, output: 4}, "toolUse", "call_1"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 60, output: 6}, "stop"),
	)

	if got := readPi(t, &PiStore{}, path); got.State != StateWaiting {
		t.Errorf("state = %q, want waiting: a completed response settles the old call", got.State)
	}
}

func TestPiUserWithoutTextDoesNotOpenTheTurn(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "   "),
		piJSON(t, map[string]any{
			"type": "message", "id": "u2", "parentId": nil,
			"timestamp": "2026-11-24T10:00:01.000Z",
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image", "data": "c2hvdA==", "mimeType": "image/png"},
				},
				"timestamp": 1_763_985_601_000,
			},
		}),
	)

	if got := readPi(t, &PiStore{}, path); got.State != StateWaiting {
		t.Errorf("state = %q, want waiting: no prompt reached the model", got.State)
	}
}

func TestPiAuxiliaryUsageWithoutAResponseLeavesContextUnknown(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUsageEntry(t, "cache_warm", piCounts{cacheRead: 500}),
	)

	got := readPi(t, &PiStore{}, path)
	if want := (Tokens{CacheRead: 500}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.Context != (Context{}) || len(got.TokenHistory) != 0 {
		t.Errorf("context/history = %+v/%v, want both unrecorded", got.Context, got.TokenHistory)
	}
}

func TestPiForkedSessionKeepsItsOwnIdentity(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_fork", piTestCWD),
		piHeader(t, "ses_parent", "/home/dev/parent"),
	)

	got := readPi(t, &PiStore{}, path)
	if got.ID != "ses_fork" || got.Dir != piTestCWD {
		t.Errorf("id/dir = %q/%q, want the fork's own header", got.ID, got.Dir)
	}
}

func TestPiTitleComesFromSessionInfoOrTheFirstPrompt(t *testing.T) {
	named := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "First prompt"),
		piSessionInfo(t, "Refactor auth"),
		piUser(t, "Second prompt"),
	)
	if got := readPi(t, &PiStore{}, named); got.Title != "Refactor auth" {
		t.Errorf("title = %q, want the session name", got.Title)
	}

	untitled := writePiSession(t, t.TempDir(), piHeader(t, "ses_b", piTestCWD))
	if got := readPi(t, &PiStore{}, untitled); got.Title != "" {
		t.Errorf("title = %q, want empty with no prompt and no name", got.Title)
	}

	stringForm := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_c", piTestCWD),
		piUserString(t, "Prompt as a string"),
	)
	if got := readPi(t, &PiStore{}, stringForm); got.Title != "Prompt as a string" {
		t.Errorf("title = %q, want the string-form prompt", got.Title)
	}

	cleared := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_d", piTestCWD),
		piUser(t, "First prompt"),
		piSessionInfo(t, "Named"),
		piSessionInfo(t, ""),
	)
	if got := readPi(t, &PiStore{}, cleared); got.Title != "First prompt" {
		t.Errorf("title = %q, want the first prompt after the name was cleared", got.Title)
	}
}

func TestPiTitleJoinsTextBlocks(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piJSON(t, map[string]any{
			"type": "message", "id": "u1", "parentId": nil,
			"timestamp": "2026-11-24T10:00:01.000Z",
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "Add a"},
					map[string]any{"type": "image", "data": "c2hvdA==", "mimeType": "image/png"},
					map[string]any{"type": "text", "text": "health check"},
				},
				"timestamp": 1_763_985_601_000,
			},
		}),
	)

	if got := readPi(t, &PiStore{}, path); got.Title != "Add a health check" {
		t.Errorf("title = %q, want the text blocks joined around the image", got.Title)
	}
}

func TestPiAuxiliaryUsageReachesTotalsButNotContextOrHistory(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Warm the cache"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 10, output: 2}, "toolUse", "call_1"),
		piToolResult(t, "call_1", &piCounts{input: 100, output: 20}),
		piUsageEntry(t, "cache_warm", piCounts{cacheRead: 500}),
		piCompaction(t, piCounts{input: 50, output: 5}),
	)

	got := readPi(t, &PiStore{}, path)

	if want := (Tokens{Input: 160, Output: 27, CacheRead: 500}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.Context.Used != 10 {
		t.Errorf("context used = %d, want the assistant response's 10", got.Context.Used)
	}
	if want := []int64{12}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want only the assistant response", got.TokenHistory)
	}
}

func TestPiModelChangeIsTheFallbackUntilAResponseNamesAModel(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piModelChange(t, "fixture-labs", "router-model"),
	)
	store := &PiStore{}
	if got := readPi(t, store, path); got.Model != "fixture-labs/router-model" {
		t.Errorf("model = %q, want the model_change fallback", got.Model)
	}

	appendPiFile(t, path, piAssistant(t, "fixture-labs", "shop-model-1",
		piCounts{input: 20, output: 2}, "stop")+"\n")
	if got := readPi(t, store, path); got.Model != "fixture-labs/shop-model-1" {
		t.Errorf("model = %q, want the physical model that answered", got.Model)
	}
}

func TestPiTranscriptReadsOnlyAppendedLines(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "First"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 100, output: 10}, "stop"),
	)
	store := &PiStore{}
	if got := readPi(t, store, path); got.Tokens.Input != 100 {
		t.Fatalf("first read input = %d", got.Tokens.Input)
	}

	line := piAssistant(t, "fixture-labs", "shop-model-1",
		piCounts{input: 25, output: 5}, "stop") + "\n"
	half := len(line) / 2
	appendPiFile(t, path, line[:half])

	if got := readPi(t, store, path); got.Tokens.Input != 100 || len(got.TokenHistory) != 1 {
		t.Errorf("partial read = %+v with %d points, want the first response only",
			got.Tokens, len(got.TokenHistory))
	}

	appendPiFile(t, path, line[half:])
	got := readPi(t, store, path)
	if got.Tokens.Input != 125 || len(got.TokenHistory) != 2 {
		t.Errorf("completed read = %+v with %d points, want the appended response once",
			got.Tokens, len(got.TokenHistory))
	}
}

func TestPiTranscriptResetsWhenTheFileIsReplaced(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_old", piTestCWD),
		piUser(t, "Old work"),
	)
	store := &PiStore{}
	if got := readPi(t, store, path); got.ID != "ses_old" {
		t.Fatalf("id = %q", got.ID)
	}

	// Replace the file, inode and all, with a longer session that starts over. Size alone would
	// not catch this: the new file is bigger than the old offset.
	replacement := writePiFile(t, filepath.Join(t.TempDir(), "replacement.jsonl"),
		piHeader(t, "ses_new", piTestCWD),
		piUser(t, "New work"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 7, output: 3}, "stop"),
	)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	got := readPi(t, store, path)
	if got.ID != "ses_new" || got.Title != "New work" {
		t.Errorf("id/title = %q/%q, want the replacement's", got.ID, got.Title)
	}
	if want := (Tokens{Input: 7, Output: 3}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the replacement's %+v", got.Tokens, want)
	}
	if want := []int64{10}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
}

func TestPiTranscriptResetsWhenTheFileIsTruncatedInPlace(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, strings.Repeat("x", 2000)),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 100, output: 50}, "stop"),
	)
	store := &PiStore{}
	if got := readPi(t, store, path); got.Tokens.Input != 100 {
		t.Fatalf("first read input = %d", got.Tokens.Input)
	}

	writePiFile(t, path,
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Fresh work"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 7, output: 3, cacheRead: 2}, "stop"),
	)

	got := readPi(t, store, path)
	if want := (Tokens{Input: 7, Output: 3, CacheRead: 2}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the truncated file's %+v", got.Tokens, want)
	}
	if want := []int64{12}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want only the truncated file's point", got.TokenHistory)
	}
	if got.Title != "Fresh work" || got.Context.Used != 9 {
		t.Errorf("title/context = %q/%d, want the truncated file's", got.Title, got.Context.Used)
	}
}

func TestPiTranscriptSkipsAnOverlongLine(t *testing.T) {
	// The oversized record is a prompt: a reader that kept it whole would let it win the title.
	huge := piUser(t, strings.Repeat("x", 11<<20))
	path := writePiSession(t, t.TempDir(),
		huge,
		piHeader(t, "ses_a", piTestCWD),
		piUser(t, "Real prompt"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 4, output: 2}, "stop"),
	)

	store := &PiStore{}
	for i := 0; i < 3; i++ {
		readPi(t, store, path)
	}
	got := readPi(t, store, path)
	if got.ID != "ses_a" || got.Tokens.Input != 4 {
		t.Errorf("id/tokens = %q/%+v, want the lines after the oversized one", got.ID, got.Tokens)
	}
	if got.Title != "Real prompt" {
		t.Errorf("title = %q, want the prompt after the oversized line", got.Title)
	}
}

func TestPiTranscriptSkipsMalformedAndUnknownLines(t *testing.T) {
	path := writePiSession(t, t.TempDir(),
		piHeader(t, "ses_a", piTestCWD),
		"not json at all",
		piJSON(t, map[string]any{"type": "future_entry", "anything": true}),
		piJSON(t, map[string]any{"type": "message", "id": "empty"}),
		piUser(t, "Real prompt"),
		piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: 5, output: 3}, "stop"),
	)

	got := readPi(t, &PiStore{}, path)
	if got.Tokens.Input != 5 || got.Tokens.Output != 3 {
		t.Errorf("tokens = %+v, want the valid record after the unusable ones", got.Tokens)
	}
	if got.Title != "Real prompt" {
		t.Errorf("title = %q", got.Title)
	}
}

func TestPiTranscriptRejectsAnEmptyPath(t *testing.T) {
	if _, err := (&PiStore{}).Transcript(""); !errors.Is(err, errPiPathEmpty) {
		t.Fatalf("Transcript(\"\") error = %v, want the empty-path guard", err)
	}
}

func TestPiTranscriptBoundsHistory(t *testing.T) {
	lines := []string{piHeader(t, "ses_a", piTestCWD)}
	for i := 0; i < maxTokenHistory+5; i++ {
		lines = append(lines, piAssistant(t, "fixture-labs", "shop-model-1",
			piCounts{input: int64(i + 1), output: 1}, "stop"))
	}

	got := readPi(t, &PiStore{}, writePiSession(t, t.TempDir(), lines...))
	if len(got.TokenHistory) != maxTokenHistory {
		t.Fatalf("history length = %d, want %d", len(got.TokenHistory), maxTokenHistory)
	}
	if want := int64(7); got.TokenHistory[0] != want {
		t.Errorf("oldest kept point = %d, want %d", got.TokenHistory[0], want)
	}
}

func TestPiStoreDropsTheOldestAccumulation(t *testing.T) {
	dir := t.TempDir()
	store := &PiStore{}
	base := time.Now().Add(-time.Hour)
	paths := make([]string, 0, maxJSONLFiles+1)
	for i := 0; i <= maxJSONLFiles; i++ {
		path := filepath.Join(dir, fmt.Sprintf("session_%03d.jsonl", i))
		writePiFile(t, path, piHeader(t, "ses_a", piTestCWD))
		when := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		readPi(t, store, path)
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

	readPi(t, store, paths[0])
	if _, ok := store.files[paths[0]]; !ok {
		t.Error("the accumulation just read was evicted")
	}
}

func TestPiTranscriptDropsStateWhenTheFileVanishes(t *testing.T) {
	path := writePiSession(t, t.TempDir(), piHeader(t, "ses_a", piTestCWD))
	store := &PiStore{}
	readPi(t, store, path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transcript(path); err == nil {
		t.Fatal("Transcript() on a vanished session returned no error")
	}
	if _, ok := store.files[path]; ok {
		t.Error("state for a vanished session was kept")
	}
}

func TestPiPathForFlatSessionDir(t *testing.T) {
	// A configured sessionDir holds every cwd's session in one directory, so the cwd comes from
	// each file's header rather than from the directory name.
	root := t.TempDir()
	base := time.Now().Add(-time.Hour)
	shop := writePiFile(t, filepath.Join(root, "2026-11-24T09-00-00-000Z_ses_shop.jsonl"),
		piHeader(t, "ses_shop", piTestCWD))
	other := writePiFile(t, filepath.Join(root, "2026-11-24T10-00-00-000Z_ses_other.jsonl"),
		piHeader(t, "ses_other", "/home/dev/other"))
	if err := os.Chtimes(shop, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	store := &PiStore{Root: root}
	if got, ok := store.PathFor(piTestCWD, time.Time{}); !ok || got != shop {
		t.Errorf("PathFor(shop) = %q/%v, want %q despite the newer other-cwd file", got, ok, shop)
	}
	if _, ok := store.PathFor("/home/dev/missing", time.Time{}); ok {
		t.Error("PathFor found a session for a cwd with no header match")
	}
	if _, ok := store.PathFor(piTestCWD, base.Add(time.Minute)); ok {
		t.Error("PathFor accepted a flat session older than since")
	}
}

func TestPiPathForFindsTheNewestTranscript(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, encodePiPath(piTestCWD))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	oldPath := filepath.Join(dir, "2026-11-24T09-00-00-000Z_ses_old.jsonl")
	newPath := filepath.Join(dir, "2026-11-24T10-00-00-000Z_ses_new.jsonl")
	writePiFile(t, oldPath, piHeader(t, "ses_old", piTestCWD))
	writePiFile(t, newPath, piHeader(t, "ses_new", piTestCWD))
	if err := os.Chtimes(oldPath, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	store := &PiStore{Root: root}
	if got, ok := store.PathFor(piTestCWD, time.Time{}); !ok || got != newPath {
		t.Errorf("PathFor(no since) = %q/%v, want %q", got, ok, newPath)
	}
	if got, ok := store.PathFor(piTestCWD, base.Add(30*time.Second)); !ok || got != newPath {
		t.Errorf("PathFor(after old) = %q/%v, want %q", got, ok, newPath)
	}
	if _, ok := store.PathFor(piTestCWD, base.Add(2*time.Minute)); ok {
		t.Error("PathFor accepted a transcript older than since")
	}
	if _, ok := store.PathFor("/home/dev/other", time.Time{}); ok {
		t.Error("PathFor found a transcript in another directory")
	}
	if _, ok := (&PiStore{}).PathFor(piTestCWD, time.Time{}); ok {
		t.Error("PathFor found a transcript with no Root")
	}
	if _, ok := store.PathFor("", time.Time{}); ok {
		t.Error("PathFor found a transcript for an empty cwd")
	}
}

func TestPiPathForSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, encodePiPath(piTestCWD))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := writePiFile(t, filepath.Join(t.TempDir(), "target.jsonl"), piHeader(t, "ses_a", piTestCWD))
	link := filepath.Join(dir, "2026-11-24T10-00-00-000Z_ses_link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got, ok := (&PiStore{Root: root}).PathFor(piTestCWD, time.Time{}); ok {
		t.Errorf("PathFor = %q, want no transcript for a symlink", got)
	}
}

func TestEncodePiPath(t *testing.T) {
	cases := map[string]string{
		"/home/dev/shop":       "--home-dev-shop--",
		"/home/dev/shop/.work": "--home-dev-shop-.work--",
		`C:\work\shop`:         "--C--work-shop--",
		// pi drops one leading separator, not every one, so a UNC path keeps a leading hyphen.
		`\\server\share\shop`: "---server-share-shop--",
	}
	for cwd, want := range cases {
		if got := encodePiPath(cwd); got != want {
			t.Errorf("encodePiPath(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestPiSessionsPath(t *testing.T) {
	if got := PiSessionsPath("/home/dev"); got != "/home/dev/.pi/agent/sessions" {
		t.Errorf("PiSessionsPath = %q", got)
	}
	if got := PiSessionsPath(""); got != "" {
		t.Errorf("PiSessionsPath(\"\") = %q, want empty", got)
	}
}
