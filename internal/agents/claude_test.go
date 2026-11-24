package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Claude Code's transcript format is large and changes often, so these fixtures are built
// line by line from the fields the reader uses. Names, ids and prompts are invented.

const claudeTestCWD = "/home/dev/shop"

func claudeJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func claudeUserLine(t *testing.T, session, text string, extra map[string]any) string {
	t.Helper()
	v := map[string]any{
		"type":      "user",
		"sessionId": session,
		"cwd":       claudeTestCWD,
		"timestamp": "2026-11-24T10:00:00Z",
		"message":   map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}},
	}
	for k, extraValue := range extra {
		v[k] = extraValue
	}
	return claudeJSON(t, v)
}

func claudeToolResultLine(t *testing.T, session string) string {
	t.Helper()
	return claudeJSON(t, map[string]any{
		"type":      "user",
		"sessionId": session,
		"cwd":       claudeTestCWD,
		"timestamp": "2026-11-24T10:00:02Z",
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tool_1", "content": "ok"},
		}},
	})
}

func claudeAssistantLine(t *testing.T, session, id, model, text, tool string, usage map[string]any) string {
	t.Helper()
	content := []any{}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	if tool != "" {
		content = append(content, map[string]any{"type": "tool_use", "id": "tool_1", "name": tool,
			"input": map[string]any{"command": "go test ./..."}})
	}
	msg := map[string]any{"id": id, "model": model, "content": content}
	if usage != nil {
		msg["usage"] = usage
	}
	return claudeJSON(t, map[string]any{
		"type":      "assistant",
		"sessionId": session,
		"cwd":       claudeTestCWD,
		"timestamp": "2026-11-24T10:00:01Z",
		"message":   msg,
	})
}

func claudeUsageMap(in, out, cacheRead, cacheCreate int64) map[string]any {
	return map[string]any{
		"input_tokens":                in,
		"output_tokens":               out,
		"cache_read_input_tokens":     cacheRead,
		"cache_creation_input_tokens": cacheCreate,
	}
}

