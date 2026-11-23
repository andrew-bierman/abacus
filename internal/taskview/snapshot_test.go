package taskview

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestRealExportDecodes(t *testing.T) {
	s := realSnapshot(t)
	if len(s.Issues) != 9 || s.Tracker != "tracker" || !s.ReadAt.Equal(fixedNow) {
		t.Fatalf("snapshot: %d issues, tracker %q, read %v", len(s.Issues), s.Tracker, s.ReadAt)
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("the real export must be consistent with itself: %v", s.Warnings)
	}
	by := map[string]core.TaskIssue{}
	for _, it := range s.Issues {
		by[it.ID] = it
	}
	list := by["tk-0at.2"]
	if list.Title != "List view" || list.Assignee != "mei" || list.Priority != 1 || list.Type != "feature" || !slices.Equal(list.Labels, []string{"ui"}) {
		t.Errorf("list view: %+v", list)
	}
	if len(list.Deps) != 2 || list.Deps[0] != (core.TaskDep{DependsOn: "tk-0at", Type: "parent-child"}) || list.Deps[1] != (core.TaskDep{DependsOn: "tk-0at.1", Type: "blocks"}) {
		t.Errorf("deps: %+v", list.Deps)
	}
	if len(list.Comments) != 2 || list.Comments[1].Author != "mei" || list.Comments[1].Text != "Agreed, after tk-0at.1" || list.Comments[1].CreatedAt.IsZero() {
		t.Errorf("comments: %+v", list.Comments)
	}
	if list.ReportedDeps != 1 || !list.HasReportedDeps || list.ReportedComments != 2 {
		t.Errorf("reported counts: %+v", list)
	}
	if p := by["tk-tbr"]; p.Priority != 0 || p.Status != "in_progress" || p.Assignee != "ada" {
		t.Errorf("P0 must decode as 0, not the default: %+v", p)
	}
	if c := by["tk-0at.1"]; c.Status != "closed" || c.CloseReason != "done" || c.ClosedAt.IsZero() || !slices.Equal(c.Labels, []string{"core", "ui"}) {
		t.Errorf("closed task: %+v", c)
	}
	if d := by["tk-x5n"]; d.Status != "deferred" || d.DeferUntil.Year() != 2099 {
		t.Errorf("deferred task: %+v", d)
	}
	if r := by["tk-o4w"]; r.Design != "Design **notes**" || r.Notes != "Some notes" || r.Acceptance != "It works" {
		t.Errorf("rich fields: %+v", r)
	}
	if e := by["tk-0at"]; e.Description != "Epic for the **tasks** workspace.\n\n- list\n- detail" {
		t.Errorf("a description keeps its line breaks: %q", e.Description)
	}
	if by["tk-wiq"].Status != "blocked" {
		t.Errorf("stored blocked status: %+v", by["tk-wiq"])
	}
}

func TestEmptyExportIsAnEmptySnapshot(t *testing.T) {
	s, err := Parse(bytes.NewReader(fixture(t, "export-empty.jsonl")), fixedNow, "")
	if err != nil || len(s.Issues) != 0 || len(s.Warnings) != 0 {
		t.Fatalf("empty export: %v %+v", err, s)
	}
	if v := Build(s, fixedNow); v.Total() != 0 || len(v.List(ListOptions{}).Rows) != 0 {
		t.Fatal("an empty tracker lists nothing")
	}
}

func TestParseToleranceAndRefusals(t *testing.T) {
	good := `{"_type":"issue","id":"a-1","title":"one"}`
	cases := []struct {
		name, in string
		issues   int
		wantErr  error
	}{
		{"blank lines and CRLF", good + "\r\n\r\n   \n" + `{"_type":"issue","id":"a-2","title":"two"}` + "\r\n", 2, nil},
		{"no trailing newline", good, 1, nil},
		{"other record types are skipped", `{"_type":"memory","id":"m-1","title":"x"}` + "\n" + good + "\n" + `{"_type":"template","id":"t-1"}`, 1, nil},
		{"a record without _type is an issue", `{"id":"a-9","title":"nine"}`, 1, nil},
		{"unknown fields are ignored", `{"_type":"issue","id":"a-1","title":"x","future_field":{"a":[1,2]}}`, 1, nil},
		{"a later duplicate wins", `{"id":"a-1","title":"old"}` + "\n" + `{"id":"a-1","title":"new"}`, 1, nil},
		{"unreadable record fails the read", good + "\n{not json\n", 0, ErrUnreadable},
		{"a truncated record fails the read", good + "\n" + `{"_type":"issue","id":"a-2","tit`, 0, ErrUnreadable},
		{"a record without an id fails the read", `{"_type":"issue","title":"x"}`, 0, ErrUnreadable},
		{"an array is not an export", `[{"id":"a"}]`, 0, ErrUnreadable},
	}
	for _, c := range cases {
		s, err := Parse(strings.NewReader(c.in), fixedNow, "")
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || len(s.Issues) != c.issues {
			t.Errorf("%s: %d issues, err %v; want %d", c.name, len(s.Issues), err, c.issues)
		}
	}
	s, _ := Parse(strings.NewReader(`{"id":"a-1","title":"old"}`+"\n"+`{"id":"a-1","title":"new"}`), fixedNow, "")
	if s.Issues[0].Title != "new" {
		t.Errorf("duplicate: %+v", s.Issues)
	}
	if s, _ := Parse(strings.NewReader(`{"id":"a-1","title":"x"}`), fixedNow, ""); s.Issues[0].Priority != 2 {
		t.Errorf("a record with no priority gets bd's default 2, got %d", s.Issues[0].Priority)
	}
}

