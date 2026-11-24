package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const jsonlSpecTestCWD = "/home/dev/warehouse"

func jsonlSpecJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type jsonlSpecCounts struct {
	in, out, read, write int64
}

func jsonlSpecRecord(t *testing.T, timestamp, kind string, body any, usage *jsonlSpecCounts) string {
	t.Helper()
	event := map[string]any{"kind": kind, "body": body}
	if usage != nil {
		event["usage"] = map[string]any{
			"in": usage.in, "out": usage.out, "read": usage.read, "write": usage.write,
		}
	}
	return jsonlSpecJSON(t, map[string]any{
		"meta": map[string]any{
			"timestamp": timestamp,
			"id":        "ses_quartz",
			"cwd":       jsonlSpecTestCWD,
			"model":     "fixture-labs/kiln-1",
			"parent":    "ses_granite",
			"window":    200000,
		},
		"event": event,
	})
}

func jsonlSpecMapping() JSONLMapping {
	return JSONLMapping{
		Timestamp: "meta.timestamp",
		Role:      "event.kind",
		Roles: map[string]JSONLRole{
			"human":   JSONLRoleUser,
			"bot":     JSONLRoleAssistant,
			"command": JSONLRoleTool,
		},
		Text:         "event.body",
		Model:        "meta.model",
		ID:           "meta.id",
		Dir:          "meta.cwd",
		ParentID:     "meta.parent",
		Title:        "meta.title",
		Input:        "event.usage.in",
		Output:       "event.usage.out",
		CacheRead:    "event.usage.read",
		CacheCreate:  "event.usage.write",
		Usage:        JSONLUsageAdditive,
		ContextLimit: "meta.window",
	}
}

func writeJSONLSpecFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ses_quartz.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendJSONLSpecFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func readJSONLSpec(t *testing.T, store *JSONLStore, path string) StoredSession {
	t.Helper()
	got, err := store.Transcript(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestJSONLSpecReadsMappedTranscript(t *testing.T) {
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", "Fix the failing test", nil),
		jsonlSpecRecord(t, "2026-11-24T10:00:01Z", "bot",
			[]any{map[string]any{"type": "text", "text": "Checking the suite."}},
			&jsonlSpecCounts{in: 100, out: 20, read: 10, write: 5}),
		jsonlSpecRecord(t, "2026-11-24T10:00:02Z", "bot",
			[]any{map[string]any{"type": "text", "text": "Fixed it."}},
			&jsonlSpecCounts{in: 40, out: 8, read: 7, write: 3}),
		jsonlSpecJSON(t, map[string]any{
			"meta": map[string]any{
				"timestamp": "2026-11-24T10:00:02Z",
				"id":        "ses_other", "cwd": "/elsewhere", "parent": "ses_other_parent",
				"model": "fixture-labs/kiln-2",
			},
			"event": map[string]any{"kind": "bot", "body": "A resumed header."},
		}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: jsonlSpecMapping()}, path)

	if got.ID != "ses_quartz" || got.Dir != jsonlSpecTestCWD || got.ParentID != "ses_granite" {
		t.Errorf("identity = %q/%q/%q", got.ID, got.Dir, got.ParentID)
	}
	if got.Model != "fixture-labs/kiln-2" {
		t.Errorf("model = %q, want the latest model", got.Model)
	}
	if got.Title != "Fix the failing test" {
		t.Errorf("title = %q, want the first prompt", got.Title)
	}
	if want := (Tokens{Input: 140, Output: 28, CacheRead: 17, CacheCreate: 8}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if want := (Context{Used: 50, Limit: 200000}); got.Context != want {
		t.Errorf("context = %+v, want %+v", got.Context, want)
	}
	if want := []int64{135, 58}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting after the final response", got.State)
	}
	if want := time.Date(2026, 11, 24, 10, 0, 2, 0, time.UTC); !got.Updated.Equal(want) {
		t.Errorf("updated = %v, want %v", got.Updated, want)
	}
}

func TestJSONLSpecReadsOnlyAppendedRecords(t *testing.T) {
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", "Run the suite", nil),
	)
	store := &JSONLStore{Mapping: jsonlSpecMapping()}

	first := readJSONLSpec(t, store, path)
	if first.State != StateThinking {
		t.Fatalf("state after a prompt = %q, want thinking", first.State)
	}
	if first.Context != (Context{}) {
		t.Fatalf("context = %+v, want none before usage", first.Context)
	}
	if len(first.TokenHistory) != 0 {
		t.Fatalf("history = %v, want none before a response", first.TokenHistory)
	}

	appendJSONLSpecFile(t, path,
		jsonlSpecRecord(t, "2026-11-24T10:00:01Z", "bot", "Working.", &jsonlSpecCounts{in: 10, out: 2}),
	)
	second := readJSONLSpec(t, store, path)
	if want := (Tokens{Input: 10, Output: 2}); second.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", second.Tokens, want)
	}
	if want := []int64{12}; !slices.Equal(second.TokenHistory, want) {
		t.Fatalf("history = %v, want %v", second.TokenHistory, want)
	}

	appendJSONLSpecFile(t, path,
		jsonlSpecRecord(t, "2026-11-24T10:00:02Z", "bot", "Done.", &jsonlSpecCounts{in: 5, out: 1}),
	)
	third := readJSONLSpec(t, store, path)
	if want := (Tokens{Input: 15, Output: 3}); third.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", third.Tokens, want)
	}
	if want := []int64{12, 6}; !slices.Equal(third.TokenHistory, want) {
		t.Errorf("history = %v, want %v", third.TokenHistory, want)
	}

	// Reading a caught-up file must not fold the same records in twice.
	again := readJSONLSpec(t, store, path)
	if again.Tokens != third.Tokens || !slices.Equal(again.TokenHistory, third.TokenHistory) {
		t.Errorf("second read changed the totals: %+v %v", again.Tokens, again.TokenHistory)
	}
}

