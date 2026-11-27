package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// GitHub Copilot CLI writes one event log per session under
// <COPILOT_HOME>/session-state/<sessionId>/events.jsonl (flat <id>.jsonl before 1.0.11), with a
// workspace.yaml sidecar. Each line is {type, data, timestamp, parentId}; the event types and
// token fields used here follow the Copilot SDK streaming-events reference and the observed
// session.shutdown modelMetrics shape. Every id, path and prompt below is invented.

const copilotTestCWD = "/home/dev/atelier"

func copilotJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func copilotEventLine(t *testing.T, typ, at string, data map[string]any) string {
	t.Helper()
	return copilotJSON(t, map[string]any{
		"type": typ, "timestamp": at, "id": "evt_" + typ, "parentId": nil, "data": data,
	})
}

func copilotUsageData(model string, in, out, cacheRead, cacheWrite int64) map[string]any {
	return map[string]any{
		"model": model, "inputTokens": in, "outputTokens": out,
		"cacheReadTokens": cacheRead, "cacheWriteTokens": cacheWrite,
	}
}

func copilotSetMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func writeCopilotFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCopilotSession(t *testing.T, root, id, workspace string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if workspace != "" {
		writeCopilotFile(t, filepath.Join(dir, "workspace.yaml"), workspace)
	}
	path := filepath.Join(dir, "events.jsonl")
	writeCopilotFile(t, path, strings.Join(lines, "\n")+"\n")
	return path
}

func appendCopilotLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readCopilot(t *testing.T, store *CopilotStore, path string) StoredSession {
	t.Helper()
	got, err := store.Transcript(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCopilotTranscriptReadsEventsAndShutdownTotals(t *testing.T) {
	root := t.TempDir()
	path := writeCopilotSession(t, root, "ses_atelier", strings.Join([]string{
		"id: ses_atelier",
		"cwd: " + copilotTestCWD,
		`name: "Atelier sidecar"`,
		"updated_at: 2026-11-24T10:00:13Z",
	}, "\n")+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_atelier", "selectedModel": "fixture-labs/copilot-1",
			"context": map[string]any{"cwd": copilotTestCWD},
		}),
		copilotEventLine(t, "user.message", "2026-11-24T10:00:02Z", map[string]any{
			"content": "tidy the atelier",
		}),
		copilotEventLine(t, "assistant.turn_start", "2026-11-24T10:00:03Z", map[string]any{
			"model": "fixture-labs/copilot-1",
		}),
		copilotEventLine(t, "assistant.message", "2026-11-24T10:00:04Z", map[string]any{
			"content": "Checking the shelves.", "model": "fixture-labs/copilot-1", "outputTokens": 30,
		}),
		copilotEventLine(t, "tool.execution_start", "2026-11-24T10:00:05Z", map[string]any{
			"toolCallId": "tool_shelf", "toolName": "bash",
		}),
		copilotEventLine(t, "tool.execution_complete", "2026-11-24T10:00:06Z", map[string]any{
			"toolCallId": "tool_shelf", "success": true,
		}),
		copilotEventLine(t, "assistant.turn_end", "2026-11-24T10:00:07Z", map[string]any{
			"turnId": "1",
		}),
		copilotEventLine(t, "assistant.usage", "2026-11-24T10:00:08Z",
			copilotUsageData("fixture-labs/copilot-1", 120, 30, 20, 10)),
		copilotEventLine(t, "session.model_change", "2026-11-24T10:00:09Z", map[string]any{
			"newModel": "fixture-labs/copilot-2",
		}),
		copilotEventLine(t, "session.task_complete", "2026-11-24T10:00:10Z", map[string]any{
			"summary": "Atelier shelves",
		}),
		copilotEventLine(t, "session.shutdown", "2026-11-24T10:00:12Z", map[string]any{
			"currentModel": "fixture-labs/copilot-2", "currentTokens": 300,
			"modelMetrics": map[string]any{
				"fixture-labs/copilot-2": map[string]any{
					"usage": map[string]any{
						"inputTokens": 300, "outputTokens": 60,
						"cacheReadTokens": 80, "cacheWriteTokens": 20,
					},
				},
			},
		}),
	)
	copilotSetMtime(t, path, time.Date(2026, 11, 24, 10, 0, 12, 0, time.UTC))

	got := readCopilot(t, &CopilotStore{Root: root}, path)

	if got.ID != "ses_atelier" || got.Dir != copilotTestCWD {
		t.Errorf("identity = %q/%q", got.ID, got.Dir)
	}
	if got.Title != "Atelier sidecar" {
		t.Errorf("title = %q, want the workspace name", got.Title)
	}
	if got.Model != "fixture-labs/copilot-2" {
		t.Errorf("model = %q, want the latest model change", got.Model)
	}
	if want := (Tokens{Input: 200, Output: 60, CacheRead: 80, CacheCreate: 20}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the shutdown rollup only %+v", got.Tokens, want)
	}
	if want := []int64{360}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want one shutdown point %v", got.TokenHistory, want)
	}
	if want := (Context{Used: 300}); got.Context != want {
		t.Errorf("context = %+v, want the shutdown context %+v", got.Context, want)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after the turn ended", got.State)
	}
	if want := time.Date(2026, 11, 24, 10, 0, 12, 0, time.UTC); !got.Updated.Equal(want) {
		t.Errorf("updated = %v, want the last event %v", got.Updated, want)
	}
}

