package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// column describes one table column. prio orders importance: 0 is dropped last. A zero max
// means the column may grow without limit and takes the slack left after bounded ones.
type column struct {
	min, max int
	prio     int
	right    bool
}

// fitColumns returns a width per column for a table w cells wide with gap cells between kept
// columns; 0 means the column is dropped. Columns drop in reverse priority (ties: rightmost
// first) until the minimums fit; the last column left is squeezed rather than dropped. The
// slack then goes to bounded columns in priority order, and what is left to the unbounded ones.
// Kept widths plus gaps never exceed w.
func fitColumns(cols []column, w, gap int) []int {
	out := make([]int, len(cols))
	if w <= 0 || len(cols) == 0 {
		return out
	}
	gap = max(gap, 0)
	mins := make([]int, len(cols))
	order := make([]int, len(cols))
	for i, c := range cols {
		mins[i] = max(c.min, 1)
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return cols[order[a]].prio < cols[order[b]].prio })

	n := len(order)
	need := func() int {
		t := gap * (n - 1)
		for _, i := range order[:n] {
			t += mins[i]
		}
		return t
	}
	for n > 1 && need() > w {
		n--
	}
	if n == 1 {
		i := order[0]
		out[i] = min(mins[i], w)
		return out
	}
	extra := w - need()
	for _, i := range order[:n] {
		out[i] = mins[i]
	}
	for _, i := range order[:n] {
		if cols[i].max > 0 && extra > 0 {
			add := min(extra, max(cols[i].max, mins[i])-mins[i])
			out[i] += add
			extra -= add
		}
	}
	var open []int
	for _, i := range order[:n] {
		if cols[i].max <= 0 {
			open = append(open, i)
		}
	}
	for k, i := range open {
		share := extra / len(open)
		if k < extra%len(open) {
			share++
		}
		out[i] += share
	}
	return out
}

// formatRow lays cells out in the widths fitColumns chose. Each cell is cut to its column (cells
// measure by display width, so styled and wide text is safe) and padded, right-aligned where the
// column asks. Cells must already be sanitized.
func formatRow(cols []column, widths []int, cells []string, gap int) string {
	var parts []string
	for i, c := range cols {
		if i >= len(widths) || widths[i] <= 0 {
			continue
		}
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		cell = trunc(cell, widths[i])
		if c.right {
			cell = strings.Repeat(" ", max(widths[i]-lipgloss.Width(cell), 0)) + cell
		} else {
			cell = padTo(cell, widths[i])
		}
		parts = append(parts, cell)
	}
	return strings.Join(parts, strings.Repeat(" ", max(gap, 0)))
}
