package taskview

import (
	"fmt"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

// row is a TaskRow with the values sorting and filtering need.
type row struct {
	core.TaskRow
	created, updated, deferUntil time.Time
	hasDesc                      bool
}

// View is one snapshot with everything derived from it. Build once per snapshot; List and Detail
// are cheap and pure after that.
type View struct {
	snap   core.TaskSnapshot
	now    time.Time
	rows   []row
	byID   map[string]int
	blkBy  map[string][]string // blocking dependencies, unfinished or not, in snapshot order
	blks   map[string][]string // what each task blocks (all dependents)
	kids   map[string][]string
	parent map[string]string
}

// Build derives blocked, ready, parents and children from the snapshot (KTD4).
//
//   - a blocker is a dependency of type "blocks" whose target is in the snapshot and not closed;
//     a target that is not in the snapshot is shown but never blocks
//   - ready: stored status open, no unfinished blocker, and not deferred into the future
//   - a parent is the target of a "parent-child" dependency; progress counts direct children
func Build(snap core.TaskSnapshot, now time.Time) *View {
	v := &View{snap: snap, now: now, byID: map[string]int{}, blkBy: map[string][]string{}, blks: map[string][]string{},
		kids: map[string][]string{}, parent: map[string]string{}}
	for i, it := range snap.Issues {
		v.byID[it.ID] = i
	}
	for _, it := range snap.Issues {
		for _, d := range it.Deps {
			switch d.Type {
			case "blocks":
				v.blkBy[it.ID] = append(v.blkBy[it.ID], d.DependsOn)
				v.blks[d.DependsOn] = append(v.blks[d.DependsOn], it.ID)
			case "parent-child":
				if _, ok := v.parent[it.ID]; !ok {
					v.parent[it.ID] = d.DependsOn
					v.kids[d.DependsOn] = append(v.kids[d.DependsOn], it.ID)
				}
			}
		}
	}
	for _, it := range snap.Issues {
		v.rows = append(v.rows, v.rowOf(it))
	}
	return v
}

func (v *View) closed(id string) bool {
	i, ok := v.byID[id]
	return ok && v.snap.Issues[i].Status == "closed"
}

func (v *View) known(id string) bool { _, ok := v.byID[id]; return ok }

func (v *View) rowOf(it core.TaskIssue) row {
	open := 0
	for _, b := range v.blkBy[it.ID] {
		if v.known(b) && !v.closed(b) {
			open++
		}
	}
	dependents := 0
	for _, d := range v.blks[it.ID] {
		if !v.closed(d) {
			dependents++
		}
	}
	deferred := it.DeferUntil.After(v.now)
	r := core.TaskRow{
		ID: it.ID, Title: it.Title, Status: it.Status, Priority: it.Priority, Type: it.Type, Assignee: it.Assignee,
		Labels: it.Labels, CreatedAt: rfc(it.CreatedAt), UpdatedAt: rfc(it.UpdatedAt), DeferUntil: rfc(it.DeferUntil),
		Age: Age(it.CreatedAt, v.now), BlockedBy: open, Blocks: dependents, Blocked: open > 0,
		Ready:  it.Status == "open" && open == 0 && !deferred,
		Parent: v.parent[it.ID], Comments: len(it.Comments),
	}
	for _, k := range v.kids[it.ID] {
		r.ChildTotal++
		if v.closed(k) {
			r.ChildDone++
		}
	}
	return row{TaskRow: r, created: it.CreatedAt, updated: it.UpdatedAt, deferUntil: it.DeferUntil, hasDesc: strings.TrimSpace(it.Description) != ""}
}

func rfc(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Age is a compact age: minutes under an hour, hours under a day, then days (under 100), months
// and years. A zero time or one in the future has no age.
func Age(t, now time.Time) string {
	if t.IsZero() || t.After(now) {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 100*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d/(30*24*time.Hour)))
	}
	return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
}

// Total and Ready are the counts of the whole tracker.
func (v *View) Total() int { return len(v.rows) }

func (v *View) Ready() int {
	n := 0
	for _, r := range v.rows {
		if r.Ready {
			n++
		}
	}
	return n
}

// Doc makes a row searchable by the shared query grammar.
func (r row) Field(name string) (query.Value, bool) {
	switch name {
	case "id":
		return query.Value{Strs: []string{r.ID}}, true
	case "title":
		return query.Value{Strs: []string{r.Title}}, true
	case "status":
		return query.Value{Strs: []string{r.Status}}, true
	case "who":
		return one(r.Assignee), true
	case "type":
		return one(r.Type), true
	case "parent":
		return one(r.Parent), true
	case "label":
		return query.Value{Strs: r.Labels}, true
	case "priority":
		return query.Value{Num: r.Priority, HasNum: true}, true
	case "blocked":
		return boolValue(r.Blocked), true
	case "ready":
		return boolValue(r.Ready), true
	case "in":
		return query.Value{Strs: []string{"tasks"}}, true
	case "is":
		var s []string
		if r.Blocked {
			s = append(s, "blocked")
		}
		if r.Ready {
			s = append(s, "ready")
		}
		return query.Value{Strs: s}, true
	case "has":
		var s []string
		for _, h := range []struct {
			has  bool
			name string
		}{{r.Comments > 0, "comments"}, {r.hasDesc, "description"}, {len(r.Labels) > 0, "labels"}, {r.Parent != "", "parent"}, {r.ChildTotal > 0, "children"}, {r.Assignee != "", "assignee"}} {
			if h.has {
				s = append(s, h.name)
			}
		}
		return query.Value{Strs: s}, true
	case "updated":
		return query.Value{Time: r.updated, HasTime: !r.updated.IsZero()}, true
	case "created":
		return query.Value{Time: r.created, HasTime: !r.created.IsZero()}, true
	}
	return query.Value{}, false
}

func one(s string) query.Value {
	if s == "" {
		return query.Value{}
	}
	return query.Value{Strs: []string{s}}
}

func boolValue(b bool) query.Value {
	if b {
		return query.Value{Strs: []string{"true"}}
	}
	return query.Value{Strs: []string{"false"}}
}

// Text is what free words search: the id, the title and the labels.
func (r row) Text() []string { return append([]string{r.ID, r.Title}, r.Labels...) }
