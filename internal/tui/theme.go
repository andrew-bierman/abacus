package tui

import (
	"hash/fnv"

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
	// colSelBG is the cursor row's fill: bright enough to find at a glance (the 16-color dark entry is
	// blue, because black is the terminal's own background there).
	colSelBG = pal("#90c4f0", "153", "7", "#264f78", "24", "4")
)

// colTint are the colors people and machines are tinted with. None is a semantic color (red, amber,
// green, blue) or close to one, so a name is never mistaken for a state. The first two are distinct
// on a 16-color terminal, which is all that profile gets: it has no other neutral hue.
var colTint = []lipgloss.CompleteAdaptiveColor{
	pal("#8250df", "98", "5", "#d2a8ff", "183", "13"),  // purple
	pal("#1b7c83", "30", "6", "#76e3ea", "117", "14"),  // teal
	pal("#bf3989", "169", "5", "#ff9bce", "211", "13"), // magenta
	pal("#5b4fcf", "62", "5", "#aab4ff", "147", "13"),  // lilac
}

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
	basic bool // 16-color terminal: bright black can equal the background, so mute with faint
	r     *lipgloss.Renderer

	Selected, Header, Title, Label      lipgloss.Style
	TabActive, TabIdle, TabDim, Divider lipgloss.Style
	// Border frames a panel; BorderFocus frames the focused one.
	Border, BorderFocus lipgloss.Style
}

// NewTheme builds the theme for r. The termenv Ascii profile (NO_COLOR, a dumb terminal or a
// pipe) strips every attribute, so structure never depends on color alone.
func NewTheme(r *lipgloss.Renderer) Theme {
	t := Theme{r: r, plain: r.ColorProfile() == termenv.Ascii, basic: r.ColorProfile() == termenv.ANSI}
	s := r.NewStyle
	t.Title = s().Bold(true).Foreground(colAccent)
	t.Header = s().Bold(true)
	t.Label = t.muted()
	t.Selected = s().Bold(true).Background(colSelBG)
	t.TabActive = s().Bold(true).Foreground(colOnBadge).Background(colAccent).Padding(0, 1)
	t.TabIdle = s().Padding(0, 1)
	t.TabDim = t.muted().Faint(true).Padding(0, 1)
	t.Divider = t.muted()
	t.Border = t.muted()
	t.BorderFocus = s().Foreground(colAccent)
	return t
}

// muted is the quiet text style: the muted color, or on a 16-color terminal faint text in the
// default foreground (ANSI 8 is often indistinguishable from the background there).
func (t Theme) muted() lipgloss.Style {
	if t.basic {
		return t.r.NewStyle().Faint(true)
	}
	return t.r.NewStyle().Foreground(colMuted)
}

// DefaultTheme follows the process's terminal (and NO_COLOR) via lipgloss's default renderer.
func DefaultTheme() Theme { return NewTheme(lipgloss.DefaultRenderer()) }

// Text colors s with the role's foreground.
func (t Theme) Text(k Kind, s string) string {
	if k == KindMuted || k == KindSkip {
		return t.muted().Render(s)
	}
	return t.r.NewStyle().Foreground(colorFor(k)).Render(s)
}

// Tint colors s by key, so one name (an agent, a channel, a machine) has one color on every tab.
// Without color it is s unchanged.
func (t Theme) Tint(key, s string) string {
	if t.plain {
		return s
	}
	n := len(colTint)
	if t.basic {
		n = 2
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	return t.r.NewStyle().Foreground(colTint[h.Sum32()%uint32(n)]).Render(s)
}

// Section is a heading inside a panel: bold and accent colored.
func (t Theme) Section(s string) string {
	return t.r.NewStyle().Bold(true).Foreground(colAccent).Render(s)
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
	desc := t.muted()
	sep := t.muted().Faint(true)
	return help.Styles{
		Ellipsis: sep, ShortKey: key, ShortDesc: desc, ShortSeparator: sep,
		FullKey: key, FullDesc: desc, FullSeparator: sep,
	}
}

// Key styles a key name in the help overlay.
func (t Theme) Key(s string) string { return t.r.NewStyle().Foreground(colAccent).Render(s) }

// ActiveTab renders the selected tab; without color it is bracketed so the selection is
// still visible.
func (t Theme) ActiveTab(name string) string {
	if t.plain {
		return t.TabActive.Render("[" + name + "]")
	}
	return t.TabActive.Render(name)
}
