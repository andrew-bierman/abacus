package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Qwen Code's transcript is a ChatRecord per line: type user/assistant/tool_result/system,
// ISO 8601 timestamp, cwd and sessionId on every record, message.parts for text, and
// usageMetadata plus contextWindowSize on assistant records. The layout is
// <qwen>/projects/<sanitized cwd>/chats/<sessionId>.jsonl from SessionService and
// Storage.getProjectDir. Fixtures here follow those shapes; every id, path and prompt is
// invented.

const (
	qwenTestCWD      = "/home/dev/atelier"
	qwenTestProject  = "-home-dev-atelier"
	qwenTestSession  = "7f3c9a1b2d4e4f6089ab01cd23ef4567"
	qwenOtherSession = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

func qwenJSON(t *testing.T, record map[string]any) string {
	t.Helper()
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func qwenBase(kind, sessionID, cwd, timestamp string) map[string]any {
	return map[string]any{
		"uuid":      "uuid-" + timestamp,
		"sessionId": sessionID,
		"timestamp": timestamp,
		"type":      kind,
		"cwd":       cwd,
		"version":   "fixture",
	}
}

func qwenUser(t *testing.T, timestamp, text string) string {
	t.Helper()
	record := qwenBase("user", qwenTestSession, qwenTestCWD, timestamp)
	record["provenance"] = "real_user"
	record["message"] = map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": text}},
	}
	return qwenJSON(t, record)
}

func qwenAssistant(t *testing.T, timestamp, text, model string, in, out, cached int) string {
	t.Helper()
	record := qwenBase("assistant", qwenTestSession, qwenTestCWD, timestamp)
	record["provenance"] = "assistant_output"
	record["message"] = map[string]any{
		"role":  "model",
		"parts": []any{map[string]any{"text": text}},
	}
	record["model"] = model
	record["usageMetadata"] = map[string]any{
		"promptTokenCount":        in,
		"candidatesTokenCount":    out,
		"cachedContentTokenCount": cached,
	}
	record["contextWindowSize"] = 200_000
	return qwenJSON(t, record)
}

func qwenToolResult(t *testing.T, timestamp string) string {
	t.Helper()
	record := qwenBase("tool_result", qwenTestSession, qwenTestCWD, timestamp)
	record["provenance"] = "tool_result"
	record["message"] = map[string]any{
		"role": "user",
		"parts": []any{map[string]any{
			"functionResponse": map[string]any{"name": "shelve"},
		}},
	}
	return qwenJSON(t, record)
}

func qwenSystem(t *testing.T, timestamp, subtype string, payload map[string]any) string {
	t.Helper()
	record := qwenBase("system", qwenTestSession, qwenTestCWD, timestamp)
	record["subtype"] = subtype
	record["provenance"] = "system"
	record["systemPayload"] = payload
	return qwenJSON(t, record)
}

func qwenSideRecord(t *testing.T, kind, subtype, provenance, timestamp, text string) string {
	t.Helper()
	record := qwenBase(kind, qwenTestSession, qwenTestCWD, timestamp)
	if subtype != "" {
		record["subtype"] = subtype
	}
	if provenance != "" {
		record["provenance"] = provenance
	}
	record["message"] = map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": text}},
	}
	record["systemPayload"] = map[string]any{"displayText": text}
	return qwenJSON(t, record)
}

func qwenSetMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func writeQwenSession(t *testing.T, chats, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(chats, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readQwen(t *testing.T, store *QwenStore, path string) StoredSession {
	t.Helper()
	got, err := store.Transcript(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestQwenTranscriptReadsTurnsAndUsage(t *testing.T) {
	chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenUser(t, "2026-11-24T10:00:01Z", "tidy the atelier"),
		qwenAssistant(t, "2026-11-24T10:00:02Z", "Checking the shelves.", "fixture-labs/kiln-1", 120, 30, 20),
		qwenToolResult(t, "2026-11-24T10:00:03Z"),
		qwenAssistant(t, "2026-11-24T10:00:04Z", "Shelves tidied.", "fixture-labs/kiln-2", 50, 8, 10),
	)

	got := readQwen(t, &QwenStore{}, path)

	if got.ID != qwenTestSession || got.Dir != qwenTestCWD || got.ParentID != "" {
		t.Errorf("identity = %q/%q/%q", got.ID, got.Dir, got.ParentID)
	}
	if got.Title != "tidy the atelier" {
		t.Errorf("title = %q, want the first prompt", got.Title)
	}
	if got.Model != "fixture-labs/kiln-2" {
		t.Errorf("model = %q, want the latest assistant model", got.Model)
	}
	// promptTokenCount includes cached content, so the adapter subtracts it once.
	if want := (Tokens{Input: 140, Output: 38, CacheRead: 30}); got.Tokens != want {
		t.Errorf("tokens = %+v, want each response summed %+v", got.Tokens, want)
	}
	if want := []int64{150, 58}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want one point per response %v", got.TokenHistory, want)
	}
	if want := (Context{Used: 50, Limit: 200_000}); got.Context != want {
		t.Errorf("context = %+v, want the latest prompt and window %+v", got.Context, want)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after the response", got.State)
	}
	if want := time.Date(2026, 11, 24, 10, 0, 4, 0, time.UTC); !got.Updated.Equal(want) {
		t.Errorf("updated = %v, want the last record %v", got.Updated, want)
	}
}

func TestQwenTranscriptUsesCustomTitleAndParent(t *testing.T) {
	chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenUser(t, "2026-11-24T10:00:01Z", "tidy the atelier"),
		qwenSystem(t, "2026-11-24T10:00:02Z", "custom_title", map[string]any{
			"customTitle": "First name",
			"titleSource": "manual",
		}),
		qwenSystem(t, "2026-11-24T10:00:03Z", "parent_session", map[string]any{
			"parentSessionId": "parent-atelier",
		}),
		// Qwen re-appends the current title at EOF so a tail read can find it; the latest
		// value wins.
		qwenSystem(t, "2026-11-24T10:00:04Z", "custom_title", map[string]any{
			"customTitle": "Atelier tidy-up",
			"titleSource": "auto",
		}),
	)

	got := readQwen(t, &QwenStore{}, path)

	if got.Title != "Atelier tidy-up" {
		t.Errorf("title = %q, want the latest custom title", got.Title)
	}
	if got.ParentID != "parent-atelier" {
		t.Errorf("parent = %q, want the parent_session payload", got.ParentID)
	}
}

func TestQwenTranscriptIgnoresUserRecordsThatAreNotTurns(t *testing.T) {
	chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		// Qwen writes notifications, cron prompts and agent traffic as type user. None of
		// them is a real prompt, a turn boundary or a title source.
		qwenSideRecord(t, "user", "notification", "system", "2026-11-24T10:00:01Z", "background task finished"),
		qwenSideRecord(t, "user", "cron", "system", "2026-11-24T10:00:02Z", "scheduled check"),
		qwenSideRecord(t, "user", "agent_mention", "external_agent", "2026-11-24T10:00:03Z", "teammate says hello"),
		qwenSideRecord(t, "user", "goal_runtime", "goal_runtime", "2026-11-24T10:00:04Z", "goal progress"),
		qwenUser(t, "2026-11-24T10:00:05Z", "dust the shelves"),
		qwenAssistant(t, "2026-11-24T10:00:06Z", "Done.", "fixture-labs/kiln-1", 10, 2, 0),
		qwenSideRecord(t, "user", "notification", "system", "2026-11-24T10:00:07Z", "background task finished"),
	)

	got := readQwen(t, &QwenStore{}, path)

	if got.Title != "dust the shelves" {
		t.Errorf("title = %q, want the first real prompt", got.Title)
	}
	if want := (Tokens{Input: 10, Output: 2}); got.Tokens != want {
		t.Errorf("tokens = %+v, want only the conversation response %+v", got.Tokens, want)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting: a notification is not a turn", got.State)
	}

	// A message typed while tools are running is a turn, but gives the list no title.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(qwenSideRecord(t, "user", "mid_turn_user_message", "real_user", "2026-11-24T10:00:08Z", "also the floor") + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readQwen(t, &QwenStore{}, path); got.State != StateThinking {
		t.Errorf("state = %q, want thinking after a mid-turn user message", got.State)
	}
}