func TestJSONLSpecCumulativeUsageTracksLevelsAndDeltas(t *testing.T) {
	mapping := jsonlSpecMapping()
	mapping.Usage = JSONLUsageCumulative
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", "Measure the build", nil),
		jsonlSpecRecord(t, "2026-11-24T10:00:01Z", "bot", "First.",
			&jsonlSpecCounts{in: 100, out: 20, read: 5}),
		jsonlSpecRecord(t, "2026-11-24T10:00:02Z", "bot", "Second.",
			&jsonlSpecCounts{in: 150, out: 30, read: 5}),
		jsonlSpecRecord(t, "2026-11-24T10:00:03Z", "bot", "Still second.",
			&jsonlSpecCounts{in: 150, out: 30, read: 5}),
		jsonlSpecRecord(t, "2026-11-24T10:00:04Z", "bot", "Compacted.", &jsonlSpecCounts{}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, path)

	if want := (Tokens{Input: 150, Output: 30, CacheRead: 5}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the latest cumulative level %+v", got.Tokens, want)
	}
	if want := []int64{125, 60}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want the first level then the delta %v", got.TokenHistory, want)
	}
	// The fill is the last response's share of the level (50 fresh input tokens), not the
	// cumulative session total.
	if want := (Context{Used: 50, Limit: 200000}); got.Context != want {
		t.Errorf("context = %+v, want the last response's share %+v", got.Context, want)
	}
}

func TestJSONLSpecCumulativeResetStartsANewSeries(t *testing.T) {
	mapping := jsonlSpecMapping()
	mapping.Usage = JSONLUsageCumulative
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", "Start over", nil),
		jsonlSpecRecord(t, "2026-11-24T10:00:01Z", "bot", "First.",
			&jsonlSpecCounts{in: 100, out: 20}),
		jsonlSpecRecord(t, "2026-11-24T10:00:02Z", "bot", "After a reset.",
			&jsonlSpecCounts{in: 20, out: 5}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, path)

	if want := (Tokens{Input: 20, Output: 5}); got.Tokens != want {
		t.Errorf("tokens = %+v, want the reset level %+v", got.Tokens, want)
	}
	if want := []int64{120, 25}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
}

