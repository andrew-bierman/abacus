package taskview

import (
	"slices"
	"sort"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The tree of epics and children. A parent is the target of a "parent-child" dependency (the first
// one wins), as Build derives it. The tree is a forest built from those links with three repairs, so
// any tracker draws and nothing hangs or vanishes: a task whose parent is not in the snapshot is a
// root (an orphan); a task that is its own parent is a root; a cycle of parent links is cut at its
// lowest id, which becomes a root. Duplicate ids are drawn once. Siblings and roots follow the
// list's sort, so the tree and the list order work the same way.

// MaxGuideWidth bounds a guide (the branch lines and fold mark before a title), so deep nesting
// cannot push the title off a narrow screen: levels beyond the last few collapse into "… ".
const MaxGuideWidth = 16

// maxGuideLevels is how many ancestor levels a guide draws.
const maxGuideLevels = 5

// Guide is the drawing prefix of a tree row. depth is 0 for a root; anc holds, for each ancestor
// between the root and the parent, whether that ancestor was the last of its siblings (its line stops),
// and may be trimmed to the nearest levels. last says the row is the last of its siblings, hasKids that
// it has visible children, folded that they are hidden.
func Guide(depth int, anc []bool, last, hasKids, folded bool) string {
	mark := " "
	switch {
	case folded:
		mark = "▹"
	case hasKids:
		mark = "▾"
	case depth > 0:
		mark = "─"
	}
	if depth == 0 {
		return mark + " "
	}
	var b strings.Builder
	if depth-1 > len(anc) {
		b.WriteString("… ")
	}
	for _, l := range anc {
		if l {
			b.WriteString("  ")
		} else {
			b.WriteString("│ ")
		}
	}
	if last {
		b.WriteString("└─")
	} else {
		b.WriteString("├─")
	}
	b.WriteString(mark + " ")
	return b.String()
}

// forest is the tree structure of a snapshot, independent of any filter or sort.
type forest struct {
	ids     []string          // unique ids, ascending
	tpar    map[string]string // tree parent; "" for a root
	orphans int
	cycles  []string
}

func (v *View) forest() forest {
	f := forest{tpar: map[string]string{}}
	for i, it := range v.snap.Issues {
		if v.byID[it.ID] == i { // a duplicate id is drawn once, as its later record
			f.ids = append(f.ids, it.ID)
		}
	}
	sort.Strings(f.ids)
	for _, id := range f.ids {
		p := v.parent[id]
		switch {
		case p == "":
		case p == id:
			f.cycles = append(f.cycles, id)
		case !v.known(p):
			f.orphans++
		default:
			f.tpar[id] = p
		}
	}
	// Cut every cycle of parent links at its lowest id. Starting from each id in ascending order
	// makes the cut depend on the ids only, never on the order of the export.
	state := map[string]int{} // 1 on the current path, 2 settled
	for _, start := range f.ids {
		var path []string
		cur := start
		for cur != "" && state[cur] == 0 {
			state[cur] = 1
			path = append(path, cur)
			cur = f.tpar[cur]
		}
		if cur != "" && state[cur] == 1 {
			members := path[slices.Index(path, cur):]
			m := slices.Min(members)
			delete(f.tpar, m)
			f.cycles = append(f.cycles, m)
		}
		for _, p := range path {
			state[p] = 2
		}
	}
	sort.Strings(f.cycles)
	return f
}

// HasHierarchy reports whether any task has a parent that is in the snapshot: the tree is worth
// showing. A summary without dependency data has none.
func (v *View) HasHierarchy() bool {
	if v.snap.DepsUnknown {
		return false
	}
	for id, p := range v.parent {
		if p != id && v.known(p) {
			return true
		}
	}
	return false
}

// Foldable lists the ids that have at least one child in the tree, ascending: what "collapse all" folds.
func (v *View) Foldable() []string {
	f := v.forest()
	has := map[string]bool{}
	for _, p := range f.tpar {
		has[p] = true
	}
	out := make([]string, 0, len(has))
	for id := range has {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// TreeOptions are the list's options plus which parents are folded. A folded id that has no
// children, or is not in the snapshot, does nothing.
type TreeOptions struct {
	ListOptions
	Folded map[string]bool
}

// Tree is the filtered, sorted tree. A task shows when it matches the preset and query, or when a
// task below it does (then it is Context: its ancestors keep the place of a hit). Folds hide the
// children of a folded parent, except while a query is active: a match is never hidden.
func (v *View) Tree(o TreeOptions) core.TaskTreeReport {
	if o.Preset == "" {
		o.Preset = PresetOpen
	}
	if o.Sort.Key == "" {
		o.Sort = DefaultSort()
	}
	f := v.forest()
	order := make([]row, 0, len(f.ids))
	for _, id := range f.ids {
		order = append(order, v.rows[v.byID[id]])
	}
	sortRows(order, o.Sort)
	match := map[string]bool{}
	kids := map[string][]string{}
	var roots []string
	for _, r := range order {
		if o.Preset.keeps(r) && (o.Query.Empty() || o.Query.Match(r, v.now)) {
			match[r.ID] = true
		}
		if p := f.tpar[r.ID]; p == "" {
			roots = append(roots, r.ID)
		} else {
			kids[p] = append(kids[p], r.ID)
		}
	}
	ctx := map[string]bool{}
	for id := range match {
		for p := f.tpar[id]; p != "" && !ctx[p]; p = f.tpar[p] {
			ctx[p] = true
		}
	}
	shown := func(id string) bool { return match[id] || ctx[id] }
	visKids := func(id string) []string {
		var out []string
		for _, k := range kids[id] {
			if shown(k) {
				out = append(out, k)
			}
		}
		return out
	}
	size := map[string]int{} // shown tasks below a task
	var measure func(id string) int
	measure = func(id string) int {
		n := 0
		for _, k := range visKids(id) {
			n += 1 + measure(k)
		}
		size[id] = n
		return n
	}
	paused := !o.Query.Empty()
	rep := core.TaskTreeReport{
		Header: core.Header{Domain: core.DomainTasks, Title: "Task tree", State: core.Connected, Source: core.SourceLive},
		Query:  o.Query.String(), Preset: string(o.Preset), Sort: string(o.Sort.Key), Descending: o.Sort.Desc,
		Matched: len(match), Total: len(v.rows), Ready: v.Ready(), FoldsPaused: paused,
		Orphans: f.orphans, ParentCycles: f.cycles, Tracker: v.snap.Tracker, ReadAt: rfc(v.snap.ReadAt),
		Warnings: v.snap.Warnings, Rows: []core.TaskTreeRow{},
	}
	var emit func(id string, depth int, anc []bool, last bool)
	emit = func(id string, depth int, anc []bool, last bool) {
		vk := visKids(id)
		folded := !paused && o.Folded[id] && len(vk) > 0
		tr := core.TaskTreeRow{TaskRow: v.rows[v.byID[id]].TaskRow, Depth: depth, HasChildren: len(vk) > 0, Folded: folded, Context: !match[id]}
		tr.Guide = Guide(depth, anc, last, tr.HasChildren, folded)
		if folded {
			tr.Hidden = size[id]
			rep.Folded++
		}
		rep.Rows = append(rep.Rows, tr)
		if folded {
			return
		}
		next := anc
		if depth > 0 {
			next = append(slices.Clone(anc), last)
			if len(next) > maxGuideLevels {
				next = next[len(next)-maxGuideLevels:]
			}
		}
		for i, k := range vk {
			emit(k, depth+1, next, i == len(vk)-1)
		}
	}
	for _, id := range roots {
		if shown(id) {
			measure(id)
			emit(id, 0, nil, false)
		}
	}
	rep.Shown = len(rep.Rows)
	return rep
}

// Progress is how many of width cells of a progress bar are filled for done of total: rounded,
// but never full while something is open and never empty once something is done, so a bar
// does not claim more or less than the count says. Zero total fills nothing.
func Progress(done, total, width int) int {
	if total <= 0 || width <= 0 {
		return 0
	}
	done = max(0, min(done, total))
	n := (done*width + total/2) / total
	if done > 0 && n == 0 {
		n = 1
	}
	if done < total && n >= width {
		n = width - 1
	}
	return min(max(n, 0), width)
}
