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

// An agent keeps one color wherever its name appears: the tint depends on the name alone.
func TestTintIsStableDistinctAndDegradesWithTheProfile(t *testing.T) {
	th := NewTheme(rendererFor(termenv.TrueColor, true))
	if th.Tint("ada", "ada") != th.Tint("ada", "ada") {
		t.Fatal("the same name must always get the same color")
	}
	if th.Tint("ada", "x") == th.Tint("ada", "y") {
		t.Fatal("the tint must apply to the given text")
	}
	seen := map[string]bool{}
	for _, k := range []string{"ada", "bob", "codex", "claude-mac", "glm-scheduler-01", "ops", "dev/web", "work", "amy", "zed", "mia", "kai"} {
		seen[th.Tint(k, "x")] = true
	}
	if len(seen) < 3 {
		t.Fatalf("twelve names should spread over several colors, got %d", len(seen))
	}
	if s := NewTheme(rendererFor(termenv.Ascii, true)).Tint("ada", "ada"); s != "ada" {
		t.Fatalf("no color, no escapes: %q", s)
	}
	for k := range map[string]bool{"ada": true, "bob": true, "codex": true, "zed": true} {
		s := NewTheme(rendererFor(termenv.ANSI, true)).Tint(k, "x")
		if strings.Contains(s, "38;2;") || strings.Contains(s, "38;5;") {
			t.Errorf("%s used extended colors on a 16-color terminal: %q", k, s)
		}
	}
	if th.Tint("a", "x") == th.Text(KindFail, "x") {
		t.Fatal("a name tint must not borrow a semantic color: red means failed")
	}
}

func TestSectionHeadingIsBoldAccentAndPlainWithoutColor(t *testing.T) {
	if s := NewTheme(rendererFor(termenv.Ascii, true)).Section("Channels"); s != "Channels" {
		t.Fatalf("plain = %q", s)
	}
	s := NewTheme(rendererFor(termenv.TrueColor, true)).Section("Channels")
	if !strings.Contains(s, "\x1b[1;") && !strings.Contains(s, "\x1b[1m") || !strings.Contains(s, "38;2;") {
		t.Fatalf("a section heading is bold and colored: %q", s)
	}
}

// A name is never mistaken for a state: on every profile and background no tint renders like any
// semantic role, and no two tints collapse into one on a 16-color terminal.
func TestNoTintLooksLikeASemanticColorOnAnyProfile(t *testing.T) {
	var keys []string
	for i := 0; i < 300; i++ {
		keys = append(keys, "name-"+string(rune('a'+i%26))+string(rune('A'+i/26)))
	}
	for name, p := range map[string]termenv.Profile{"truecolor": termenv.TrueColor, "256": termenv.ANSI256, "16": termenv.ANSI} {
		for _, dark := range []bool{true, false} {
			th := NewTheme(rendererFor(p, dark))
			tints := map[string]bool{}
			for _, k := range keys {
				s := th.Tint(k, "x")
				tints[s] = true
				for _, kind := range allKinds() {
					if s == th.Text(kind, "x") {
						t.Fatalf("%s dark=%v: the tint for %q equals semantic kind %d: %q", name, dark, k, kind, s)
					}
				}
				if s == th.BorderFocus.Render("x") {
					t.Fatalf("%s dark=%v: a tint equals the focus color: %q", name, dark, s)
				}
			}
			if want := map[string]int{"truecolor": 4, "256": 4, "16": 2}[name]; len(tints) != want {
				t.Errorf("%s dark=%v: %d distinct tints, want %d", name, dark, len(tints), want)
			}
		}
	}
}

// On 16 colors the two usable tints split names about evenly (not three to one because three of
// the four would render alike).
func TestSixteenColorTintsAreBalanced(t *testing.T) {
	th := NewTheme(rendererFor(termenv.ANSI, true))
	count := map[string]int{}
	for i := 0; i < 400; i++ {
		count[th.Tint("agent-"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('A'+i/26)), "x")]++
	}
	if len(count) != 2 {
		t.Fatalf("16 colors have two tints, got %d", len(count))
	}
	for s, n := range count {
		if n < 140 || n > 260 {
			t.Errorf("tint %q covers %d of 400 names: the split is skewed", s, n)
		}
	}
}
