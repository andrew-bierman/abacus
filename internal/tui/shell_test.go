package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

func fake(id string, st core.State) *modtest.Fake {
	return &modtest.Fake{IDStr: id, Det: core.Detection{State: st, Detail: id + " detail"}}
}

func entriesFor(mods ...*modtest.Fake) []core.Entry {
	var out []core.Entry
	for _, f := range mods {
		out = append(out, core.Entry{Module: f, Detection: f.Det})
	}
	return out
}

func asciiTheme() Theme { return NewTheme(rendererFor(termenv.Ascii, true)) }

func sized(m tea.Model, w, h int) tea.Model {
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func press(m tea.Model, k string) tea.Model {
	var msg tea.KeyMsg
	switch k {
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyMsg{Type: tea.KeyShiftTab}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+k":
		msg = tea.KeyMsg{Type: tea.KeyCtrlK}
	case "ctrl+p":
		msg = tea.KeyMsg{Type: tea.KeyCtrlP}
	case "ctrl+n":
		msg = tea.KeyMsg{Type: tea.KeyCtrlN}
	case "ctrl+u":
		msg = tea.KeyMsg{Type: tea.KeyCtrlU}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	m, _ = m.Update(msg)
	return m
}

func lines(m tea.Model) []string { return strings.Split(m.View(), "\n") }

func TestHelpOverlayTogglesAndFooterIsGeneratedFromBindings(t *testing.T) {
	var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
	m = sized(m, 100, 20)
	foot := lines(m)[len(lines(m))-1]
	for _, want := range []string{"? help", "q quit", "tab"} {
		if !strings.Contains(foot, want) {
			t.Errorf("footer %q missing %q", foot, want)
		}
	}
	m = press(m, "?")
	for _, want := range []string{"Keys", "next tab", "jump", "quit"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("help overlay missing %q:\n%s", want, m.View())
		}
	}
	m = press(m, "esc")
	if strings.Contains(m.View(), "jump") {
		t.Fatal("esc must close the overlay")
	}
	m = press(m, "?")
	m = press(m, "?")
	if strings.Contains(m.View(), "jump") {
		t.Fatal("? must toggle the overlay")
	}
}

func TestFilterModeCapturesGlobalKeys(t *testing.T) {
	demo := lambe.NewDemo()
	var m tea.Model = New([]core.Entry{{Module: demo, Detection: demo.Detect(context.Background())}}, WithTheme(asciiTheme()))
	m = sized(m, 100, 20)
	v, _ := demo.Read(context.Background())
	m, _ = m.Update(lambeViewMsg{view: v})
	m = press(m, "4") // Queues
	m = press(m, "/")
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		t.Fatal("q while typing a filter must not quit")
	}
	m = press(m, "2")
	if !strings.Contains(lines(m)[0], "[Queues]") || !strings.Contains(m.View(), "/q2") {
		t.Fatalf("digits must go to the filter, not switch tabs:\n%s", m.View())
	}
	m = press(m, "esc")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q quits again once the filter is closed")
	}
}

func TestDoctorTabLoadsOnFirstSelectionFromTheTypedReport(t *testing.T) {
	a := fake("a", core.Ready)
	a.Checks = []core.Check{{Name: "bin", Status: core.StatusOK, Detail: "found"}, {Name: "hub", Status: core.StatusFail, Detail: "down", Fix: "restart it"}}
	var m tea.Model = New(entriesFor(a, fake("b", core.Broken)), WithTheme(asciiTheme()))
	m = sized(m, 100, 60)
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")}) // Doctor
	m = run(t, m, cmd)
	v := m.View()
	for _, want := range []string{"Fake a", "(ok)", "bin", "(fail)", "hub", "restart it", "(broken)", "Fake b"} {
		if !strings.Contains(v, want) {
			t.Errorf("doctor view missing %q:\n%s", want, v)
		}
	}
}

func TestDegenerateSizesNeverPanicAndKeepHeaderAndFooter(t *testing.T) {
	demo := lambe.NewDemo()
	en := []core.Entry{{Module: demo, Detection: demo.Detect(context.Background())}, {Module: fake("g", core.Ready), Detection: core.Detection{State: core.Ready}}}
	for _, size := range [][2]int{{0, 0}, {1, 1}, {3, 2}, {10, 3}, {40, 10}, {40, 30}, {99, 12}, {100, 12}, {200, 60}} {
		var m tea.Model = New(en, WithTheme(asciiTheme()))
		m = sized(m, size[0], size[1])
		v, _ := demo.Read(context.Background())
		m, _ = m.Update(lambeViewMsg{view: v})
		for _, k := range []string{"", "?", "esc", "tab", "enter"} {
			if k != "" {
				m = press(m, k)
			}
			out := lines(m)
			if size[1] > 1 && len(out) > size[1] {
				t.Fatalf("%v after %q: %d lines", size, k, len(out))
			}
			for _, l := range out {
				if size[0] > 0 && lipgloss.Width(l) > size[0] {
					t.Fatalf("%v after %q: %q is %d wide", size, k, l, lipgloss.Width(l))
				}
			}
		}
	}
}

func TestActiveTabIsMarkedEvenWithoutColor(t *testing.T) {
	var m tea.Model = New(nil, WithTheme(asciiTheme()))
	m = sized(m, 100, 20)
	if h := lines(m)[0]; !strings.Contains(h, "[Board]") || strings.Contains(h, "[Tasks]") {
		t.Fatalf("header %q", h)
	}
	m = press(m, "tab")
	if h := lines(m)[0]; !strings.Contains(h, "[Tasks]") {
		t.Fatalf("header %q", h)
	}
}

