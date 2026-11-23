package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The size grid straddles every breakpoint (one cell below, at, and above) plus degenerate sizes
// and sizes larger than any real terminal.
var (
	gridWidths  = []int{1, 2, 3, 5, 8, 13, 21, 34, 39, 40, 41, 59, 60, 61, 79, 80, 81, 99, 100, 101, 119, 120, 139, 140, 141, 200, 300}
	gridHeights = []int{1, 2, 3, 4, 5, 8, 11, 12, 13, 15, 16, 17, 23, 24, 25, 39, 40, 41, 60, 100}
)

// Whatever the size, every tab renders without panicking, no line is wider than the terminal,
// the screen is no taller than it, and the chrome rules in invariantErrs hold.
func TestEveryTabRendersWithinEverySize(t *testing.T) {
	th := asciiTheme()
	for _, sc := range scenarios() {
		for _, w := range gridWidths {
			for _, h := range gridHeights {
				for _, e := range invariantErrs(sc, render(t, sc, th, w, h), w, h) {
					t.Errorf("%s %dx%d: %s", sc.name, w, h, e)
				}
			}
		}
	}
}

func TestEveryTabRendersWithinEverySizeWithColor(t *testing.T) {
	th := NewTheme(rendererFor(termenv.TrueColor, true))
	for _, sc := range scenarios()[:4] {
		for _, w := range []int{1, 39, 40, 60, 100, 140, 300} {
			for _, h := range []int{1, 3, 11, 12, 24, 100} {
				out := render(t, sc, th, w, h)
				for i, l := range strings.Split(out, "\n") {
					if lipgloss.Width(l) > w {
						t.Fatalf("%s %dx%d line %d is %d wide: %q", sc.name, w, h, i, lipgloss.Width(l), l)
					}
				}
			}
		}
	}
}

// The thresholds are named in one place; these pin the values the layout rules (and the owner's
// breakpoint table) rely on, so moving one is a deliberate edit of this test too.
func TestBreakpointValues(t *testing.T) {
	for name, c := range map[string][2]int{
		"bpMin": {bpMin, 40}, "bpCompact": {bpCompact, 60}, "bpWide": {bpWide, 100}, "bpUltra": {bpUltra, 140},
		"hMin": {hMin, 12}, "hRule": {hRule, 16}, "hSummary": {hSummary, 24}, "hTall": {hTall, 40},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %d, want %d", name, c[0], c[1])
		}
	}
}

func TestTooSmallPredicate(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want bool
	}{
		{40, 12, false}, {39, 12, true}, {40, 11, true}, {100, 30, false}, {200, 11, true},
		{80, 0, false}, // height not known yet
		{1, 0, true},   // width alone can be too small
		{40, 100, false},
	} {
		if got := tooSmall(tc.w, tc.h); got != tc.want {
			t.Errorf("tooSmall(%d, %d) = %v, want %v", tc.w, tc.h, got, tc.want)
		}
	}
}
