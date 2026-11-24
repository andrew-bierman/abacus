package taskview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func edView(unknown bool, issues ...core.TaskIssue) *View {
	return Build(core.TaskSnapshot{Issues: issues, DepsUnknown: unknown}, time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC))
}

func edIssue(id, status string, deps ...core.TaskDep) core.TaskIssue {
	return core.TaskIssue{ID: id, Title: "task " + id, Status: status, Priority: 2, Deps: deps}
}

func edDep(on, typ string) core.TaskDep { return core.TaskDep{DependsOn: on, Type: typ} }

// chain: c waits for b, b waits for a (a is the root blocker); d is unrelated; kid is a child of a.
func edChain() *View {
	return edView(false,
		edIssue("a", "open"), edIssue("b", "open", edDep("a", "blocks")), edIssue("c", "open", edDep("b", "blocks")),
		edIssue("d", "closed"), edIssue("kid", "open", edDep("a", "parent-child")), edIssue("r", "open", edDep("d", "related")))
}

func TestPlanDepAddBuildsATypedChange(t *testing.T) {
	ch, err := edChain().PlanDep(true, "d", "c")
	if err != nil || ch.Op != core.TaskDepAdd || ch.ID != "d" || ch.Blocker != "c" || ch.BlockerTitle != "task c" || ch.Expect != "closed" || ch.Before.Title != "task d" {
		t.Fatalf("%+v %v", ch, err)
	}
}

func TestPlanDepRefusesWhatTheTrackerWouldRefuseBeforePlanning(t *testing.T) {
	v := edChain()
	cases := map[string]struct {
		add             bool
		blocked, by, in string
	}{
		"self":                       {true, "a", "a", "itself"},
		"unknown blocked":            {true, "zz", "a", "zz is not in this tracker"},
		"unknown blocker":            {true, "a", "zz", "zz is not in this tracker"},
		"direct cycle":               {true, "a", "b", "cycle"},
		"transitive cycle":           {true, "a", "c", "cycle"},
		"cycle through a hierarchy":  {true, "a", "kid", "cycle"},
		"already linked":             {true, "b", "a", "already linked"},
		"already linked parent":      {true, "kid", "a", "already linked"},
		"already linked non-blocker": {true, "r", "d", "already linked"},
		"remove missing":             {false, "a", "b", "does not wait"},
		"remove a hierarchy link":    {false, "kid", "a", "not as a blocker"},
		"remove a related link":      {false, "r", "d", "not as a blocker"},
		"remove self":                {false, "a", "a", "itself"},
	}
	for name, c := range cases {
		ch, err := v.PlanDep(c.add, c.blocked, c.by)
		if err == nil || !strings.Contains(err.Error(), c.in) || ch.Op != "" {
			t.Errorf("%s: %+v %v (want an error naming %q)", name, ch, err, c.in)
		}
	}
	if _, err := edView(true, edIssue("a", "open"), edIssue("b", "open")).PlanDep(true, "a", "b"); err == nil || !strings.Contains(err.Error(), "not read") {
		t.Errorf("without dependency data nothing can be checked: %v", err)
	}
}

func TestPlanDepCycleMessageNamesTheChain(t *testing.T) {
	_, err := edChain().PlanDep(true, "a", "c")
	if err == nil || !strings.Contains(err.Error(), "c waits for b waits for a waits for c") {
		t.Fatalf("%v", err)
	}
}

func TestPlanDepAllowsWhatIsNotACycle(t *testing.T) {
	v := edChain()
	for name, c := range map[string][3]any{
		"fan in":          {true, "c", "a"}, // c already waits (transitively) for a: a second path is not a cycle
		"unrelated":       {true, "a", "d"},
		"related blocker": {true, "d", "r"},
	} {
		if ch, err := v.PlanDep(c[0].(bool), c[1].(string), c[2].(string)); err != nil || ch.Op != core.TaskDepAdd {
			t.Errorf("%s: %+v %v", name, ch, err)
		}
	}
	// removing a real blocking link
	ch, err := v.PlanDep(false, "b", "a")
	if err != nil || ch.Op != core.TaskDepRemove || ch.ID != "b" || ch.Blocker != "a" {
		t.Fatalf("%+v %v", ch, err)
	}
}

