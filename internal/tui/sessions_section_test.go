package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
)

func sessionsFixtureReport() core.SessionsReport {
	now := goldenNow
	return core.SessionsReport{
		Header: core.Header{Domain: core.DomainSessions, Title: "Sessions", State: core.Connected, Source: core.SourceLive},
		Snapshot: agents.Snapshot{
			Taken: now,
			Sessions: []agents.Session{
				{Harness: agents.HarnessClaude, PID: 1000, CWD: "/home/dev/shop", Title: "Invoice list", Model: "claude-sonnet-5", State: agents.StateThinking,
					Tokens: agents.Tokens{Input: 100, Output: 20, CacheRead: 300, CacheCreate: 10}, Context: agents.Context{Used: 5000, Limit: 100000},
					TokenHistory: []int64{10, 40, 30, 90, 60, 120}, Git: agents.GitStatus{Branch: "main", Added: 2, Modified: 3},
					Children:   []agents.Child{{PID: 1001, Command: "node dev-server", RSSKB: 204800}},
					Ports:      []agents.Port{{Port: 3000, PID: 1001, Command: "node dev-server"}},
					LastActive: now.Add(-20 * time.Second), Started: now.Add(-2*time.Hour - 14*time.Minute)},
				{Harness: agents.HarnessCodex, PID: 2000, CWD: "/home/dev/tools", Title: "tidy the scripts", Model: "gpt-5.5", State: agents.StateWaiting,
					Tokens: agents.Tokens{Input: 600, Output: 50, CacheRead: 400}, Context: agents.Context{Used: 500, Limit: 258000}},
			},
			Orphans:    []agents.Port{{Port: 3100, Command: "node", Project: "shop", Orphan: true}},
			Unreadable: []string{"claude: store unreadable"},
		},
		Quotas: []agents.Quota{{Source: agents.HarnessClaude, Updated: now,
			Short: &agents.QuotaWindow{UsedPercent: 42, ResetsAt: now.Add(2*time.Hour + 14*time.Minute)},
			Long:  &agents.QuotaWindow{UsedPercent: 7, WindowMinutes: 30 * 24 * 60, ResetsAt: now.Add(4 * 24 * time.Hour)}}},
	}
}

func sessionsAt(w, h int, rep core.SessionsReport) tea.Model {
	s := newSessionsSection(func(context.Context) (core.SessionsReport, error) { return rep, nil })
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(sessionsMsg{report: rep})
	return sized(m, w, h)
}

func TestSessionsTabShowsTheTypedReport(t *testing.T) {
	v := sessionsAt(140, 40, sessionsFixtureReport()).View()
	for _, want := range []string{
		"Sessions · 2 sessions, 1 active, 1 orphaned ports, 1 unreadable",
		"quota  claude  5h 42%  30d 7%",
		"state", "harness", "session", "tokens", "ctx", "branch", "ports",
		"◐ thinking", "Invoice list", "claude-sonnet-5", "130", "5%", "main +2 ~3",
		"Context", "5k / 100k",
		"Tokens", "total 130", "cache read 300",
		"Per turn", "▁▃▂▆▄█  6 turns",
		"Quota claude", "████░░░░░░ 42%", "30d", "resets 2h14m",
		"Children", "node dev-server", "200M",
		"Ports", "3000",
		"Orphaned ports", "3100", "(shop)",
		"Unreadable", "claude: store unreadable",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	for _, line := range strings.Split(v, "\n") {
		if lipgloss.Width(line) > 140 {
			t.Errorf("line too wide: %q", line)
		}
	}
}

func TestSessionsTabKeepsTheIdentityColumnsWhenNarrow(t *testing.T) {
	v := sessionsAt(40, 24, sessionsFixtureReport()).View()
	for _, want := range []string{"state", "session", "ctx", "Invoice list", "◐ thinking", "░░░░░ 5%"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q at 40 columns:\n%s", want, v)
		}
	}
	for _, line := range strings.Split(v, "\n") {
		if lipgloss.Width(line) > 40 {
			t.Errorf("line too wide: %q", line)
		}
	}
}

func TestSessionsTabDetailScrollsWhenDrilled(t *testing.T) {
	m := press(sessionsAt(40, 24, sessionsFixtureReport()), "enter")
	first := m.View()
	if !strings.Contains(first, "Invoice list") || !strings.Contains(first, "more") {
		t.Fatalf("the drilled detail starts at the selected session and says it is clipped:\n%s", first)
	}
	m = press(m, "pgdown")
	second := m.View()
	if second == first || !strings.Contains(second, "Children") {
		t.Fatalf("pgdn moves the detail viewport:\n%s", second)
	}
	if back := press(m, "esc").View(); !strings.Contains(back, "Sessions ·") {
		t.Fatalf("esc returns to the list:\n%s", back)
	}
}

func TestSessionsTabKeepsTheLastGoodFrameAfterALaterReadFails(t *testing.T) {
	rep := sessionsFixtureReport()
	var m tea.Model = sessionsAt(100, 30, rep)
	m, _ = m.Update(sessionsMsg{err: errors.New("process table vanished")})
	if v := m.View(); !strings.Contains(v, "Invoice list") || !strings.Contains(v, "read failed: process table vanished") {
		t.Fatalf("a transient failure must not erase the live frame:\n%s", v)
	}
	m, _ = m.Update(sessionsMsg{report: rep})
	if v := m.View(); strings.Contains(v, "read failed") {
		t.Fatalf("a recovered poll clears the failure notice:\n%s", v)
	}
}

