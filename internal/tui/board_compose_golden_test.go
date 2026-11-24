package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

func init() { core.SetBoardClockForTest(func() time.Time { return goldenNow }) } // a stable idempotency key

// boardScenarios are the goldens of the Board's writing screens and states, over the demo board and
// a fake writer (nothing here runs a CLI or touches a board).
func boardScenarios() []scenario {
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	// model is the shell over a Board that can write, with report loaded.
	model := func(th Theme, r core.BoardReport, writer bool) (tea.Model, *boardSection) {
		f := &boardWriteFake{boardProv: boardFake(), describe: []string{"existing channel dev"}}
		var m tea.Model
		if writer {
			m = New([]core.Entry{{Module: f, Detection: f.Det}}, WithTheme(th), WithFullView(noFullView), WithAudit(&memAudit{}))
		} else {
			b := boardFake()
			m = New([]core.Entry{{Module: b, Detection: b.Det}}, WithTheme(th), WithFullView(noFullView), WithAudit(&memAudit{}))
		}
		m, _ = m.Update(boardMsg{report: r})
		return m, m.(*Model).active().(*boardSection)
	}
	std := func(keys []string) func(*testing.T, Theme) tea.Model {
		return func(t *testing.T, th Theme) tea.Model {
			m, _ := model(th, fixtureBoard(), true)
			for _, k := range keys {
				m = press(m, k)
			}
			return m
		}
	}
	// confirmed is the confirm screen of the keys, with the board's answer injected.
	confirmed := func(keys []string, then ...string) func(*testing.T, Theme) tea.Model {
		return func(t *testing.T, th Theme) tea.Model {
			m, s := model(th, fixtureBoard(), true)
			for _, k := range keys {
				m = press(m, k)
			}
			m, _ = m.Update(boardDescribedMsg{seq: s.seq, lines: []string{"existing channel dev"}})
			for _, k := range then {
				m = press(m, k)
			}
			return m
		}
	}
	unknown := func(t *testing.T, th Theme) tea.Model {
		m, s := model(th, fixtureBoard(), true)
		for _, k := range cat([]string{"n"}, typed("dev"), []string{"enter"}, typed("rolled back to d4e5f6")) {
			m = press(m, k)
		}
		m = press(m, "ctrl+s")
		m, _ = m.Update(boardDescribedMsg{seq: s.seq, lines: []string{"existing channel dev"}})
		m = press(m, "y")
		m, _ = m.Update(boardAppliedMsg{seq: s.seq, err: core.UnknownOutcome("the board did not answer (the board did not answer in time); it may have been posted. Refresh the Board before trying again")})
		return m
	}
	refused := func(t *testing.T, th Theme) tea.Model {
		m, s := model(th, fixtureBoard(), true)
		for _, k := range cat([]string{"n"}, typed("dev"), []string{"enter"}, typed("hello")) {
			m = press(m, k)
		}
		m = press(m, "ctrl+s")
		m, _ = m.Update(boardDescribedMsg{seq: s.seq})
		m = press(m, "y")
		m, _ = m.Update(boardAppliedMsg{seq: s.seq, err: &core.NotConnectedError{Reason: core.BoardNotSignedInReason, HowToConnect: core.BoardNotSignedIn}})
		return m
	}
	// a board changed since it was first read: one thread has a new reply and one thread is new
	changed := func(t *testing.T, th Theme) tea.Model {
		m, _ := model(th, fixtureBoard(), true)
		r := fixtureBoard()
		r.Threads[1].Replies = 1
		r.Threads[1].Last = &core.BoardMessage{ID: "18", Channel: "dev", Author: "ada/claude", At: "2026-11-23T11:40:00Z", Text: "green on every region"}
		r.Threads = append(r.Threads, core.BoardThread{Root: core.BoardMessage{ID: "19", Channel: "ops", Author: "codex", At: "2026-11-23T11:55:00Z", Text: "queue drained, nothing stuck"}})
		m, _ = m.Update(boardMsg{report: r})
		return m
	}
	empty := func(writer bool) func(*testing.T, Theme) tea.Model {
		return func(t *testing.T, th Theme) tea.Model {
			m, _ := model(th, core.BoardReport{Header: core.Header{Source: core.SourceLive}}, writer)
			return m
		}
	}
	many := func(t *testing.T, th Theme) tea.Model {
		r := fixtureBoard()
		for i := 0; i < 180; i++ {
			r.Channels = append(r.Channels, core.BoardChannel{Path: fmt.Sprintf("team/channel-%03d", i), Threads: i % 7})
		}
		r.Truncated = "only the first 200 channels are listed; threads are shown for the 3 most recently active channels"
		m, _ := model(th, r, true)
		return m
	}
	long := func(keys ...string) func(*testing.T, Theme) tea.Model {
		return func(t *testing.T, th Theme) tea.Model {
			r := fixtureBoard()
			var b []string
			for i := 0; i < 40; i++ {
				b = append(b, fmt.Sprintf("line %02d of a long message that goes on and on", i))
			}
			r.Threads[0].Root.Text, r.Threads[0].Root.Cut = strings.Join(b, "\n"), true
			m, _ := model(th, r, true)
			for _, k := range keys {
				m = press(m, k)
			}
			return m
		}
	}
	form := []string{"ctrl+s review"}
	return []scenario{
		{name: "board-post-channel", build: std([]string{"n"}), tab: "Board", footHas: form, extra: framedSizes},
		{name: "board-post-filtered", build: std(cat([]string{"n"}, typed("web"), []string{"down"})), tab: "Board", footHas: form, extra: framedSizes},
		{name: "board-post-text", build: std(cat([]string{"n"}, typed("dev"), []string{"enter"}, typed("deploys are green"), []string{"enter"}, typed("second line"))), tab: "Board", footHas: form, extra: framedSizes},
		{name: "board-post-error", build: std([]string{"n", "ctrl+s"}), tab: "Board", footHas: form, extra: framedSizes},
		{name: "board-reply-form", build: std(cat([]string{"c"}, typed("on it"))), tab: "Board", footHas: form, extra: framedSizes},
		{name: "board-post-confirm", build: confirmed(cat([]string{"n"}, typed("dev"), []string{"enter"}, typed("deploys are green"), []string{"ctrl+s"})), tab: "Board", footHas: []string{"n/esc edit"}, extra: framedSizes, notGrid: true},
		{name: "board-reply-confirm", build: confirmed(cat([]string{"c"}, typed("on it"), []string{"ctrl+s"})), tab: "Board", footHas: []string{"n/esc edit"}, extra: framedSizes, notGrid: true},
		{name: "board-post-unknown", build: unknown, tab: "Board", extra: framedSizes, notGrid: true},
		{name: "board-post-refused", build: refused, tab: "Board", footHas: form, extra: framedSizes, notGrid: true},
		{name: "board-new-markers", build: changed, tab: "Board", extra: framedSizes},
		{name: "board-empty", build: empty(false), tab: "Board", extra: framedSizes},
		{name: "board-empty-writable", build: empty(true), tab: "Board", extra: framedSizes},
		{name: "board-many-channels", build: many, tab: "Board", extra: framedSizes},
		{name: "board-long-message", build: long(), tab: "Board", extra: framedSizes},
		{name: "board-long-message-scrolled", build: long("pgdown"), tab: "Board", extra: framedSizes, notGrid: true},
		{name: "board-long-message-drilled", build: long("enter", "j", "j"), tab: "Board", extra: framedSizes, notGrid: true},
	}
}
