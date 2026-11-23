package query

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Value is a document's value for one field: any of the three views may be set.
type Value struct {
	Strs    []string // text values (a task has one status and many labels); empty means "none"
	Bool    bool
	HasBool bool
	Num     int
	HasNum  bool
	Time    time.Time
	HasTime bool
}

// Doc is what Match needs from a searchable item. Each domain implements it over its own typed
// report rows, so the grammar knows nothing about tasks, threads or leases.
type Doc interface {
	// Field returns the value of a canonical field. ok is false when the document has no such
	// field at all (a lease has no label), which excludes it from any query that names the field;
	// a field that exists but is empty returns ok true and no values.
	Field(name string) (v Value, ok bool)
	// Text is what free words search: the fields a plain word may match.
	Text() []string
}

// Match reports whether d satisfies q. now anchors durations such as 7d.
func (q Query) Match(d Doc, now time.Time) bool {
	for _, w := range q.Words {
		if !wordMatches(w, d.Text()) {
			return false
		}
	}
	byField := map[string][]Pred{}
	var order []string
	for _, p := range q.Preds {
		if _, seen := byField[p.Field]; !seen {
			order = append(order, p.Field)
		}
		byField[p.Field] = append(byField[p.Field], p)
	}
	for _, f := range order {
		v, ok := d.Field(f)
		if !ok {
			return false
		}
		if !fieldMatches(f, byField[f], v, now) {
			return false
		}
	}
	return true
}

func wordMatches(w Word, texts []string) bool {
	for _, t := range texts {
		t = strings.ToLower(t)
		if w.Phrase || len([]rune(w.Text)) > ShortWord {
			if strings.Contains(t, w.Text) {
				return true
			}
		} else if subsequence(w.Text, t) {
			return true
		}
	}
	return false
}

func subsequence(needle, hay string) bool {
	n := []rune(needle)
	i := 0
	for _, r := range hay {
		if i < len(n) && r == n[i] {
			i++
		}
	}
	return i == len(n)
}

// fieldMatches applies every predicate on one field: the positive equality values are alternatives
// (OR), each comparison and each exclusion must also hold (AND).
func fieldMatches(field string, preds []Pred, v Value, now time.Time) bool {
	var alts []Pred
	for _, p := range preds {
		switch {
		case p.Neg:
			if predHolds(field, p, v, now) {
				return false
			}
		case p.Op == OpEq:
			alts = append(alts, p)
		default:
			if !predHolds(field, p, v, now) {
				return false
			}
		}
	}
	if len(alts) == 0 {
		return true
	}
	for _, p := range alts {
		if predHolds(field, p, v, now) {
			return true
		}
	}
	return false
}

// predHolds is whether the (non-negated) predicate is true of v: any of its values for equality.
func predHolds(field string, p Pred, v Value, now time.Time) bool {
	def := fields[field]
	for _, want := range p.Values {
		switch def.kind {
		case kInt:
			n, _ := parsePriority(want)
			if v.HasNum && cmpInt(v.Num, n, p.Op) {
				return true
			}
		case kTime:
			t, _ := parseTimeSpec(want, now)
			if v.HasTime && cmpTime(v.Time, t, p.Op) {
				return true
			}
		case kBool:
			if b, _ := parseBool(want); v.HasBool && v.Bool == b {
				return true
			}
		case kPrefix:
			for _, s := range v.Strs {
				if strings.HasPrefix(strings.ToLower(s), want) {
					return true
				}
			}
		case kSub:
			for _, s := range v.Strs {
				if strings.Contains(strings.ToLower(s), want) {
					return true
				}
			}
		default:
			if want == "none" && len(v.Strs) == 0 || hasStr(v.Strs, want) {
				return true
			}
		}
	}
	return false
}

func hasStr(strs []string, want string) bool {
	return slices.ContainsFunc(strs, func(s string) bool { return strings.ToLower(s) == want })
}

// holds is whether a comparison result c (-1, 0, +1) satisfies op.
func holds(op Op, c int) bool {
	switch op {
	case OpLT:
		return c < 0
	case OpLE:
		return c <= 0
	case OpGT:
		return c > 0
	case OpGE:
		return c >= 0
	}
	return c == 0
}

func cmpInt(a, b int, op Op) bool { return holds(op, cmp.Compare(a, b)) }

func cmpTime(a, b time.Time, op Op) bool { return holds(op, a.Compare(b)) }