func TestCopilotTranscriptFallsBackToCallUsage(t *testing.T) {
	root := t.TempDir()
	path := writeCopilotSession(t, root, "ses_loom", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_loom", "context": map[string]any{"cwd": copilotTestCWD},
		}),
		copilotEventLine(t, "user.message", "2026-11-24T10:00:02Z", map[string]any{
			"content": "weave the linen",
		}),
		copilotEventLine(t, "assistant.message", "2026-11-24T10:00:03Z", map[string]any{
			"content": "Weaving.", "outputTokens": 10,
		}),
		copilotEventLine(t, "assistant.usage", "2026-11-24T10:00:04Z",
			copilotUsageData("fixture-labs/loom-1", 100, 20, 30, 10)),
		copilotEventLine(t, "assistant.usage", "2026-11-24T10:00:05Z",
			copilotUsageData("fixture-labs/loom-1", 50, 8, 20, 0)),
		copilotEventLine(t, "assistant.turn_end", "2026-11-24T10:00:06Z", map[string]any{
			"turnId": "1",
		}),
		copilotEventLine(t, "session.shutdown", "2026-11-24T10:00:07Z", map[string]any{
			"modelMetrics": map[string]any{},
		}),
	)

	got := readCopilot(t, &CopilotStore{Root: root}, path)

	if got.Title != "weave the linen" {
		t.Errorf("title = %q, want the first prompt without a workspace name", got.Title)
	}
	if want := (Tokens{Input: 90, Output: 28, CacheRead: 50, CacheCreate: 10}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the per-call rows %+v", got.Tokens, want)
	}
	if want := []int64{120, 58}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want one point per call %v", got.TokenHistory, want)
	}
	if want := (Context{Used: 50}); got.Context != want {
		t.Errorf("context = %+v, want the last call's gross input %+v", got.Context, want)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after the turn ended", got.State)
	}
}

