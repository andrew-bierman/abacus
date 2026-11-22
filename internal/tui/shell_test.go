package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
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