// a ring of many tasks must not recurse without bound or hang
func TestPlanDepWalkTerminatesOnExistingCycles(t *testing.T) {
	n := 2000
	var issues []core.TaskIssue
	for i := 0; i < n; i++ {
		issues = append(issues, edIssue(ringID(i), "open", edDep(ringID((i+1)%n), "blocks")))
	}
	issues = append(issues, edIssue("x", "open"))
	if _, err := edView(false, issues...).PlanDep(true, "x", ringID(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := edView(false, issues...).PlanDep(true, ringID(0), "x"); err != nil {
		t.Fatal(err)
	}
}

func ringID(i int) string { return "t" + strings.Repeat("0", 4-len(ringItoa(i))) + ringItoa(i) }

func ringItoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(rune('0'+i%10)) + s
	}
	return s
}

// a dangling blocking edge (its target is gone) can be removed; adding one to a missing task cannot
func TestPlanDepRemovesADanglingBlockingEdge(t *testing.T) {
	v := edView(false, edIssue("a", "open", edDep("gone", "blocks")), edIssue("b", "open", edDep("gone2", "parent-child")))
	ch, err := v.PlanDep(false, "a", "gone")
	if err != nil || ch.Op != core.TaskDepRemove || ch.Blocker != "gone" || ch.BlockerTitle != "" {
		t.Fatalf("%+v %v", ch, err)
	}
	if _, err := v.PlanDep(false, "b", "gone2"); err == nil || !strings.Contains(err.Error(), "not as a blocker") {
		t.Errorf("a dangling parent link is not removed here: %v", err)
	}
	if _, err := v.PlanDep(false, "a", "never"); err == nil || !strings.Contains(err.Error(), "does not wait") {
		t.Errorf("no such edge: %v", err)
	}
	if _, err := v.PlanDep(true, "a", "gone"); err == nil || !strings.Contains(err.Error(), "not in this tracker") {
		t.Errorf("adding to a missing task: %v", err)
	}
}

// at apply the same checks run on a fresh snapshot: a cycle made in between is refused
func TestRecheckDepRefusesWhatTheFreshSnapshotNoLongerAllows(t *testing.T) {
	snap := core.TaskSnapshot{Issues: []core.TaskIssue{edIssue("a", "open"), edIssue("b", "open"), edIssue("c", "open", edDep("a", "blocks"))}}
	re := RecheckDep(snapSrc{snap: snap}, time.Now)
	add := core.TaskChange{Op: core.TaskDepAdd, ID: "a", Blocker: "c"} // c waits for a: a waits for c is a cycle
	if err := re(context.Background(), add); !errors.Is(err, core.ErrChanged) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("%v", err)
	}
	if err := re(context.Background(), core.TaskChange{Op: core.TaskDepAdd, ID: "b", Blocker: "c"}); err != nil {
		t.Fatalf("%v", err)
	}
	if err := re(context.Background(), core.TaskChange{Op: core.TaskDepRemove, ID: "b", Blocker: "a"}); !errors.Is(err, core.ErrChanged) {
		t.Fatalf("an edge that is gone: %v", err)
	}
	bad := RecheckDep(snapSrc{err: errors.New("boom")}, time.Now)
	if err := bad(context.Background(), add); !errors.Is(err, core.ErrCannotVerify) {
		t.Fatalf("an unreadable tracker refuses: %v", err)
	}
}

type snapSrc struct {
	snap core.TaskSnapshot
	err  error
}

func (s snapSrc) Snapshot(context.Context) (core.TaskSnapshot, error) { return s.snap, s.err }