func TestCopilotTranscriptFoldsResumedShutdowns(t *testing.T) {
	root := t.TempDir()
	shutdown := func(at string, in, out int64) string {
		return copilotEventLine(t, "session.shutdown", at, map[string]any{
			"modelMetrics": map[string]any{
				"fixture-labs/kiln-1": map[string]any{
					"usage": map[string]any{"inputTokens": in, "outputTokens": out},
				},
			},
		})
	}
	path := writeCopilotSession(t, root, "ses_kiln", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_kiln", "context": map[string]any{"cwd": copilotTestCWD},
		}),
		shutdown("2026-11-24T10:00:02Z", 100, 10),
		// Resuming the session appends another cumulative rollup; only the difference is new.
		shutdown("2026-11-24T10:05:00Z", 160, 14),
		// A counter that goes backwards starts new accounting and counts in full.
		shutdown("2026-11-24T10:10:00Z", 100, 10),
		// A repeated identical rollup adds no point.
		shutdown("2026-11-24T10:15:00Z", 100, 10),
	)

	got := readCopilot(t, &CopilotStore{Root: root}, path)

	if want := (Tokens{Input: 260, Output: 24}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the cumulative levels plus the reset %+v", got.Tokens, want)
	}
	if want := []int64{110, 64, 110}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want one point per shutdown delta and a full reset point %v", got.TokenHistory, want)
	}
}

func TestCopilotTranscriptTracksTurnStateIncrementally(t *testing.T) {
	root := t.TempDir()
	path := writeCopilotSession(t, root, "ses_forge", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_forge", "context": map[string]any{"cwd": copilotTestCWD},
		}),
		copilotEventLine(t, "user.message", "2026-11-24T10:00:02Z", map[string]any{
			"content": "shape the bracket",
		}),
	)
	store := &CopilotStore{Root: root}

	first := readCopilot(t, store, path)
	if first.State != StateThinking {
		t.Fatalf("state after a prompt = %q, want thinking", first.State)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join([]string{
		copilotEventLine(t, "tool.execution_start", "2026-11-24T10:00:03Z", map[string]any{
			"toolCallId": "tool_forge", "toolName": "bash",
		}),
	}, "\n") + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readCopilot(t, store, path); got.State != StateExecuting {
		t.Fatalf("state after a tool started = %q, want executing", got.State)
	}

	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join([]string{
		copilotEventLine(t, "tool.execution_complete", "2026-11-24T10:00:04Z", map[string]any{
			"toolCallId": "tool_forge", "success": true,
		}),
		copilotEventLine(t, "assistant.turn_end", "2026-11-24T10:00:05Z", map[string]any{
			"turnId": "1",
		}),
	}, "\n") + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readCopilot(t, store, path); got.State != StateWaiting {
		t.Errorf("state after the turn ended = %q, want waiting", got.State)
	}
}

func TestCopilotTranscriptFallsBackToMessageOutputAndUsageInfo(t *testing.T) {
	root := t.TempDir()
	path := writeCopilotSession(t, root, "ses_lantern", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_lantern", "context": map[string]any{"cwd": copilotTestCWD},
		}),
		copilotEventLine(t, "user.message", "2026-11-24T10:00:02Z", map[string]any{
			"content": "light the lantern",
		}),
		copilotEventLine(t, "assistant.message", "2026-11-24T10:00:03Z", map[string]any{
			"content": "Lit.", "outputTokens": 12,
		}),
		copilotEventLine(t, "session.usage_info", "2026-11-24T10:00:04Z", map[string]any{
			"currentTokens": 512, "tokenLimit": 200000,
		}),
		copilotEventLine(t, "assistant.turn_end", "2026-11-24T10:00:05Z", map[string]any{
			"turnId": "1",
		}),
	)

	got := readCopilot(t, &CopilotStore{Root: root}, path)

	if want := (Tokens{Output: 12}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the message's output fallback %+v", got.Tokens, want)
	}
	if want := []int64{12}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want the message point %v", got.TokenHistory, want)
	}
	if want := (Context{Used: 512, Limit: 200000}); got.Context != want {
		t.Errorf("context = %+v, want the usage-info window %+v", got.Context, want)
	}
}

