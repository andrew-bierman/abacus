package core_test

import (
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The one-line summary and the per-task line are what the confirm screen and the bulk plan print, so
// each operation's wording is pinned here.
func TestBulkSummaryAndItemLinesSayWhatEachOperationDoes(t *testing.T) {
	two, empty, bob := 2, "", "bob"
	before := core.Task{ID: "t-1", Title: "fix it", Status: "open", Priority: 3}
	cases := []struct {
		spec    core.BulkSpec
		ch      core.TaskChange
		summary string
		item    string
	}{
		{core.BulkSpec{Op: core.TaskClose}, core.TaskChange{Op: core.TaskClose, ID: "t-1", Before: before}, "close", "t-1  fix it  (close)"},
		{core.BulkSpec{Op: core.TaskUpdate, Status: "blocked"}, core.TaskChange{Op: core.TaskUpdate, ID: "t-1", Status: "blocked", Before: before}, "set the status to blocked on", "t-1  fix it  (status open -> blocked)"},
		{core.BulkSpec{Op: core.TaskPriority, Priority: &two}, core.TaskChange{Op: core.TaskPriority, ID: "t-1", Priority: &two, Before: before}, "set the priority to 2 on", "t-1  fix it  (priority 3 -> 2)"},
		{core.BulkSpec{Op: core.TaskPriority}, core.TaskChange{}, "set the priority of", ""},
		{core.BulkSpec{Op: core.TaskLabelAdd, Label: "ui"}, core.TaskChange{Op: core.TaskLabelAdd, ID: "t-1", Label: "ui", Before: before}, "add the label ui to", "t-1  fix it  (add label ui)"},
		{core.BulkSpec{Op: core.TaskLabelRemove, Label: "ui"}, core.TaskChange{Op: core.TaskLabelRemove, ID: "t-1", Label: "ui", Before: before}, "remove the label ui from", "t-1  fix it  (remove label ui)"},
		{core.BulkSpec{Op: core.TaskAssign, Assignee: &bob}, core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: &bob, Before: before}, "assign bob to", "t-1  fix it  (assignee nobody -> bob)"},
		{core.BulkSpec{Op: core.TaskAssign, Assignee: &empty}, core.TaskChange{Op: core.TaskAssign, ID: "t-1", Assignee: &empty, IfAssignee: "ann", Before: before}, "unassign", "t-1  fix it  (assignee ann -> nobody)"},
		{core.BulkSpec{Op: core.TaskAssign}, core.TaskChange{}, "assign", ""},
		{core.BulkSpec{Op: core.TaskAddComment}, core.TaskChange{}, "comment", ""},
	}
	for _, c := range cases {
		if got := c.spec.Summary(); got != c.summary {
			t.Errorf("%s Summary = %q, want %q", c.spec.Op, got, c.summary)
		}
		if c.item != "" {
			if got := core.BulkItemText(c.ch); got != c.item {
				t.Errorf("%s BulkItemText = %q, want %q", c.spec.Op, got, c.item)
			}
		}
	}
	if core.Plural(1, "task") != "1 task" || core.Plural(0, "task") != "0 tasks" || core.Plural(3, "task") != "3 tasks" {
		t.Fatal("Plural")
	}
}

// A batch the spec cannot describe is refused before any task is read.
func TestBulkSpecValidateRefusesWhatNoTaskCouldBePlannedFrom(t *testing.T) {
	five, bad := 5, "bob\u202e"
	for _, s := range []core.BulkSpec{
		{Op: core.TaskUpdate, Status: "closed"},
		{Op: core.TaskPriority},
		{Op: core.TaskPriority, Priority: &five},
		{Op: core.TaskLabelAdd, Label: "has space"},
		{Op: core.TaskAssign},
		{Op: core.TaskAssign, Assignee: &bad},
		{Op: core.TaskAddComment},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v was accepted", s)
		}
	}
}
