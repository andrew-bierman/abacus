package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The header context (k9s keeps its cluster and context in the header): from bpWide columns the
// right side of the tab-bar row says which tracker and board this is and how many tasks there are.
// The data is core.ContextLine; this file only decides how much of it fits. It never pushes the tab
// bar: the parts are dropped from the least important end, and the first is cut with an ellipsis
// before anything is wrapped.

// contextLine gathers the context from the sections that have some and the board target the shell
// was built with.
func (m *Model) contextLine() core.ContextLine {
	var c core.ContextLine
	for _, t := range m.tabs {
		if cx, ok := inner(t.sec).(contexter); ok {
			c = cx.contextLine()
		}
	}
	c.Board = m.boardTarget
	if c.Board == core.BoardTarget("") {
		for _, t := range m.tabs {
			if board, ok := t.sec.(*boardSection); ok && (!board.loaded || board.err != nil) {
				c.Board = ""
				break
			}
		}
	}
	return c
}

// contextMinRoom is the least room, in cells, worth drawing context in; contextMinName the least
// worth keeping of a tracker name that has to be cut to leave room for the count.
const (
	contextMinRoom = 8
	contextMinName = 4
)

// withContext right-aligns the context on the tab-bar line when there is room for it.
func (m *Model) withContext(line string, w int) string {
	if w < bpWide {
		return line
	}
	room := w - lipgloss.Width(line) - 2
	if room < contextMinRoom {
		return line
	}
	c := m.contextLine()
	parts := c.Parts()
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, " · ")) > room {
		if len(parts) == 2 && c.HaveTasks && strings.TrimSpace(c.Tracker) != "" { // tracker and count: the name is cut before the count goes
			if keep := room - lipgloss.Width(" · "+parts[1]); keep >= contextMinName {
				parts[0] = trunc(parts[0], keep)
				break
			}
		}
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return line
	}
	text := trunc(strings.Join(parts, " · "), room)
	return line + strings.Repeat(" ", w-lipgloss.Width(line)-lipgloss.Width(text)) + m.th.Text(KindMuted, text)
}
