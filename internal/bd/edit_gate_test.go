package bd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }

func TestEditWriteAllowlistIsExact(t *testing.T) {
	ok := [][]string{
		{"update", "tt-1", "--json", "--add-label=ui"},
		{"update", "tt-1", "--json", "--remove-label=ui:v2/x"},
		{"update", "tt-1", "--json", "--priority=0"},
		{"update", "tt-1", "--json", "--assignee=Ada Lovelace", "--if-assignee=bob"},
		{"update", "tt-1", "--json", "--assignee=", "--if-assignee=bob"}, // unassign
		{"update", "tt-1", "--json", "--assignee=bob", "--if-assignee="}, // take an unowned task
		{"dep", "add", "tt-1", "tt-2", "--json"},
		{"dep", "remove", "tt-1", "tt-2", "--json"},
		{"comment", "tt-1", "--json", "--", "hello"},
		{"comment", "tt-1", "--json", "--", "--force -rf\nline two\t"},
		{"-C", "/trk", "dep", "add", "tt-1", "tt-2", "--json"},
	}
	for _, a := range ok {
		if !writeAllowed(a, "/trk") && !(a[0] != "-C" && writeAllowed(a, "")) {
			t.Errorf("should be allowed: %q", a)
		}
	}
	bad := [][]string{
		// labels: one, valid, alone
		{"update", "tt-1", "--json", "--add-label=a,b"},
		{"update", "tt-1", "--json", "--add-label=a b"},
		{"update", "tt-1", "--json", "--add-label="},
		{"update", "tt-1", "--json", "--add-label=--force"},
		{"update", "tt-1", "--json", "--add-label=a", "--remove-label=b"},
		{"update", "tt-1", "--json", "--add-label=a", "--add-label=b"},
		{"update", "tt-1", "--json", "--add-label=a", "--title=x"},
		{"update", "tt-1", "--json", "--add-label=a", "--priority=1"},
		{"update", "tt-1", "--json", "--set-labels=a"},
		{"update", "tt-1", "--json", "--add-label", "a"},
		// assignment: guard mandatory, alone, never forced or claimed
		{"update", "tt-1", "--json", "--assignee=bob"},
		{"update", "tt-1", "--json", "--if-assignee=bob"},
		{"update", "tt-1", "--json", "--assignee=bob", "--if-assignee=x", "--force"},
		{"update", "tt-1", "--json", "--assignee=bob", "--if-assignee=x", "--claim"},
		{"update", "tt-1", "--json", "--assignee=bob", "--if-assignee=x", "--title=t"},
		{"update", "tt-1", "--json", "--assignee=bob", "--if-assignee=x", "--status=open", "--if-status=blocked"},
		{"update", "tt-1", "--json", "--assignee=bob", "--if-assignee=x", "--add-label=a"},
		{"update", "tt-1", "--json", "--assignee=a\x1bb", "--if-assignee=x"},
		{"update", "tt-1", "--json", "--assignee=bob", "--if-assignee=x\n"},
		{"update", "tt-1", "--json", "--assignee= bob", "--if-assignee=x"},
		{"update", "tt-1", "--json", "--assignee=bob", "--if-assignee=x", "--if-assignee=y"},
		{"update", "tt-1", "--json", "--if-status=open", "--assignee=bob", "--if-assignee=x"},
		// priority stays the existing shape
		{"update", "tt-1", "--json", "--priority=5"},
		// dependencies: both ends, distinct, no shorthand, no type, no files
		{"dep", "add", "tt-1", "tt-1", "--json"},
		{"dep", "add", "tt-1", "--json"},
		{"dep", "add", "tt-1", "tt-2"},
		{"dep", "add", "tt-1", "tt-2", "--json", "--type=parent-child"},
		{"dep", "add", "tt-1", "--blocked-by=tt-2", "--json"},
		{"dep", "add", "tt-1", "external:p:c", "--json"},
		{"dep", "add", "--file=x", "--json"},
		{"dep", "add", "--force", "tt-2", "--json"},
		{"dep", "relate", "tt-1", "tt-2", "--json"},
		{"dep", "rm", "tt-1", "tt-2", "--json"},
		{"dep", "tt-1", "--blocks", "tt-2"},
		{"dep", "tree", "tt-1", "--json"},
		{"dep", "cycles", "--json"},
		// comment: one text after --, never a flag, a file or stdin
		{"comment", "tt-1", "--json", "hello"},
		{"comment", "tt-1", "--json", "-x", "hello"}, // the text must follow --, or it could be a flag
		{"comment", "tt-1", "--json", "hello", "there"},
		{"comment", "tt-1", "--json", "--", ""},
		{"comment", "tt-1", "--json", "--", "  \n"},
		{"comment", "tt-1", "--json", "--", "a\x1b[2Jb"},
		{"comment", "tt-1", "--json", "--", strings.Repeat("x", core.MaxCommentBytes+1)},
		{"comment", "tt-1", "--json", "--", "a", "b"},
		{"comment", "tt-1", "--json", "--stdin"},
		{"comment", "tt-1", "--json", "--file=/etc/passwd"},
		{"comment", "tt-1", "--", "hello"},
		{"comment", "--", "hello"},
		{"comment", "-x", "--json", "--", "hello"},
		{"comments", "add", "tt-1", "x"},
		{"label", "add", "tt-1", "x"},
	}
	for _, a := range bad {
		if writeAllowed(a, "") || writeAllowed(a, "/trk") {
			t.Errorf("should be refused: %q", a)
		}
	}
}

