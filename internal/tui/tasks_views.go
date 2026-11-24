package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// taskViewID names a view of the Tasks tab. Each later view (tree, kanban, graph) is one new
// file and one line in taskViews; the strip, `v` and `V` come from this table.
type taskViewID int

const (
	viewList taskViewID = iota
	viewKanban
	viewGraph
	viewInsights
)

// taskViews are the Tasks views in strip order.
var taskViews = []struct {
	id   taskViewID
	name string
}{
	{viewList, "List"},
	{viewKanban, "Kanban"},
	{viewGraph, "Graph"},
	{viewInsights, "Insights"},
}

// body draws the active view.
func (s *tasksSection) body() string {
	switch s.list.cur {
	case viewInsights:
		return s.insightsView()
	case viewKanban:
		return s.kanbanView()
	}
	if s.list.cur == viewGraph {
		return s.graphView()
	}
	return s.listView()
}

// strip is the row of views, the active one in brackets so it reads without color, with the
// tracker's size on the right from bpCompact columns.
func (s *tasksSection) strip(w int) string {
	th := s.th
	var parts []string
	for _, v := range taskViews {
		if v.id == s.list.cur {
			parts = append(parts, th.Title.Render("["+v.name+"]"))
		} else {
			parts = append(parts, " "+v.name+" ")
		}
	}
	left := strings.Join(parts, "")
	ctx := ""
	if s.list.view != nil {
		ctx = fmt.Sprintf("%d tasks · %d ready", s.list.rep.Total, s.list.rep.Ready)
		if s.list.depsUnknown { // dependencies were not read: nothing can be called ready
			ctx = fmt.Sprintf("%d tasks", s.list.rep.Total)
		}
		ctx = th.Text(KindMuted, ctx)
	}
	if ctx == "" || lipgloss.Width(left)+2+lipgloss.Width(ctx) > w {
		return trunc(left, w)
	}
	return left + strings.Repeat(" ", w-lipgloss.Width(left)-lipgloss.Width(ctx)) + ctx
}
