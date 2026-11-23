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
	heldBy map[string]string // task -> the nearest ancestor held by an unfinished blocker
	ready  int
}

// blocking is whether a dependency type holds the dependent back. Verified against bd 1.3.4 (see
// testdata/README.md): blocks and conditional-blocks hold; waits-for, related, discovered-from and
// every other kind do not, and a blocker outside the tracker (external:project:capability) never
// holds.
func blocking(t string) bool { return t == "blocks" || t == "conditional-blocks" }

// maxDepth bounds the walk up parent-child links, so a cycle in them cannot hang a read.
const maxDepth = 64

// Build derives blocked, ready, parents and children from the snapshot (KTD4), as bd ready does:
//
//   - a blocker is a blocking dependency whose target is in the snapshot and not closed
//   - a task is also held while an ancestor (through parent-child links) is held by a blocker
//   - closed work is never held and never ready
//   - ready: stored status open, not held, and not deferred into the future
//   - a parent is the target of a "parent-child" dependency; progress counts direct children
//
// A snapshot without dependency data (DepsUnknown) claims nothing is held or ready.
func Build(snap core.TaskSnapshot, now time.Time) *View {
	v := &View{snap: snap, now: now, byID: map[string]int{}, blkBy: map[string][]string{}, blks: map[string][]string{},
		kids: map[string][]string{}, parent: map[string]string{}, heldBy: map[string]string{}}
	for i, it := range snap.Issues {
		v.byID[it.ID] = i
	}
	for i, it := range snap.Issues {
		if v.byID[it.ID] != i { // a duplicate id: only the record that is shown contributes links
			continue
		}
		for _, d := range it.Deps {
			switch {
			case blocking(d.Type):
				v.blkBy[it.ID] = append(v.blkBy[it.ID], d.DependsOn)
				v.blks[d.DependsOn] = append(v.blks[d.DependsOn], it.ID)
			case d.Type == "parent-child":
				if _, ok := v.parent[it.ID]; !ok {
					v.parent[it.ID] = d.DependsOn
					v.kids[d.DependsOn] = append(v.kids[d.DependsOn], it.ID)
				}
			}
		}
	}
	if !snap.DepsUnknown {
		for _, it := range snap.Issues {
			if it.Status == "closed" {
				continue
			}
			p := it.ID
			for depth := 0; depth < maxDepth; depth++ {
				pp, ok := v.parent[p]
				if !ok || !v.known(pp) || v.closed(pp) {
					break
				}
				if v.openBlockers(pp) > 0 {
					v.heldBy[it.ID] = pp
					break
				}
				p = pp
			}
		}
	}
	for _, it := range snap.Issues {
		r := v.rowOf(it)
		if r.Ready {
			v.ready++
		}
		v.rows = append(v.rows, r)
	}
	return v
}

func (v *View) closed(id string) bool {
	i, ok := v.byID[id]
	return ok && v.snap.Issues[i].Status == "closed"
}

func (v *View) known(id string) bool { _, ok := v.byID[id]; return ok }

// openBlockers counts the unfinished blockers of id that are in the snapshot.
func (v *View) openBlockers(id string) int {
	n := 0
	for _, b := range v.blkBy[id] {
		if v.known(b) && !v.closed(b) {
			n++
		}
	}
	return n
}

func (v *View) rowOf(it core.TaskIssue) row {
	open, dependents, held := 0, 0, ""
	closed := it.Status == "closed"
	if !v.snap.DepsUnknown {
		if !closed {
			open, held = v.openBlockers(it.ID), v.heldBy[it.ID]
		}
		for _, d := range v.blks[it.ID] {
			if !v.closed(d) {
				dependents++
			}
		}
	}
	blocked := open > 0 || held != ""
	deferred := it.DeferUntil.After(v.now)
	r := core.TaskRow{
		ID: it.ID, Title: it.Title, Status: it.Status, Priority: it.Priority, Type: it.Type, Assignee: it.Assignee,
		Labels: it.Labels, CreatedAt: rfc(it.CreatedAt), UpdatedAt: rfc(it.UpdatedAt), DeferUntil: rfc(it.DeferUntil),
		Age: Age(it.CreatedAt, v.now), BlockedBy: open, Blocks: dependents, Blocked: blocked, HeldBy: held,
		Ready:  !v.snap.DepsUnknown && it.Status == "open" && !blocked && !deferred,
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

func (v *View) Ready() int { return v.ready }

// Counts are the tracker's totals by stored status plus the derived ready count: what the strip,
// the list and the Insights summary all show, from the one snapshot. A closed or open status is
// bd's own; any other status (pinned, hooked) is in Total only.
func (v *View) Counts() core.TaskCounts {
	c := core.TaskCounts{Total: len(v.rows), Ready: v.ready}
	for _, r := range v.rows {
		switch r.Status {
		case "open":
			c.Open++
		case "in_progress":
			c.InProgress++
		case "blocked":
			c.Blocked++
		case "deferred":
			c.Deferred++
		case "closed":
			c.Closed++
		}
	}
	return c
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

func boolValue(b bool) query.Value { return query.Value{Bool: b, HasBool: true} }

// Text is what free words search: the id, the title and the labels.
func (r row) Text() []string { return append([]string{r.ID, r.Title}, r.Labels...) }
