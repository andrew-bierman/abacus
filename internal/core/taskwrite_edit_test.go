package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }

func TestEditPlanValidation(t *testing.T) {
	w := &fakeWriter{}
	bad := map[string]core.TaskChange{
		"label empty":          {Op: core.TaskLabelAdd, ID: "t-1"},
		"label with comma":     {Op: core.TaskLabelAdd, ID: "t-1", Label: "a,b"},
		"label with space":     {Op: core.TaskLabelAdd, ID: "t-1", Label: "a b"},
		"label flag-like":      {Op: core.TaskLabelRemove, ID: "t-1", Label: "--force"},
		"label too long":       {Op: core.TaskLabelAdd, ID: "t-1", Label: strings.Repeat("a", 65)},
		"label no id":          {Op: core.TaskLabelAdd, Label: "x"},
		"priority missing":     {Op: core.TaskPriority, ID: "t-1"},
		"priority 5":           {Op: core.TaskPriority, ID: "t-1", Priority: ip(5)},
		"priority unchanged":   {Op: core.TaskPriority, ID: "t-1", Priority: ip(2), Before: core.Task{ID: "t-1", Priority: 2}},
		"assign missing":       {Op: core.TaskAssign, ID: "t-1"},
		"assign control":       {Op: core.TaskAssign, ID: "t-1", Assignee: sp("a\x1bb")},
		"assign edge space":    {Op: core.TaskAssign, ID: "t-1", Assignee: sp(" bob")},
		"assign guard control": {Op: core.TaskAssign, ID: "t-1", Assignee: sp("bob"), IfAssignee: "x\ny"},
		"assign same":          {Op: core.TaskAssign, ID: "t-1", Assignee: sp("bob"), IfAssignee: "bob"},
		"unassign unassigned":  {Op: core.TaskAssign, ID: "t-1", Assignee: sp("")},
		"dep self":             {Op: core.TaskDepAdd, ID: "t-1", Blocker: "t-1"},
		"dep flag-like":        {Op: core.TaskDepAdd, ID: "t-1", Blocker: "--force"},
		"dep external":         {Op: core.TaskDepRemove, ID: "t-1", Blocker: "external:p:c"},
		"dep none":             {Op: core.TaskDepAdd, ID: "t-1"},
		"blocker on a label":   {Op: core.TaskLabelAdd, ID: "t-1", Label: "x", Blocker: "t-2"},
		"comment blank":        {Op: core.TaskAddComment, ID: "t-1", Text: "  \n"},
		"comment control":      {Op: core.TaskAddComment, ID: "t-1", Text: "a\x1b[2Jb"},
		"comment too long":     {Op: core.TaskAddComment, ID: "t-1", Text: strings.Repeat("x", core.MaxCommentBytes+1)},
		"comment bad utf8":     {Op: core.TaskAddComment, ID: "t-1", Text: "a\xffb"},
		"comment no id":        {Op: core.TaskAddComment, Text: "hi"},
	}
	for name, ch := range bad {
		if _, err := core.PlanTaskChange(w, ch); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	ok := map[string]core.TaskChange{
		"label add":    {Op: core.TaskLabelAdd, ID: "t-1", Label: "ui:v2/x.y_z-1"},
		"label remove": {Op: core.TaskLabelRemove, ID: "t-1", Label: "ui"},
		"priority":     {Op: core.TaskPriority, ID: "t-1", Priority: ip(0)},
		"assign":       {Op: core.TaskAssign, ID: "t-1", Assignee: sp("Ada Lovelace"), IfAssignee: "bob"},
		"unassign":     {Op: core.TaskAssign, ID: "t-1", Assignee: sp(""), IfAssignee: "bob"},
		"take unowned": {Op: core.TaskAssign, ID: "t-1", Assignee: sp("bob")},
		"dep add":      {Op: core.TaskDepAdd, ID: "t-1", Blocker: "t-2"},
		"dep remove":   {Op: core.TaskDepRemove, ID: "t-1", Blocker: "t-2"},
		"comment":      {Op: core.TaskAddComment, ID: "t-1", Text: "line1\n\tline2 -x --y"},
	}
	for name, ch := range ok {
		if _, err := core.PlanTaskChange(w, ch); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEditPlanLinesNameTheExactChange(t *testing.T) {
	w := &fakeWriter{}
	line := func(ch core.TaskChange) string { return strings.Join(plan(t, w, ch).Lines, "|") }
	before := core.Task{ID: "t-1", Title: "the task", Priority: 3}
	cases := map[string]struct {
		ch   core.TaskChange
		want []string
	}{
		"label":    {core.TaskChange{Op: core.TaskLabelAdd, ID: "t-1", Label: "ui", Before: before}, []string{"add a label to task t-1", `title: "the task"`, `label: "ui"`}},
		"unlabel":  {core.TaskChange{Op: core.TaskLabelRemove, ID: "t-1", Label: "ui"}, []string{"remove a label from task t-1", `label: "ui"`}},
		"priority": {core.TaskChange{Op: core.TaskPriority, ID: "t-1", Priority: ip(1), Before: before}, []string{"set the priority of task t-1", `priority: "3" -> "1"`}},
		"assign":   {core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: sp("ada"), IfAssignee: "bob"}, []string{"assign task t-1", `assignee: "bob" -> "ada"`}},
		"unassign": {core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: sp(""), IfAssignee: "bob"}, []string{"unassign task t-1", `assignee: "bob" -> "nobody"`}},
		"take":     {core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: sp("ada")}, []string{`assignee: "nobody" -> "ada"`}},
		"dep":      {core.TaskChange{Op: core.TaskDepAdd, ID: "t-1", Blocker: "t-2", BlockerTitle: "first"}, []string{"make task t-1 wait for t-2", `blocker: "t-2 first"`}},
		"undep":    {core.TaskChange{Op: core.TaskDepRemove, ID: "t-1", Blocker: "t-2"}, []string{"stop task t-1 waiting for t-2"}},
		"comment":  {core.TaskChange{Op: core.TaskAddComment, ID: "t-1", Text: "hello\nworld"}, []string{"add a comment to task t-1", `comment: "hello\nworld"`}},
	}
	for name, c := range cases {
		got := line(c.ch)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", name, got, w)
			}
		}
	}
}