func TestCopilotTranscriptPrefersTheLargerUsageSource(t *testing.T) {
	root := t.TempDir()
	path := writeCopilotSession(t, root, "ses_resumed", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_resumed", "context": map[string]any{"cwd": copilotTestCWD},
		}),
		copilotEventLine(t, "session.shutdown", "2026-11-24T10:00:02Z", map[string]any{
			"modelMetrics": map[string]any{
				"fixture-labs/kiln-1": map[string]any{
					"usage": map[string]any{"inputTokens": 100, "outputTokens": 10},
				},
			},
		}),
		copilotEventLine(t, "assistant.usage", "2026-11-24T10:05:00Z",
			copilotUsageData("fixture-labs/kiln-1", 200, 20, 0, 0)),
	)

	got := readCopilot(t, &CopilotStore{Root: root}, path)

	if want := (Tokens{Input: 200, Output: 20}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the larger per-call lifetime %+v", got.Tokens, want)
	}
	if want := []int64{220}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want the per-call point %v", got.TokenHistory, want)
	}
}

func TestCopilotTranscriptFoldsMultipleModelShutdowns(t *testing.T) {
	root := t.TempDir()
	usage := func(in, out int64) map[string]any {
		return map[string]any{
			"usage": map[string]any{"inputTokens": in, "outputTokens": out},
		}
	}
	path := writeCopilotSession(t, root, "ses_kiln", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_kiln", "context": map[string]any{"cwd": copilotTestCWD},
		}),
		copilotEventLine(t, "session.shutdown", "2026-11-24T10:00:02Z", map[string]any{
			"modelMetrics": map[string]any{"fixture-labs/kiln-1": usage(100, 10)},
		}),
		copilotEventLine(t, "session.shutdown", "2026-11-24T10:05:00Z", map[string]any{
			"modelMetrics": map[string]any{
				"fixture-labs/kiln-1": usage(160, 14),
				"fixture-labs/kiln-2": usage(40, 4),
			},
		}),
	)

	got := readCopilot(t, &CopilotStore{Root: root}, path)

	if want := (Tokens{Input: 200, Output: 18}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the first model's delta plus the new model %+v", got.Tokens, want)
	}
	if want := []int64{110, 108}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want one point per shutdown %v", got.TokenHistory, want)
	}
}

func TestCopilotTranscriptClearsInterruptedToolCalls(t *testing.T) {
	root := t.TempDir()
	path := writeCopilotSession(t, root, "ses_forge", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_forge", "context": map[string]any{"cwd": copilotTestCWD},
		}),
		copilotEventLine(t, "user.message", "2026-11-24T10:00:02Z", map[string]any{
			"content": "shape the bracket",
		}),
		copilotEventLine(t, "tool.execution_start", "2026-11-24T10:00:03Z", map[string]any{
			"toolCallId": "tool_forge", "toolName": "bash",
		}),
	)
	store := &CopilotStore{Root: root}
	if got := readCopilot(t, store, path); got.State != StateExecuting {
		t.Fatalf("state with a running tool = %q, want executing", got.State)
	}

	appendCopilotLines(t, path, copilotEventLine(t, "assistant.turn_end", "2026-11-24T10:00:04Z", map[string]any{
		"turnId": "1",
	}))
	if got := readCopilot(t, store, path); got.State != StateWaiting {
		t.Errorf("state after an interrupted turn = %q, want waiting", got.State)
	}

	appendCopilotLines(t, path,
		copilotEventLine(t, "tool.execution_start", "2026-11-24T10:00:05Z", map[string]any{
			"toolName": "grep",
		}),
	)
	if got := readCopilot(t, store, path); got.State != StateExecuting {
		t.Fatalf("state with an anonymous tool = %q, want executing", got.State)
	}
	appendCopilotLines(t, path,
		copilotEventLine(t, "tool.execution_complete", "2026-11-24T10:00:06Z", map[string]any{
			"success": true,
		}),
		copilotEventLine(t, "assistant.turn_end", "2026-11-24T10:00:07Z", map[string]any{
			"turnId": "2",
		}),
	)
	if got := readCopilot(t, store, path); got.State != StateWaiting {
		t.Errorf("state after the anonymous tool completed = %q, want waiting", got.State)
	}

	appendCopilotLines(t, path,
		copilotEventLine(t, "tool.execution_start", "2026-11-24T10:00:08Z", map[string]any{
			"toolCallId": "tool_second", "toolName": "bash",
		}),
		copilotEventLine(t, "user.message", "2026-11-24T10:00:09Z", map[string]any{
			"content": "try again",
		}),
	)
	if got := readCopilot(t, store, path); got.State != StateThinking {
		t.Errorf("state after a new prompt cleared the dangling call = %q, want thinking", got.State)
	}
}

