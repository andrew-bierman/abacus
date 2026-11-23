package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestTruncUsesDisplayWidthNotBytes(t *testing.T) {
	for _, s := range []string{"日本語のタイトル", "héllo wörld", "emoji 🚀🚀🚀 rocket", "plain ascii text"} {
		for w := 0; w <= 12; w++ {
			got := trunc(s, w)
			if lipgloss.Width(got) > w {
				t.Errorf("trunc(%q,%d)=%q is %d cells wide", s, w, got, lipgloss.Width(got))
			}
		}
	}
	if got := trunc("abcdefghij", 5); got != "abcd…" {
		t.Fatalf("got %q, want an ellipsis in the last cell", got)
	}
	if got := trunc("abc", 5); got != "abc" {
		t.Fatalf("short text must pass through, got %q", got)
	}
}

func TestWindowKeepsCursorVisibleAndNeverExceedsHeight(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = string(rune('a' + i))
	}
	top := 0
	for cursor := 0; cursor < len(lines); cursor++ {
		var vis []string
		vis, top = window(lines, cursor, 5, top)
		if len(vis) > 5 || !strings.Contains(strings.Join(vis, ""), lines[cursor]) {
			t.Fatalf("cursor %d: window %v", cursor, vis)
		}
	}
	for cursor := len(lines) - 1; cursor >= 0; cursor-- {
		var vis []string
		vis, top = window(lines, cursor, 5, top)
		if !strings.Contains(strings.Join(vis, ""), lines[cursor]) {
			t.Fatalf("cursor %d scrolling up: window %v", cursor, vis)
		}
	}
	if vis, _ := window(lines, 3, 0, 0); len(vis) != len(lines) {
		t.Fatal("height 0 means unconstrained")
	}
}

func TestDuoSideBySideWhenWideStackedWhenNarrow(t *testing.T) {
	th := NewTheme(rendererFor(0, true))
	list, detail := []string{"LIST"}, []string{"DETAIL"}
	g := duoLayout(120, 7)
	wide := g.draw(th, 7, "L", list, "D", detail, false)
	if !strings.Contains(wide[1], "LIST") || !strings.Contains(wide[1], "DETAIL") {
		t.Fatalf("wide layout must show both panels on one row: %q", wide)
	}
	g = duoLayout(80, 7)
	narrow := strings.Join(g.draw(th, 7, "L", list, "D", detail, false), "\n")
	if !strings.Contains(narrow, "LIST") || strings.Contains(narrow, "DETAIL") {
		t.Fatalf("narrow layout shows only the list until drilled in:\n%s", narrow)
	}
	drilled := strings.Join(g.draw(th, 7, "L", list, "D", detail, true), "\n")
	if strings.Contains(drilled, "LIST") || !strings.Contains(drilled, "DETAIL") {
		t.Fatalf("drilled-in layout shows only the detail:\n%s", drilled)
	}
}

func TestDuoNeverExceedsWidthOrHeight(t *testing.T) {
	th := NewTheme(rendererFor(0, true))
	long := strings.Repeat("日本語x", 40)
	for _, w := range []int{1, 5, 40, 99, 100, 120, 200} {
		for _, h := range []int{0, 1, 3, 10, 50} {
			for _, drilled := range []bool{false, true} {
				g := duoLayout(w, h)
				out := g.draw(th, h, long, []string{long, long, long}, long, []string{long, long}, drilled)
				if h > 0 && len(out) > h {
					t.Fatalf("w=%d h=%d: %d lines", w, h, len(out))
				}
				for _, l := range out {
					if lipgloss.Width(l) > w {
						t.Fatalf("w=%d h=%d: line %q is %d wide", w, h, l, lipgloss.Width(l))
					}
				}
			}
		}
	}
}

// The two panels of a wide body are always the same height, never taller than the body, and no
// taller than their content plus a little air: a short list in a tall terminal is a compact box.
func TestDuoPanelsAreCompactAndEqualHeight(t *testing.T) {
	th := NewTheme(rendererFor(0, true))
	out := duoLayout(120, 40).draw(th, 40, "L", []string{"a"}, "D", []string{"b", "c", "d"}, false)
	if want := duoMinRows + panelChromeH; len(out) != want {
		t.Fatalf("short content gets %d rows, got %d\n%s", want, len(out), strings.Join(out, "\n"))
	}
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, "row")
	}
	out = duoLayout(120, 20).draw(th, 20, "L", many[:30], "D", []string{"b"}, false)
	if len(out) != 20 {
		t.Fatalf("long content fills the body, got %d rows", len(out))
	}
	for _, rows := range [][]string{duoLayout(120, 40).draw(th, 40, "L", []string{"a"}, "D", []string{"b"}, false), out} {
		if last := rows[len(rows)-1]; strings.Count(last, "╯") != 2 {
			t.Fatalf("the bottom row must close both panels: %q", last)
		}
	}
	if g := duoLayout(120, 0); g.inner != 0 {
		t.Fatalf("an unknown height must stay unknown, inner=%d", g.inner)
	}
}