type readerFake struct {
	t   core.Task
	err error
	n   int
}

func (r *readerFake) ReadTask(context.Context, string) (core.Task, error) { r.n++; return r.t, r.err }

func TestCheckFreshRefusesWhatNoLongerFits(t *testing.T) {
	type c struct {
		ch   core.TaskChange
		cur  core.Task
		fail bool
	}
	cases := map[string]c{
		"add label new":        {core.TaskChange{Op: core.TaskLabelAdd, ID: "t-1", Label: "x"}, core.Task{Labels: []string{"y"}}, false},
		"add label present":    {core.TaskChange{Op: core.TaskLabelAdd, ID: "t-1", Label: "x"}, core.Task{Labels: []string{"x"}}, true},
		"remove label present": {core.TaskChange{Op: core.TaskLabelRemove, ID: "t-1", Label: "x"}, core.Task{Labels: []string{"x"}}, false},
		"remove label gone":    {core.TaskChange{Op: core.TaskLabelRemove, ID: "t-1", Label: "x"}, core.Task{}, true},
		"priority same":        {core.TaskChange{Op: core.TaskPriority, ID: "t-1", Priority: ip(1), Before: core.Task{ID: "t-1", Priority: 3}}, core.Task{Priority: 3}, false},
		"priority moved":       {core.TaskChange{Op: core.TaskPriority, ID: "t-1", Priority: ip(1), Before: core.Task{ID: "t-1", Priority: 3}}, core.Task{Priority: 0}, true},
		"priority now target":  {core.TaskChange{Op: core.TaskPriority, ID: "t-1", Priority: ip(1), Before: core.Task{ID: "t-1", Priority: 3}}, core.Task{Priority: 1}, true},
		"assign held same":     {core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: sp("a"), IfAssignee: "bob"}, core.Task{Assignee: "bob"}, false},
		"assign taken":         {core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: sp("a"), IfAssignee: "bob"}, core.Task{Assignee: "eve"}, true},
		"assign freed":         {core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: sp("a"), IfAssignee: "bob"}, core.Task{}, true},
		"dep add new":          {core.TaskChange{Op: core.TaskDepAdd, ID: "t-1", Blocker: "t-2"}, core.Task{DependsOn: []string{"t-3"}}, false},
		"dep add present":      {core.TaskChange{Op: core.TaskDepAdd, ID: "t-1", Blocker: "t-2"}, core.Task{DependsOn: []string{"t-2"}}, true},
		"dep remove present":   {core.TaskChange{Op: core.TaskDepRemove, ID: "t-1", Blocker: "t-2"}, core.Task{DependsOn: []string{"t-2"}}, false},
		"dep remove gone":      {core.TaskChange{Op: core.TaskDepRemove, ID: "t-1", Blocker: "t-2"}, core.Task{}, true},
		"comment":              {core.TaskChange{Op: core.TaskAddComment, ID: "t-1", Text: "x"}, core.Task{}, false},
	}
	for name, c := range cases {
		err := core.CheckFresh(c.ch, c.cur)
		if (err != nil) != c.fail {
			t.Errorf("%s: err=%v, want failure=%v", name, err, c.fail)
		}
		if err != nil && (!errors.Is(err, core.ErrChanged) || !strings.Contains(err.Error(), "nothing was written")) {
			t.Errorf("%s: a stale change is ErrChanged and says nothing was written: %v", name, err)
		}
	}
}

