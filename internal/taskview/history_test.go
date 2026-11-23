package taskview

import (
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestParseHistoryRealEvents(t *testing.T) {
	rep, err := ParseHistory(fixture(t, "history-events.json"), "hs-yqf", 50, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range rep.Events {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, " "); got != "reopened closed dependency_added updated label_added updated status_changed created" {
		t.Errorf("kinds (newest first) = %q", got)
	}
	want := map[int]string{
		1: "shipped",
		2: "Added dependency: hs-yqf blocks hs-8md",
		3: `assignee="mei"`,
		4: "Added label: backend",
		5: `priority=1 title="Write the parser and tests"`,
		6: `status="in_progress"`,
	}
	for i, d := range want {
		if rep.Events[i].Detail != d {
			t.Errorf("event %d (%s) detail = %q, want %q", i, rep.Events[i].Kind, rep.Events[i].Detail, d)
		}
	}
	if rep.Events[0].Actor != "ada" || rep.Events[0].At.IsZero() || rep.Truncated || rep.ID != "hs-yqf" {
		t.Errorf("first = %+v truncated=%v", rep.Events[0], rep.Truncated)
	}
	for i := 1; i < len(rep.Events); i++ {
		if rep.Events[i].At.After(rep.Events[i-1].At) {
			t.Errorf("event %d is newer than %d", i, i-1)
		}
	}
}

func TestParseHistoryBoundsAndEmpty(t *testing.T) {
	rep, err := ParseHistory(fixture(t, "history-events.json"), "hs-yqf", 3, fixedNow)
	if err != nil || len(rep.Events) != 3 || !rep.Truncated || rep.Events[0].Kind != "reopened" {
		t.Errorf("limit 3: %v %+v truncated=%v", err, rep.Events, rep.Truncated)
	}
	for _, in := range []string{"", "  \n", "null", "[]"} {
		rep, err := ParseHistory([]byte(in), "x-1", 10, fixedNow)
		if err != nil || rep.Events == nil || len(rep.Events) != 0 {
			t.Errorf("%q: %v %+v (an empty history is an empty list, not an error)", in, err, rep)
		}
	}
	if _, err := ParseHistory([]byte("{nope"), "x-1", 10, fixedNow); err == nil {
		t.Error("garbage must be an error")
	}
	if _, err := ParseHistory(make([]byte, maxHistoryRaw+1), "x-1", 10, fixedNow); err == nil {
		t.Error("oversized output must be refused")
	}
}

func TestParseHistoryHostileText(t *testing.T) {
	in := `[{"event_type":"upda\u001b[2Jted","actor":"evil‮admin","new_value":"{\"title\":\"\\u001b]0;pwned\\u0007x\\u202e\"}","created_at":"2026-11-23T06:00:00Z"},
	      {"event_type":"closed","actor":"` + strings.Repeat("a", 500) + `","new_value":"` + strings.Repeat("long ", 200) + `","created_at":"2026-11-23T05:00:00Z"},
	      {"event_type":"label_added","comment":"Added label: \u001b[31mred","created_at":"not a time"}]`
	rep, err := ParseHistory([]byte(in), "x-1\x1b", 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Events {
		for _, s := range []string{e.Kind, e.Actor, e.Detail, rep.ID} {
			for _, r := range s {
				if unicode.IsControl(r) || r == 0x202e {
					t.Errorf("control or bidi rune %q survived in %q", r, s)
				}
			}
		}
		if len([]rune(e.Detail)) > maxDetail || len([]rune(e.Actor)) > 40 {
			t.Errorf("unbounded: actor %d detail %d", len([]rune(e.Actor)), len([]rune(e.Detail)))
		}
	}
	if rep.Events[0].Detail != `title="]0;pwnedx"` {
		t.Errorf("detail = %q", rep.Events[0].Detail)
	}
}
