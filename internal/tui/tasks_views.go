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
	viewInsights
)

// taskViews are the Tasks views in strip order.
var taskViews = []struct {
	id   taskViewID
	name string
}{
	{viewList, "List"},
	{viewInsights, "Insights"},
}

// body draws the active view.
func (s *tasksSection) body() string {
	if s.list.cur == viewInsights {
		return s.insightsView()
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
		ctx = th.Text(KindMuted, fmt.Sprintf("%d tasks · %d ready", s.list.rep.Total, s.list.rep.Ready))
	}
	if ctx == "" || lipgloss.Width(left)+2+lipgloss.Width(ctx) > w {
		return trunc(left, w)
	}
	return left + strings.Repeat(" ", w-lipgloss.Width(left)-lipgloss.Width(ctx)) + ctx
}

// insightsView is the summary and graph insights panel the Tasks tab always had, now one view
// beside the list.
func (s *tasksSection) insightsView() string {
	th, w := s.th, s.width()
	h := s.bodyH(s.noticeText())
	iw := innerW(w)
	c := s.report.Counts
	var lines []string
	if bar := th.stackBar(min(iw, taskBarMax), taskBarSegments(c)); bar != "" {
		lines = append(lines, bar)
	}
	lines = append(lines, wrap(fmt.Sprintf("%d tasks: %d open · %d in progress · %d blocked · %d deferred · %d closed", c.Total, c.Open, c.InProgress, c.Blocked, c.Deferred, c.Closed), iw)...)
	lines = append(lines, "ready "+th.Text(KindOK, fmt.Sprint(c.Ready)), "")
	sub, ins := s.insightParts(iw)
	head := "Insights"
	if sub != "" {
		head += " · " + sub
	}
	lines = append(lines, ins...)
	panelH := h
	if h > 0 {
		panelH = max(h-1, 1)
	}
	body := []string{s.strip(w)}
	rows := 0
	if panelH > 0 {
		rows = max(panelH-panelChromeH, 1)
	}
	body = append(body, panel(th, head, clipLines(th, lines, rows), w, panelH, true)...)
	if n := s.noticeText(); n != "" {
		body = append(body, noticeLine(th, w, n)...)
	}
	return joinLines(body)
}