func TestFreshReadsTheTaskAndNeverApplyUnchecked(t *testing.T) {
	ch := core.TaskChange{Op: core.TaskLabelAdd, ID: "t-1", Label: "x"}
	r := &readerFake{t: core.Task{Labels: []string{"x"}}}
	if err := core.Fresh(context.Background(), r, ch); !errors.Is(err, core.ErrChanged) || r.n != 1 {
		t.Fatalf("%v reads=%d", err, r.n)
	}
	if err := core.Fresh(context.Background(), nil, ch); !errors.Is(err, core.ErrCannotVerify) {
		t.Fatalf("no reader must refuse: %v", err)
	}
	if err := core.Fresh(context.Background(), &readerFake{err: errors.New("boom")}, ch); !errors.Is(err, core.ErrCannotVerify) {
		t.Fatalf("an unreadable task must refuse: %v", err)
	}
	// create, update and close keep their own checks: Fresh leaves them alone and never reads
	r = &readerFake{}
	for _, op := range []core.TaskOp{core.TaskCreate, core.TaskUpdate, core.TaskClose, core.TaskReopen} {
		if err := core.Fresh(context.Background(), nil, core.TaskChange{Op: op, ID: "t-1"}); err != nil || core.NeedsFresh(op) {
			t.Errorf("%s: %v", op, err)
		}
	}
}

func TestEditsGoThroughTheGateAndAreAudited(t *testing.T) {
	w, a := &fakeWriter{}, &memAudit{}
	ap := core.NewApplier(w, a)
	p := plan(t, w, core.TaskChange{Op: core.TaskAddComment, ID: "t-1", Text: "hi"})
	if _, err := ap.Apply(context.Background(), p); !errors.Is(err, core.ErrNotConfirmed) || len(w.ran) != 0 {
		t.Fatalf("unconfirmed must not run: %v", err)
	}
	w.fail = core.UnknownOutcome("timed out")
	if _, err := ap.Apply(context.Background(), p.Confirm()); !errors.Is(err, core.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if len(a.lines) != 2 || a.lines[0].Verb != "comment" || a.lines[0].Phase != "attempt" || a.lines[1].Phase != "unknown" {
		t.Fatalf("%+v", a.lines)
	}
	if _, err := ap.Apply(context.Background(), p.Confirm()); !errors.Is(err, core.ErrAlreadyApplied) {
		t.Fatalf("single use: %v", err)
	}
}
