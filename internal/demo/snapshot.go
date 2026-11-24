package demo

import (
	"context"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// TaskSnapshot is the demo tracker: fourteen invented tasks with an epic and its children, a
// blocking chain, a dependency cycle, labels, comments and every status, so the Tasks list and
// detail show real structure without a tracker. Times are relative to now.
func TaskSnapshot(now time.Time) core.TaskSnapshot {
	ago := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }
	blocks := func(id string) core.TaskDep { return core.TaskDep{DependsOn: id, Type: "blocks"} }
	child := core.TaskDep{DependsOn: "ac-01", Type: "parent-child"}
	t := func(id, title, status string, prio int, typ, who string, created float64, labels []string, deps ...core.TaskDep) core.TaskIssue {
		return core.TaskIssue{ID: id, Title: title, Status: status, Priority: prio, Type: typ, Assignee: who, Labels: labels,
			CreatedAt: ago(created), UpdatedAt: ago(created / 3), Deps: deps}
	}
	epic := t("ac-01", "Fleet workspace", "open", 1, "epic", "mei", 24*30, []string{"workspace"})
	epic.Description = "# Goal\n\nOne place to see and steer the fleet.\n\n- leases show who holds what\n- queues show what is running\n- tasks show what is next\n\nKeep every screen readable at **60x24**."
	hb := t("ac-12", "wire the lease heartbeat", "in_progress", 0, "bug", "ada", 24*9, []string{"core", "leases"}, child)
	hb.Description = "The heartbeat misses a beat when a session resumes, so a live lease looks stale for up to a minute.\n\n## Plan\n\n- renew on resume, not on the next tick\n- show the miss in Doctor\n\n```\nrenew --lease dm-zoj --now\n```\n\nUses `renew` with the `--now` flag."
	hb.Comments = []core.TaskComment{
		{Author: "mei", Text: "Seen twice on the laptop after sleep.", CreatedAt: ago(30)},
		{Author: "ada", Text: "Reproduced. The tick interval is the cause; fix is small.", CreatedAt: ago(6)},
	}
	hb.Acceptance = "A resumed session keeps its lease without a stale window."
	digest := t("ac-09", "queue attention digest", "open", 2, "feature", "", 24*12, []string{"queues"}, child, blocks("ac-12"))
	digest.Description = "Summarize what needs attention across queues in one row."
	deferred := t("ac-36", "verify tool flags against help", "deferred", 3, "chore", "mei", 24*40, []string{"later"})
	deferred.DeferUntil = now.Add(14 * 24 * time.Hour)
	closed := t("ac-03", "retire the old poller", "closed", 3, "task", "ada", 24*20, []string{"core"}, child)
	closed.ClosedAt, closed.CloseReason = ago(24*5), "replaced by the snapshot reader"
	iss := []core.TaskIssue{
		epic,
		closed,
		func() core.TaskIssue {
			c := t("ac-04", "document the write gate", "closed", 2, "chore", "mei", 24*18, nil)
			c.ClosedAt = ago(24 * 8)
			return c
		}(),
		digest,
		hb,
		t("ac-15", "lease holder column", "open", 1, "feature", "mei", 24*6, []string{"leases", "ui"}, child),
		t("ac-16", "stale lease warning", "open", 2, "task", "", 24*5, []string{"leases"}, child, blocks("ac-15")),
		t("ac-20", "split graph source from export", "in_progress", 1, "task", "ada", 24*4, []string{"core"}, blocks("ac-21")),
		t("ac-21", "export reader", "open", 2, "task", "", 24*4, []string{"core"}, blocks("ac-20")),
		t("ac-25", "doctor: stale export warning", "blocked", 2, "bug", "ada", 24*3, []string{"doctor"}),
		t("ac-27", "board thread digest", "blocked", 3, "feature", "", 24*2, []string{"later"}),
		t("ac-30", "saved filters", "open", 3, "feature", "", 24*2, []string{"later", "ui"}),
		t("ac-33", "help overlay per view", "in_progress", 3, "task", "mei", 30, []string{"ui"}),
		deferred,
	}
	return core.TaskSnapshot{Issues: iss, ReadAt: now, Tracker: "fleet"}
}

// Snapshot makes the demo Tasks provider the snapshot source too.
func (Tasks) Snapshot(context.Context) (core.TaskSnapshot, error) {
	return TaskSnapshot(time.Now()), nil
}
