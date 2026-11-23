package tui

import (
	"sort"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// barSegment is one share of a stacked bar: a count, the color role it is drawn in and the glyph.
// The glyphs differ per share so the bar reads without color.
type barSegment struct {
	n     int
	kind  Kind
	glyph string
}

// taskBarMax is the widest the Tasks bar is drawn: a glance, not a banner.
const taskBarMax = 40

// taskBarSegments are the task states as bar shares, in the order the counts line lists them.
func taskBarSegments(c core.TaskCounts) []barSegment {
	return []barSegment{
		{c.Open, KindAccent, "▒"}, {c.InProgress, KindOK, "█"}, {c.Blocked, KindFail, "▓"},
		{c.Deferred, KindWarn, "░"}, {c.Closed, KindMuted, "·"},
	}
}

// stackBar draws segs as one bar exactly w cells wide, each share in proportion to its count. A
// share with a count keeps at least one cell; with fewer cells than shares the biggest keep theirs.
// No shares or no width draws nothing.
func (t Theme) stackBar(w int, segs []barSegment) string {
	var live []barSegment
	total := 0
	for _, s := range segs {
		if s.n > 0 {
			live = append(live, s)
			total += s.n
		}
	}
	if w <= 0 || total == 0 {
		return ""
	}
	cells := make([]int, len(live))
	if w < len(live) {
		order := make([]int, len(live))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return live[order[a]].n > live[order[b]].n })
		for _, i := range order[:w] {
			cells[i] = 1
		}
	} else {
		extra := w - len(live) // everyone has one cell; the rest go by share, largest remainder first
		given := 0
		rem := make([]int, len(live))
		for i, s := range live {
			cells[i] = 1 + extra*s.n/total
			rem[i] = extra * s.n % total
			given += cells[i]
		}
		for ; given < w; given++ {
			best := 0
			for i := range rem {
				if rem[i] > rem[best] {
					best = i
				}
			}
			cells[best]++
			rem[best] = -1
		}
	}
	var b strings.Builder
	for i, s := range live {
		if cells[i] > 0 {
			b.WriteString(t.Text(s.kind, strings.Repeat(s.glyph, cells[i])))
		}
	}
	return b.String()
}
