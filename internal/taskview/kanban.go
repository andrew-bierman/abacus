package taskview

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

// The board's groupings. Moving a card is only defined in the status grouping.
const (
	GroupStatus   = "status"
	GroupPriority = "priority"
	GroupType     = "type"
)

// Groups is the cycle order of the grouping.
var Groups = []string{GroupStatus, GroupPriority, GroupType}

// NextGroup is the grouping after g, wrapping; an unknown one cycles to status.
func NextGroup(g string) string {
	i := slices.Index(Groups, g)
	return Groups[(i+1)%len(Groups)]
}

// BoardOptions say which tasks the board shows and how it groups them. There is no preset: the
// board shows every status, closed folded unless ShowClosed.
type BoardOptions struct {
	Query      query.Query
	Group      string // empty or unknown means status
	ShowClosed bool
}

// statusColumns are the stored statuses that always have a column, in the order work moves.
var statusColumns = []string{"open", "in_progress", "blocked", "deferred"}

var statusTitles = map[string]string{"open": "Open", "in_progress": "In progress", "blocked": "Blocked", "deferred": "Deferred", "closed": "Closed"}

func statusTitle(s string) string {
	if t, ok := statusTitles[s]; ok {
		return t
	}
	return core.Sanitize(s) // a custom status is the tracker's text
}

func card(r row) core.KanbanCard {
	return core.KanbanCard{ID: r.ID, Title: r.Title, Status: r.Status, Priority: r.Priority, Type: r.Type, Assignee: r.Assignee, Age: r.Age,
		BlockedBy: r.BlockedBy, Blocks: r.Blocks, Blocked: r.Blocked, Ready: r.Ready}
}

// Board groups the snapshot into columns (KTD2). Status grouping has a column per stored status
// (the four working ones always, a custom status when a task holds it, closed last and folded to a
// count); priority and type grouping have a column per value held. Derived blocked is a badge on
// the card and a count on the column, never a column. Within a column cards are ordered by
// priority, then id, so a refresh never shuffles equal cards.
func (v *View) Board(o BoardOptions) core.KanbanReport {
	if !slices.Contains(Groups, o.Group) {
		o.Group = GroupStatus
	}
	var kept []row
	for _, r := range v.rows {
		if o.Query.Empty() || o.Query.Match(r, v.now) {
			kept = append(kept, r)
		}
	}
	sortRows(kept, DefaultSort())
	rep := core.KanbanReport{
		Header: core.Header{Domain: core.DomainTasks, Title: "Kanban", State: core.Connected, Source: core.SourceLive},
		Group:  o.Group, Query: o.Query.String(), Total: len(v.rows), Tracker: v.snap.Tracker, ReadAt: rfc(v.snap.ReadAt),
		Warnings: v.snap.Warnings, DepsUnknown: v.snap.DepsUnknown,
	}
	var keys []string
	title := func(k string) string { return k }
	key := func(r row) string { return r.Status }
	switch o.Group {
	case GroupPriority:
		key = func(r row) string { return fmt.Sprintf("p%d", r.Priority) }
		title = func(k string) string { return strings.ToUpper(k) }
	case GroupType:
		key = func(r row) string { return orNone(r.Type) }
		title = core.Sanitize
	default:
		title = statusTitle
	}
	held := map[string][]row{}
	for _, r := range kept {
		k := key(r)
		if o.Group != GroupStatus && r.Status == "closed" {
			// closed work is hidden (or shown) as a whole, never a column of its own outside status grouping
			if !o.ShowClosed {
				rep.ClosedHidden++
				continue
			}
		}
		held[k] = append(held[k], r)
	}
	switch o.Group {
	case GroupStatus:
		keys = append(keys, statusColumns...)
		var custom []string
		for k := range held {
			if !slices.Contains(statusColumns, k) && k != "closed" {
				custom = append(custom, k)
			}
		}
		sort.Strings(custom)
		keys = append(append(keys, custom...), "closed")
	default:
		for k := range held {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return groupRank(o.Group, keys[i]) < groupRank(o.Group, keys[j]) })
	}
	for _, k := range keys {
		c := core.KanbanColumn{Key: k, Title: title(k), Cards: []core.KanbanCard{}}
		for _, r := range held[k] {
			c.Count++
			if r.Priority <= 1 {
				c.P0P1++
			}
			if r.Blocked {
				c.Blocked++
			}
			if o.Group == GroupStatus && k == "closed" && !o.ShowClosed {
				continue
			}
			c.Cards = append(c.Cards, card(r))
		}
		if o.Group == GroupStatus && k == "closed" {
			c.Folded = !o.ShowClosed
			if c.Folded {
				rep.ClosedHidden += c.Count
			}
		}
		rep.Shown += len(c.Cards)
		rep.Columns = append(rep.Columns, c)
	}
	return rep
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// groupRank orders priority columns P0 first and type columns by the known type order, then A to Z,
// with "none" last.
func groupRank(group, k string) string {
	if group == GroupType {
		if i := slices.Index(core.TaskTypes, k); i >= 0 {
			return fmt.Sprintf("0%02d", i)
		}
		if k == "none" {
			return "2"
		}
		return "1" + k
	}
	return k
}
