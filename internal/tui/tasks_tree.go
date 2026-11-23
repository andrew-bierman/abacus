package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// The tree is a mode of the List view: the same rows, filters and sort, drawn as epics and their
// children. It is the default when the tracker has parent-child links and `T` switches to the flat
// list and back. All building (parents, orphans, cycles, which rows a filter keeps, folds) is
// taskview's; this file keeps the fold choices, saves them and draws.

var (
	keyTree    = key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "tree/flat"))
	keyFold    = key.NewBinding(key.WithKeys("h", "l"), key.WithHelp("h/l", "fold"))
	keyFoldAll = key.NewBinding(key.WithKeys("+", "-"), key.WithHelp("+/-", "all"))
)

// treeBarW is the width of the progress bar of an epic row.
const treeBarW = 4

// The cells the list's other columns and the title are guaranteed beside the tree's branch lines
// and the id: the status and priority cells with the gaps between four columns, the narrowest id,
// and the least title worth showing.
const (
	treeFixed      = 2 + 2 + 3
	treeIDFloor    = 5
	treeTitleFloor = 14
)

// treeIDMax is the widest an id column grows for the dotted ids of nested children.
const treeIDMax = 18

// taskTree is the tree mode's state: the fold choices of the open tracker (kept in abacus's state
// directory, never in the tracker), the user's tree-or-flat choice, and the tree of the current
// snapshot, query and sort.
type taskTree struct {
	store  *core.TreeState // nil keeps nothing across runs
	key    string          // the tracker the folds belong to
	mode   string          // "", "tree" or "flat": the saved choice
	folded map[string]bool
	hier   bool // the snapshot has at least one parent-child link
	rep    core.TaskTreeReport
	rows   []core.TaskRow // rep's rows as the list sees them
}

// treeOn is whether the List view is drawn as a tree.
func (s *tasksSection) treeOn() bool {
	l := &s.list
	return l.view != nil && !l.depsUnknown && l.tree.hier && l.tree.mode != "flat"
}

// treeKeyFor names the tracker whose fold state this is: the absolute directory of the project the
// snapshot was read from, never its folder name (two projects can both be called api). Without a
// root (a provider that cannot say) the name is a last resort and is namespaced apart from roots.
func (s *tasksSection) treeKeyFor(snap core.TaskSnapshot) string {
	switch {
	case snap.Root != "":
		return snap.Root
	case s.writer != nil && s.writer.TrackerDir() != "":
		return s.writer.TrackerDir()
	}
	return "unrooted:" + snap.Tracker
}

// loadTree takes the saved choices when a snapshot of another tracker arrives (and the first one).
func (s *tasksSection) loadTree(snap core.TaskSnapshot) {
	t := &s.list.tree
	k := s.treeKeyFor(snap)
	if t.key == k && t.folded != nil {
		return
	}
	saved := t.store.Load(k)
	t.key, t.mode, t.folded = k, saved.View, map[string]bool{}
	for _, id := range saved.Folded {
		t.folded[id] = true
	}
}

// recomputeTree rebuilds the tree from the snapshot under the list's preset, query and sort.
func (s *tasksSection) recomputeTree() {
	l := &s.list
	t := &l.tree
	t.rep, t.rows = core.TaskTreeReport{}, nil
	t.hier = l.view != nil && l.view.HasHierarchy()
	if !s.treeOn() {
		return
	}
	t.rep = l.view.Tree(taskview.TreeOptions{ListOptions: taskview.ListOptions{Query: l.q, Preset: l.preset, Sort: l.sort}, Folded: t.folded, GuideLevels: s.treeGuideLevels()})
	t.rows = make([]core.TaskRow, len(t.rep.Rows))
	for i, r := range t.rep.Rows {
		t.rows[i] = r.TaskRow
	}
}

