package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Breakpoints. Every layout decision keys off one of these names, so a threshold is changed in
// one place and the tests that pin it say which one they mean.
//
//	width:  bpMin      the narrowest layout we draw; below it the body is a "too small" message
//	        bpCompact  the brand joins the tab bar; fuller rows
//	        bpWide     list and detail sit side by side
//	        bpUltra    extra columns and, when tall enough, a summary strip
//	height: hMin       the shortest body we draw; below it the body is a "too small" message
//	        hRule      a rule is drawn under the tab bar
//	        hSummary   room for a summary strip
//	        hTall      grow the list panel instead of leaving rows blank
const (
	bpMin     = 40
	bpCompact = 60
	bpWide    = 100
	bpUltra   = 140

	hMin     = 12
	hRule    = 16
	hSummary = 24
	hTall    = 40
)

// tooSmall reports whether a terminal w x h cannot hold the layout. A zero height means the
// size is not known yet, which is never too small.
func tooSmall(w, h int) bool { return w < bpMin || (h > 0 && h < hMin) }

// defaultWidth stands in until the first tea.WindowSizeMsg arrives.
const defaultWidth = 80

// joinLines is a section's View: the lines, one per row, ending in a newline.
func joinLines(lines []string) string { return strings.Join(lines, "\n") + "\n" }

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

func isStacked(w int) bool { return w < bpWide }

// windowMore is window plus room for a "+N more" marker: when lines do not fit it shows h-1
// lines around the cursor and reports how many lines are hidden (the caller draws the marker).
func windowMore(lines []string, cursor, h, top int) (vis []string, newTop, hidden int) {
	if h <= 0 || len(lines) <= h {
		return lines, 0, 0
	}
	vis, newTop = window(lines, cursor, max(h-1, 1), top)
	return vis, newTop, len(lines) - len(vis)
}

// scroller is a scroll offset for sections without a cursor.
type scroller struct{ off int }

// rows is how many content lines fit in h, leaving one row for the "+N more" marker when the
// content is clipped.
func rows(total, h int) int {
	if h <= 0 || total <= h {
		return total
	}
	return max(h-1, 1)
}

// move scrolls by delta and clamps against the content and visible height.
func (s *scroller) move(delta, total, h int) {
	s.off = max(0, min(s.off+delta, total-rows(total, h)))
}

// view returns the visible lines and how many are hidden (h <= 0: all).
func (s *scroller) view(lines []string, h int) ([]string, int) {
	n := rows(len(lines), h)
	s.off = max(0, min(s.off, len(lines)-n))
	return lines[s.off : s.off+n], len(lines) - n
}

// track is one slot in a split: a fixed length, a minimum that also shares leftover room, or
// a pure share of the leftover room.
type track struct {
	kind   trackKind
	n      int // length or minimum
	weight int // share of the leftover room (flexible tracks)
}

type trackKind int

const (
	trackLength trackKind = iota
	trackAtLeast
	trackFill
)

func length(n int) track  { return track{kind: trackLength, n: max(n, 0)} }
func atLeast(n int) track { return track{kind: trackAtLeast, n: max(n, 0), weight: 1} }
func fill(weight int) track {
	if weight <= 0 {
		weight = 1
	}
	return track{kind: trackFill, weight: weight}
}

// split divides total cells among tracks, like a ratatui layout. Lengths and minimums are served
// in order and clamped to what is left, then the rest is shared by weight among the flexible
// tracks, remainders going to the earliest. The sizes never sum to more than total and are never
// negative; with no flexible track the unclaimed room stays unassigned.
func split(total int, tracks ...track) []int {
	out := make([]int, len(tracks))
	rem := max(total, 0)
	weights := 0
	for i, t := range tracks {
		if t.kind != trackFill {
			out[i] = min(t.n, rem)
			rem -= out[i]
		}
		if t.kind != trackLength {
			weights += t.weight
		}
	}
	if weights == 0 || rem == 0 {
		return out
	}
	given := 0
	for i, t := range tracks {
		if t.kind != trackLength {
			share := rem * t.weight / weights
			out[i] += share
			given += share
		}
	}
	for i := 0; given < rem && i < len(tracks); i++ {
		if tracks[i].kind != trackLength {
			out[i]++
			given++
		}
	}
	return out
}

// windowLines is the visible part of lines (rows <= 0: all) around the cursor line, with the
// "+N more" marker in the last row when something is hidden. top is the scroll state.
func windowLines(th Theme, lines []string, cursor, rows int, top *int) []string {
	vis, newTop, hidden := windowMore(lines, cursor, rows, *top)
	*top = newTop
	if hidden > 0 && rows >= 2 {
		vis = append(append([]string(nil), vis...), th.moreMarker(hidden))
	}
	return vis
}

// clipLines shows at most rows lines (rows <= 0: all), replacing the last with a "+N more" marker
// when it cuts.
func clipLines(th Theme, lines []string, rows int) []string {
	if rows <= 0 || len(lines) <= rows {
		return lines
	}
	if rows == 1 {
		return lines[:1]
	}
	return append(append([]string(nil), lines[:rows-1]...), th.moreMarker(len(lines)-rows+1))
}