func writeClaudeTranscript(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "ses_a.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendClaudeTranscript(t *testing.T, path string, raw string) {
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

func readClaude(t *testing.T, store *ClaudeStore, path string) StoredSession {
	t.Helper()
	got, err := store.Transcript(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// One API response is written as several assistant lines, and every line carries the same
// usage. Counting per line doubles the real totals.
func TestClaudeTranscriptCountsOneResponseOnce(t *testing.T) {
	dir := t.TempDir()
	path := writeClaudeTranscript(t, dir,
		claudeUserLine(t, "ses_a", "Refactor the payment flow", nil),
		claudeAssistantLine(t, "ses_a", "msg_1", "claude-sonnet-5-5", "", "", claudeUsageMap(10, 5, 100, 20)),
		claudeAssistantLine(t, "ses_a", "msg_1", "claude-sonnet-5-5", "Done.", "Bash", claudeUsageMap(10, 5, 100, 20)),
		claudeToolResultLine(t, "ses_a"),
		claudeAssistantLine(t, "ses_a", "msg_2", "claude-sonnet-5-5", "All green.", "", claudeUsageMap(3, 7, 200, 0)),
		// Claude Code closes some sessions with a local message that never called the model.
		claudeJSON(t, map[string]any{
			"type": "assistant", "sessionId": "ses_a", "cwd": claudeTestCWD,
			"message": map[string]any{
				"id": "msg_local", "model": "<synthetic>",
				"content": []any{map[string]any{"type": "text", "text": "No response requested."}},
				"usage":   claudeUsageMap(0, 0, 0, 0),
			},
		}),
	)

	got := readClaude(t, &ClaudeStore{}, path)

	if want := (Tokens{Input: 13, Output: 12, CacheRead: 300, CacheCreate: 20}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.Model != "claude-sonnet-5-5" {
		t.Errorf("model = %q", got.Model)
	}
	if got.Title != "Refactor the payment flow" {
		t.Errorf("title = %q, want the first real prompt", got.Title)
	}
	if got.ID != "ses_a" || got.Dir != claudeTestCWD {
		t.Errorf("id/dir = %q/%q", got.ID, got.Dir)
	}
	if got.Context.Used != 203 || got.Context.Limit != 200_000 {
		t.Errorf("context = %+v, want 203/200000", got.Context)
	}
	if len(got.TokenHistory) != 2 {
		t.Fatalf("history = %v, want one point per response", got.TokenHistory)
	}
	if got.TokenHistory[0] != 135 || got.TokenHistory[1] != 210 {
		t.Errorf("history = %v, want per-response totals", got.TokenHistory)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after a closed turn", got.State)
	}
}

func TestClaudeTranscriptStateFollowsTheOpenTurn(t *testing.T) {
	dir := t.TempDir()
	path := writeClaudeTranscript(t, dir, claudeUserLine(t, "ses_a", "Add a health check", nil))
	store := &ClaudeStore{}

	if got := readClaude(t, store, path); got.State != StateThinking {
		t.Fatalf("state after a prompt = %q, want thinking", got.State)
	}

	appendClaudeTranscript(t, path, claudeAssistantLine(t, "ses_a", "msg_1", "claude-opus-5-5", "", "Bash", claudeUsageMap(1, 1, 0, 0))+"\n")
	if got := readClaude(t, store, path); got.State != StateExecuting {
		t.Fatalf("state after a tool call = %q, want executing", got.State)
	}

	// The tool finished and the model is being asked to answer it, so the turn is open again.
	appendClaudeTranscript(t, path, claudeToolResultLine(t, "ses_a")+"\n")
	if got := readClaude(t, store, path); got.State != StateThinking {
		t.Fatalf("state after a tool result = %q, want thinking", got.State)
	}

	appendClaudeTranscript(t, path, claudeAssistantLine(t, "ses_a", "msg_2", "claude-opus-5-5", "Shipping.", "", claudeUsageMap(2, 3, 10, 0))+"\n")
	if got := readClaude(t, store, path); got.State != StateWaiting {
		t.Fatalf("state after the reply = %q, want waiting", got.State)
	}
}

func TestClaudeTranscriptSkipsSyntheticUserLines(t *testing.T) {
	dir := t.TempDir()
	path := writeClaudeTranscript(t, dir,
		claudeUserLine(t, "ses_a", "You are a conversation title generator", map[string]any{"isMeta": true}),
		claudeJSON(t, map[string]any{
			"type": "user", "sessionId": "ses_a", "cwd": claudeTestCWD,
			"timestamp": "2026-11-24T10:00:00Z",
			"message":   map[string]any{"role": "user", "content": "<command-name>/plugin</command-name>"},
		}),
	)
	store := &ClaudeStore{}

	got := readClaude(t, store, path)
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting: no real prompt was sent", got.State)
	}
	if got.Title != "" {
		t.Errorf("title = %q, want empty", got.Title)
	}

	appendClaudeTranscript(t, path, claudeUserLine(t, "ses_a", "Now really fix the build", nil)+"\n")
	got = readClaude(t, store, path)
	if got.State != StateThinking {
		t.Errorf("state = %q, want thinking after a real prompt", got.State)
	}
	if got.Title != "Now really fix the build" {
		t.Errorf("title = %q", got.Title)
	}
}

func TestClaudeTranscriptPrefersTheRecordedTitle(t *testing.T) {
	cases := map[string]string{
		"ai-title": `{"type":"ai-title","sessionId":"ses_a","aiTitle":"Payments, cleaned up"}`,
		"summary":  `{"type":"summary","sessionId":"ses_a","summary":"Payments, cleaned up"}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeClaudeTranscript(t, dir,
				claudeUserLine(t, "ses_a", "long initial prompt that nobody wants to read", nil),
				line,
			)
			if got := readClaude(t, &ClaudeStore{}, path); got.Title != "Payments, cleaned up" {
				t.Errorf("title = %q", got.Title)
			}
		})
	}
}

func TestClaudeTranscriptReadsOnlyNewBytes(t *testing.T) {
	dir := t.TempDir()
	path := writeClaudeTranscript(t, dir,
		claudeUserLine(t, "ses_a", "Add metrics", nil),
		claudeAssistantLine(t, "ses_a", "msg_1", "claude-haiku-5-5", "Started.", "", claudeUsageMap(10, 1, 0, 0)),
	)
	store := &ClaudeStore{}

	if got := readClaude(t, store, path); got.Tokens.Input != 10 {
		t.Fatalf("first read input = %d, want 10", got.Tokens.Input)
	}

	appendClaudeTranscript(t, path, claudeAssistantLine(t, "ses_a", "msg_2", "claude-haiku-5-5", "More.", "", claudeUsageMap(2, 3, 0, 0))+"\n")
	if got := readClaude(t, store, path); got.Tokens.Input != 12 || got.Tokens.Output != 4 {
		t.Fatalf("second read = %+v, want 12/4", got.Tokens)
	}

	partial := claudeAssistantLine(t, "ses_a", "msg_3", "claude-haiku-5-5", "Last.", "", claudeUsageMap(7, 1, 0, 0))
	appendClaudeTranscript(t, path, partial)
	if got := readClaude(t, store, path); got.Tokens.Input != 12 {
		t.Fatalf("partial line counted: input = %d", got.Tokens.Input)
	}
	appendClaudeTranscript(t, path, "\n")
	got := readClaude(t, store, path)
	if got.Tokens.Input != 19 || got.Tokens.Output != 5 {
		t.Fatalf("completed line = %+v, want 19/5", got.Tokens)
	}

	// A read with nothing appended returns the same snapshot, and the history is a copy the
	// caller may keep.
	again := readClaude(t, store, path)
	if again.Tokens != got.Tokens || again.State != got.State || again.Title != got.Title {
		t.Errorf("no-change read = %+v, want %+v", again, got)
	}
	again.TokenHistory[0] = -1
	if fresh := readClaude(t, store, path); fresh.TokenHistory[0] != got.TokenHistory[0] {
		t.Errorf("history aliased the cached slice: %v", fresh.TokenHistory)
	}
}

func TestClaudeTranscriptReplacesAGrowingUsageSnapshot(t *testing.T) {
	path := writeClaudeTranscript(t, t.TempDir(),
		claudeUserLine(t, "ses_a", "Stream this", nil),
		claudeAssistantLine(t, "ses_a", "msg_1", "claude-haiku-5-5", "", "", claudeUsageMap(5, 1, 0, 0)),
		claudeAssistantLine(t, "ses_a", "msg_1", "claude-haiku-5-5", "Done.", "", claudeUsageMap(5, 9, 0, 0)),
	)

	got := readClaude(t, &ClaudeStore{}, path)
	if want := (Tokens{Input: 5, Output: 9}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the final snapshot %+v", got.Tokens, want)
	}
	if len(got.TokenHistory) != 1 || got.TokenHistory[0] != 14 {
		t.Errorf("history = %v, want one point of the final snapshot", got.TokenHistory)
	}
}

func TestClaudeTranscriptCountsEveryPromptToken(t *testing.T) {
	// Real responses mix a cache read with cache creation: the prompt is all three counts.
	path := writeClaudeTranscript(t, t.TempDir(),
		claudeUserLine(t, "ses_a", "Mixed caching", nil),
		claudeAssistantLine(t, "ses_a", "msg_1", "claude-opus-5-5", "Ok.", "", claudeUsageMap(2, 5, 11_863, 19_335)),
	)

	got := readClaude(t, &ClaudeStore{}, path)
	if got.Context.Used != 31_200 {
		t.Errorf("context used = %d, want 2 + 11863 + 19335", got.Context.Used)
	}
}

func TestClaudeTranscriptTreatsAnInterruptAsWaiting(t *testing.T) {
	path := writeClaudeTranscript(t, t.TempDir(),
		claudeUserLine(t, "ses_a", "Try this", nil),
		claudeAssistantLine(t, "ses_a", "msg_1", "claude-haiku-5-5", "Working.", "", claudeUsageMap(1, 1, 0, 0)),
		claudeUserLine(t, "ses_a", "[Request interrupted by user]", nil),
	)

	if got := readClaude(t, &ClaudeStore{}, path); got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after an interrupt", got.State)
	}
}

func TestClaudeTranscriptResetsWhenTheFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	path := writeClaudeTranscript(t, dir,
		claudeUserLine(t, "ses_a", "Old work", nil),
		claudeAssistantLine(t, "ses_a", "msg_old", "claude-haiku-5-5", "Old.", "", claudeUsageMap(100, 50, 0, 0)),
	)
	store := &ClaudeStore{}
	if got := readClaude(t, store, path); got.Tokens.Input != 100 {
		t.Fatalf("first read input = %d", got.Tokens.Input)
	}

	// Replace the file, inode and all, with a longer transcript that starts over. Size alone
	// would not catch this: the new file is bigger than the old offset.
	replacement := writeClaudeTranscript(t, t.TempDir(),
		claudeUserLine(t, "ses_b", "New work", nil),
		claudeAssistantLine(t, "ses_b", "msg_1", "claude-haiku-5-5", "One.", "", claudeUsageMap(1, 1, 0, 0)),
		claudeAssistantLine(t, "ses_b", "msg_2", "claude-haiku-5-5", "Two.", "", claudeUsageMap(2, 2, 0, 0)),
		claudeAssistantLine(t, "ses_b", "msg_3", "claude-haiku-5-5", "Three.", "", claudeUsageMap(3, 3, 0, 0)),
	)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	got := readClaude(t, store, path)
	if got.Tokens.Input != 6 || got.Tokens.Output != 6 {
		t.Errorf("tokens = %+v, want the replacement's 6/6", got.Tokens)
	}
	if got.ID != "ses_b" || got.Title != "New work" {
		t.Errorf("id/title = %q/%q, want the replacement", got.ID, got.Title)
	}
}

func TestClaudeTranscriptResetsWhenTheFileIsTruncatedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeClaudeTranscript(t, dir,
		claudeUserLine(t, "ses_a", "Old work", nil),
		claudeAssistantLine(t, "ses_a", "msg_old", "claude-haiku-5-5", "Old.", "", claudeUsageMap(100, 50, 0, 0)),
	)
	store := &ClaudeStore{}
	if got := readClaude(t, store, path); got.Tokens.Input != 100 {
		t.Fatalf("first read input = %d", got.Tokens.Input)
	}

	// Same inode, shorter content: os.WriteFile truncates in place, so only the size betrays it.
	writeClaudeTranscript(t, dir,
		claudeUserLine(t, "ses_a", "New work", nil),
		claudeAssistantLine(t, "ses_a", "msg_new", "claude-haiku-5-5", "New.", "", claudeUsageMap(1, 1, 0, 0)),
	)

	got := readClaude(t, store, path)
	if got.Tokens.Input != 1 || got.Tokens.Output != 1 {
		t.Errorf("tokens = %+v, want the truncated file's 1/1", got.Tokens)
	}
	if got.Title != "New work" {
		t.Errorf("title = %q, want the new prompt", got.Title)
	}
}

func TestClaudeTranscriptDropsStateWhenTheFileVanishes(t *testing.T) {
	path := writeClaudeTranscript(t, t.TempDir(), claudeUserLine(t, "ses_a", "Gone", nil))
	store := &ClaudeStore{}
	readClaude(t, store, path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transcript(path); err == nil {
		t.Fatal("a vanished transcript must return an error")
	}
	if _, ok := store.files[path]; ok {
		t.Error("cached state kept after the file vanished")
	}

	// Reappearing at the same path is a new file and must be read from the start.
	writeClaudeTranscript(t, filepath.Dir(path), claudeUserLine(t, "ses_b", "Back", nil))
	if got := readClaude(t, store, path); got.ID != "ses_b" {
		t.Errorf("id = %q, want the recreated file parsed from byte 0", got.ID)
	}
}

func TestClaudeTranscriptSkipsAnOverlongLine(t *testing.T) {
	dir := t.TempDir()
	// The oversized record is a valid prompt. If the reader kept it whole, it would parse and
	// own the title; truncating it at the cap makes the JSON invalid, so it is skipped.
	huge := claudeUserLine(t, "ses_a", strings.Repeat("x", maxJSONLLine), nil)
	path := writeClaudeTranscript(t, dir,
		huge,
		claudeUserLine(t, "ses_a", "Real prompt", nil),
		claudeAssistantLine(t, "ses_a", "msg_1", "claude-haiku-5-5", "Ok.", "", claudeUsageMap(4, 2, 0, 0)),
	)

	store := &ClaudeStore{}
	// The oversized line alone exceeds one read's byte budget, and a read that starts with the
	// budget spent still makes one line of progress, so several calls finish the file.
	for i := 0; i < 3; i++ {
		readClaude(t, store, path)
	}
	got := readClaude(t, store, path)
	if got.Tokens.Input != 4 || got.ID != "ses_a" {
		t.Errorf("tokens/id = %+v/%q, want the lines after the oversized one", got.Tokens, got.ID)
	}
	if got.Title != "Real prompt" {
		t.Errorf("title = %q, want the prompt after the oversized line", got.Title)
	}
}

func TestClaudeTranscriptKeepsTheTitleValidUTF8(t *testing.T) {
	dir := t.TempDir()
	path := writeClaudeTranscript(t, dir, claudeUserLine(t, "ses_a", strings.Repeat("é", 60), nil))

	got := readClaude(t, &ClaudeStore{}, path)
	if !utf8.ValidString(got.Title) {
		t.Fatalf("title is not valid UTF-8: %q", got.Title)
	}
	if n := len([]rune(got.Title)); n != 50 {
		t.Errorf("title rune length = %d, want 50", n)
	}
}

func TestClaudeTranscriptInfersTheOneMillionWindow(t *testing.T) {
	cases := map[string]struct {
		model string
		in    int64
	}{
		"observed above 200k": {"claude-sonnet-5-5", 250_001},
		"model marked 1m":     {"claude-sonnet-5-5[1m]", 1000},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeClaudeTranscript(t, t.TempDir(),
				claudeUserLine(t, "ses_a", "Huge context", nil),
				claudeAssistantLine(t, "ses_a", "msg_1", c.model, "Ok.", "", claudeUsageMap(c.in, 1, 0, 0)),
			)
			got := readClaude(t, &ClaudeStore{}, path)
			if got.Context.Used != c.in || got.Context.Limit != 1_000_000 {
				t.Errorf("context = %+v, want the 1M window", got.Context)
			}
		})
	}
}

func TestClaudeTranscriptBoundsHistory(t *testing.T) {
	dir := t.TempDir()
	lines := []string{claudeUserLine(t, "ses_a", "Long session", nil)}
	for i := 0; i < maxTokenHistory+5; i++ {
		lines = append(lines, claudeAssistantLine(t, "ses_a", fmt.Sprintf("msg_%d", i),
			"claude-haiku-5-5", "", "", claudeUsageMap(int64(i+1), 0, 0, 0)))
	}
	got := readClaude(t, &ClaudeStore{}, writeClaudeTranscript(t, dir, lines...))
	if len(got.TokenHistory) != maxTokenHistory {
		t.Fatalf("history length = %d, want %d", len(got.TokenHistory), maxTokenHistory)
	}
	if want := int64(6); got.TokenHistory[0] != want {
		t.Errorf("oldest kept point = %d, want %d", got.TokenHistory[0], want)
	}
}

func TestClaudePromptCleansAPastedPrompt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[Image #1] fix the login bug", "fix the login bug"},
		{"# Heading\n\nDo the thing", "Do the thing"},
		{"look [Image #2", "look"},
		{"first line\nsecond line", "first line second line"},
	}
	for _, c := range cases {
		if got := promptTitle(c.in); got != c.want {
			t.Errorf("promptTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClaudeTranscriptTreatsMixedContentAsAPrompt(t *testing.T) {
	path := writeClaudeTranscript(t, t.TempDir(), claudeJSON(t, map[string]any{
		"type": "user", "sessionId": "ses_a", "cwd": claudeTestCWD,
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok"},
			map[string]any{"type": "text", "text": "carry on"},
		}},
	}))

	got := readClaude(t, &ClaudeStore{}, path)
	if got.State != StateThinking {
		t.Errorf("state = %q, want thinking: mixed content is still a prompt", got.State)
	}
	if got.Title != "carry on" {
		t.Errorf("title = %q", got.Title)
	}
}

func TestClaudeStoreDropsTheOldestAccumulation(t *testing.T) {
	dir := t.TempDir()
	store := &ClaudeStore{}
	base := time.Now().Add(-time.Hour)
	paths := make([]string, 0, maxJSONLFiles+1)
	for i := 0; i <= maxJSONLFiles; i++ {
		path := filepath.Join(dir, fmt.Sprintf("ses_%03d.jsonl", i))
		if err := os.WriteFile(path, []byte(claudeUserLine(t, "ses_a", "Read me", nil)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		readClaude(t, store, path)
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
}

func TestClaudePathForFindsTheTranscript(t *testing.T) {
	root := t.TempDir()
	// The directory is Claude Code's real naming for this cwd, not something the encoder under
	// test computes: every separator, underscore and dot becomes a hyphen.
	cwd := "/home/dev/my_shop.v2"
	dir := filepath.Join(root, "-home-dev-my-shop-v2")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "ses_old.jsonl")
	latest := filepath.Join(dir, "ses_new.jsonl")
	for path, when := range map[string]time.Time{
		old:    time.Now().Add(-time.Hour),
		latest: time.Now(),
	} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := (&ClaudeStore{Root: root}).PathFor(cwd, time.Time{})
	if !ok || got != latest {
		t.Fatalf("PathFor = %q/%v, want %q", got, ok, latest)
	}

	// A worktree session lives under a directory keyed by the branch, not by the cwd.
	other := filepath.Join(root, "-home-dev-shop-worktrees-feature")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(other, "ses_worktree.jsonl")
	if err := os.WriteFile(worktree, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(worktree, future, future); err != nil {
		t.Fatal(err)
	}
	got, ok = (&ClaudeStore{Root: root}).PathFor("/tmp/no-such-project", time.Now())
	if !ok || got != worktree {
		t.Fatalf("fallback PathFor = %q/%v, want %q", got, ok, worktree)
	}

	if _, ok := (&ClaudeStore{Root: root}).PathFor(cwd, time.Now().Add(2*time.Hour)); ok {
		t.Error("a transcript older than since must not be returned")
	}
}

func TestClaudePathForRechecksTheNewestTranscript(t *testing.T) {
	root := t.TempDir()
	cwd := "/home/dev/shop"
	dir := filepath.Join(root, "-home-dev-shop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "ses_first.jsonl")
	if err := os.WriteFile(first, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &ClaudeStore{Root: root}

	if got, ok := store.PathFor(cwd, time.Time{}); !ok || got != first {
		t.Fatalf("PathFor = %q/%v, want %q", got, ok, first)
	}

	// /clear starts a new transcript in the same directory; the cached hit must follow it.
	second := filepath.Join(dir, "ses_second.jsonl")
	if err := os.WriteFile(second, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(second, future, future); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.PathFor(cwd, time.Time{}); !ok || got != second {
		t.Fatalf("PathFor = %q/%v, want the newer %q", got, ok, second)
	}

	// A cached transcript that disappears resolves again instead of being returned.
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.PathFor(cwd, time.Time{}); !ok || got != first {
		t.Fatalf("PathFor = %q/%v, want %q after removal", got, ok, first)
	}

	// A miss is remembered for the same since, and a new since re-walks.
	absent := "/tmp/absent-project"
	since := time.Now().Add(time.Hour)
	if got, ok := store.PathFor(absent, since); ok {
		t.Fatalf("found %q for a project with no transcript", got)
	}
	absentDir := filepath.Join(root, "-tmp-absent-project")
	if err := os.MkdirAll(absentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(absentDir, "ses_late.jsonl")
	if err := os.WriteFile(late, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	written := since.Add(time.Minute)
	if err := os.Chtimes(late, written, written); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.PathFor(absent, since); ok {
		t.Fatalf("cached miss re-walked and found %q", got)
	}
	if got, ok := store.PathFor(absent, since.Add(-time.Hour)); !ok || got != late {
		t.Fatalf("PathFor = %q/%v, want %q after a new since", got, ok, late)
	}
}

func TestClaudePathForSkipsSymlinks(t *testing.T) {
	target := writeClaudeTranscript(t, t.TempDir(), claudeUserLine(t, "ses_a", "Outside", nil))
	root := t.TempDir()
	dir := filepath.Join(root, "-home-dev-shop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "ses_link.jsonl")); err != nil {
		t.Fatal(err)
	}

	if got, ok := (&ClaudeStore{Root: root}).PathFor("/home/dev/shop", time.Time{}); ok {
		t.Fatalf("PathFor followed a symlink to %q", got)
	}
}
