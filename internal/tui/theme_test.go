package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func rendererFor(p termenv.Profile, dark bool) *lipgloss.Renderer {
	r := lipgloss.NewRenderer(&bytes.Buffer{})
	r.SetColorProfile(p)
	r.SetHasDarkBackground(dark)
	return r
}

func allKinds() []Kind { return []Kind{KindOK, KindWarn, KindFail, KindSkip, KindMuted, KindAccent} }

func TestNoColorProfileEmitsNoEscapes(t *testing.T) {
	th := NewTheme(rendererFor(termenv.Ascii, true))
	for _, k := range allKinds() {
		for _, s := range []string{th.Text(k, "x"), th.Badge(k, "x")} {
			if strings.Contains(s, "\x1b") {
				t.Errorf("kind %d leaked an escape under Ascii: %q", k, s)
			}
		}
	}
	for _, s := range []string{th.Selected.Render("x"), th.Header.Render("x"), th.TabActive.Render("x"), th.TabDim.Render("x"), th.Border.Render("x"), th.BorderFocus.Render("x")} {
		if strings.Contains(s, "\x1b") {
			t.Errorf("escape under Ascii: %q", s)
		}
	}
}

func TestNoColorBadgesStayLegibleWithoutColor(t *testing.T) {
	th := NewTheme(rendererFor(termenv.Ascii, true))
	if got := th.Badge(KindFail, "fail"); got != "(fail)" {
		t.Fatalf("plain badge = %q, want (fail)", got)
	}
}

func TestEnvNoColorWinsOverForcedColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("TERM", "xterm-256color")
	th := NewTheme(lipgloss.NewRenderer(&bytes.Buffer{}))
	if s := th.Badge(KindOK, "ok"); strings.Contains(s, "\x1b") {
		t.Fatalf("NO_COLOR ignored: %q", s)
	}
}

func TestLightAndDarkBackgroundsPickDifferentColors(t *testing.T) {
	dark := NewTheme(rendererFor(termenv.TrueColor, true)).Text(KindOK, "x")
	light := NewTheme(rendererFor(termenv.TrueColor, false)).Text(KindOK, "x")
	if dark == light || !strings.Contains(dark, "\x1b[38;2;") {
		t.Fatalf("adaptive colors not applied:\ndark=%q\nlight=%q", dark, light)
	}
}

func TestSixteenColorProfileUsesOnlyBasicEscapes(t *testing.T) {
	th := NewTheme(rendererFor(termenv.ANSI, true))
	for _, k := range allKinds() {
		s := th.Badge(k, "x") + th.Text(k, "x")
		if strings.Contains(s, "38;2;") || strings.Contains(s, "38;5;") || strings.Contains(s, "48;5;") || strings.Contains(s, "48;2;") {
			t.Errorf("kind %d used extended colors on a 16-color terminal: %q", k, s)
		}
	}
}

func TestSemanticKindsRenderDistinctly(t *testing.T) {
	th := NewTheme(rendererFor(termenv.TrueColor, true))
	seen := map[string]Kind{}
	for _, k := range allKinds() {
		if k == KindMuted { // text-only role, shares skip's color
			continue
		}
		s := th.Badge(k, "x")
		if prev, dup := seen[s]; dup {
			t.Errorf("kinds %d and %d render identically: %q", prev, k, s)
		}
		seen[s] = k
	}
}

// Colors live in theme.go only, so a palette change is one file.
func TestNoColorLiteralsOutsideTheme(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if f == "theme.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"lipgloss.Color(", "AdaptiveColor", `"#`, ".Foreground(", "Style().Background(", ".Reverse("} {
			if strings.Contains(string(src), bad) {
				t.Errorf("%s contains %q; define it in theme.go", f, bad)
			}
		}
	}
}

// ANSI 8 (bright black) can equal the background on 16-color terminals, so muted text there
// must be faint default-foreground text, not color 8.
func TestSixteenColorMutedTextDoesNotUseBrightBlack(t *testing.T) {
	th := NewTheme(rendererFor(termenv.ANSI, true))
	for name, s := range map[string]string{
		"muted": th.Text(KindMuted, "x"), "skip": th.Text(KindSkip, "x"),
		"label": th.Label.Render("x"), "dim tab": th.TabDim.Render("x"), "divider": th.Divider.Render("x"),
		"border": th.Border.Render("x"),
	} {
		if !strings.Contains(s, "\x1b[2m") || strings.Contains(s, "90") || strings.Contains(s, "30;1") {
			t.Errorf("%s = %q, want faint text with no bright-black color", name, s)
		}
	}
}

// Focus is carried by color on a color terminal (and by the title glyph without one, see
// panel_test.go), so the two border styles must differ wherever color exists.
func TestFocusedBorderDiffersFromTheQuietBorder(t *testing.T) {
	for name, p := range map[string]termenv.Profile{"truecolor": termenv.TrueColor, "256": termenv.ANSI256, "16": termenv.ANSI} {
		th := NewTheme(rendererFor(p, true))
		if th.Border.Render("│") == th.BorderFocus.Render("│") {
			t.Errorf("%s: focused and quiet borders render identically", name)
		}
	}
}

func TestBorderStylesUseOnlyBasicEscapesOnSixteenColors(t *testing.T) {
	th := NewTheme(rendererFor(termenv.ANSI, true))
	for name, s := range map[string]string{"border": th.Border.Render("x"), "focus": th.BorderFocus.Render("x")} {
		if strings.Contains(s, "38;2;") || strings.Contains(s, "38;5;") {
			t.Errorf("%s used extended colors on a 16-color terminal: %q", name, s)
		}
	}
}
