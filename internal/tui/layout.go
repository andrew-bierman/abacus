package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// wideBreakpoint is the width at which list and detail sit side by side.
const wideBreakpoint = 100

// defaultWidth stands in until the first tea.WindowSizeMsg arrives.
const defaultWidth = 80

// trunc cuts s to w display cells, ending with an ellipsis when it had to cut. It measures
// cells (wide runes, combining marks, SGR escapes), never bytes or runes.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// padTo right-pads s with spaces to w cells (s is assumed already truncated).
func padTo(s string, w int) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// window returns at most h lines of lines containing cursor, scrolling from top only as far
// as needed. h <= 0 means unconstrained. It returns the new top for the next call.
func window(lines []string, cursor, h, top int) ([]string, int) {
	if h <= 0 || len(lines) <= h {
		return lines, 0
	}
	top = min(max(top, cursor-h+1), len(lines)-h)
	top = max(0, min(top, cursor))
	return lines[top : top+h], top
}

func isStacked(w int) bool { return w < wideBreakpoint }

// paneWidths is the list and detail content widths for a body w cells wide.
func paneWidths(w int) (list, detail int) {
	if isStacked(w) {
		return w, w
	}
	list = max(w*2/5, 30)
	return list, w - list - 3 // " │ " between the panes
}

// panes lays list and detail out for a body of w x h cells (h <= 0: as tall as needed).
// Wide bodies show both side by side; narrow ones show one pane, the detail once drilled in.
func panes(th Theme, w, h int, list, detail []string, drilled bool) []string {
	if w <= 0 {
		w = defaultWidth
	}
	lw, dw := paneWidths(w)
	if isStacked(w) {
		one := list
		if drilled {
			one = detail
		}
		if h > 0 && len(one) > h {
			one = one[:h]
		}
		out := make([]string, len(one))
		for i, l := range one {
			out[i] = trunc(l, lw)
		}
		return out
	}
	rows := max(len(list), len(detail))
	if h > 0 {
		rows = min(rows, h)
	}
	div := th.Divider.Render("│")
	out := make([]string, rows)
	for i := range out {
		var l, d string
		if i < len(list) {
			l = list[i]
		}
		if i < len(detail) {
			d = detail[i]
		}
		out[i] = padTo(trunc(l, lw), lw) + " " + div + " " + trunc(d, dw)
	}
	return out
}
