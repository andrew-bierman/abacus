package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The screens that are not a list (the task form, the verify and confirm screens, the guided setup
// prompt and the not-connected states) are each one framed panel with the title in the border, as
// tall as its content and never taller than the body. Their text is wrapped to the panel's inner
// width, never cut, so a long title or command is whole; only a body too short for it is clipped,
// and then a "+N more" row says so.

// innerW is the width of the text inside a panel w cells wide.
func innerW(w int) int { return max(w-panelChromeW, 1) }

// box draws body in a focused panel w wide and as tall as the body, but no taller than h (h <= 0:
// not known yet). A body that does not fit ends in a "+N more" row. The lines are expected to be
// wrapped to innerW(w) already.
func box(th Theme, title string, body []string, w, h int) []string {
	return boxWith(th, title, body, w, h, th.moreMarker)
}

// reviewBox is box for a screen whose text the person must read whole before a key applies it
// (the confirm and busy screens): its "+N more" row says to enlarge the terminal to review. Use
// fitsBox for the same body to know whether the key may apply.
func reviewBox(th Theme, title string, body []string, w, h int) []string {
	return boxWith(th, title, body, w, h, th.reviewMarker)
}

func boxWith(th Theme, title string, body []string, w, h int, marker func(int) string) []string {
	if h >= panelMinH {
		body = clipWrapped(th, body, h-panelChromeH, innerW(w), marker)
	}
	height := len(body) + panelChromeH
	if h > 0 {
		height = min(height, h)
	}
	return panel(th, title, body, w, height, true)
}

// fitsBox reports whether a panel at height h shows all of body (h <= 0: the height is not known
// yet, nothing is clipped). A body that does not fit is clipped, so it is not wholly on screen.
func fitsBox(body []string, h int) bool { return h <= 0 || len(body)+panelChromeH <= h }

// reviewMarker is the "+N more" row of a screen that cannot be acted on until it is shown whole.
func (t Theme) reviewMarker(hidden int) string {
	return t.Text(KindMuted, "↕ +"+strconv.Itoa(hidden)+" more: enlarge the terminal to review")
}

// clipWrapped shows at most rows lines (rows <= 0: all). When it cuts, the last rows are the
// marker (wrapped to inner, so a long marker is whole); with a single row left the marker is the
// short "↕+N" at the end of the first line, so even a one-row panel says something is hidden.
func clipWrapped(th Theme, lines []string, rows, inner int, marker func(int) string) []string {
	if rows <= 0 || len(lines) <= rows {
		return lines
	}
	if rows == 1 {
		m := th.Text(KindMuted, "↕+"+strconv.Itoa(len(lines)-1))
		return []string{trunc(lines[0], max(inner-lipgloss.Width(m)-1, 0)) + " " + m}
	}
	mk := wrap(marker(len(lines)), inner)
	keep := rows - len(mk)
	if keep < 1 {
		return append([]string(nil), mk[:rows]...)
	}
	mk = wrap(marker(len(lines)-keep), inner) // the count changed with keep: its width may too
	if keep = rows - len(mk); keep < 1 {
		return append([]string(nil), mk[:rows]...)
	}
	return append(append([]string(nil), lines[:keep]...), mk...)
}

// fieldLabelMin is the narrowest value column the form keeps beside its label column; narrower
// than that the label goes on a row of its own above the value.
const fieldLabelMin = 12

// fieldRows draws form fields as a table: a marker column (▸ on the focused field, so focus
// survives NO_COLOR), a label column, and the value column, with the caret at the end of the
// focused value. A long value wraps inside its column. In a narrow panel each label sits above its
// value instead.
func fieldRows(th Theme, fields []formField, focus, inner int) []string {
	lw := 0
	for _, f := range fields {
		lw = max(lw, lipgloss.Width(f.label))
	}
	const marker = 2
	aligned := inner-marker-lw-2 >= fieldLabelMin
	var out []string
	for i, f := range fields {
		mark, label := "  ", th.Label.Render(f.label)
		if i == focus {
			mark, label = "▸ ", th.Header.Render(f.label)
		}
		val := san(f.value)
		if i == focus {
			val += "▏"
		}
		if aligned {
			pad := strings.Repeat(" ", lw-lipgloss.Width(f.label)+2)
			vals := wrap(val, inner-marker-lw-2)
			out = append(out, mark+label+pad+vals[0])
			for _, v := range vals[1:] {
				out = append(out, strings.Repeat(" ", marker+lw+2)+v)
			}
			continue
		}
		out = append(out, mark+label)
		for _, v := range wrap(val, inner-marker) {
			out = append(out, strings.Repeat(" ", marker)+v)
		}
	}
	return out
}

// confirmBody is the body of a confirm screen: an optional note (the issue changed, or could not
// be checked), the plan lines, and the exact command wrapped in full. busy adds the applying row.
func confirmBody(th Theme, note string, plan []string, argv []string, busy bool, inner int) []string {
	var out []string
	if note != "" {
		out = append(out, wrap(th.Text(KindWarn, san(note)), inner)...)
		out = append(out, "")
	}
	for _, l := range plan {
		out = append(out, hang("  ", san(l), inner)...)
	}
	out = append(out, "", th.Text(KindMuted, "exact command:"))
	out = append(out, hang("  ", san(core.DisplayArgv(argv)), inner)...)
	if busy {
		out = append(out, "", th.Text(KindMuted, "applying…"))
	}
	return out
}

// confirmFits reports whether the confirm screen for this plan shows all of its text at w x h:
// the note, every plan line and the whole exact command. Only then may y apply it.
func confirmFits(th Theme, note string, plan, argv []string, w, h int) bool {
	return fitsBox(confirmBody(th, note, plan, argv, false, innerW(w)), h)
}

// notConnectedBody is the body of a not-connected panel: the state, why, and how to connect, which
// may name a tool and is kept in its own blank-line-delimited block.
func notConnectedBody(th Theme, reason, how string, inner int) []string {
	out := []string{th.Badge(KindSkip, "not connected")}
	if reason != "" {
		out = append(out, "")
		out = append(out, wrap(th.Text(KindWarn, san(reason)), inner)...)
	}
	if how != "" {
		out = append(out, "")
		out = append(out, wrap(th.Text(KindAccent, "how to connect: ")+san(how), inner)...)
	}
	return out
}

// notConnectedBox is the not-connected panel for a section with nothing to scroll. When the
// natural order (state, why, how) does not fit the height, how to connect comes first, so the one
// line the person needs is not what the "+N more" row hides.
func notConnectedBox(th Theme, title, reason, how string, w, h int) []string {
	inner := innerW(w)
	body := notConnectedBody(th, reason, how, inner)
	if !fitsBox(body, h) && how != "" {
		body = wrap(th.Text(KindAccent, "how to connect: ")+san(how), inner)
		body = append(body, "", th.Badge(KindSkip, "not connected"))
		if reason != "" {
			body = append(body, "")
			body = append(body, wrap(th.Text(KindWarn, san(reason)), inner)...)
		}
	}
	return box(th, title, body, w, h)
}

// unavailableBody is the body of a panel for a domain that could not be read.
func unavailableBody(th Theme, why string, inner int) []string {
	return wrap(th.Text(KindWarn, "state unavailable: "+san(why)), inner)
}