func TestTooSmallBodyReplacesOnlyTheBody(t *testing.T) {
	for _, tc := range []struct {
		w, h  int
		small bool
	}{{40, 12, false}, {100, 12, false}, {39, 30, true}, {40, 11, true}, {100, 11, true}, {60, 5, true}} {
		var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
		m = sized(m, tc.w, tc.h)
		out := lines(m)
		got := strings.Contains(m.View(), "Terminal too small")
		if got != tc.small {
			t.Fatalf("%dx%d: too-small message shown = %v, want %v:\n%s", tc.w, tc.h, got, tc.small, m.View())
		}
		if !tc.small {
			continue
		}
		if tc.w >= 40 && tc.h >= 3 {
			if !strings.Contains(out[0], "Board") || !strings.Contains(out[len(out)-1], "? help") {
				t.Errorf("%dx%d: chrome must stay around the message:\n%s", tc.w, tc.h, m.View())
			}
		}
		if tc.w >= 40 && tc.h >= 6 && !strings.Contains(m.View(), "40x12") {
			t.Errorf("%dx%d: the message must say what is needed:\n%s", tc.w, tc.h, m.View())
		}
	}
}

func TestTooSmallNamesNoTool(t *testing.T) {
	var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
	m = sized(m, 39, 30)
	for _, bad := range toolNames {
		if strings.Contains(m.View(), bad) {
			t.Errorf("too-small screen names %q:\n%s", bad, m.View())
		}
	}
}

func TestHelpOverlayIsAFocusedPanelWhenItFits(t *testing.T) {
	var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
	m = sized(m, 100, 30)
	m = press(m, "?")
	out := lines(m)
	if !strings.Contains(out[2], "╭─ ▸ Keys") {
		t.Fatalf("help must be a framed, focused panel under the header:\n%s", m.View())
	}
	cell := func(l, sub string) int { return lipgloss.Width(l[:strings.Index(l, sub)]) }
	var key, desc int
	for _, l := range out {
		if strings.HasPrefix(l, "│") && strings.Contains(l, "next tab") {
			key, desc = cell(l, "tab/→"), cell(l, "next tab")
		}
	}
	if key == 0 || desc-key != 14 {
		t.Fatalf("keys and descriptions must stay in aligned columns (key at %d, desc at %d):\n%s", key, desc, m.View())
	}
}

func TestHelpOverlayDropsItsFrameRatherThanLoseRowsWhenShort(t *testing.T) {
	var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
	m = sized(m, 100, 12) // body of 10 rows: the help text needs 9, a frame would need 11
	m = press(m, "?")
	if strings.Contains(m.View(), "╭") || !strings.Contains(m.View(), "quit") {
		t.Fatalf("a short help overlay must stay a plain list:\n%s", m.View())
	}
}

func TestTooSmallBodyWinsOverTheHelpOverlay(t *testing.T) {
	for _, size := range [][2]int{{39, 30}, {100, 11}} {
		var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
		m = sized(m, size[0], size[1])
		m = press(m, "?")
		if !strings.Contains(m.View(), "Terminal too small") || strings.Contains(m.View(), "Keys") {
			t.Fatalf("%v: the too-small message must stay in charge:\n%s", size, m.View())
		}
	}
}

// The overlay is framed exactly when the whole text fits inside a frame, and otherwise a plain list
// that keeps every key row. Scan the heights so the boundary itself is pinned, not just two points.
func TestHelpOverlayFrameBoundaryIsExact(t *testing.T) {
	build := func(h int) string {
		var m tea.Model = New(entriesFor(fake("a", core.Ready)), WithTheme(asciiTheme()))
		m = sized(m, 100, h)
		return press(m, "?").View()
	}
	framedFrom := -1
	for h := hMin; h <= 40; h++ {
		v := build(h)
		framed := strings.Contains(v, "╭")
		if framed && framedFrom < 0 {
			framedFrom = h
		}
		if framedFrom >= 0 && !framed {
			t.Fatalf("height %d lost the frame it had at %d:\n%s", h, framedFrom, v)
		}
		if framed && (!strings.Contains(v, "? or esc closes") || !strings.Contains(v, "╰")) {
			t.Fatalf("height %d: a framed overlay must be whole:\n%s", h, v)
		}
		if !framed && !strings.Contains(v, "jump to tab") {
			t.Fatalf("height %d: a plain overlay must keep its key rows:\n%s", h, v)
		}
	}
	if framedFrom < 0 {
		t.Fatal("the overlay is never framed")
	}
	// One row short of the frame, the plain list has already lost its hint row: that is why the
	// frame waits for the full text instead of clipping it.
	if framedFrom > hMin && strings.Contains(build(framedFrom-1), "? or esc closes") {
		t.Fatalf("the plain list at %d should not fit its hint, so the frame could have started earlier", framedFrom-1)
	}
}

// A demo session changes nothing, so it records nothing: its audit never reaches the real state dir.
func TestDemoSessionDoesNotWriteTheRealAuditLog(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	a := auditFor([]core.Entry{{Module: demo.NewTasks(), Detection: core.Detection{State: core.Ready}}})
	if err := a.Record(core.AuditEntry{}); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(state, "*", "*")); len(files) != 0 {
		t.Fatalf("a demo audit wrote %v", files)
	}
	// A real session still audits to the state dir.
	real := auditFor([]core.Entry{{Module: fake("a", core.Ready), Detection: core.Detection{State: core.Ready}}})
	if _, discards := real.(discardAudit); discards {
		t.Fatal("a real session must keep its audit")
	}
}