func TestJSONLSpecTitlesAndUnknownRecords(t *testing.T) {
	mapping := jsonlSpecMapping()
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", "This prompt is not the title", nil),
		`{"meta":`,
		jsonlSpecJSON(t, map[string]any{
			"meta": map[string]any{
				"timestamp": "2026-11-24T10:00:01Z", "title": "Warehouse migration",
			},
			"event": map[string]any{
				"kind": "notice", "body": "ignored",
				"usage": map[string]any{"in": 7, "out": 3},
			},
		}),
		jsonlSpecRecord(t, "2026-11-24T10:00:02Z", "bot", "Done.", &jsonlSpecCounts{in: 9, out: 1}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, path)

	if got.Title != "Warehouse migration" {
		t.Errorf("title = %q, want the mapped title", got.Title)
	}
	if want := (Tokens{Input: 16, Output: 4}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v: an unknown role contributes spend but no turn", got.Tokens, want)
	}
	if want := []int64{10}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
	if want := (Context{Used: 9, Limit: 200000}); got.Context != want {
		t.Errorf("context = %+v, want only the assistant response %+v", got.Context, want)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q, want waiting", got.State)
	}
}

func TestJSONLSpecBlockArrayPromptText(t *testing.T) {
	mapping := jsonlSpecMapping()
	for _, tc := range []struct {
		name string
		body any
		want string
	}{
		{"block array", []any{
			map[string]any{"type": "text", "text": "Block array title"},
		}, "Block array title"},
		{"joined blocks", []any{
			map[string]any{"type": "text", "text": "First block"},
			map[string]any{"type": "text", "text": "second block"},
		}, "First block second block"},
		{"nested content", map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "Nested content title"},
		}}, "Nested content title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeJSONLSpecFile(t,
				jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", tc.body, nil),
			)
			got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, path)
			if got.Title != tc.want {
				t.Errorf("title = %q, want %q", got.Title, tc.want)
			}
		})
	}
}

func TestJSONLSpecExplicitContextAndToolState(t *testing.T) {
	mapping := jsonlSpecMapping()
	mapping.ContextUsed = "meta.contextUsed"
	mapping.ContextLimit = "meta.contextLimit"
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", "Run the build", nil),
	)
	store := &JSONLStore{Mapping: mapping}

	if got := readJSONLSpec(t, store, path); got.State != StateThinking {
		t.Fatalf("state after a prompt = %q, want thinking", got.State)
	}

	appendJSONLSpecFile(t, path, jsonlSpecJSON(t, map[string]any{
		"meta": map[string]any{
			"timestamp":   "2026-11-24T10:00:01Z",
			"contextUsed": 512, "contextLimit": 8192,
		},
		"event": map[string]any{"kind": "command", "body": "go build ./..."},
	}))

	got := readJSONLSpec(t, store, path)
	if want := (Context{Used: 512, Limit: 8192}); got.Context != want {
		t.Errorf("context = %+v, want the explicitly mapped fields %+v", got.Context, want)
	}
	if got.State != StateExecuting {
		t.Errorf("state = %q, want executing after a tool record", got.State)
	}

	appendJSONLSpecFile(t, path, jsonlSpecRecord(t, "2026-11-24T10:00:02Z", "bot", "Done.",
		&jsonlSpecCounts{in: 12, out: 2}))
	if after := readJSONLSpec(t, store, path); after.State != StateWaiting {
		t.Errorf("state = %q, want waiting after the assistant response", after.State)
	}
}

