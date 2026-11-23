package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPanelFrameBudgetAtEverySize(t *testing.T) {
	th := asciiTheme()
	body := []string{"one", "日本語日本語日本語", "é́ long long long long long long long"}
	for w := 0; w <= 120; w++ {
		for h := 0; h <= 12; h++ {
			out := panel(th, "Title", body, w, h, false)
			if w <= 0 && len(out) != 0 {
				t.Fatalf("w=%d must draw nothing, got %q", w, out)
			}
			if h > 0 && len(out) > h {
				t.Fatalf("%dx%d: %d lines", w, h, len(out))
			}
			for _, l := range out {
				if lipgloss.Width(l) > w {
					t.Fatalf("%dx%d: %q is %d wide", w, h, l, lipgloss.Width(l))
				}
			}
		}
	}
}

func TestPanelDrawsATitledRoundedFrameWhenItFits(t *testing.T) {
	out := panel(asciiTheme(), "Keys", []string{"a", "bb"}, 20, 6, false)
	want := []string{
		"╭─ Keys ───────────╮",
		"│ a                │",
		"│ bb               │",
		"╰──────────────────╯",
	}
	if strings.Join(out, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(out, "\n"), strings.Join(want, "\n"))
	}
}

func TestPanelClipsToItsHeightAndKeepsTheFrame(t *testing.T) {
	body := []string{"1", "2", "3", "4", "5"}
	out := panel(asciiTheme(), "T", body, 12, 4, false)
	if len(out) != 4 || !strings.HasPrefix(out[0], "╭") || !strings.HasPrefix(out[3], "╰") {
		t.Fatalf("clipped panel lost its frame:\n%s", strings.Join(out, "\n"))
	}
	if !strings.Contains(out[1], "1") || !strings.Contains(out[2], "2") || strings.Contains(strings.Join(out, ""), "3") {
		t.Fatalf("clip must keep the first lines:\n%s", strings.Join(out, "\n"))
	}
}

func TestPanelMarksFocusWithoutColor(t *testing.T) {
	plain := panel(asciiTheme(), "List", []string{"x"}, 20, 5, false)[0]
	focus := panel(asciiTheme(), "List", []string{"x"}, 20, 5, true)[0]
	if plain == focus || !strings.Contains(focus, "▸") || strings.Contains(plain, "▸") {
		t.Fatalf("focus must be visible with no color:\nplain %q\nfocus %q", plain, focus)
	}
}

func TestPanelTruncatesALongTitleInsteadOfOverflowing(t *testing.T) {
	out := panel(asciiTheme(), strings.Repeat("title ", 20), []string{"x"}, 16, 5, true)
	if lipgloss.Width(out[0]) > 16 || !strings.HasPrefix(out[0], "╭") || !strings.HasSuffix(out[0], "╮") {
		t.Fatalf("title row broken: %q", out[0])
	}
}

func TestPanelTooSmallForAFrameFallsBackToPlainLines(t *testing.T) {
	out := panel(asciiTheme(), "T", []string{"abcdef"}, 3, 5, false)
	for _, l := range out {
		if strings.ContainsAny(l, "╭╮╰╯│") || lipgloss.Width(l) > 3 {
			t.Fatalf("w=3 cannot hold a frame: %q", out)
		}
	}
	out = panel(asciiTheme(), "T", []string{"abc", "def"}, 20, 2, false)
	if len(out) > 2 {
		t.Fatalf("h=2 cannot hold a frame: %q", out)
	}
}
