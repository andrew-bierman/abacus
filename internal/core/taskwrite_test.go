package core_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

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
		"update bogus status":  {Op: core.TaskUpdate, ID: "t-1", Status: "weird", IfStatus: "open"},
		"status without guard": {Op: core.TaskUpdate, ID: "t-1", Status: "blocked"},
		"guard without status": {Op: core.TaskUpdate, ID: "t-1", Title: "x", IfStatus: "open"},
		"guard from closed":    {Op: core.TaskUpdate, ID: "t-1", Status: "open", IfStatus: "closed"},
		"guard with a flag":    {Op: core.TaskUpdate, ID: "t-1", Status: "open", IfStatus: "--force"},
		"guard equals target":  {Op: core.TaskUpdate, ID: "t-1", Status: "open", IfStatus: "open"},
		"reopen no id":         {Op: core.TaskReopen},
		"reopen odd reason":    {Op: core.TaskReopen, ID: "t-1", Reason: "a\nb"},
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
		"create":             {Op: core.TaskCreate, Title: "t", Description: "line1\nline2", Priority: &p2},
		"update":             {Op: core.TaskUpdate, ID: "tt-ioo", Status: "in_progress", IfStatus: "open"},
		"update from custom": {Op: core.TaskUpdate, ID: "tt-ioo", Status: "open", IfStatus: "pinned"},
		"reopen":             {Op: core.TaskReopen, ID: "tt-ioo", Reason: "back"},
		"close":              {Op: core.TaskClose, ID: "tt-ioo", Reason: "done"},
		"close dotid":        {Op: core.TaskClose, ID: "tt-ioo.1"},
		"underscore":         {Op: core.TaskClose, ID: "my_proj-yzs"}, // bd accepts underscores in a prefix (verified in an isolated tracker)
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

func TestUnknownOutcomeIsAuditedAsUnknownAndStaysTyped(t *testing.T) {
	w := &fakeWriter{fail: fmt.Errorf("bd timed out: %w", core.ErrOutcomeUnknown)}
	a := &memAudit{}
	_, err := core.NewApplier(w, a).Apply(context.Background(), plan(t, w, core.TaskChange{Op: core.TaskClose, ID: "t-1"}).Confirm())
	if !errors.Is(err, core.ErrOutcomeUnknown) || a.lines[1].Phase != "unknown" {
		t.Fatalf("%v %+v", err, a.lines)
	}
}

func TestDefaultAuditPathIgnoresARelativeStateHomeAndFailsWithoutAHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "relative/dir")
	p, err := core.DefaultAuditPath()
	if err != nil || !filepath.IsAbs(p) || !strings.HasPrefix(p, home) {
		t.Fatalf("%q %v", p, err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "st"))
	if p, _ := core.DefaultAuditPath(); !strings.HasPrefix(p, filepath.Join(home, "st")) {
		t.Fatalf("absolute XDG_STATE_HOME must win: %q", p)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if _, err := core.DefaultAuditPath(); err == nil {
		t.Fatal("no home must be an error, not a relative path")
	}
	if err := core.DefaultAudit().Record(core.AuditEntry{}); err == nil {
		t.Fatal("the default audit must fail closed without a path")
	}
}

func TestFileAuditRefusesEmptyOrRelativePaths(t *testing.T) {
	for _, p := range []string{"", "audit.jsonl", "./a/b.jsonl"} {
		if err := core.FileAudit(p).Record(core.AuditEntry{}); err == nil {
			t.Errorf("%q must be refused", p)
		}
	}
}

func TestUnknownOutcomeWordingNeverSaysNotApplied(t *testing.T) {
	e := core.UnknownOutcome("bd timed out; refresh before retrying")
	got := core.DescribeWriteError(e)
	if !errors.Is(e, core.ErrOutcomeUnknown) || got != "result unknown: bd timed out; refresh before retrying" || strings.Contains(got, "not applied") {
		t.Fatalf("%q", got)
	}
	if got := core.DescribeWriteError(errors.New("bd: boom\x1b")); got != "not applied: bd: boom" {
		t.Fatalf("%q", got)
	}
}

func TestUpdateRejectsBlankTitleAndDescriptionLikeCreate(t *testing.T) {
	w := &fakeWriter{}
	for _, ch := range []core.TaskChange{
		{Op: core.TaskUpdate, ID: "t-1", Title: " "},
		{Op: core.TaskUpdate, ID: "t-1", Title: "\t"},
		{Op: core.TaskUpdate, ID: "t-1", Description: "  \n"},
	} {
		if _, err := core.PlanTaskChange(w, ch); err == nil {
			t.Errorf("%+v must be refused", ch)
		}
	}
}