func TestQwenTranscriptRejectsAUserRecordByProvenance(t *testing.T) {
	// A subtype-less user record is the only shape where the provenance gate decides: the
	// subtype gate admits it, so a disallowed provenance alone must keep it from titling the
	// session or opening a turn.
	chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenSideRecord(t, "user", "", "external_agent", "2026-11-24T10:00:01Z", "teammate says hello"),
		qwenUser(t, "2026-11-24T10:00:02Z", "dust the shelves"),
		qwenAssistant(t, "2026-11-24T10:00:03Z", "Done.", "fixture-labs/kiln-1", 10, 2, 0),
	)

	got := readQwen(t, &QwenStore{}, path)
	if got.Title != "dust the shelves" {
		t.Errorf("title = %q, want the first real prompt", got.Title)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after the response", got.State)
	}

	// With no later record, the rejected line must not leave the session thinking.
	only := writeQwenSession(t, chats, qwenOtherSession+".jsonl",
		qwenSideRecord(t, "user", "", "goal_runtime", "2026-11-24T10:00:04Z", "goal progress"),
	)
	if got := readQwen(t, &QwenStore{}, only); got.Title != "" || got.State != StateWaiting {
		t.Errorf("title/state = %q/%q, want no title and waiting from a rejected record", got.Title, got.State)
	}
}

func TestQwenTranscriptTitleIgnoresMidTurnMessages(t *testing.T) {
	chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenSideRecord(t, "user", "mid_turn_user_message", "real_user", "2026-11-24T10:00:01Z", "also the floor"),
		qwenAssistant(t, "2026-11-24T10:00:02Z", "Flushed the floor.", "fixture-labs/kiln-1", 10, 2, 0),
	)

	if got := readQwen(t, &QwenStore{}, path); got.Title != "" {
		t.Errorf("title = %q, want none from a mid-turn message", got.Title)
	}
}

func TestQwenTranscriptPrefersThePromptProjection(t *testing.T) {
	chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
	record := qwenBase("user", qwenTestSession, qwenTestCWD, "2026-11-24T10:00:01Z")
	record["provenance"] = "real_user"
	record["message"] = map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": "<expanded attachment> tidy the shelves"}},
	}
	record["systemPayload"] = map[string]any{"displayText": "tidy the shelves"}
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenJSON(t, record),
		qwenAssistant(t, "2026-11-24T10:00:02Z", "Done.", "fixture-labs/kiln-1", 10, 2, 0),
	)

	if got := readQwen(t, &QwenStore{}, path); got.Title != "tidy the shelves" {
		t.Errorf("title = %q, want the display projection", got.Title)
	}
}

func TestQwenTranscriptLabelsAModPromptLikeQwen(t *testing.T) {
	chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenSideRecord(t, "user", "", "mod_prompt", "2026-11-24T10:00:01Z", "apply the patch"),
		qwenAssistant(t, "2026-11-24T10:00:02Z", "Applied.", "fixture-labs/kiln-1", 10, 2, 0),
	)

	// Qwen's list reader labels a session from any subtype-less user record, including one
	// a Mod submitted.
	if got := readQwen(t, &QwenStore{}, path); got.Title != "apply the patch" {
		t.Errorf("title = %q, want the mod prompt's projection", got.Title)
	}
}

func TestQwenTranscriptFoldsAppendedRecords(t *testing.T) {
	chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenUser(t, "2026-11-24T10:00:01Z", "tidy the atelier"),
	)
	store := &QwenStore{}

	first := readQwen(t, store, path)
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
		qwenAssistant(t, "2026-11-24T10:00:02Z", "Working.", "fixture-labs/kiln-1", 20, 4, 0),
		qwenToolResult(t, "2026-11-24T10:00:03Z"),
	}, "\n") + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	second := readQwen(t, store, path)
	if want := (Tokens{Input: 20, Output: 4}); second.Tokens != want {
		t.Fatalf("tokens = %+v, want the appended response once %+v", second.Tokens, want)
	}
	if second.State != StateThinking {
		t.Fatalf("state after a tool result = %q, want thinking", second.State)
	}
}

