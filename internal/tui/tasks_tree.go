package tui

import (
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

// treeKeyFor names the tracker whose fold state this is: its directory when known, else its name.
func (s *tasksSection) treeKeyFor(snap core.TaskSnapshot) string {
	if s.writer != nil {
		if d := s.writer.TrackerDir(); d != "" {
			return d
		}
	}
	return "name:" + snap.Tracker
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
	t.rep = l.view.Tree(taskview.TreeOptions{ListOptions: taskview.ListOptions{Query: l.q, Preset: l.preset, Sort: l.sort}, Folded: t.folded})
	t.rows = make([]core.TaskRow, len(t.rep.Rows))
	for i, r := range t.rep.Rows {
		t.rows[i] = r.TaskRow
	}
}

// saveTree keeps the fold choices of this tracker (only folds of tasks that exist).
func (s *tasksSection) saveTree() {
	t := &s.list.tree
	var ids []string
	for id := range t.folded {
		if _, ok := s.list.view.Row(id); ok {
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

// refold rebuilds after a fold change and keeps the selection: on the same task if it is still
// listed, else on its nearest listed ancestor (a task folded away under the cursor).
func (s *tasksSection) refold() {
	parents := map[string]string{}
	for _, r := range s.rows() {
		parents[r.ID] = r.Parent
	}
	want := s.selected
	s.recompute()
	for n := 0; want != "" && n <= len(parents); n++ {
		if s.selectID(want) {
			break
		}
		want = parents[want]
	}
	s.saveTree()
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
	guide, idw := 0, 0 // the title keeps room beside the widest branch lines; an id is never cut short of itself
	for _, r := range rows {
		guide = max(guide, lipgloss.Width(r.Guide))
		idw = max(idw, lipgloss.Width(san(r.ID)))
	}
	for i, r := range rows {
		g := th.Text(KindMuted, r.Guide)
		title := plain[i][1]
		cell := title
		if r.Context || r.Status == "closed" {
			cell = th.Text(KindMuted, title)
		}
		if r.Context {
			styled[i][0] = th.Text(KindMuted, plain[i][0])
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
	cols := slices.Clone(tb.cols)
	// Reserve room for the branch lines beside the title and for a whole id, but only as far as the
	// width allows: asking for more than fits would drop the title, the one column that must stay.
	avail, fixed := max(iw-2, 1), 2+2+3 // the status and priority cells and the gaps between four columns
	titleMin, idMin := cols[1].min+guide, min(idw, treeIDMax)
	if idMin+titleMin+fixed <= avail {
		cols[0].max = max(cols[0].max, idMin) // dotted child ids run long
		cols[0].min = max(cols[0].min, idMin)
	}
	if cols[0].min+titleMin+fixed <= avail {
		cols[1].min = titleMin
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