func TestCopilotTranscriptNamesAFlatLegacySession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ses_legacy.jsonl")
	writeCopilotFile(t, path, strings.Join([]string{
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"context": map[string]any{"cwd": copilotTestCWD},
		}),
		copilotEventLine(t, "user.message", "2026-11-24T10:00:02Z", map[string]any{
			"content": "tidy the loft",
		}),
		copilotEventLine(t, "session.task_complete", "2026-11-24T10:00:03Z", map[string]any{
			"summary": "Loft tidied",
		}),
	}, "\n")+"\n")

	got := readCopilot(t, &CopilotStore{Root: root}, path)

	if got.ID != "ses_legacy" || got.Dir != copilotTestCWD {
		t.Errorf("identity = %q/%q", got.ID, got.Dir)
	}
	if got.Title != "Loft tidied" {
		t.Errorf("title = %q, want the task summary", got.Title)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting", got.State)
	}
}

func TestCopilotTranscriptReloadsARewrittenWorkspace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ses_atelier")
	path := writeCopilotSession(t, root, "ses_atelier", "cwd: "+copilotTestCWD+"\nname: First title\n",
		copilotEventLine(t, "session.start", "2026-11-24T10:00:01Z", map[string]any{
			"sessionId": "ses_atelier", "context": map[string]any{"cwd": copilotTestCWD},
		}),
	)
	store := &CopilotStore{Root: root}
	if got := readCopilot(t, store, path); got.Title != "First title" {
		t.Fatalf("title = %q, want the first workspace name", got.Title)
	}

	writeCopilotFile(t, filepath.Join(dir, "workspace.yaml"),
		"cwd: "+copilotTestCWD+"\nname: Second title, longer\n")
	if got := readCopilot(t, store, path); got.Title != "Second title, longer" {
		t.Errorf("title = %q, want the rewritten workspace name", got.Title)
	}
}

func TestCopilotTrimOldestEvictsTheOldest(t *testing.T) {
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	cwds := map[string]copilotCWD{
		"/old":    {checked: base},
		"/middle": {checked: base.Add(time.Second)},
		"/new":    {checked: base.Add(2 * time.Second)},
	}
	trimCopilotOldest(cwds, 2, "/new", func(c copilotCWD) time.Time { return c.checked })
	if len(cwds) != 2 {
		t.Fatalf("cwds = %d, want 2", len(cwds))
	}
	if _, ok := cwds["/old"]; ok {
		t.Errorf("the oldest entry survived eviction")
	}
}

