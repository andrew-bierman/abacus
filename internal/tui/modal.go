package tui

import (
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
	if h >= panelMinH {
		body = clipLines(th, body, h-panelChromeH)
	}
	height := len(body) + panelChromeH
	if h > 0 {
		height = min(height, h)
	}
	return panel(th, title, body, w, height, true)
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

// unavailableBody is the body of a panel for a domain that could not be read.
func unavailableBody(th Theme, why string, inner int) []string {
	return wrap(th.Text(KindWarn, "state unavailable: "+san(why)), inner)
}
