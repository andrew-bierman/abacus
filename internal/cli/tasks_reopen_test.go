package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func closedMod() *editMod {
	m := newEditMod()
	m.cur = core.Task{Title: "one", Status: "closed", Priority: 2}
	return m
}

func TestTasksReopenPlansTheExactCommandAndDryRunRunsNothing(t *testing.T) {
	m := closedMod()
	code, out, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "reopen", "t-1", "--reason", "regressed", "--dry-run")
	if code != 0 || len(m.ran) != 0 || au.n != 0 {
		t.Fatalf("code=%d ran=%v audit=%d err=%s", code, m.ran, au.n, errOut)
	}
	for _, w := range []string{"reopen task t-1", `status: "closed" -> "open"`, `reason: "regressed"`, "command: bd reopen t-1"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestTasksReopenAppliesThroughTheGateAndIsAudited(t *testing.T) {
	m := closedMod()
	code, out, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "reopen", "t-1", "--yes", "--json")
	if code != 0 || len(m.ran) != 1 || m.ran[0][1] != "reopen" || au.n != 2 || !strings.Contains(out, `"applied": true`) {
		t.Fatalf("code=%d ran=%v audit=%d out=%s err=%s", code, m.ran, au.n, out, errOut)
	}
}

func TestTasksReopenRefusesATaskThatIsNotClosed(t *testing.T) {
	m := newEditMod() // open
	code, out, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "reopen", "t-1", "--yes", "--json")
	if code != 1 || len(m.ran) != 0 || au.n != 0 || errorCode(out) != "refused" || !strings.Contains(errOut, "not closed") {
		t.Fatalf("code=%d ran=%v audit=%d out=%s err=%s", code, m.ran, au.n, out, errOut)
	}
}

// someone reopened it (or moved it) between the plan and the write: refused, nothing run or audited
func TestTasksReopenRefusedWhenTheTaskChangedAfterThePlan(t *testing.T) {
	m := closedMod()
	m.changeAfter = 1
	m.again = core.Task{ID: "t-1", Title: "one", Status: "in_progress"}
	code, out, _, au := runIn(t, "", []core.Module{m}, "tasks", "reopen", "t-1", "--yes", "--json")
	if code != 1 || len(m.ran) != 0 || au.n != 0 || errorCode(out) != "changed" {
		t.Fatalf("code=%d ran=%v audit=%d out=%s", code, m.ran, au.n, out)
	}
}

func TestTasksReopenNeedsAReaderAndAWholeID(t *testing.T) {
	m := newTaskMod()
	if code, out, _, _ := runIn(t, "", []core.Module{writeOnlyMod{m}}, "tasks", "reopen", "t-1", "--yes", "--json"); code != 1 || errorCode(out) != "unsupported" || len(m.ran) != 0 {
		t.Errorf("no reader: code=%d out=%s ran=%v", code, out, m.ran)
	}
	if code, _, _, _ := runIn(t, "", []core.Module{closedMod()}, "tasks", "reopen", "--yes"); code != 2 {
		t.Errorf("no id: %d", code)
	}
	if code, _, _, _ := runIn(t, "", []core.Module{closedMod()}, "tasks", "reopen", "t 1", "--yes"); code != 2 {
		t.Errorf("bad id: %d", code)
	}
}

// the tracker answered for another issue (a prefix or alias resolved): nothing is reopened
type aliasMod struct{ *editMod }

func (a aliasMod) ReadTask(ctx context.Context, id string) (core.Task, error) {
	t, err := a.editMod.ReadTask(ctx, id)
	t.ID = id + "-full"
	return t, err
}

func TestTasksReopenRefusesAnIdTheTrackerResolvedToAnother(t *testing.T) {
	m := aliasMod{closedMod()}
	code, _, errOut, au := runIn(t, "", []core.Module{m}, "tasks", "reopen", "t-1", "--yes")
	if code != 2 || len(m.ran) != 0 || au.n != 0 || !strings.Contains(errOut, "whole task id") {
		t.Fatalf("code=%d ran=%v err=%s", code, m.ran, errOut)
	}
}
