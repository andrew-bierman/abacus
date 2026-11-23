package tui

// duoGeom is the geometry of a list panel with an optional detail panel beside it. From bpWide
// columns the two sit side by side; below it only one is drawn, the list until the user drills in.
type duoGeom struct {
	wide   bool
	list   int // outer width of the list panel
	detail int // outer width of the detail panel (the whole width when stacked)
	inner  int // rows of content each panel holds; 0 means the body height is not known
}

// duoMinRows is the fewest content rows a panel keeps when there is height to spare.
const duoMinRows = 10

// The list panel takes duoListFifths/5 of a wide body, but never less than duoListMinW cells.
const (
	duoListFifths = 3
	duoListMinW   = 40
)

// duoLayout sizes the panels for a body w wide and h tall (h <= 0: as tall as the content).
func duoLayout(w, h int) duoGeom {
	g := duoGeom{wide: w >= bpWide, list: w, detail: w}
	if g.wide {
		g.list = max(w*duoListFifths/5, duoListMinW)
		g.detail = w - g.list - 1 // one blank column between the panels
	}
	if h > 0 {
		g.inner = max(h-panelChromeH, 1)
	}
	return g
}

func (g duoGeom) listInner() int { return max(g.list-panelChromeW, 1) }

// draw frames the list and, when there is room or the user drilled in, the detail. The list panel
// is the one focusable region; the detail is read-only. When the height is known both panels fill
// it, so a short list does not leave a ragged bottom edge.
func (g duoGeom) draw(th Theme, h int, listTitle string, list []string, detailTitle string, detail []string, drilled bool) []string {
	rows := max(len(list), len(detail))
	if !g.wide {
		rows = len(list)
		if drilled {
			rows = len(detail)
		}
	}
	if g.inner > 0 {
		// Never taller than the body, and no taller than the content plus a little air: a four-row
		// list in a 50-row terminal is a compact box, not a screen of empty frame.
		rows = min(g.inner, max(rows, duoMinRows))
	}
	fill := func(lines []string) []string {
		out := append([]string(nil), lines...)
		for len(out) < rows {
			out = append(out, "")
		}
		return out
	}
	height := rows + panelChromeH
	if h > 0 {
		height = min(height, h)
	}
	if !g.wide {
		if drilled {
			return panel(th, detailTitle, fill(detail), g.detail, height, true)
		}
		return panel(th, listTitle, fill(list), g.list, height, true)
	}
	return sideBySide(panel(th, listTitle, fill(list), g.list, height, true), panel(th, detailTitle, fill(detail), g.detail, height, false))
}

// sideBySide joins two panels row by row with one blank column between them. The panels are the
// same height; a shorter right one is padded with nothing rather than panicking.
func sideBySide(left, right []string) []string {
	out := make([]string, 0, len(left))
	for i := range left {
		r := ""
		if i < len(right) {
			r = right[i]
		}
		out = append(out, left[i]+" "+r)
	}
	return out
}
