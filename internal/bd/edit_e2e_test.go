package bd

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// The edits through the real bd, in an isolated temp tracker with an isolated HOME. Skips without bd.
func TestRealBdEditsAgainstAnIsolatedTemporaryTracker(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd is not installed")
	}
	home, proj := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", home+"/s")
	t.Setenv("XDG_CONFIG_HOME", home+"/c")
	t.Setenv("XDG_DATA_HOME", home+"/d")
	t.Setenv("BEADS_ACTOR", "abacus-test")
	t.Setenv("BEADS_DIR", "")
	for _, c := range [][]string{{"git", "init", "-q", "."}, {"bd", "init", "--non-interactive", "--quiet", "--skip-agents", "--skip-hooks", "--prefix=ed"}} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = proj
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot set up an isolated tracker (%v): %s", err, out)
		}
	}
	ctx := context.Background()
	m := New(Options{Dir: proj})
	if d := m.Detect(ctx); d.State != core.Ready {
		t.Fatalf("detect %+v", d)
	}
	au := &memAudit{}
	ap := core.NewApplier(m, au)
	apply := func(ch core.TaskChange) (core.TaskResult, error) {
		t.Helper()
		p, err := core.PlanTaskChange(m, ch)
		if err != nil {
			t.Fatalf("%s: %v", ch.Op, err)
		}
		return ap.Apply(ctx, p.Confirm())
	}
	mk := func(title string) string {
		t.Helper()
		r, err := apply(core.TaskChange{Op: core.TaskCreate, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	read := func(id string) core.Task {
		t.Helper()
		got, err := m.ReadTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a, b, c := mk("a"), mk("b"), mk("c")
	writes := 3 // the three creates

	// labels: add, show in a re-read, remove; the freshness check sees both
	if _, err := apply(core.TaskChange{Op: core.TaskLabelAdd, ID: a, Label: "ui:v2"}); err != nil {
		t.Fatalf("label add: %v", err)
	}
	writes++
	if got := read(a); !slices.Contains(got.Labels, "ui:v2") {
		t.Fatalf("labels %v", got.Labels)
	}
	if err := core.CheckFresh(core.TaskChange{Op: core.TaskLabelAdd, ID: a, Label: "ui:v2"}, read(a)); !errors.Is(err, core.ErrChanged) {
		t.Errorf("adding a label that is there: %v", err)
	}
	if _, err := apply(core.TaskChange{Op: core.TaskLabelRemove, ID: a, Label: "ui:v2"}); err != nil {
		t.Fatalf("label remove: %v", err)
	}
	writes++
	if got := read(a); len(got.Labels) != 0 {
		t.Fatalf("labels %v", got.Labels)
	}

	// priority, including 0
	for _, n := range []int{0, 4} {
		n := n
		if _, err := apply(core.TaskChange{Op: core.TaskPriority, ID: a, Priority: &n}); err != nil || read(a).Priority != n {
			t.Fatalf("priority %d: %v (now %d)", n, err, read(a).Priority)
		}
		writes++
	}

	// assign, take over with the right guard, a stale guard writes nothing, unassign
	ada, bob, none := "ada", "bob", ""
	if _, err := apply(core.TaskChange{Op: core.TaskAssign, ID: a, Assignee: &ada, IfAssignee: ""}); err != nil || read(a).Assignee != "ada" {
		t.Fatalf("assign: %v", err)
	}
	writes++
	if _, err := apply(core.TaskChange{Op: core.TaskAssign, ID: a, Assignee: &bob, IfAssignee: "somebody-else"}); !errors.Is(err, core.ErrChanged) {
		t.Fatalf("stale assignee guard: %v", err)
	}
	writes++
	if read(a).Assignee != "ada" {
		t.Fatalf("a refused assignment wrote %q", read(a).Assignee)
	}
	if _, err := apply(core.TaskChange{Op: core.TaskAssign, ID: a, Assignee: &none, IfAssignee: "ada"}); err != nil || read(a).Assignee != "" {
		t.Fatalf("unassign: %v (now %q)", err, read(a).Assignee)
	}
	writes++

	// dependencies: planned from the real snapshot, so cycles, self and unknown targets never reach bd
	snap := func() *taskview.View {
		t.Helper()
		s, err := m.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return taskview.Build(s, s.ReadAt)
	}
	plan := func(add bool, blocked, blocker string) (core.TaskResult, error) {
		t.Helper()
		ch, err := snap().PlanDep(add, blocked, blocker)
		if err != nil {
			return core.TaskResult{}, err
		}
		writes++
		return apply(ch)
	}
	if res, err := plan(true, b, a); err != nil || res.Status != "added" { // b waits for a
		t.Fatalf("dep add: %+v %v", res, err)
	}
	if res, err := plan(true, c, b); err != nil || res.Status != "added" { // c waits for b
		t.Fatalf("dep add: %+v %v", res, err)
	}
	if got := read(c).DependsOn; len(got) != 1 || got[0] != b {
		t.Fatalf("depends_on %v", got)
	}
	for name, args := range map[string][3]any{
		"cycle":       {true, a, c},
		"direct":      {true, a, b},
		"self":        {true, a, a},
		"missing":     {true, a, "ed-nope"},
		"duplicate":   {true, b, a},
		"not present": {false, a, c},
	} {
		if _, err := plan(args[0].(bool), args[1].(string), args[2].(string)); err == nil {
			t.Errorf("%s: must be refused before planning", name)
		}
	}
	// the tracker is the backstop: a cycle forced past the planner is still refused by bd, and says so
	cyc := core.TaskChange{Op: core.TaskDepAdd, ID: a, Blocker: c}
	if _, err := apply(cyc); err == nil || !strings.Contains(err.Error(), "cycle") || errors.Is(err, core.ErrOutcomeUnknown) {
		t.Fatalf("bd must refuse a cycle: %v", err)
	}
	writes++
	if len(read(a).DependsOn) != 0 {
		t.Fatalf("a refused dependency wrote %v", read(a).DependsOn)
	}
	if _, err := apply(core.TaskChange{Op: core.TaskDepAdd, ID: a, Blocker: "ed-nope"}); err == nil {
		t.Fatal("bd must refuse a missing target")
	}
	writes++
	if res, err := plan(false, c, b); err != nil || res.Status != "removed" || len(read(c).DependsOn) != 0 {
		t.Fatalf("dep remove: %+v %v", res, err)
	}

	// comments: a leading dash, several lines, quotes and unicode all arrive as one comment
	for _, text := range []string{"--not-a-flag -x", "line one\nline two\n\ttabbed", `quote " and ' and $HOME and ; rm -rf`, "héllo ☃"} {
		res, err := apply(core.TaskChange{Op: core.TaskAddComment, ID: c, Text: text})
		if err != nil || res.ID != c || res.Status != "commented" {
			t.Fatalf("comment %q: %+v %v", text, res, err)
		}
		writes++
	}
	d := snap()
	det, ok := d.Detail(c)
	if !ok || len(det.Comments) != 4 || det.Comments[0].Text != "--not-a-flag -x" || !strings.HasPrefix(det.Comments[1].Text, "line one\nline two\n") || !strings.HasSuffix(det.Comments[1].Text, "tabbed") /* the read view sanitizes the tab */ || det.Comments[3].Text != "héllo ☃" {
		t.Fatalf("comments: %+v", det.Comments)
	}
	if _, err := apply(core.TaskChange{Op: core.TaskAddComment, ID: "ed-nope", Text: "x"}); err == nil {
		t.Fatal("a comment on a missing task must fail")
	}
	writes++
	if au.n != 2*writes {
		t.Fatalf("audit lines = %d, want %d (attempt + result per write, refused ones included)", au.n, 2*writes)
	}
}