// The command a person reads is the argv that runs: an argument with a rune that is not printable
// is quoted (escaped), so Sanitize has nothing to strip from the displayed command.
func TestDisplayArgvQuotesArgumentsWithNonPrintableRunes(t *testing.T) {
	for name, arg := range map[string]string{
		"bidi override":  "pay‮evil",
		"zero-width":     "a‍b",
		"zero-width sp":  "a​b",
		"bell":           "x\ay",
		"nbsp":           "a b",
		"invalid utf-8":  "a\xffb",
		"line separator": "a b",
	} {
		got := core.DisplayArgv([]string{"bd", "create", arg})
		if core.Sanitize(got) != got {
			t.Errorf("%s: the displayed command changes under Sanitize: %q", name, got)
		}
		if strings.ContainsFunc(got, func(r rune) bool { return !unicode.IsPrint(r) }) {
			t.Errorf("%s: the displayed command still holds a raw non-printable rune: %q", name, got)
		}
	}
	if got := core.DisplayArgv([]string{"bd", "create", "plain-title", "héllo", "日本"}); got != "bd create plain-title héllo 日本" {
		t.Errorf("printable arguments stay unquoted: %q", got)
	}
}

func TestGuardShapeErrorNamesTheCause(t *testing.T) {
	_, err := core.PlanTaskChange(&fakeWriter{}, core.TaskChange{Op: core.TaskUpdate, ID: "t-1", Status: "open", IfStatus: "Weird Status"})
	if err == nil || !strings.Contains(err.Error(), "plain lower-case word") || !strings.Contains(err.Error(), "Weird Status") {
		t.Fatalf("%v", err)
	}
}

// The confirm screen says exactly what will change: every field a create or an update sets, old value to
// new where the old one is known, and any warning, before anything runs.
func TestPlanLinesNameEveryFieldOfACreateAndAnUpdate(t *testing.T) {
	w := &fakeWriter{}
	three, one := 3, 1
	create := plan(t, w, core.TaskChange{Op: core.TaskCreate, Title: "t", Description: "d", Priority: &three, Type: "bug"})
	if got, want := strings.Join(create.Lines, "|"), `create a new task|title: "t"|description: "d"|priority: 3|type: bug`; got != want {
		t.Fatalf("create lines %q, want %q", got, want)
	}
	upd := plan(t, w, core.TaskChange{
		Op: core.TaskUpdate, ID: "t-1", Title: "new", Description: "why", Status: "blocked", IfStatus: "open",
		Priority: &one, Warning: "it has an open blocker", Before: core.Task{ID: "t-1", Title: "old", Priority: 2},
	})
	want := `update task t-1|title: "old" -> "new"|description: "why"|status: "open" -> "blocked"|warning: it has an open blocker|priority: "2" -> "1"`
	if got := strings.Join(upd.Lines, "|"); got != want {
		t.Fatalf("update lines %q, want %q", got, want)
	}
	// a value that is not changing is shown once, not as "x -> x"
	same := plan(t, w, core.TaskChange{Op: core.TaskUpdate, ID: "t-1", Title: "same", Before: core.Task{ID: "t-1", Title: "same"}})
	if got := strings.Join(same.Lines, "|"); got != `update task t-1|title: "same"` {
		t.Fatalf("lines %q", got)
	}
	// with no task read first, the old priority is not invented
	noBefore := plan(t, w, core.TaskChange{Op: core.TaskUpdate, ID: "t-1", Priority: &one})
	if got := strings.Join(noBefore.Lines, "|"); got != `update task t-1|priority: "1"` {
		t.Fatalf("lines %q", got)
	}
}

func TestPlanRefusesACreateWithoutATitleOrWithAnUnknownType(t *testing.T) {
	w := &fakeWriter{}
	for _, c := range []struct {
		ch   core.TaskChange
		want string
	}{
		{core.TaskChange{Op: core.TaskCreate, Title: "   "}, "a title is required"},
		{core.TaskChange{Op: core.TaskCreate, Title: "t", Type: "saga"}, `unknown type "saga"`},
	} {
		if _, err := core.PlanTaskChange(w, c.ch); err == nil || err.Error() != c.want {
			t.Errorf("%+v: err=%v, want %q", c.ch, err, c.want)
		}
	}
	if len(w.ran) != 0 {
		t.Fatalf("a refused plan ran something: %v", w.ran)
	}
}

// A write that landed with a different result is neither an error to retry nor "not applied".
func TestAppliedMismatchIsRecognizedAndKeepsItsMessage(t *testing.T) {
	err := fmt.Errorf("task t-1: %w", core.AppliedMismatch("it is now blocked, not in_progress"))
	if !errors.Is(err, core.ErrAppliedMismatch) || !strings.Contains(err.Error(), "it is now blocked") {
		t.Fatalf("err=%v", err)
	}
	if errors.Is(errors.New("plain"), core.ErrAppliedMismatch) {
		t.Fatal("a plain error is not a mismatch")
	}
}
