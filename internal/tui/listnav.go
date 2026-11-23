package tui

import "github.com/charmbracelet/bubbles/key"

// listNav is the cursor, scroll and drill state shared by the sections that browse a list of rows
// (Leases, Queues, Board, Tasks). A section supplies ids, one stable id per selectable row in cursor
// order, on every call; listNav never holds the rows themselves, so a refresh can reorder or drop
// rows without the selection moving onto a different one.
type listNav struct {
	cursor, top int
	// selected is the identity of the chosen row, so a refresh that reorders or removes rows cannot
	// move the selection (and anything acted on it) onto a different row.
	selected string
	drilled  bool // stacked layout only: showing the detail panel
}

// up moves the cursor one row up and remembers the row it lands on.
func (n *listNav) up(ids []string) {
	n.cursor = max(0, n.cursor-1)
	n.remember(ids)
}

// down moves the cursor one row down, stopping on the last row.
func (n *listNav) down(ids []string) {
	n.cursor = min(max(0, len(ids)-1), n.cursor+1)
	n.remember(ids)
}

// remember records the row under the cursor as the selection.
func (n *listNav) remember(ids []string) {
	if n.cursor < len(ids) {
		n.selected = ids[n.cursor]
	}
}

// reanchor puts the cursor back on the selected row after the rows changed. When that row is gone
// the cursor falls back to the clamped index, and then the two policies differ: with follow the
// selection moves to the row now under the cursor (Board, Tasks); without it the selection stays
// pinned to the vanished row, so it is chosen again if it comes back (Leases, Queues). The
// selection is only set here when there was none yet.
func (n *listNav) reanchor(ids []string, follow bool) {
	for i, id := range ids {
		if n.selected != "" && id == n.selected {
			n.cursor = i
			return
		}
	}
	n.cursor = min(n.cursor, max(0, len(ids)-1))
	if follow || n.selected == "" {
		n.remember(ids)
	}
}

// enter shows the detail panel, only when the layout is stacked and there is a row to show.
func (n *listNav) enter(width, count int) {
	n.drilled = isStacked(width) && count > 0
}

// back leaves the detail panel and reports whether there was one to leave, so a section can peel
// a further layer (a filter) on the next press.
func (n *listNav) back() bool {
	was := n.drilled
	n.drilled = false
	return was
}

// fit drops the drill when the layout is no longer stacked: side by side, the detail is always shown.
func (n *listNav) fit(width int) { n.drilled = n.drilled && isStacked(width) }

// settle drops the drill when there are no rows left to show.
func (n *listNav) settle(count int) { n.drilled = n.drilled && count > 0 }

// bindings are the enter and back keys, enabled as the state allows: enter only while stacked,
// not yet drilled and with rows; back only while drilled.
func (n *listNav) bindings(width, count int) (enter, back key.Binding) {
	enter, back = keyEnter, keyBack
	enter.SetEnabled(isStacked(width) && !n.drilled && count > 0)
	back.SetEnabled(n.drilled)
	return enter, back
}
