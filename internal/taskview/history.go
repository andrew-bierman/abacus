package taskview

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andrew-bierman/abacus/internal/core"
)

// maxHistoryRaw bounds the history output that is decoded; maxDetail bounds one event's summary.
const (
	maxHistoryRaw = 4 << 20
	maxDetail     = 120
)

// rawEvent is one record of `bd history ID --events --json` (field names verified against bd 1.3.4;
// see testdata/README.md). old_value of an update holds the whole old issue and is not used.
type rawEvent struct {
	Type      string          `json:"event_type"`
	Actor     string          `json:"actor"`
	NewValue  json.RawMessage `json:"new_value"`
	Comment   string          `json:"comment"`
	CreatedAt string          `json:"created_at"`
}

// ParseHistory decodes the output of `bd history ID --events --json --limit=N` into a report: events
// newest first, at most limit of them, every string sanitized and bounded. An empty output or `null`
// (an id with no events, or no such id) is an empty history, not an error.
func ParseHistory(out []byte, id string, limit int, now time.Time) (core.TaskHistoryReport, error) {
	rep := core.TaskHistoryReport{ID: core.Sanitize(id), ReadAt: now, Events: []core.TaskHistoryEvent{}}
	if len(out) > maxHistoryRaw {
		return rep, errors.New("the history output is too large to read")
	}
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 || string(out) == "null" {
		return rep, nil
	}
	var raw []rawEvent
	if err := json.Unmarshal(out, &raw); err != nil {
		return rep, fmt.Errorf("the history output is not readable: %w", err)
	}
	for _, r := range raw {
		at, _ := time.Parse(time.RFC3339, r.CreatedAt)
		rep.Events = append(rep.Events, core.TaskHistoryEvent{
			At: at.UTC(), Actor: clip(core.Sanitize(r.Actor), 40), Kind: clip(core.Sanitize(r.Type), 40), Detail: eventDetail(r),
		})
	}
	sort.SliceStable(rep.Events, func(i, j int) bool { return rep.Events[i].At.After(rep.Events[j].At) })
	if limit > 0 && len(rep.Events) > limit {
		rep.Events, rep.Truncated = rep.Events[:limit], true
	}
	return rep, nil
}

// eventDetail is a one-line summary: for an update, the fields that changed with their new values
// (`title="..." priority=1`); otherwise the event's own text.
func eventDetail(r rawEvent) string {
	var text string // bd writes new_value as a JSON string, which for an update holds a JSON object
	if json.Unmarshal(r.NewValue, &text) != nil {
		text = strings.TrimSpace(string(r.NewValue))
	}
	if text == "" || text == "null" {
		return clip(core.Sanitize(r.Comment), maxDetail)
	}
	var fields map[string]any
	if json.Unmarshal([]byte(text), &fields) == nil && len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, core.Sanitize(k)+"="+scalar(fields[k]))
		}
		return clip(strings.Join(parts, " "), maxDetail)
	}
	return clip(core.Sanitize(text), maxDetail)
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("%q", core.Sanitize(x))
	case nil:
		return "none"
	case float64:
		return fmt.Sprint(x)
	}
	b, _ := json.Marshal(v)
	return core.Sanitize(string(b))
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
