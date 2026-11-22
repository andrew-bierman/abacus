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

func TestPanesSideBySideWhenWideStackedWhenNarrow(t *testing.T) {
	th := NewTheme(rendererFor(0, true))
	list, detail := []string{"LIST"}, []string{"DETAIL"}
	wide := panes(th, 120, 5, list, detail, false)
	if !strings.Contains(wide[0], "LIST") || !strings.Contains(wide[0], "DETAIL") {
		t.Fatalf("wide layout must show both panes on one row: %q", wide[0])
	}
	narrow := strings.Join(panes(th, 80, 5, list, detail, false), "\n")
	if !strings.Contains(narrow, "LIST") || strings.Contains(narrow, "DETAIL") {
		t.Fatalf("narrow layout shows only the list until drilled in:\n%s", narrow)
	}
	drilled := strings.Join(panes(th, 80, 5, list, detail, true), "\n")
	if strings.Contains(drilled, "LIST") || !strings.Contains(drilled, "DETAIL") {
		t.Fatalf("drilled-in layout shows only the detail:\n%s", drilled)
	}
}

func TestPanesNeverExceedWidthOrHeight(t *testing.T) {
	th := NewTheme(rendererFor(0, true))
	long := strings.Repeat("日本語x", 40)
	for _, w := range []int{0, 1, 5, 40, 99, 100, 120} {
		for _, h := range []int{0, 1, 3, 10} {
			for _, drilled := range []bool{false, true} {
				out := panes(th, w, h, []string{long, long, long}, []string{long, long}, drilled)
				if h > 0 && len(out) > h {
					t.Fatalf("w=%d h=%d: %d lines", w, h, len(out))
				}
				for _, l := range out {
					if lipgloss.Width(l) > max(w, 0) && w > 0 {
						t.Fatalf("w=%d h=%d: line %q is %d wide", w, h, l, lipgloss.Width(l))
					}
				}
			}
		}
	}
}