func TestQwenTranscriptClosesTheTurnOnATurnResult(t *testing.T) {
	// Qwen settles an admitted prompt with a system turn_result record. An abort or a
	// pre-send refusal before the first assistant record leaves no assistant line to close
	// the turn, so the settled turn itself must.
	for _, state := range []string{"completed", "cancelled", "error"} {
		t.Run(state, func(t *testing.T) {
			chats := filepath.Join(t.TempDir(), qwenTestProject, "chats")
			path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
				qwenUser(t, "2026-11-24T10:00:01Z", "tidy the atelier"),
				qwenSystem(t, "2026-11-24T10:00:02Z", "turn_result", map[string]any{"state": state}),
			)
			if got := readQwen(t, &QwenStore{}, path); got.State != StateWaiting {
				t.Errorf("state = %q, want waiting after a %s turn", got.State, state)
			}
		})
	}
}

func TestQwenPathForResolvesTheSanitizedProject(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, qwenTestProject, "chats")
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	old := writeQwenSession(t, chats, qwenOtherSession+".jsonl",
		qwenUser(t, "2026-11-24T08:00:01Z", "yesterday's work"))
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenUser(t, "2026-11-24T10:00:01Z", "tidy the atelier"))
	qwenSetMtime(t, old, started.Add(-time.Hour))
	qwenSetMtime(t, path, started.Add(time.Minute))

	store := &QwenStore{Root: root}
	if got, ok := store.PathFor(qwenTestCWD, started); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want %q, true", got, ok, path)
	}
	if _, ok := store.PathFor(qwenTestCWD, started.Add(time.Hour)); ok {
		t.Errorf("PathFor adopted a transcript written before the window")
	}
	if _, ok := store.PathFor("/home/dev/other", started); ok {
		t.Errorf("PathFor found a session for another project")
	}
}

func TestQwenPathForSkipsACollidingProjectDirectory(t *testing.T) {
	// Qwen's sanitizer replaces every non-alphanumeric rune, so these two roots share one
	// project directory. The transcripts' own cwd tells them apart.
	root := t.TempDir()
	chats := filepath.Join(root, "-home-dev-a-b", "chats")
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	other := writeQwenSession(t, chats, qwenOtherSession+".jsonl", qwenJSON(t, map[string]any{
		"type": "user", "sessionId": qwenOtherSession, "cwd": "/home/dev/a_b",
		"timestamp": "2026-11-24T10:00:01Z", "provenance": "real_user",
		"message": map[string]any{"role": "user", "parts": []any{map[string]any{"text": "other root"}}},
	}))
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl", qwenJSON(t, map[string]any{
		"type": "user", "sessionId": qwenTestSession, "cwd": "/home/dev/a-b",
		"timestamp": "2026-11-24T10:00:00Z", "provenance": "real_user",
		"message": map[string]any{"role": "user", "parts": []any{map[string]any{"text": "our root"}}},
	}))
	qwenSetMtime(t, path, started.Add(time.Minute))
	qwenSetMtime(t, other, started.Add(2*time.Minute))

	store := &QwenStore{Root: root}
	if got, ok := store.PathFor("/home/dev/a-b", started); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want the colliding sibling skipped and %q chosen", got, ok, path)
	}
	if got, ok := store.PathFor("/home/dev/a_b", started); !ok || got != other {
		t.Errorf("PathFor for the sibling = %q, %v; want %q", got, ok, other)
	}
}

func TestQwenPathForKeepsACandidateWithoutACompleteHead(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, qwenTestProject, "chats")
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	older := writeQwenSession(t, chats, qwenOtherSession+".jsonl",
		qwenUser(t, "2026-11-24T10:00:01Z", "older session"))
	newest := filepath.Join(chats, qwenTestSession+".jsonl")
	if err := os.WriteFile(newest, []byte(`{"type":"user","sessionId":"`+qwenTestSession+`","cwd":"`), 0o600); err != nil {
		t.Fatal(err)
	}
	qwenSetMtime(t, older, started.Add(time.Minute))
	qwenSetMtime(t, newest, started.Add(2*time.Minute))

	// The newer file has no complete first record yet. It is still the session the live
	// process is writing, so it wins rather than falling back to the older transcript.
	store := &QwenStore{Root: root}
	if got, ok := store.PathFor(qwenTestCWD, started); !ok || got != newest {
		t.Errorf("PathFor = %q, %v; want the live transcript %q", got, ok, newest)
	}
}

func TestQwenHeadCacheSurvivesAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"cwd":"/home/dev/atelier"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"assistant"}` + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	grown, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !qwenHeadCurrent(first, grown) {
		t.Error("an append invalidated the cached head")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	shrunk, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if qwenHeadCurrent(first, shrunk) {
		t.Error("a truncation kept the cached head")
	}
	other := filepath.Join(dir, "other.jsonl")
	if err := os.WriteFile(other, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if qwenHeadCurrent(first, otherInfo) {
		t.Error("another file kept the cached head")
	}
}

func TestQwenPathForIgnoresSidecarsAndArchivedSessions(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, qwenTestProject, "chats")
	path := writeQwenSession(t, chats, qwenTestSession+".jsonl",
		qwenUser(t, "2026-11-24T10:00:00Z", "tidy the atelier"))
	// The prompt ledger and the archived transcript are not active sessions.
	writeQwenSession(t, chats, qwenTestSession+".ledger.jsonl", `{"kind":"prompt"}`)
	writeQwenSession(t, filepath.Join(chats, "archive"), qwenOtherSession+".jsonl",
		qwenUser(t, "2026-11-24T10:00:01Z", "old work"))

	store := &QwenStore{Root: root}
	if got, ok := store.PathFor(qwenTestCWD, time.Time{}); !ok || got != path {
		t.Errorf("PathFor = %q, %v; want the active transcript %q", got, ok, path)
	}
}

func TestQwenSessionsPathHonoursTheOverrides(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case "QWEN_RUNTIME_DIR":
			return "/home/dev/qwen-runtime"
		case "QWEN_HOME":
			return "/home/dev/qwen-home"
		}
		return ""
	}
	// The runtime base owns project directories, and QWEN_HOME only supplies its default.
	if got, want := QwenSessionsPathFrom("/home/dev", getenv), "/home/dev/qwen-runtime/projects"; got != want {
		t.Errorf("runtime path = %q, want %q", got, want)
	}
	qwenHomeOnly := func(key string) string {
		if key == "QWEN_HOME" {
			return "/home/dev/qwen-home"
		}
		return ""
	}
	if got, want := QwenSessionsPathFrom("/home/dev", qwenHomeOnly), "/home/dev/qwen-home/projects"; got != want {
		t.Errorf("home path = %q, want %q", got, want)
	}
	if got, want := QwenSessionsPathFrom("/home/dev", func(string) string { return "" }), "/home/dev/.qwen/projects"; got != want {
		t.Errorf("default path = %q, want %q", got, want)
	}
	if got := QwenSessionsPathFrom("", getenv); got != "" {
		t.Errorf("path for an unknown home = %q, want empty", got)
	}
}

func TestQwenProjectIDMatchesTheSanitizer(t *testing.T) {
	if got := qwenProjectID("/home/dev/atelier"); got != qwenTestProject {
		t.Errorf("project id = %q, want %q", got, qwenTestProject)
	}
	if got := qwenProjectID("/home/dev/a_b"); got != "-home-dev-a-b" {
		t.Errorf("project id = %q, want punctuation folded", got)
	}
}

func TestQwenTrimHeadsEvictsTheOldest(t *testing.T) {
	store := &QwenStore{heads: make(map[string]qwenHead)}
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	for i := 0; i <= maxQwenHeads; i++ {
		store.heads[fmt.Sprintf("/session/%d", i)] = qwenHead{checked: base.Add(time.Duration(i) * time.Second)}
	}
	store.trimHeads("/session/0")
	if len(store.heads) != maxQwenHeads {
		t.Fatalf("heads = %d, want %d", len(store.heads), maxQwenHeads)
	}
	if _, ok := store.heads["/session/1"]; ok {
		t.Errorf("the oldest head survived eviction")
	}
}