func TestSessionsTabEmptyReportSaysNothingIsRunning(t *testing.T) {
	rep := sessionsFixtureReport()
	rep.Sessions = nil
	v := sessionsAt(100, 30, rep).View()
	if !strings.Contains(v, "no agent sessions are running") || !strings.Contains(v, "0 sessions, 0 active") {
		t.Fatalf("empty report:\n%s", v)
	}
}

func TestSessionsTabSelectionFallsBackWhenTheSessionIsGone(t *testing.T) {
	rep := sessionsFixtureReport()
	var m tea.Model = sessionsAt(120, 30, rep)
	if !strings.Contains(m.View(), "Invoice list") {
		t.Fatalf("first session selected:\n%s", m.View())
	}
	m, _ = m.Update(sessionsMsg{report: core.SessionsReport{Header: rep.Header, Snapshot: agents.Snapshot{
		Taken: rep.Taken, Sessions: []agents.Session{rep.Sessions[1]},
	}}})
	if v := m.View(); !strings.Contains(v, "tidy the scripts") || strings.Contains(v, "Invoice list") {
		t.Fatalf("the remaining session is selected:\n%s", v)
	}
	m, _ = m.Update(sessionsMsg{report: core.SessionsReport{Header: rep.Header, Snapshot: agents.Snapshot{Taken: rep.Taken}}})
	if v := m.View(); !strings.Contains(v, "no agent sessions are running") {
		t.Fatalf("an empty refresh falls back to the global detail:\n%s", v)
	}
}

func TestSessionsTabReloadStartsOneReadWhenIdle(t *testing.T) {
	rep := sessionsFixtureReport()
	s := newSessionsSection(func(context.Context) (core.SessionsReport, error) { return rep, nil })
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m, _ = m.Update(sessionsMsg{report: rep})
	m = press(m, "r")
	if s.fetches != 1 || !s.busy {
		t.Fatalf("r starts one read: %d busy=%v", s.fetches, s.busy)
	}
	m, _ = m.Update(sessionsMsg{report: rep})
	m = press(m, "r")
	m = press(m, "r")
	if s.fetches != 2 {
		t.Fatalf("a second r while idle starts another read, but not while one is in flight: %d", s.fetches)
	}
}

func TestSessionsTabMarksAStaleQuota(t *testing.T) {
	rep := sessionsFixtureReport()
	rep.Quotas[0].Updated = goldenNow.Add(-11 * time.Minute)
	v := sessionsAt(140, 40, rep).View()
	if !strings.Contains(v, "stale: not updated in the last 10 minutes") || strings.Contains(v, "resets 2h14m") {
		t.Fatalf("stale quota hides its reset countdown:\n%s", v)
	}
}

func TestSessionsTabSaysWhenADataSentinelMeansNotRecorded(t *testing.T) {
	rep := sessionsFixtureReport()
	rep.Sessions[0].Tokens = agents.Tokens{}
	rep.Sessions[0].Context = agents.Context{}
	rep.Sessions[0].TokenHistory = nil
	rep.Quotas = nil
	v := sessionsAt(140, 40, rep).View()
	for _, want := range []string{"no tokens recorded", "window not recorded", "this harness records no per-turn history"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
}

func TestSessionsTabShowsAReadFailureWithTheRetryHint(t *testing.T) {
	s := newSessionsSection(func(context.Context) (core.SessionsReport, error) {
		return core.SessionsReport{}, errors.New("process table vanished")
	})
	s.SetTheme(asciiTheme())
	var m tea.Model = s
	m = run(t, m, s.fetch()) // the read seam, not a hand-built message
	v := sized(m, 100, 30).View()
	for _, want := range []string{"Sessions", "state unavailable: process table vanished", "press r"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
}

func TestSessionsTabPollsOnlyWhileVisibleAndNeverOverlaps(t *testing.T) {
	rep := sessionsFixtureReport()
	s := newSessionsSection(func(context.Context) (core.SessionsReport, error) { return rep, nil })
	s.SetTheme(asciiTheme())
	s.activate()
	if s.fetches != 1 || !s.busy {
		t.Fatalf("activation starts one read: %d busy=%v", s.fetches, s.busy)
	}
	gen := s.gen
	s.Update(sessionsTickMsg{gen})
	s.Update(sessionsTickMsg{gen})
	if s.fetches != 1 {
		t.Fatalf("a stuck read is never overlapped by the poll: %d reads", s.fetches)
	}
	s.Update(sessionsMsg{report: rep})
	s.Update(sessionsTickMsg{gen})
	if s.fetches != 2 {
		t.Fatalf("an idle visible tab polls: %d reads", s.fetches)
	}
	s.Update(sessionsMsg{report: rep})
	s.deactivate()
	if _, cmd := s.Update(sessionsTickMsg{gen}); cmd != nil || s.fetches != 2 {
		t.Fatalf("a hidden tab does not poll and its old tick chain ends: %d", s.fetches)
	}
	s.activate()
	if _, cmd := s.Update(sessionsTickMsg{gen}); cmd != nil {
		t.Fatal("a tick from an earlier visit is dropped")
	}
}

func TestSessionsTabSanitizesTranscriptAndProcessText(t *testing.T) {
	rep := sessionsFixtureReport()
	rep.Sessions[0].Title = "evil \x1b[2Jtitle"
	rep.Sessions[0].CWD = "/home/dev/\x07shop"
	rep.Sessions[0].Children[0].Command = "node\x1b]0;x\x07"
	rep.Orphans[0].Command = "node\x1b[2J"
	v := sessionsAt(140, 40, rep).View()
	if strings.ContainsAny(v, "\x1b\x07") {
		t.Fatalf("control runes reached the terminal:\n%q", v)
	}
	if !strings.Contains(v, "evil [2Jtitle") {
		t.Fatalf("the title was flattened and kept printable:\n%s", v)
	}
}