func TestJSONLSpecDottedKeyEscape(t *testing.T) {
	mapping := jsonlSpecMapping()
	mapping.Text = `meta.a\.b`
	path := writeJSONLSpecFile(t, jsonlSpecJSON(t, map[string]any{
		"meta":  map[string]any{"a.b": "Escaped prompt"},
		"event": map[string]any{"kind": "human"},
	}))

	got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, path)

	if got.Title != "Escaped prompt" {
		t.Errorf("title = %q, want the key whose dot is escaped", got.Title)
	}
}

func TestJSONLSpecMappingRolesFrozenAfterFirstRead(t *testing.T) {
	mapping := jsonlSpecMapping()
	store := &JSONLStore{Mapping: mapping}
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", "Frozen roles", nil),
	)

	if got := readJSONLSpec(t, store, path); got.State != StateThinking {
		t.Fatalf("state after a prompt = %q, want thinking", got.State)
	}

	// The mapping is compiled once, so editing the caller's Roles map afterwards must not
	// change how later records translate.
	store.Mapping.Roles["human"] = JSONLRoleTool
	appendJSONLSpecFile(t, path,
		jsonlSpecRecord(t, "2026-11-24T10:00:01Z", "human", "Second prompt", nil),
	)
	if got := readJSONLSpec(t, store, path); got.State != StateThinking {
		t.Errorf("state = %q, want thinking: the compiled mapping must ignore the later Roles edit", got.State)
	}
}

func TestJSONLSpecKeepsLargeCountersExact(t *testing.T) {
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "bot", "Large.",
			&jsonlSpecCounts{in: 9007199254740993, out: 1}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: jsonlSpecMapping()}, path)

	if got.Tokens.Input != 9007199254740993 {
		t.Errorf("input = %d, want the exact integer", got.Tokens.Input)
	}
}

func TestJSONLSpecWithoutTokenPathsReportsNoUsage(t *testing.T) {
	mapping := jsonlSpecMapping()
	mapping.Input, mapping.Output = "", ""
	mapping.CacheRead, mapping.CacheCreate = "", ""
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "bot", "No counters.",
			&jsonlSpecCounts{in: 100, out: 20}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, path)

	if got.Tokens != (Tokens{}) || got.Context != (Context{}) || len(got.TokenHistory) != 0 {
		t.Errorf("tokens/context/history = %+v %+v %v, want no usage", got.Tokens, got.Context, got.TokenHistory)
	}
}

func TestJSONLSpecTimestampUnits(t *testing.T) {
	want := time.Date(2026, 11, 24, 10, 0, 2, 0, time.UTC)
	for _, n := range []int64{
		want.Unix(), want.UnixMilli(), want.UnixMicro(), want.UnixNano(),
	} {
		if got := jsonlUnixTime(n); !got.Equal(want) {
			t.Errorf("jsonlUnixTime(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestJSONLSpecTimestampAcceptsUnixMillis(t *testing.T) {
	when := time.Date(2026, 11, 24, 10, 0, 2, 0, time.UTC)
	path := writeJSONLSpecFile(t, jsonlSpecJSON(t, map[string]any{
		"meta":  map[string]any{"timestamp": when.UnixMilli()},
		"event": map[string]any{"kind": "human", "body": "Time it"},
	}))

	got := readJSONLSpec(t, &JSONLStore{Mapping: jsonlSpecMapping()}, path)

	if !got.Updated.Equal(when) {
		t.Errorf("updated = %v, want %v", got.Updated, when)
	}
}

func TestJSONLSpecArrayIndexPath(t *testing.T) {
	body := jsonlSpecJSON(t, map[string]any{
		"meta": map[string]any{
			"timestamp": "2026-11-24T10:00:00Z",
			"models":    []any{"fixture-labs/kiln-3", "fixture-labs/kiln-4"},
		},
		"event": map[string]any{"kind": "human", "body": "Indexed model"},
	})
	for _, tc := range []struct {
		path string
		want string
	}{
		{"meta.models.0", "fixture-labs/kiln-3"},
		{"meta.models.7", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			mapping := jsonlSpecMapping()
			mapping.Model = tc.path
			file := writeJSONLSpecFile(t, body)
			if got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, file); got.Model != tc.want {
				t.Errorf("model = %q, want %q", got.Model, tc.want)
			}
		})
	}
}

func TestJSONLSpecLiteralRolesWithoutMap(t *testing.T) {
	mapping := jsonlSpecMapping()
	mapping.Roles = nil
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "user", "Literal roles", nil),
		jsonlSpecRecord(t, "2026-11-24T10:00:01Z", "assistant", "Done.",
			&jsonlSpecCounts{in: 8, out: 2}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, path)

	if got.Title != "Literal roles" {
		t.Errorf("title = %q, want the literal user prompt", got.Title)
	}
	if want := (Tokens{Input: 8, Output: 2}); got.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got.Tokens, want)
	}
}

