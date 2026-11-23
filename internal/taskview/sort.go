package taskview

import (
	"sort"
	"strings"
	"time"
)

// SortKey is one of the eight orderings (the same set b9s offers).
type SortKey string

const (
	SortPriority SortKey = "priority"
	SortCreated  SortKey = "created"
	SortUpdated  SortKey = "updated"
	SortTitle    SortKey = "title"
	SortStatus   SortKey = "status"
	SortType     SortKey = "type"
	SortDeps     SortKey = "deps"
	SortDeferred SortKey = "deferred"
)

// SortKeys is the cycle order of the sort key.
var SortKeys = []SortKey{SortPriority, SortCreated, SortUpdated, SortTitle, SortStatus, SortType, SortDeps, SortDeferred}

// Sort is a key and a direction. Ties are always broken by id, ascending, whatever the direction,
// so the order is total and a refresh never shuffles equal rows.
type Sort struct {
	Key  SortKey
	Desc bool
}

// DefaultSort is priority, most urgent first.
func DefaultSort() Sort { return Sort{Key: SortPriority} }

// DefaultDesc is the natural direction of a key: newest first for times, most dependencies first
// for deps, and A to Z or P0 first for the rest.
func (k SortKey) DefaultDesc() bool {
	return k == SortCreated || k == SortUpdated || k == SortDeps
}

// Valid reports whether k is one of the eight keys.
func (k SortKey) Valid() bool {
	for _, s := range SortKeys {
		if s == k {
			return true
		}
	}
	return false
}

// Next is the following key in its natural direction.
func (s Sort) Next() Sort {
	for i, k := range SortKeys {
		if k == s.Key {
			n := SortKeys[(i+1)%len(SortKeys)]
			return Sort{Key: n, Desc: n.DefaultDesc()}
		}
	}
	return DefaultSort()
}

// Flip reverses the direction.
func (s Sort) Flip() Sort { return Sort{Key: s.Key, Desc: !s.Desc} }

// statusRank orders the known statuses by how far along they are; an unknown status goes last.
func statusRank(s string) int {
	for i, k := range []string{"open", "in_progress", "blocked", "deferred", "closed"} {
		if s == k {
			return i
		}
	}
	return 5
}

// cmp compares two rows on the key alone: -1, 0 or +1 in ascending order, and whether the
// value is missing on either side (a missing value sorts last in both directions).
func cmpKey(k SortKey, a, b row) (c int, missing bool) {
	switch k {
	case SortPriority:
		return cmpInt(a.Priority, b.Priority), false
	case SortCreated:
		return cmpTime(a.created, b.created)
	case SortUpdated:
		return cmpTime(a.updated, b.updated)
	case SortDeferred:
		return cmpTime(a.deferUntil, b.deferUntil)
	case SortTitle:
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)), false
	case SortStatus:
		if c := cmpInt(statusRank(a.Status), statusRank(b.Status)); c != 0 {
			return c, false
		}
		return strings.Compare(a.Status, b.Status), false
	case SortType:
		return strings.Compare(a.Type, b.Type), false
	case SortDeps:
		if c := cmpInt(a.BlockedBy, b.BlockedBy); c != 0 {
			return c, false
		}
		return cmpInt(a.Blocks, b.Blocks), false
	}
	return 0, false
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpTime(a, b time.Time) (int, bool) {
	switch {
	case a.IsZero() && b.IsZero():
		return 0, true
	case a.IsZero():
		return 1, true
	case b.IsZero():
		return -1, true
	case a.Before(b):
		return -1, false
	case a.After(b):
		return 1, false
	}
	return 0, false
}

// less is the whole comparator: the key in the chosen direction, a missing value last, then id.
func less(s Sort, a, b row) bool {
	c, missing := cmpKey(s.Key, a, b)
	if c != 0 {
		if s.Desc && !missing {
			c = -c
		}
		return c < 0
	}
	return a.ID < b.ID
}

func sortRows(rows []row, s Sort) {
	sort.SliceStable(rows, func(i, j int) bool { return less(s, rows[i], rows[j]) })
}