func TestCopilotPathForUsesTheWorkspaceCwd(t *testing.T) {
	root := t.TempDir()
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	old := writeCopilotSession(t, root, "ses_old", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T08:00:00Z", map[string]any{
			"sessionId": "ses_old", "context": map[string]any{"cwd": copilotTestCWD},
		}),
	)
	newest := writeCopilotSession(t, root, "ses_new", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T09:05:00Z", map[string]any{
			"sessionId": "ses_new", "context": map[string]any{"cwd": copilotTestCWD},
		}),
	)
	other := writeCopilotSession(t, root, "ses_other", "cwd: /home/dev/other\n",
		copilotEventLine(t, "session.start", "2026-11-24T09:10:00Z", map[string]any{
			"sessionId": "ses_other", "context": map[string]any{"cwd": "/home/dev/other"},
		}),
	)
	copilotSetMtime(t, old, started.Add(-time.Hour))
	copilotSetMtime(t, newest, started.Add(5*time.Minute))
	copilotSetMtime(t, other, started.Add(10*time.Minute))

	store := &CopilotStore{Root: root}
	if got, ok := store.PathFor(copilotTestCWD, started); !ok || got != newest {
		t.Errorf("PathFor = %q, %v; want %q, true", got, ok, newest)
	}
	if _, ok := store.PathFor(copilotTestCWD, started.Add(time.Hour)); ok {
		t.Errorf("PathFor adopted a transcript written before the window")
	}
	if got, ok := store.PathFor("/home/dev/other", started); !ok || got != other {
		t.Errorf("PathFor other = %q, %v; want %q, true", got, ok, other)
	}
	if _, ok := store.PathFor("/home/dev/unknown", started); ok {
		t.Errorf("PathFor found a session for an unknown project")
	}
}

func TestCopilotPathForReadsAFlatLegacyTranscript(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ses_legacy.jsonl")
	writeCopilotFile(t, path, copilotEventLine(t, "session.start", "2026-11-24T09:00:00Z", map[string]any{
		"sessionId": "ses_legacy", "context": map[string]any{"cwd": copilotTestCWD},
	})+"\n")
	copilotSetMtime(t, path, time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC))

	store := &CopilotStore{Root: root}
	if got, ok := store.PathFor(copilotTestCWD, time.Time{}); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want the flat legacy session %q", got, ok, path)
	}
}

func TestCopilotPathForPicksUpANewSessionAfterACachedHit(t *testing.T) {
	root := t.TempDir()
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	first := writeCopilotSession(t, root, "ses_first", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T09:01:00Z", map[string]any{
			"sessionId": "ses_first", "context": map[string]any{"cwd": copilotTestCWD},
		}),
	)
	copilotSetMtime(t, first, started.Add(time.Minute))
	if err := os.Chtimes(root, started.Add(time.Minute), started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	store := &CopilotStore{Root: root}
	if got, ok := store.PathFor(copilotTestCWD, started); !ok || got != first {
		t.Fatalf("PathFor = %q, %v; want the first session %q", got, ok, first)
	}

	newest := writeCopilotSession(t, root, "ses_second", "cwd: "+copilotTestCWD+"\n",
		copilotEventLine(t, "session.start", "2026-11-24T09:05:00Z", map[string]any{
			"sessionId": "ses_second", "context": map[string]any{"cwd": copilotTestCWD},
		}),
	)
	copilotSetMtime(t, newest, started.Add(5*time.Minute))
	if err := os.Chtimes(root, started.Add(5*time.Minute), started.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.PathFor(copilotTestCWD, started); !ok || got != newest {
		t.Errorf("PathFor after a cached hit = %q, %v; want the newer session %q", got, ok, newest)
	}
}

func TestCopilotSessionsPathHonoursTheCLIHome(t *testing.T) {
	withHome := func(key string) string {
		if key == "COPILOT_HOME" {
			return "/tmp/fixture-copilot"
		}
		return ""
	}
	if got, want := CopilotSessionsPathFrom("/home/dev", withHome), "/tmp/fixture-copilot/session-state"; got != want {
		t.Errorf("CopilotSessionsPathFrom with COPILOT_HOME = %q, want %q", got, want)
	}
	if got, want := CopilotSessionsPathFrom("/home/dev", func(string) string { return "" }), "/home/dev/.copilot/session-state"; got != want {
		t.Errorf("CopilotSessionsPathFrom without COPILOT_HOME = %q, want %q", got, want)
	}
	if got := CopilotSessionsPathFrom("", withHome); got != "" {
		t.Errorf("CopilotSessionsPathFrom empty home = %q, want empty", got)
	}
}