func TestJSONLSpecInputIncludesCache(t *testing.T) {
	mapping := jsonlSpecMapping()
	mapping.InputIncludesCache = true
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "bot", "Cached input.",
			&jsonlSpecCounts{in: 100, out: 10, read: 40, write: 5}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: mapping}, path)

	if want := (Tokens{Input: 60, Output: 10, CacheRead: 40, CacheCreate: 5}); got.Tokens != want {
		t.Errorf("tokens = %+v, want cache split out of input %+v", got.Tokens, want)
	}
	if want := (Context{Used: 105, Limit: 200000}); got.Context != want {
		t.Errorf("context = %+v, want %+v", got.Context, want)
	}
	if want := []int64{115}; !slices.Equal(got.TokenHistory, want) {
		t.Errorf("history = %v, want %v", got.TokenHistory, want)
	}
}

func TestJSONLSpecRejectsInvalidMapping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*JSONLMapping)
		wantErr string
	}{
		{"unknown usage", func(m *JSONLMapping) { m.Usage = "cumulitive" }, "unknown jsonl usage"},
		{"unknown role", func(m *JSONLMapping) { m.Roles["human"] = "robot" }, "unknown jsonl role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapping := jsonlSpecMapping()
			tc.mutate(&mapping)
			path := writeJSONLSpecFile(t,
				jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "human", "Bad mapping", nil),
			)
			_, err := (&JSONLStore{Mapping: mapping}).Transcript(path)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestJSONLSpecSkipsTrailingGarbage(t *testing.T) {
	line := jsonlSpecRecord(t, "2026-11-24T10:00:00Z", "bot", "Partly valid.",
		&jsonlSpecCounts{in: 100, out: 20}) + ` {"second":true}`
	path := writeJSONLSpecFile(t, line)

	got := readJSONLSpec(t, &JSONLStore{Mapping: jsonlSpecMapping()}, path)

	if got.Tokens != (Tokens{}) || got.State != StateWaiting {
		t.Errorf("tokens/state = %+v/%q, want the malformed line skipped", got.Tokens, got.State)
	}
}

func TestJSONLSpecZeroTimestampKeepsPrevious(t *testing.T) {
	want := time.Date(2026, 11, 24, 10, 0, 1, 0, time.UTC)
	path := writeJSONLSpecFile(t,
		jsonlSpecRecord(t, "2026-11-24T10:00:01Z", "human", "Keep the clock", nil),
		jsonlSpecJSON(t, map[string]any{
			"meta":  map[string]any{"timestamp": 0},
			"event": map[string]any{"kind": "bot", "body": "No timestamp."},
		}),
	)

	got := readJSONLSpec(t, &JSONLStore{Mapping: jsonlSpecMapping()}, path)

	if !got.Updated.Equal(want) {
		t.Errorf("updated = %v, want the last non-zero timestamp %v", got.Updated, want)
	}
}

func TestJSONLSpecEmptyPath(t *testing.T) {
	_, err := (&JSONLStore{Mapping: jsonlSpecMapping()}).Transcript("")
	if !errors.Is(err, errJSONLPathEmpty) {
		t.Errorf("err = %v, want %v", err, errJSONLPathEmpty)
	}
}
