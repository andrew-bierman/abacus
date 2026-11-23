package taskview

import (
	"context"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

func moveRow(status string) core.TaskRow {
	return core.TaskRow{ID: "kb-1", Title: "card", Status: status, Priority: 1}
}

// Every pair of statuses is either one legal move with one exact shape or a refusal with a reason.
func TestPlanMoveEveryPair(t *testing.T) {
	statuses := []string{"open", "in_progress", "blocked", "deferred", "closed", "pinned"}
	for _, from := range statuses {
		for _, to := range statuses {
			ch, err := PlanMove(moveRow(from), to)
			switch {
			case from == to:
				if err == nil {
					t.Errorf("%s -> %s: already there, want a refusal", from, to)
				}
			case to == "closed":
				if err != nil || ch.Op != core.TaskClose || ch.ID != "kb-1" || ch.Before.Status != from || ch.Status != "" || ch.Expect != from {
					t.Errorf("%s -> closed: %+v %v", from, ch, err)
				}
			case from == "closed" && to == "open":
				if err != nil || ch.Op != core.TaskReopen || ch.IfStatus != "" || ch.Before.Status != "closed" || ch.Expect != "closed" {
					t.Errorf("closed -> open: %+v %v", ch, err)
				}
			case from == "closed":
				if err == nil || !strings.Contains(err.Error(), "reopen") {
					t.Errorf("closed -> %s: out of closed goes to open first, got %+v %v", to, ch, err)
				}
			case to == "pinned":
				if err == nil {
					t.Errorf("%s -> pinned: only the four writable statuses are targets", from)
				}
			default:
				if err != nil || ch.Op != core.TaskUpdate || ch.Status != to || ch.IfStatus != from || ch.Before.Status != from || ch.Expect != from {
					t.Errorf("%s -> %s: %+v %v", from, to, ch, err)
				}
			}
		}
	}
}

func TestPlanMoveWarnsWhenAnUnfinishedBlockerHoldsTheCard(t *testing.T) {
	r := moveRow("open")
	r.BlockedBy = 2
	ch, err := PlanMove(r, "in_progress")
	if err != nil || !strings.Contains(ch.Warning, "2 unfinished") {
		t.Fatalf("%+v %v", ch, err)
	}
	if ch, _ = PlanMove(r, "deferred"); ch.Warning != "" {
		t.Errorf("only starting work is warned about: %q", ch.Warning)
	}
	if ch, _ = PlanMove(moveRow("open"), "in_progress"); ch.Warning != "" {
		t.Errorf("an unblocked card has no warning: %q", ch.Warning)
	}
}

// Every planned move is one the write gate's validation accepts.
func TestPlannedMovesPassCoreValidation(t *testing.T) {
	for _, from := range []string{"open", "in_progress", "blocked", "deferred", "closed"} {
		for _, to := range []string{"open", "in_progress", "blocked", "deferred", "closed"} {
			ch, err := PlanMove(moveRow(from), to)
			if err != nil {
				continue
			}
			if _, perr := core.PlanTaskChange(okWriter{}, ch); perr != nil {
				t.Errorf("%s -> %s: core refuses its own plan: %v", from, to, perr)
			}
		}
	}
}

type okWriter struct{}

func (okWriter) BuildTaskArgv(core.TaskChange) ([]string, error) { return []string{"bd"}, nil }
func (okWriter) RunTask(context.Context, []string) (core.TaskResult, error) {
	return core.TaskResult{}, nil
}
func (okWriter) TrackerDir() string { return "" }
