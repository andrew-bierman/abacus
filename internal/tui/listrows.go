package tui

// renderRows lays out rows in tb for a panel iw cells wide, with a two-cell selection marker. The
// selected row is drawn from its plain cells on the Selected style (per-cell color would be cut
// off by the style); the others use their styled cells.
func renderRows(th Theme, tb table, iw int, styled, plain [][]string, cursor int) []string {
	widths := tb.fitRows(max(iw-2, 1), styled)
	out := make([]string, len(styled))
	for i := range styled {
		if i == cursor {
			out[i] = th.Selected.Render(padTo(trunc("▸ "+tb.row(widths, plain[i]), iw), iw))
			continue
		}
		out[i] = "  " + tb.row(widths, styled[i])
	}
	return out
}
