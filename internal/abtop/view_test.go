package abtop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
)

func loaded(t *testing.T, rep core.SessionsReport, w, h int) *model {
	t.Helper()
	m := newModel(nil, time.Second, func() time.Time { return taken })
	next, _ := m.Update(reportMsg{rep: rep})
	next, _ = next.Update(tea.WindowSizeMsg{Width: w, Height: h})
	got, ok := next.(*model)
	if !ok {
		t.Fatalf("Update returned %T", next)
	}
	return got
}

func TestLayoutDrawsTheHeaderSessionsDetailAndPorts(t *testing.T) {
	view := loaded(t, fixture(), 120, 40).View()
	for _, want := range []string{
		"abtop", "1 sessions, 1 active",
		"Sessions", "state", "agent", "▸ ◐ thinking", "invoice totals",
		"Session", "claude-sonnet-5", "Context", "Tokens", "Per turn",
		"quota claude", "5h", "30d",
		"Ports", "3000  claude  invoice totals", "3100  node  (invoices)  orphan",
		"q quit  r reload  j/k select",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
}

func TestHostPanelDrawsCPUHistoryMemoryAndLoad(t *testing.T) {
	m := newModel(nil, time.Second, func() time.Time { return taken })
	m.host = func(context.Context) (agents.HostVitals, error) { return agents.HostVitals{}, nil }
	sample := func(cpu float64) agents.HostVitals {
		return agents.HostVitals{
			CPUPercent: cpu, MemUsed: 12 << 30, MemTotal: 32 << 30,
			Load1: 1.20, Load5: 0.90, Load15: 0.70, LoadKnown: true,
		}
	}
	next, _ := m.Update(reportMsg{rep: fixture(), vitals: sample(10)})
	m = next.(*model)
	next, _ = m.Update(reportMsg{rep: fixture(), vitals: sample(100)})
	m = next.(*model)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := next.View()
	for _, want := range []string{
		"Host", "CPU 100%", "▂█", "mem 12G/32G (38%)", "load 1.20 0.90 0.70",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
}

func TestHostPanelSaysWhyItIsEmpty(t *testing.T) {
	view := loaded(t, fixture(), 120, 40).View()
	if !strings.Contains(view, "Host") || !strings.Contains(view, "host stats unavailable") {
		t.Fatalf("a monitor with no host read did not say so:\n%s", view)
	}

	m := newModel(nil, time.Second, func() time.Time { return taken })
	m.host = func(context.Context) (agents.HostVitals, error) { return agents.HostVitals{}, nil }
	next, _ := m.Update(reportMsg{rep: fixture(), vitalsErr: errors.New("no /proc/meminfo")})
	next, _ = next.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view = next.View()
	if !strings.Contains(view, "unavailable: no /proc/meminfo") || !strings.Contains(view, "invoice totals") {
		t.Fatalf("a host failure replaced the sessions frame:\n%s", view)
	}
}

func TestHostHistoryRingKeepsTheLatestSamples(t *testing.T) {
	var points []float64
	for i := 0; i < 100; i++ {
		points = appendCPUHistory(points, float64(i))
	}
	if len(points) != hostHistoryPoints || points[0] != 40 || points[len(points)-1] != 99 {
		t.Fatalf("ring = %d points starting %.0f and ending %.0f", len(points), points[0], points[len(points)-1])
	}
}

func TestSelectionMovesTheDetailPanel(t *testing.T) {
	rep := fixture()
	rep.Sessions = append(rep.Sessions, agents.Session{
		Harness: agents.HarnessCodex, PID: 4343, CWD: "/home/dev/receipts",
		Title: "receipt totals", Model: "gpt-fixture", State: agents.StateWaiting,
	})
	m := loaded(t, rep, 120, 40)
	if view := m.View(); !strings.Contains(view, "claude-sonnet-5") || strings.Contains(view, "gpt-fixture") {
		t.Fatalf("the first session was not selected:\n%s", view)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	view := next.View()
	if !strings.Contains(view, "gpt-fixture") || strings.Contains(view, "claude-sonnet-5") {
		t.Fatalf("j did not move the detail to the second session:\n%s", view)
	}
	if !strings.Contains(view, "invoice totals") || !strings.Contains(view, "receipt totals") {
		t.Fatalf("the table lost a row while the cursor moved:\n%s", view)
	}
	back, _ := next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if view := back.View(); !strings.Contains(view, "claude-sonnet-5") {
		t.Fatalf("k did not move the detail back:\n%s", view)
	}
}

func TestFrameNeverExceedsTheTerminal(t *testing.T) {
	for _, size := range []struct{ w, h int }{{40, 12}, {60, 24}, {100, 40}, {200, 60}} {
		view := loaded(t, fixture(), size.w, size.h).View()
		lines := strings.Split(view, "\n")
		if len(lines) > size.h {
			t.Errorf("%dx%d: drew %d lines", size.w, size.h, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got > size.w {
				t.Errorf("%dx%d: line %d is %d cells: %q", size.w, size.h, i, got, line)
			}
		}
	}
}

func TestTooSmallTerminalExplainsTheMinimum(t *testing.T) {
	view := loaded(t, fixture(), 30, 40).View()
	if !strings.Contains(view, "terminal too small") || !strings.Contains(view, "40x12") {
		t.Fatalf("a too-small frame did not explain the minimum:\n%s", view)
	}
	if strings.Contains(view, "invoice totals") {
		t.Fatalf("a too-small frame drew a full session row:\n%s", view)
	}
}

func TestEmptyMachineStillDrawsThePanels(t *testing.T) {
	rep := core.SessionsReport{Header: core.Header{Domain: core.DomainSessions, State: core.Connected, Source: core.SourceLive}}
	view := loaded(t, rep, 120, 40).View()
	if got := strings.Count(view, "no agent sessions are running"); got != 2 {
		t.Errorf("the Sessions and detail panels should both report an idle machine, got %d:\n%s", got, view)
	}
	if !strings.Contains(view, "no listening ports") {
		t.Errorf("the Ports panel did not report an idle machine:\n%s", view)
	}
}

func TestSelectedRowStaysVisiblePastThePanelHeight(t *testing.T) {
	rep := fixture()
	for i := 0; i < 30; i++ {
		rep.Sessions = append(rep.Sessions, agents.Session{
			Harness: agents.HarnessCodex, PID: 5000 + i, CWD: "/home/dev/row",
			Title: fmt.Sprintf("row-%02d", i), State: agents.StateWaiting,
		})
	}
	m := loaded(t, rep, 100, 24)
	for i := 0; i < 30; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = next.(*model)
	}
	view := m.View()
	if !strings.Contains(view, "▸ ") || !strings.Contains(view, "row-29") {
		t.Fatalf("the selected row scrolled out of view:\n%s", view)
	}
}

func TestSelectionStopsAtTheEndsAndKeepsTheSessionAcrossAReorder(t *testing.T) {
	second := agents.Session{
		Harness: agents.HarnessCodex, PID: 4343, CWD: "/home/dev/receipts",
		Title: "receipt totals", Model: "gpt-fixture", State: agents.StateWaiting,
	}
	rep := fixture()
	rep.Sessions = append(rep.Sessions, second)
	m := loaded(t, rep, 120, 40)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = next.(*model)
	if !strings.Contains(m.View(), "claude-sonnet-5") {
		t.Fatalf("k moved above the first session:\n%s", m.View())
	}
	for i := 0; i < 3; i++ {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = next.(*model)
	}
	if !strings.Contains(m.View(), "gpt-fixture") {
		t.Fatalf("j moved below the last session:\n%s", m.View())
	}

	reordered := fixture()
	reordered.Sessions = []agents.Session{second, fixture().Sessions[0]}
	next, _ = m.Update(reportMsg{rep: reordered})
	m = next.(*model)
	if view := m.View(); !strings.Contains(view, "gpt-fixture") || strings.Contains(view, "claude-sonnet-5") {
		t.Fatalf("a reordered read moved the detail off the selected session:\n%s", view)
	}
}

func TestDetailRendersHistoryDurationsAndResets(t *testing.T) {
	rep := fixture()
	s := &rep.Sessions[0]
	s.TokenHistory = []int64{1, 3, 2, 8}
	s.Started = taken.Add(-90 * time.Minute)
	s.LastActive = taken.Add(-5 * time.Minute)
	s.Children = []agents.Child{{PID: 4243, Command: "node server.js", RSSKB: 2048}}
	rep.Quotas[0].Short.ResetsAt = taken.Add(2 * time.Hour)

	for _, size := range []struct{ w, h int }{{40, 44}, {120, 40}} {
		view := loaded(t, rep, size.w, size.h).View()
		for _, want := range []string{"4 turns", "1h30m", "5m ago", "node server.js", "2M", "resets 2h00m"} {
			if !strings.Contains(view, want) {
				t.Errorf("%dx%d: missing %q:\n%s", size.w, size.h, want, view)
			}
		}
	}
}

func TestNarrowFrameKeepsTheSessionNameColumn(t *testing.T) {
	view := loaded(t, fixture(), 40, 24).View()
	for _, want := range []string{"state", "agent", "▸ ◐ thinking", "invoice totals"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q at 40 columns:\n%s", want, view)
		}
	}
	if strings.Contains(view, "branch") {
		t.Errorf("a 40-column table kept the lowest-priority column:\n%s", view)
	}
}

func TestFrameSanitizesTranscriptText(t *testing.T) {
	rep := fixture()
	rep.Sessions[0].Title = "line one\r\nline two\x1b[31m"
	rep.Sessions[0].Model = "model\x1b]0;evil\x07"
	rep.Sessions[0].Git.Branch = "main\x1b[2J"
	rep.Sessions[0].Children = []agents.Child{{PID: 7, Command: "node\x1b[2J"}}
	rep.Orphans[0].Command = "node\x1b[2J"
	view := loaded(t, rep, 120, 40).View()
	if strings.ContainsAny(view, "\x1b\r\x07") {
		t.Fatalf("control runes reached the terminal:\n%q", view)
	}
}

func TestCompactCountCoversEveryMagnitude(t *testing.T) {
	for n, want := range map[int64]string{
		999: "999", 1_500: "1.5k", 100_000: "100k", 1_000_000: "1M", 1_250_000_000: "1.2B",
	} {
		if got := compactCount(n); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDetailPanelStillShowsGlobalFacts(t *testing.T) {
	rep := core.SessionsReport{
		Header: core.Header{Domain: core.DomainSessions, State: core.Connected, Source: core.SourceLive},
		Snapshot: agents.Snapshot{
			Orphans:    []agents.Port{{Port: 8080, Command: "node"}},
			Unreadable: []string{"/home/dev/store: permission denied"},
		},
		Quotas: []agents.Quota{{Source: agents.HarnessClaude}},
	}
	view := loaded(t, rep, 120, 40).View()
	for _, want := range []string{"no agent sessions are running", "quota claude", "Orphaned ports", "8080", "Unreadable"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
}

func TestMinimumFrameWithAnIncompleteReadStillShowsARow(t *testing.T) {
	rep := fixture()
	rep.State = core.Degraded
	rep.Reason = "one store unreadable"
	rep.HowToConnect = "run abacus doctor"
	view := loaded(t, rep, 100, 12).View()
	for _, want := range []string{"degraded", "one store unreadable", "how to connect: run abacus doctor", "▸ ◐ thinking", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q at the minimum height:\n%s", want, view)
		}
	}
}

func TestMonitorHeaderKeepsTheSnapshotWords(t *testing.T) {
	got := strings.TrimRight(ansi.Strip(monitorHeader(fixture(), "", 200)[0]), " ")
	want := reportLines(fixture(), taken, nil)[0]
	if got != want {
		t.Fatalf("header drifted from the text snapshot:\n got %q\nwant %q", got, want)
	}
}

func TestReadClampsASelectionPastTheShorterList(t *testing.T) {
	rep := fixture()
	rep.Sessions = append(rep.Sessions, agents.Session{
		Harness: agents.HarnessCodex, PID: 4343, CWD: "/home/dev/receipts",
		Title: "receipt totals", Model: "gpt-fixture", State: agents.StateWaiting,
	})
	m := loaded(t, rep, 120, 40)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(*model)
	next, _ = m.Update(reportMsg{rep: fixture()})
	m = next.(*model)
	if m.sel != 0 || !strings.Contains(m.View(), "claude-sonnet-5") {
		t.Fatalf("a shrinking read left the cursor at %d:\n%s", m.sel, m.View())
	}
}
