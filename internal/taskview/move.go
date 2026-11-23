package taskview

import (
	"fmt"
	"slices"

	"github.com/andrew-bierman/abacus/internal/core"
)

// PlanMove is the pure move planner of the board: what moving this card to the status column "to"
// means as a write, or why it is not allowed. It never runs anything; the change goes through the
// write gate (plan, confirm, single use, audit) like every other Tasks write.
//
// The legal transitions, by stored status:
//
//	open, in_progress, blocked, deferred (or any custom status) -> another of those four: bd update --status with --if-status
//	any status but closed                                       -> closed: close
//	closed                                                      -> open: reopen (nothing else; reopen first)
//
// Anything else (same column, a custom status as a target, out of closed to anywhere but open) is
// refused with a sentence the user can act on.
func PlanMove(r core.TaskRow, to string) (core.TaskChange, error) {
	from := r.Status
	ch := core.TaskChange{ID: r.ID, Expect: from, Before: core.Task{ID: r.ID, Title: r.Title, Status: from, Priority: r.Priority, Type: r.Type, Assignee: r.Assignee, UpdatedAt: r.UpdatedAt}}
	switch {
	case from == to:
		return core.TaskChange{}, fmt.Errorf("%s is already %s", r.ID, to)
	case to == "closed":
		ch.Op = core.TaskClose
	case from == "closed" && to == "open":
		ch.Op = core.TaskReopen
	case from == "closed":
		return core.TaskChange{}, fmt.Errorf("%s is closed: reopen it (move it to open) before moving it on", r.ID)
	case slices.Contains(core.TaskStatuses, to):
		ch.Op, ch.Status, ch.IfStatus = core.TaskUpdate, to, from
		if to == "in_progress" && r.BlockedBy > 0 {
			ch.Warning = fmt.Sprintf("%s is held by %d unfinished task(s)", r.ID, r.BlockedBy)
		}
	default:
		return core.TaskChange{}, fmt.Errorf("a card cannot be moved to %s", to)
	}
	return ch, nil
}
