package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/andrew-bierman/abacus/internal/core"
)

// section is one tab's body. The shell sends it the body size (not the window size) as a
// tea.WindowSizeMsg, and renders View() inside the chrome.
type section interface {
	tea.Model
	// Capturing reports text input in progress; global keys (q, digits, ?) are then the
	// section's, not the shell's.
	Capturing() bool
	// Bindings are the section's keys, for the footer and the help overlay.
	Bindings() []key.Binding
	SetTheme(Theme)
}

// activator is implemented by sections that load lazily, on first selection.
type activator interface{ activate() tea.Cmd }

// geom is the body size a section was last given; zero means "not told yet".
type geom struct{ w, h int }

func (g *geom) resize(msg tea.WindowSizeMsg) { g.w, g.h = msg.Width, msg.Height }

func (g geom) width() int {
	if g.w <= 0 {
		return defaultWidth
	}
	return g.w
}

// wrap hard-wraps text to w cells so a long message is readable instead of cut off.
func wrap(s string, w int) []string {
	return strings.Split(ansi.Wrap(s, max(w, 1), ""), "\n")
}

// san is the one door for text that came from outside abacus: every dynamic field passes
// through core.Sanitize before it is styled or measured.
func san(s string) string { return core.Sanitize(s) }

// orDash shows an empty optional field as a dash rather than a blank.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return san(s)
}

// moreMarker is the "+N more" row shown whenever content is clipped.
func (t Theme) moreMarker(hidden int) string {
	return t.Text(KindMuted, "  ↕ +"+strconv.Itoa(hidden)+" more")
}

// linesSection is the shared body of the scrollable, cursor-less sections (generic, doctor).
type linesSection struct {
	th Theme
	geom
	scroll scroller
}

func (l *linesSection) SetTheme(t Theme) { l.th = t }
func (l *linesSection) Capturing() bool  { return false }

// scrollKey scrolls on up/down over total lines and reports whether k was a scroll key.
func (l *linesSection) scrollKey(k tea.KeyMsg, total int) bool {
	switch {
	case key.Matches(k, keyUp):
		l.scroll.move(-1, total, l.h)
	case key.Matches(k, keyDown):
		l.scroll.move(1, total, l.h)
	default:
		return false
	}
	return true
}

// render draws lines at the scroll offset, truncated to the width, with a +N more marker.
func (l *linesSection) render(lines []string) string {
	vis, hidden := l.scroll.view(lines, l.h)
	out := make([]string, 0, len(vis)+1)
	for _, ln := range vis {
		out = append(out, trunc(ln, l.width()))
	}
	if hidden > 0 && l.h >= 2 {
		out = append(out, l.th.moreMarker(hidden))
	}
	return strings.Join(out, "\n") + "\n"
}

// toneKind maps core's typed row tone to a color role.
func toneKind(t core.Tone) Kind {
	switch t {
	case core.ToneOK:
		return KindOK
	case core.ToneWarn:
		return KindWarn
	case core.ToneFail:
		return KindFail
	case core.ToneInfo:
		return KindAccent
	}
	return KindMuted
}
