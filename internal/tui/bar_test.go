package tui

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
)

func coreCountsForTest() core.TaskCounts {
	return core.TaskCounts{Total: 14, Open: 6, InProgress: 3, Blocked: 2, Deferred: 1, Closed: 2}
}

func TestStackBarIsExactlyTheWidthAndProportional(t *testing.T) {
	th := asciiTheme()
	segs := []barSegment{{n: 6, kind: KindAccent, glyph: "a"}, {n: 3, kind: KindOK, glyph: "b"}, {n: 2, kind: KindFail, glyph: "c"}, {n: 1, kind: KindWarn, glyph: "d"}, {n: 2, kind: KindMuted, glyph: "e"}}
	for w := 5; w <= 80; w++ {
		bar := th.stackBar(w, segs)
		if lipgloss.Width(bar) != w {
			t.Fatalf("w=%d: bar is %d cells", w, lipgloss.Width(bar))
		}
		for _, g := range "abcde" {
			if !strings.ContainsRune(bar, g) {
				t.Fatalf("w=%d: a nonzero segment must keep at least one cell (%c missing): %q", w, g, bar)
			}
		}
	}
	bar := th.stackBar(70, segs)
	if strings.Count(bar, "a") <= strings.Count(bar, "b") || strings.Count(bar, "b") <= strings.Count(bar, "d") {
		t.Fatalf("segments are in proportion to their counts: %q", bar)
	}
	if strings.Index(bar, "a") > strings.Index(bar, "b") || strings.Index(bar, "b") > strings.Index(bar, "e") {
		t.Fatalf("segments keep their order: %q", bar)
	}
}

func TestStackBarSkipsEmptySegmentsAndHandlesTinyAndEmptyInput(t *testing.T) {
	th := asciiTheme()
	if got := th.stackBar(20, nil); got != "" {
		t.Fatalf("no segments, no bar: %q", got)
	}
	if got := th.stackBar(20, []barSegment{{n: 0, glyph: "x"}}); got != "" {
		t.Fatalf("all zero, no bar: %q", got)
	}
	if got := th.stackBar(0, []barSegment{{n: 3, glyph: "x"}}); got != "" {
		t.Fatalf("no width, no bar: %q", got)
	}
	bar := th.stackBar(10, []barSegment{{n: 0, glyph: "z"}, {n: 5, glyph: "a"}, {n: 5, glyph: "b"}})
	if strings.Contains(bar, "z") || lipgloss.Width(bar) != 10 {
		t.Fatalf("a zero segment takes no cells: %q", bar)
	}
	// Fewer cells than segments: the largest keep their cells, the width still holds.
	if bar := th.stackBar(2, []barSegment{{n: 9, glyph: "a"}, {n: 1, glyph: "b"}, {n: 5, glyph: "c"}}); lipgloss.Width(bar) != 2 || !strings.Contains(bar, "a") || !strings.Contains(bar, "c") {
		t.Fatalf("a bar narrower than its segments keeps the biggest: %q", bar)
	}
}

func TestStackBarNeverExceedsItsWidthForRandomInput(t *testing.T) {
	th := asciiTheme()
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 3000; i++ {
		var segs []barSegment
		for j := 0; j < rng.Intn(7); j++ {
			segs = append(segs, barSegment{n: rng.Intn(50), kind: KindOK, glyph: "█"})
		}
		w := rng.Intn(120)
		bar := th.stackBar(w, segs)
		nonzero := 0
		for _, s := range segs {
			if s.n > 0 {
				nonzero++
			}
		}
		want := w
		if nonzero == 0 {
			want = 0
		}
		if lipgloss.Width(bar) != want {
			t.Fatalf("segs=%v w=%d: width %d, want %d", segs, w, lipgloss.Width(bar), want)
		}
	}
}

func TestStackBarSegmentsAreColoredByKindAndDistinctWithoutColor(t *testing.T) {
	th := NewTheme(rendererFor(termenv.TrueColor, true))
	bar := th.stackBar(20, []barSegment{{n: 1, kind: KindOK, glyph: "█"}, {n: 1, kind: KindFail, glyph: "█"}})
	if !strings.Contains(bar, th.Text(KindOK, "██████████")) || !strings.Contains(bar, th.Text(KindFail, "██████████")) {
		t.Fatalf("each segment takes its kind's color: %q", bar)
	}
	plain := asciiTheme().stackBar(20, taskBarSegments(coreCountsForTest()))
	for _, g := range []string{"▒", "█", "▓", "░", "·"} {
		if !strings.Contains(plain, g) {
			t.Errorf("without color the task states stay distinguishable by glyph, %q missing: %q", g, plain)
		}
	}
}

// The cell counts are the largest-remainder split: every share has one cell, the rest go by quota
// and the leftover cells to the biggest fractions. 6,3,2,1,2 over 14 cells is exactly 5,3,2,2,2.
func TestStackBarCellCountsAreTheLargestRemainderSplit(t *testing.T) {
	segs := []barSegment{{n: 6, glyph: "a"}, {n: 3, glyph: "b"}, {n: 2, glyph: "c"}, {n: 1, glyph: "d"}, {n: 2, glyph: "e"}}
	bar := asciiTheme().stackBar(14, segs)
	for g, want := range map[string]int{"a": 5, "b": 3, "c": 2, "d": 2, "e": 2} {
		if got := strings.Count(bar, g); got != want {
			t.Errorf("%s has %d cells, want %d: %q", g, got, want, bar)
		}
	}
	if bar != "aaaaabbbccddee" {
		t.Errorf("shares are drawn in order, side by side: %q", bar)
	}
}
