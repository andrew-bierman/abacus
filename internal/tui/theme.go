package tui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Kind is a semantic color role; sections ask for a Kind, never for a color.
type Kind int

const (
	KindOK Kind = iota
	KindWarn
	KindFail
	KindSkip
	KindMuted
	KindAccent
)

// pal is one role's color per background and per terminal depth (truecolor, 256, 16), so a
// 16-color terminal gets real ANSI colors instead of a lossy downgrade.
func pal(lt, l256, l16, dk, d256, d16 string) lipgloss.CompleteAdaptiveColor {
	return lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: lt, ANSI256: l256, ANSI: l16},
		Dark:  lipgloss.CompleteColor{TrueColor: dk, ANSI256: d256, ANSI: d16},
	}
}

var (
	colOK     = pal("#1a7f37", "28", "2", "#56d364", "78", "10")
	colWarn   = pal("#9a6700", "136", "3", "#e3b341", "179", "11")
	colFail   = pal("#cf222e", "160", "1", "#ff7b72", "203", "9")
	colMuted  = pal("#57606a", "240", "8", "#8b949e", "245", "8")
	colAccent = pal("#0969da", "26", "4", "#58a6ff", "75", "12")
	// colOnBadge is the text color on a filled badge: light text on the darker light-mode
	// fills, dark text on the brighter dark-mode fills.
	colOnBadge = pal("#ffffff", "231", "15", "#0d1117", "233", "0")
	colSelBG   = pal("#ddf4ff", "153", "7", "#1f2a37", "236", "0")
)

func colorFor(k Kind) lipgloss.TerminalColor {
	switch k {
	case KindOK:
		return colOK
	case KindWarn:
		return colWarn
	case KindFail:
		return colFail
	case KindAccent:
		return colAccent
	}
	return colMuted // skip and muted share the quiet color
}

// Theme is the only place styles are defined. Build it from a renderer so the color
// profile (NO_COLOR, 16/256/truecolor) and light/dark background come from one source.
type Theme struct {
	plain bool // no color profile: badges fall back to parentheses
	r     *lipgloss.Renderer

	Selected, Header, Footer, Title, Label lipgloss.Style
	TabActive, TabIdle, TabDim, Divider    lipgloss.Style
}

// NewTheme builds the theme for r. The termenv Ascii profile (NO_COLOR, a dumb terminal or a
// pipe) strips every attribute, so structure never depends on color alone.
func NewTheme(r *lipgloss.Renderer) Theme {
	t := Theme{r: r, plain: r.ColorProfile() == termenv.Ascii}
	s := r.NewStyle
	t.Title = s().Bold(true).Foreground(colAccent)
	t.Header = s().Bold(true)
	t.Label = s().Foreground(colMuted)
	t.Footer = s().Foreground(colMuted)
	t.Selected = s().Bold(true).Background(colSelBG)
	t.TabActive = s().Bold(true).Foreground(colOnBadge).Background(colAccent).Padding(0, 1)
	t.TabIdle = s().Padding(0, 1)
	t.TabDim = s().Foreground(colMuted).Faint(true).Padding(0, 1)
	t.Divider = s().Foreground(colMuted)
	return t
}

// DefaultTheme follows the process's terminal (and NO_COLOR) via lipgloss's default renderer.
func DefaultTheme() Theme { return NewTheme(lipgloss.DefaultRenderer()) }

// Text colors s with the role's foreground.
func (t Theme) Text(k Kind, s string) string {
	return t.r.NewStyle().Foreground(colorFor(k)).Render(s)
}

// Badge is a filled pill; without color it is "(text)" so it stays distinguishable.
func (t Theme) Badge(k Kind, text string) string {
	if t.plain {
		return "(" + text + ")"
	}
	return t.r.NewStyle().Bold(true).Foreground(colOnBadge).Background(colorFor(k)).Padding(0, 1).Render(text)
}

// HelpStyles styles bubbles' help bar from the theme.
func (t Theme) HelpStyles() help.Styles {
	key := t.r.NewStyle().Foreground(colAccent)
	desc := t.r.NewStyle().Foreground(colMuted)
	sep := t.r.NewStyle().Foreground(colMuted).Faint(true)
	return help.Styles{
		Ellipsis: sep, ShortKey: key, ShortDesc: desc, ShortSeparator: sep,
		FullKey: key, FullDesc: desc, FullSeparator: sep,
	}
}

// Key styles a key name in the help overlay.
func (t Theme) Key(s string) string { return t.r.NewStyle().Foreground(colAccent).Render(s) }
