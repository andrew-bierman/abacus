package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

// What the Tasks section offers the prompt, the describe keys and the header: select a view by name,
// set the list filter, say which item is under the cursor, say how big the tracker is. All of it is
// read-only navigation over state the section already holds.

// loadingNote is what a view or filter command says while the tracker is still being read: it would
// change state that the first snapshot then overwrites.
const loadingNote = "Tasks is still loading: try again in a moment"

// switchView selects a Tasks view by the name the prompt uses, through showView, the same entry the
// `v` key takes. `tree` and `list` are the two modes of the List view; the layout file is only
// written when the mode actually changes (the prompt keeps UI layout state, nothing else).
func (s *tasksSection) switchView(name string) (tea.Cmd, string) {
	l := &s.list
	name = strings.ToLower(name)
	if name == "tree" || name == "list" {
		if l.view == nil {
			return nil, loadingNote
		}
		t := &l.tree
		switch {
		case name == "tree" && l.depsUnknown:
			return s.showView(viewList), "the tree needs the whole tracker: not available in recent changes"
		case name == "tree" && !t.hier:
			return s.showView(viewList), "no parent-child links in this tracker: nothing to fold"
		}
		cmd := s.showView(viewList)
		want := map[string]string{"tree": "tree", "list": "flat"}[name]
		if (name == "tree") != s.treeOn() || (name == "list" && t.mode != "flat") {
			t.mode = want
			s.recompute()
			s.saveTree()
			s.moved()
		}
		return cmd, ""
	}
	for _, v := range taskViews {
		if strings.EqualFold(v.name, name) {
			return s.showView(v.id), ""
		}
	}
	return nil, name + " view is not available"
}

// setFilter applies the query to the List (an empty one clears it). The prompt has already parsed it
// with the shared grammar; a parse error here would only mean the grammar changed underneath.
func (s *tasksSection) setFilter(text string) string {
	l := &s.list
	if l.view == nil {
		return loadingNote
	}
	q, err := query.Parse(text)
	if err != nil {
		return "bad filter: " + core.Sanitize(err.Error())
	}
	s.showView(viewList)
	l.text, l.q, l.qerr = text, q, nil
	s.recompute()
	s.moved()
	return ""
}

// selectedID is the task under the cursor in the active view: the list's row, the board's selected
// card, or the Graph's selected node.
func (s *tasksSection) selectedID() string {
	switch {
	case s.list.cur == viewKanban:
		return s.kb.sel
	case s.list.cur == viewGraph && s.graph.sel != "":
		return s.graph.sel
	case s.list.cur == viewList && s.cursor < len(s.rows()):
		return s.rows()[s.cursor].ID
	}
	return ""
}

// describeSelected is the selected task as the detail pane draws it and as `tasks show --json`
// prints it.
func (s *tasksSection) describeSelected() (describeItem, string) {
	if s.list.view == nil {
		return describeItem{}, "Tasks is still loading"
	}
	id := s.selectedID()
	if id == "" {
		if s.list.cur == viewInsights {
			return describeItem{}, "nothing to describe in this view: pick a task in the List, the Kanban or the Graph"
		}
		return describeItem{}, "no task selected"
	}
	d, ok := s.list.view.Detail(id)
	if !ok {
		return describeItem{}, "that task is no longer in the tracker"
	}
	return describeItem{title: "Task " + san(id), report: d, lines: func(w int) []string { return s.detailLines(d, w) }}, ""
}

// contextLine is what the header says about Tasks: the tracker and its size.
func (s *tasksSection) contextLine() core.ContextLine {
	if !s.loaded && s.list.view == nil {
		return core.ContextLine{}
	}
	var c core.ContextLine
	switch {
	case s.list.view != nil && !s.list.fallback:
		c = core.ContextLine{Tracker: s.list.rep.Tracker, Tasks: s.list.rep.Total, Ready: s.list.rep.Ready, HaveTasks: true, ReadyKnown: !s.list.depsUnknown}
	case s.loaded && s.err == nil: // the whole tracker was not read: the summary's total, with no ready count
		c = core.ContextLine{Tracker: s.list.rep.Tracker, Tasks: s.report.Counts.Total, HaveTasks: true}
	}
	return c
}
