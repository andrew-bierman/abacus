package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// panel draws body inside a rounded frame with the title set into the top edge, w cells wide and
// at most h tall (h <= 0: as tall as the body). Body lines are cut to the inner width and
// clipped to the inner height; the caller owns any "more" marker. The focused panel is marked
// with a title glyph as well as color, so focus survives NO_COLOR. A panel too small to hold a
// frame (under 4 wide or 3 tall) is drawn as plain lines instead of overflowing.
func panel(th Theme, title string, body []string, w, h int, focused bool) []string {
	if w <= 0 {
		return nil
	}
	if w < 4 || (h > 0 && h < 3) {
		lines := body
		if title != "" {
			lines = append([]string{title}, body...)
		}
		if h > 0 && len(lines) > h {
			lines = lines[:h]
		}
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = trunc(l, w)
		}
		return out
	}
	border, titleStyle := th.Border, th.Header
	if focused {
		border, titleStyle = th.BorderFocus, th.Title
		if title != "" {
			title = "▸ " + title
		}
	}
	inner := w - 4
	if h > 0 && len(body) > h-2 {
		body = body[:h-2]
	}

	span := w - 2 // between the corners
	top := strings.Repeat("─", span)
	if title != "" && span >= 5 {
		t := trunc(title, span-4)            // "─ " + title + " " + at least one "─"
		fill := span - 3 - lipgloss.Width(t) // "─", two spaces and the title are already placed
		top = border.Render("─") + " " + titleStyle.Render(t) + " " + border.Render(strings.Repeat("─", max(fill, 0)))
	} else {
		top = border.Render(top)
	}
	out := make([]string, 0, len(body)+2)
	out = append(out, border.Render("╭")+top+border.Render("╮"))
	side := border.Render("│")
	for _, l := range body {
		out = append(out, side+" "+padTo(trunc(l, inner), inner)+" "+side)
	}
	return append(out, border.Render("╰"+strings.Repeat("─", span)+"╯"))
}