// zeros yields an endless stream of one byte, to prove the size ceilings without a big file.
type endless struct{ b byte }

func (e endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = e.b
	}
	return len(p), nil
}

// lines yields valid records of about 1 MiB each, forever.
type lines struct{ buf []byte }

func (l *lines) Read(p []byte) (int, error) {
	if len(l.buf) == 0 {
		l.buf = []byte(`{"_type":"memory","pad":"` + strings.Repeat("x", 1<<20) + `"}` + "\n")
	}
	n := copy(p, l.buf)
	l.buf = l.buf[n:]
	return n, nil
}

func TestSizeCeilings(t *testing.T) {
	if _, err := Parse(endless{'a'}, fixedNow, ""); !errors.Is(err, ErrTooLarge) {
		t.Errorf("one endless record: %v", err)
	}
	if _, err := Parse(&lines{}, fixedNow, ""); !errors.Is(err, ErrTooLarge) {
		t.Errorf("an export over 64 MiB: %v", err)
	}
	under := io.LimitReader(&lines{}, 10<<20) // ten records, then EOF mid-record is unreadable, not too large
	if _, err := Parse(under, fixedNow, ""); errors.Is(err, ErrTooLarge) {
		t.Errorf("10 MiB is fine: %v", err)
	}
}

func TestHostileTextIsSanitized(t *testing.T) {
	in := `{"id":"a-\u001b[31m1","title":"\u001b]0;pwn\u0007bad‮title​","assignee":"ev\u0000il","labels":["x\ny","  "],` +
		`"description":"line1\r\nline2\u001b[2J\ttab","comments":[{"author":"a\u001b","text":"hi\u0085there\nsecond","created_at":"nope"}]}`
	s, err := Parse(strings.NewReader(in), fixedNow, "dir\u001b[0m")
	if err != nil {
		t.Fatal(err)
	}
	it := s.Issues[0]
	for _, v := range []string{it.ID, it.Title, it.Assignee, it.Labels[0], it.Description, it.Comments[0].Author, it.Comments[0].Text, s.Tracker} {
		for _, r := range v {
			if r < 0x20 && r != '\n' || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x202e || r == 0x200b {
				t.Errorf("control or invisible rune %U survives in %q", r, v)
			}
		}
	}
	if len(it.Labels) != 1 || !strings.Contains(it.Description, "line1\nline2") || !strings.Contains(it.Comments[0].Text, "\nsecond") {
		t.Errorf("labels %q, description %q, comment %q", it.Labels, it.Description, it.Comments[0].Text)
	}
	if len(s.Warnings) == 0 || !strings.Contains(s.Warnings[0], "time") {
		t.Errorf("an unreadable time is a warning: %v", s.Warnings)
	}
}

func TestConsistencyNoticesLostRecords(t *testing.T) {
	mk := func(deps []core.TaskDep, reported int, has bool, comments, reportedComments int) core.TaskSnapshot {
		it := core.TaskIssue{ID: "a-1", Deps: deps, ReportedDeps: reported, HasReportedDeps: has, ReportedComments: reportedComments}
		for i := 0; i < comments; i++ {
			it.Comments = append(it.Comments, core.TaskComment{Text: "c"})
		}
		return core.TaskSnapshot{Issues: []core.TaskIssue{it}}
	}
	blocks := core.TaskDep{DependsOn: "a-2", Type: "blocks"}
	parent := core.TaskDep{DependsOn: "a-3", Type: "parent-child"}
	cases := []struct {
		name string
		s    core.TaskSnapshot
		want int
	}{
		{"consistent", mk([]core.TaskDep{blocks, parent}, 1, true, 2, 2), 0},
		{"lost a blocking dependency", mk([]core.TaskDep{parent}, 1, true, 0, 0), 1},
		{"a parent link does not count as blocking", mk([]core.TaskDep{parent, parent}, 1, true, 0, 0), 1},
		{"reported count missing from the export", mk(nil, 0, false, 0, 0), 0},
		{"more read than reported is not a loss", mk([]core.TaskDep{blocks, blocks}, 1, true, 0, 0), 0},
		{"lost a comment", mk(nil, 0, false, 1, 3), 1},
		{"both", mk(nil, 2, true, 0, 1), 2},
	}
	for _, c := range cases {
		if got := Consistency(c.s); len(got) != c.want {
			t.Errorf("%s: %v, want %d warnings", c.name, got, c.want)
		}
	}
	var many core.TaskSnapshot
	for i := 0; i < 20; i++ {
		many.Issues = append(many.Issues, core.TaskIssue{ID: "x", ReportedComments: 1})
	}
	if w := Consistency(many); len(w) != maxWarnings+1 || !strings.Contains(w[len(w)-1], "more records") {
		t.Errorf("warnings are bounded: %v", w)
	}
}

func TestFreshness(t *testing.T) {
	poll := 30 * time.Second
	for _, c := range []struct {
		age  time.Duration
		want bool
	}{{0, true}, {59 * time.Second, true}, {60 * time.Second, true}, {61 * time.Second, false}, {10 * time.Minute, false}} {
		if got := Fresh(fixedNow.Add(-c.age), fixedNow, poll); got != c.want {
			t.Errorf("read %v ago: fresh = %v, want %v", c.age, got, c.want)
		}
	}
	if Fresh(time.Time{}, fixedNow, poll) {
		t.Error("a snapshot never read is not fresh")
	}
	if !Fresh(fixedNow.Add(time.Hour), fixedNow, poll) {
		t.Error("a read time slightly in the future (clock skew) is not stale")
	}
}