func TestEditBuildsTheExactArgv(t *testing.T) {
	m := New(Options{Dir: "/trk"})
	cases := map[string]struct {
		ch   core.TaskChange
		want string
	}{
		"label add":    {core.TaskChange{Op: core.TaskLabelAdd, ID: "tt-1", Label: "ui"}, "bd -C /trk update tt-1 --json --add-label=ui"},
		"label remove": {core.TaskChange{Op: core.TaskLabelRemove, ID: "tt-1", Label: "ui"}, "bd -C /trk update tt-1 --json --remove-label=ui"},
		"priority":     {core.TaskChange{Op: core.TaskPriority, ID: "tt-1", Priority: ip(0)}, "bd -C /trk update tt-1 --json --priority=0"},
		"assign":       {core.TaskChange{Op: core.TaskAssign, ID: "tt-1", Assignee: sp("ada"), IfAssignee: "bob"}, "bd -C /trk update tt-1 --json --assignee=ada --if-assignee=bob"},
		"unassign":     {core.TaskChange{Op: core.TaskAssign, ID: "tt-1", Assignee: sp(""), IfAssignee: "bob"}, "bd -C /trk update tt-1 --json --assignee= --if-assignee=bob"},
		"take":         {core.TaskChange{Op: core.TaskAssign, ID: "tt-1", Assignee: sp("ada")}, "bd -C /trk update tt-1 --json --assignee=ada --if-assignee="},
		"dep add":      {core.TaskChange{Op: core.TaskDepAdd, ID: "tt-1", Blocker: "tt-2"}, "bd -C /trk dep add tt-1 tt-2 --json"},
		"dep remove":   {core.TaskChange{Op: core.TaskDepRemove, ID: "tt-1", Blocker: "tt-2"}, "bd -C /trk dep remove tt-1 tt-2 --json"},
		"comment":      {core.TaskChange{Op: core.TaskAddComment, ID: "tt-1", Text: "-x hi"}, "bd -C /trk comment tt-1 --json -- -x hi"},
	}
	for name, c := range cases {
		got, err := m.BuildTaskArgv(c.ch)
		if err != nil || strings.Join(got, " ") != c.want {
			t.Errorf("%s: %q %v, want %q", name, got, err, c.want)
		}
	}
	// the builder refuses what the allowlist refuses even if validation was skipped
	for name, ch := range map[string]core.TaskChange{
		"assign nil":   {Op: core.TaskAssign, ID: "tt-1"},
		"priority nil": {Op: core.TaskPriority, ID: "tt-1"},
		"self dep":     {Op: core.TaskDepAdd, ID: "tt-1", Blocker: "tt-1"},
		"label comma":  {Op: core.TaskLabelAdd, ID: "tt-1", Label: "a,b"},
		"empty text":   {Op: core.TaskAddComment, ID: "tt-1"},
	} {
		if _, err := m.BuildTaskArgv(ch); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

func edit(f *fakeRun, argv ...string) (core.TaskResult, error) {
	return writeMod(f, Options{}).RunTask(context.Background(), append([]string{"bd"}, argv...))
}

func fakeFor(key, out string, exit int) *fakeRun {
	return &fakeRun{resp: map[string]string{key: out}, exit: map[string]int{key: exit}, errs: map[string]error{}}
}

func TestLabelAssignAndPriorityAnswersAreVerified(t *testing.T) {
	const add = "update tt-1 --json --add-label=ui"
	if res, err := edit(fakeFor(add, `[{"id":"tt-1","title":"t","labels":["ui"]}]`, 0), strings.Fields(add)...); err != nil || res.ID != "tt-1" {
		t.Fatalf("%+v %v", res, err)
	}
	// acknowledged but the answer does not show it: written-but-different, never "not applied"
	_, err := edit(fakeFor(add, `[{"id":"tt-1","title":"t","labels":[]}]`, 0), strings.Fields(add)...)
	if !errors.Is(err, core.ErrAppliedMismatch) || strings.Contains(core.DescribeWriteError(err), "not applied") {
		t.Errorf("label missing from the answer: %v", err)
	}
	const rm = "update tt-1 --json --remove-label=ui"
	if _, err := edit(fakeFor(rm, `[{"id":"tt-1","labels":["ui"]}]`, 0), strings.Fields(rm)...); !errors.Is(err, core.ErrAppliedMismatch) {
		t.Errorf("label still listed: %v", err)
	}
	const asg = "update tt-1 --json --assignee=ada --if-assignee=bob"
	if _, err := edit(fakeFor(asg, `[{"id":"tt-1","assignee":"ada"}]`, 0), strings.Fields(asg)...); err != nil {
		t.Errorf("assign: %v", err)
	}
	if _, err := edit(fakeFor(asg, `[{"id":"tt-1","assignee":"eve"}]`, 0), strings.Fields(asg)...); !errors.Is(err, core.ErrAppliedMismatch) {
		t.Errorf("assignee differs: %v", err)
	}
	const un = "update tt-1 --json --assignee= --if-assignee=bob"
	if _, err := edit(fakeFor(un, `[{"id":"tt-1"}]`, 0), strings.Fields(un)...); err != nil {
		t.Errorf("unassign: %v", err)
	}
	const pr = "update tt-1 --json --priority=0"
	if _, err := edit(fakeFor(pr, `[{"id":"tt-1","priority":0}]`, 0), strings.Fields(pr)...); err != nil {
		t.Errorf("priority 0: %v", err)
	}
	if _, err := edit(fakeFor(pr, `[{"id":"tt-1","priority":2}]`, 0), strings.Fields(pr)...); !errors.Is(err, core.ErrAppliedMismatch) {
		t.Errorf("priority differs: %v", err)
	}
}

// An assignment whose guard no longer holds is bd's exit 13: nothing written, ErrChanged.
func TestAssignWithAStaleGuardIsChangedAndNothingElseRuns(t *testing.T) {
	const asg = "update tt-1 --json --assignee=ada --if-assignee=bob"
	f := fakeFor(asg, `{"error":"1 of 1 issues failed to update","failed":[{"id":"tt-1","guard_mismatch":true}],"schema_version":1}`, 13)
	_, err := edit(f, strings.Fields(asg)...)
	if !errors.Is(err, core.ErrChanged) || errors.Is(err, core.ErrOutcomeUnknown) || len(f.calls) != 1 {
		t.Fatalf("%v %v", err, f.calls)
	}
}

func TestDependencyAndCommentAnswersAreHeldToTheAskedIDs(t *testing.T) {
	const add, rm, cm = "dep add tt-1 tt-2 --json", "dep remove tt-1 tt-2 --json", "comment tt-1 --json -- hi there"
	good := `{"depends_on_id":"tt-2","issue_id":"tt-1","schema_version":1,"status":"added","type":"blocks"}`
	if res, err := edit(fakeFor(add, good, 0), strings.Fields(add)...); err != nil || res.ID != "tt-1" || res.Status != "added" {
		t.Fatalf("%+v %v", res, err)
	}
	wrong := map[string]string{
		"other issue":  `{"depends_on_id":"tt-2","issue_id":"tt-9","status":"added"}`,
		"other target": `{"depends_on_id":"tt-9","issue_id":"tt-1","status":"added"}`,
		"other status": `{"depends_on_id":"tt-2","issue_id":"tt-1","status":"removed"}`,
		"empty":        ``,
		"array":        `[]`,
	}
	for name, body := range wrong {
		if res, err := edit(fakeFor(add, body, 0), strings.Fields(add)...); err == nil || res.ID != "" {
			t.Errorf("dep add, %s: exit 0 with %q must be an error, got %+v", name, body, res)
		}
	}
	if _, err := edit(fakeFor(rm, `{"depends_on_id":"tt-2","issue_id":"tt-1","status":"removed"}`, 0), strings.Fields(rm)...); err != nil {
		t.Errorf("dep remove: %v", err)
	}
	if _, err := edit(fakeFor(rm, `{"depends_on_id":"tt-2","issue_id":"tt-1","status":"added"}`, 0), strings.Fields(rm)...); err == nil {
		t.Error("remove answered with added must fail")
	}
	// bd's own refusal (cycle, missing target) surfaces
	_, err := edit(fakeFor(add, `{"error":"adding dependency would create a cycle","schema_version":1}`, 1), strings.Fields(add)...)
	if err == nil || !strings.Contains(err.Error(), "cycle") || errors.Is(err, core.ErrOutcomeUnknown) {
		t.Errorf("cycle: %v", err)
	}
	argv := []string{"comment", "tt-1", "--json", "--", "hi there"}
	k := strings.Join(argv[:4], " ") + " " + argv[4]
	if res, err := edit(fakeFor(k, `{"id":"c-1","issue_id":"tt-1","text":"hi there"}`, 0), argv...); err != nil || res.ID != "tt-1" || res.Status != "commented" {
		t.Errorf("comment: %+v %v", res, err)
	}
	for name, body := range map[string]string{"no id": `{"issue_id":"tt-1"}`, "other issue": `{"id":"c","issue_id":"tt-2"}`, "empty": ""} {
		if _, err := edit(fakeFor(k, body, 0), argv...); err == nil {
			t.Errorf("comment, %s: must fail", name)
		}
	}
	_ = cm
}

// A comment is not idempotent: a timeout says it may have been added, so a retry could post it twice.
func TestCommentTimeoutIsUnknownAndWarnsAboutDuplicates(t *testing.T) {
	argv := []string{"comment", "tt-1", "--json", "--", "hi"}
	k := "comment tt-1 --json -- hi"
	for _, e := range []error{core.ErrTimedOut, core.ErrCanceled} {
		f := fakeFor(k, "", 0)
		f.errs[k] = e
		_, err := edit(f, argv...)
		if !errors.Is(err, core.ErrOutcomeUnknown) || !strings.Contains(err.Error(), "may or may not have been added") {
			t.Errorf("%v: %v", e, err)
		}
		if got := core.DescribeWriteError(err); strings.Contains(got, "not applied") {
			t.Errorf("an unknown outcome is never called not applied: %q", got)
		}
	}
	if _, err := edit(fakeFor(k, "", -1), argv...); !errors.Is(err, core.ErrOutcomeUnknown) {
		t.Errorf("killed: %v", err)
	}
}

// Every edit goes through the shared gate: unconfirmed never runs, one run, audited before and after.
func TestEditsAreGatedSingleUseAndAudited(t *testing.T) {
	good := `{"depends_on_id":"tt-2","issue_id":"tt-1","status":"added"}`
	f, au := fakeFor("dep add tt-1 tt-2 --json", good, 0), &recAudit{}
	m := writeMod(f, Options{})
	plan, err := core.PlanTaskChange(m, core.TaskChange{Op: core.TaskDepAdd, ID: "tt-1", Blocker: "tt-2"})
	if err != nil {
		t.Fatal(err)
	}
	ap := core.NewApplier(m, au)
	if _, err := ap.Apply(context.Background(), plan); !errors.Is(err, core.ErrNotConfirmed) || len(f.calls) != 0 || len(au.lines) != 0 {
		t.Fatalf("unconfirmed: %v %v %v", err, f.calls, au.lines)
	}
	if _, err := ap.Apply(context.Background(), plan.Confirm()); err != nil {
		t.Fatal(err)
	}
	if len(au.lines) != 2 || au.lines[0].Verb != "dep-add" || au.lines[0].Phase != "attempt" || au.lines[1].Phase != "ok" || strings.Join(au.lines[0].Argv, " ") != "bd dep add tt-1 tt-2 --json" {
		t.Fatalf("%+v", au.lines)
	}
	if _, err := ap.Apply(context.Background(), plan.Confirm()); !errors.Is(err, core.ErrAlreadyApplied) || len(f.calls) != 1 {
		t.Fatalf("single use: %v %v", err, f.calls)
	}
}

func TestReadTaskCarriesLabelsAndBlockingDependenciesOnly(t *testing.T) {
	body := `[{"id":"tt-1","title":"t","status":"open","priority":1,"labels":["a","b\u001b[2J"],"dependencies":[{"id":"tt-2","dependency_type":"blocks"},{"id":"tt-3","dependency_type":"parent-child"},{"id":"tt-4","dependency_type":"related"},{"id":"tt-5","dependency_type":"conditional-blocks"}]}]`
	f := &fakeRun{resp: map[string]string{showKey: body}, exit: map[string]int{}}
	got, err := writeMod(f, Options{}).ReadTask(context.Background(), "tt-1")
	if err != nil || strings.Join(got.DependsOn, ",") != "tt-2,tt-5" || len(got.Labels) != 2 || strings.ContainsRune(got.Labels[1], 0x1b) {
		t.Fatalf("%+v %v", got, err)
	}
}

// bd exited 0 but its answer is not the issue that was asked for: the write may have landed, so it is
// "result unknown" (refresh before retrying), never "not applied".
func TestAnAnswerThatCannotBeVerifiedIsAnUnknownOutcome(t *testing.T) {
	const add = "update tt-1 --json --add-label=ui"
	for name, out := range map[string]string{
		"no issue":      `[]`,
		"another issue": `[{"id":"tt-9","title":"t","labels":["ui"]}]`,
	} {
		_, err := edit(fakeFor(add, out, 0), strings.Fields(add)...)
		msg := core.DescribeWriteError(err)
		if !errors.Is(err, core.ErrOutcomeUnknown) || strings.Contains(msg, "not applied") || !strings.Contains(msg, "refresh before retrying") || core.CodeFor(err) != core.CodeUnknownOutcome {
			t.Errorf("%s: %v / %q", name, err, msg)
		}
	}
	const c = "comment tt-1 --json -- hi"
	_, err := edit(fakeFor(c, `not json`, 0), strings.Fields(c)...)
	if !errors.Is(err, core.ErrOutcomeUnknown) {
		t.Errorf("comment answer: %v", err)
	}
}
