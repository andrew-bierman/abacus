package taskview

import (
	"errors"
	"fmt"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// PlanDep is the pure planner of a dependency edit: blocked is the held task, blocker the one it waits for.
// It reads the snapshot only and never runs anything; the change goes through the write gate like every
// other Tasks write. It refuses, with a sentence the user can act on, what the tracker would refuse (a task
// on itself, an unknown task, a cycle) before anything is planned, and what would be a no-op or reach the
// wrong link (an edge that is already there, a removal of a link that is not a blocking one).
func (v *View) PlanDep(add bool, blocked, blocker string) (core.TaskChange, error) {
	if v.snap.DepsUnknown {
		return core.TaskChange{}, errors.New("dependencies were not read (only the recent changes are loaded), so a dependency cannot be checked: refresh and try again")
	}
	if blocked == blocker {
		return core.TaskChange{}, errors.New("a task cannot depend on itself")
	}
	bi, ok := v.byID[blocked]
	if !ok {
		return core.TaskChange{}, fmt.Errorf("%s is not in this tracker", blocked)
	}
	ki, ok := v.byID[blocker]
	if !ok {
		return core.TaskChange{}, fmt.Errorf("%s is not in this tracker", blocker)
	}
	held, by := v.snap.Issues[bi], v.snap.Issues[ki]
	ch := core.TaskChange{Op: core.TaskDepAdd, ID: blocked, Blocker: blocker, BlockerTitle: by.Title, Expect: held.Status,
		Before: core.Task{ID: held.ID, Title: held.Title, Status: held.Status, Priority: held.Priority, Type: held.Type, Assignee: held.Assignee}}
	var link string
	for _, d := range held.Deps {
		if d.DependsOn == blocker {
			link = d.Type
		}
	}
	if !add {
		ch.Op = core.TaskDepRemove
		switch {
		case link == "":
			return core.TaskChange{}, fmt.Errorf("%s does not wait for %s", blocked, blocker)
		case !blocking(link):
			return core.TaskChange{}, fmt.Errorf("%s is linked to %s as %q, not as a blocker: only a blocking link can be removed here", blocked, blocker, link)
		}
		return ch, nil
	}
	if link != "" {
		return core.TaskChange{}, fmt.Errorf("%s is already linked to %s (%s)", blocked, blocker, link)
	}
	if path := v.waitPath(blocker, blocked); path != nil {
		return core.TaskChange{}, fmt.Errorf("that would make a cycle: %s", strings.Join(append(path, blocker), " waits for "))
	}
	return ch, nil
}

// waitPath is a chain of waits-for (and parent-child) links from "from" down to "to", or nil. The
// tracker also refuses a cycle, but this says so before a plan exists and names the chain. The walk is
// bounded by the number of tasks.
func (v *View) waitPath(from, to string) []string {
	seen := map[string]bool{from: true}
	var walk func(id string) []string
	walk = func(id string) []string {
		i, ok := v.byID[id]
		if !ok {
			return nil
		}
		for _, d := range v.snap.Issues[i].Deps {
			if !blocking(d.Type) && d.Type != "parent-child" {
				continue
			}
			if d.DependsOn == to {
				return []string{id, to}
			}
			if !seen[d.DependsOn] {
				seen[d.DependsOn] = true
				if p := walk(d.DependsOn); p != nil {
					return append([]string{id}, p...)
				}
			}
		}
		return nil
	}
	return walk(from)
}
