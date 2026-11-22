package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

type fakeWriter struct {
	ran  [][]string
	fail error
}

func (f *fakeWriter) BuildTaskArgv(ch core.TaskChange) ([]string, error) {
	return []string{"bd", string(ch.Op), ch.ID, ch.Title}, nil
}
func (f *fakeWriter) RunTask(_ context.Context, argv []string) (core.TaskResult, error) {
	f.ran = append(f.ran, argv)
	return core.TaskResult{ID: "x-1"}, f.fail
}
func (f *fakeWriter) TrackerDir() string { return "/tmp/t" }

type memAudit struct {
	lines []core.AuditEntry
	err   error
}

func (m *memAudit) Record(e core.AuditEntry) error {
	if m.err != nil {
		return m.err
	}
	m.lines = append(m.lines, e)
	return nil
}

func plan(t *testing.T, w core.TaskWriter, ch core.TaskChange) core.WritePlan {
	t.Helper()
	p, err := core.PlanTaskChange(w, ch)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUnconfirmedPlanNeverRuns(t *testing.T) {
	w, a := &fakeWriter{}, &memAudit{}
	ap := core.NewApplier(w, a)
	_, err := ap.Apply(context.Background(), plan(t, w, core.TaskChange{Op: core.TaskCreate, Title: "t"}))
	if !errors.Is(err, core.ErrNotConfirmed) || len(w.ran) != 0 || len(a.lines) != 0 {
		t.Fatalf("err=%v ran=%v audit=%v", err, w.ran, a.lines)
	}
}

func TestConfirmedPlanRunsOnceAndIsAudited(t *testing.T) {
	w, a := &fakeWriter{}, &memAudit{}
	ap := core.NewApplier(w, a)
	p := plan(t, w, core.TaskChange{Op: core.TaskCreate, Title: "t"}).Confirm()
	res, err := ap.Apply(context.Background(), p)
	if err != nil || res.ID != "x-1" || len(w.ran) != 1 {
		t.Fatalf("%v %v %v", res, err, w.ran)
	}
	if len(a.lines) != 2 || a.lines[0].Phase != "attempt" || a.lines[1].Phase != "ok" || a.lines[0].Verb != "create" {
		t.Fatalf("audit %+v", a.lines)
	}
	if _, err := ap.Apply(context.Background(), p); !errors.Is(err, core.ErrAlreadyApplied) || len(w.ran) != 1 {
		t.Fatalf("second apply: %v ran=%d", err, len(w.ran))
	}
}

func TestAuditFailureBlocksTheMutation(t *testing.T) {
	w := &fakeWriter{}
	ap := core.NewApplier(w, &memAudit{err: errors.New("disk full")})
	_, err := ap.Apply(context.Background(), plan(t, w, core.TaskChange{Op: core.TaskCreate, Title: "t"}).Confirm())
	if err == nil || len(w.ran) != 0 {
		t.Fatalf("err=%v ran=%v", err, w.ran)
	}
}

func TestFailedRunIsAuditedAsError(t *testing.T) {
	w, a := &fakeWriter{fail: errors.New("boom")}, &memAudit{}
	_, err := core.NewApplier(w, a).Apply(context.Background(), plan(t, w, core.TaskChange{Op: core.TaskClose, ID: "t-1"}).Confirm())
	if err == nil || a.lines[1].Phase != "error" || a.lines[1].Error != "boom" {
		t.Fatalf("%v %+v", err, a.lines)
	}
}

func TestPlanValidation(t *testing.T) {
	w := &fakeWriter{}
	p5, p2 := 5, 2
	bad := map[string]core.TaskChange{
		"create without title": {Op: core.TaskCreate},
		"create blank title":   {Op: core.TaskCreate, Title: "  "},
		"control in title":     {Op: core.TaskCreate, Title: "a\x1b[2Jb"},
		"newline in title":     {Op: core.TaskCreate, Title: "a\nb"},
		"bad priority":         {Op: core.TaskCreate, Title: "t", Priority: &p5},
		"update no id":         {Op: core.TaskUpdate, Title: "t"},
		"update flag-like id":  {Op: core.TaskUpdate, ID: "--force", Title: "t"},
		"update nothing":       {Op: core.TaskUpdate, ID: "t-1"},
		"update to closed":     {Op: core.TaskUpdate, ID: "t-1", Status: "closed"},
		"update bogus status":  {Op: core.TaskUpdate, ID: "t-1", Status: "weird"},
		"close no id":          {Op: core.TaskClose},
		"close odd id":         {Op: core.TaskClose, ID: "a b"},
		"unknown op":           {Op: "delete", ID: "t-1"},
	}
	for name, ch := range bad {
		if _, err := core.PlanTaskChange(w, ch); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	ok := map[string]core.TaskChange{
		"create":      {Op: core.TaskCreate, Title: "t", Description: "line1\nline2", Priority: &p2},
		"update":      {Op: core.TaskUpdate, ID: "tt-ioo", Status: "in_progress"},
		"close":       {Op: core.TaskClose, ID: "tt-ioo", Reason: "done"},
		"close dotid": {Op: core.TaskClose, ID: "tt-ioo.1"},
	}
	for name, ch := range ok {
		if _, err := core.PlanTaskChange(w, ch); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPlanLinesShowTheExactChange(t *testing.T) {
	w := &fakeWriter{}
	p := plan(t, w, core.TaskChange{Op: core.TaskUpdate, ID: "t-1", Title: "new", Before: core.Task{ID: "t-1", Title: "old"}})
	got := strings.Join(p.Lines, "|")
	if !strings.Contains(got, `title: "old" -> "new"`) || !strings.Contains(got, "t-1") {
		t.Fatal(got)
	}
	if len(p.Argv) == 0 {
		t.Fatal("plan carries the argv")
	}
}

func TestFileAuditAppendsJSONLinesAndFailsWhenUnwritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "audit.jsonl")
	a := core.FileAudit(path)
	if err := a.Record(core.AuditEntry{Verb: "create", Phase: "attempt"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"verb":"create"`) || !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("%q", b)
	}
	if err := core.FileAudit(filepath.Join(path, "x", "y")).Record(core.AuditEntry{}); err == nil {
		t.Fatal("unwritable path must error")
	}
}