// saveTree keeps the fold choices of this tracker (only folds of tasks that exist).
func (s *tasksSection) saveTree() {
	t := &s.list.tree
	// A task that is gone loses its fold, but only judged against a complete, fresh snapshot: a
	// fallback list, a failed read or a read that looks incomplete would drop folds of tasks it merely lacks.
	l := &s.list
	prune := !l.fallback && l.snapErr == nil && len(l.warnings) == 0
	var ids []string
	for id := range t.folded {
		if _, ok := l.view.Row(id); ok || !prune {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if err := t.store.Save(t.key, core.TreeLayout{Folded: ids, View: t.mode}); err != nil {
		s.notice = "could not keep the fold state: " + core.Sanitize(err.Error())
	}
}

// selectID moves the cursor onto a task if it is listed.
func (s *tasksSection) selectID(id string) bool {
	ids := s.ids()
	i := slices.Index(ids, id)
	if i < 0 {
		return false
	}
	s.cursor = i
	s.remember(ids)
	s.moved()
	return true
}

// refold rebuilds after a fold change and saves it. The selection stays on the same task, or climbs to
// its nearest listed ancestor if its branch was folded away (recompute does that for every path).
func (s *tasksSection) refold() {
	s.recompute()
	s.saveTree()
}

// climbToListed moves the selection to its nearest listed ancestor when the selected task is not
// in the tree (it sits inside a folded parent, or a filter or refresh just hid it), so a recompute
// never leaves the cursor on an unrelated row. It walks the parent links of the snapshot, bounded.
func (s *tasksSection) climbToListed() {
	l := &s.list
	if !s.treeOn() || s.selected == "" {
		return
	}
	listed := make(map[string]bool, len(l.tree.rows))
	for _, r := range l.tree.rows {
		listed[r.ID] = true
	}
	if listed[s.selected] {
		return
	}
	want := s.selected
	for n := 0; n < 1000; n++ {
		r, ok := l.view.Row(want)
		if !ok || r.Parent == "" {
			return
		}
		if want = r.Parent; listed[want] {
			s.selected = want
			return
		}
	}
}

// idCell fits an id into w cells. A cut id keeps its end, which tells siblings apart, behind an
// ellipsis led by the task's depth ("4…1.1"), which tells the levels of a first-child chain apart:
// ids like tr-x.1.1.1.1 differ only in length, so neither end alone is enough. The root is cut
// plainly ("…bcd").
func idCell(id string, depth, w int) string {
	if lipgloss.Width(id) <= w {
		return id
	}
	prefix := "…"
	if depth > 0 && w >= 4 {
		prefix = fmt.Sprint(depth) + "…"
	}
	r := []rune(id)
	for len(r) > 0 && lipgloss.Width(prefix)+lipgloss.Width(string(r)) > w {
		r = r[1:]
	}
	return prefix + string(r)
}

// treeGuideLevels is how many ancestor levels the branch lines may draw at this width: as many as
// leave the title its floor beside an id and the other columns, so deep nesting at 40 columns does
// not turn the title into branch lines.
func (s *tasksSection) treeGuideLevels() int {
	avail := max(duoLayout(s.width(), 0).listInner()-2, 1)
	budget := avail - treeFixed - treeIDFloor - treeTitleFloor
	return min(max((budget-6)/2, 1), taskview.MaxGuideLevels)
}

// treeKey handles T, h, l, + and - on the List view.
func (s *tasksSection) treeKey(k string) {
	l := &s.list
	t := &l.tree
	if s.inDetail() {
		return
	}
	if k == "T" {
		switch {
		case !t.hier:
			s.notice = "no parent-child links in this tracker: nothing to fold"
			return
		case s.treeOn():
			t.mode = "flat"
		default:
			t.mode = "tree"
		}
		s.recompute()
		s.saveTree()
		s.moved()
		return
	}
	rows := t.rep.Rows
	if len(rows) == 0 || s.cursor >= len(rows) {
		return
	}
	r := rows[s.cursor]
	paused := t.rep.FoldsPaused
	switch k {
	case "h":
		switch {
		case r.HasChildren && !r.Folded && !paused:
			t.folded[r.ID] = true
			s.refold()
		case r.Depth > 0:
			s.selectID(r.Parent)
		case r.HasChildren && paused:
			s.notice = "folds are paused while a query is active (esc clears it)"
		}
	case "l":
		switch {
		case r.Folded:
			delete(t.folded, r.ID)
			s.refold()
		case r.HasChildren && s.cursor+1 < len(rows):
			s.selectID(rows[s.cursor+1].ID)
		}
	case "+":
		if paused {
			s.notice = "folds are paused while a query is active (esc clears it)"
			return
		}
		if len(t.folded) > 0 {
			clear(t.folded)
			s.refold()
		}
	case "-":
		if paused {
			s.notice = "folds are paused while a query is active (esc clears it)"
			return
		}
		for _, id := range l.view.Foldable() {
			t.folded[id] = true
		}
		s.refold()
	}
}

// treeLead is the fold key: it comes right after the filter key in the footer, because a narrow
// footer is cut from the right and h/l is what tells a tree from a list.
func (s *tasksSection) treeLead() []key.Binding {
	if s.treeOn() {
		return []key.Binding{keyFold}
	}
	return nil
}

// treeBindings are the other tree keys shown in the footer and the help.
func (s *tasksSection) treeBindings() []key.Binding {
	switch {
	case s.treeOn():
		return []key.Binding{keyTree, keyFoldAll}
	case s.list.tree.hier && s.list.view != nil && !s.list.depsUnknown:
		return []key.Binding{keyTree}
	}
	return nil
}

// treeColumns turns the list's cells into the tree's: the branch lines and fold mark before each
// title, context rows dimmed, and a progress bar column after the title (dropped first when narrow;
// the count stays in the title).
func (s *tasksSection) treeColumns(iw int, tb table, hdr []string, styled, plain [][]string) (table, []string, [][]string, [][]string) {
	th := s.th
	rows := s.list.tree.rep.Rows
	guide, idw := 0, 0
	for _, r := range rows {
		guide = max(guide, lipgloss.Width(r.Guide))
		idw = max(idw, lipgloss.Width(san(r.ID)))
	}
	cols := slices.Clone(tb.cols)
	// Reserve room for the branch lines beside the title, and give the id what is left of a whole
	// id (cut from the left if need be), but never at the title's expense.
	avail := max(iw-2, 1)
	idW := min(idw, treeIDMax, avail-treeFixed-treeTitleFloor-guide)
	idW = max(idW, min(treeIDFloor, idw))
	if idW+treeTitleFloor+guide+treeFixed <= avail {
		cols[0].min, cols[0].max = idW, max(cols[0].max, idW)
		cols[1].min = treeTitleFloor + guide
	}
	for i, r := range rows {
		g := th.Text(KindMuted, r.Guide)
		title := plain[i][1]
		cell := title
		if r.Context || r.Status == "closed" {
			cell = th.Text(KindMuted, title)
		}
		id := idCell(plain[i][0], r.Depth, max(cols[0].min, 1))
		plain[i][0], styled[i][0] = id, id
		if r.Context {
			styled[i][0] = th.Text(KindMuted, id)
		}
		bar, barPlain := "", ""
		if r.ChildTotal > 0 {
			n := taskview.Progress(r.ChildDone, r.ChildTotal, treeBarW)
			barPlain = strings.Repeat("▰", n) + strings.Repeat("▱", treeBarW-n)
			bar = th.Text(KindOK, strings.Repeat("▰", n)) + th.Text(KindMuted, strings.Repeat("▱", treeBarW-n))
		}
		plain[i][1] = r.Guide + title
		styled[i][1] = g + cell
		styled[i] = slices.Insert(styled[i], 2, bar)
		plain[i] = slices.Insert(plain[i], 2, barPlain)
	}
	cols = slices.Insert(cols, 2, column{min: treeBarW, max: treeBarW, prio: 2})
	return table{cols: cols, gap: tb.gap}, slices.Insert(slices.Clone(hdr), 2, "done"), styled, plain
}

// treeLegendWanted is whether the legend spends a row on the tree's own marks: always when the rows
// are not all matches (dim context rows and paused folds need their words, since a dim row means
// nothing without color), otherwise only when the panel is tall enough to spare it.
func (s *tasksSection) treeLegendWanted(rows int) bool {
	t := s.list.tree
	return t.rep.FoldsPaused || t.rep.Shown > t.rep.Matched || rows >= 18
}

// treeLegend explains the marks only the tree has.
func (s *tasksSection) treeLegend(iw int) string {
	t := s.list.tree
	line := "▾ open ▹ folded"
	if t.rep.FoldsPaused {
		line += " · folds paused"
	}
	if t.rep.Shown > t.rep.Matched {
		line += " · dim: context"
	}
	return trunc(s.th.Text(KindMuted, line), iw)
}
