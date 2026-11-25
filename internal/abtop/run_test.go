package abtop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
)

// taken and now pin a frame so quota staleness and summaries do not depend on the clock.
var taken = time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

// fixture is an invented machine: one thinking session, its port, an orphan and one store
// abacus could not read. No transcript text is captured from a real agent.
func fixture() core.SessionsReport {
	hdr := core.Header{Domain: core.DomainSessions, Title: "Sessions", State: core.Connected, Source: core.SourceLive}
	sess := agents.Session{
		Harness: agents.HarnessClaude, PID: 4242, CWD: "/home/dev/invoices",
		Title: "invoice totals", Model: "claude-sonnet-5", State: agents.StateThinking,
		Tokens:  agents.Tokens{Input: 100, Output: 20, CacheCreate: 10},
		Context: agents.Context{Used: 5000, Limit: 100000},
		Git:     agents.GitStatus{Branch: "main"},
		Ports:   []agents.Port{{Port: 3000, PID: 4242}},
	}
	return core.SessionsReport{
		Header: hdr,
		Snapshot: agents.Snapshot{
			Taken:      taken,
			Sessions:   []agents.Session{sess},
			Orphans:    []agents.Port{{Port: 3100, Command: "node", Project: "invoices", Orphan: true}},
			Unreadable: []string{"copilot: store unreadable"},
		},
		Quotas: []agents.Quota{{
			Source: agents.HarnessClaude, Updated: taken,
			Short: &agents.QuotaWindow{UsedPercent: 42},
			Long:  &agents.QuotaWindow{UsedPercent: 7, WindowMinutes: 30 * 24 * 60},
		}},
	}
}

// reader counts calls and serves fixture. A test that must fail uses failingReader instead.
func reader(calls *int) Reader {
	return func(context.Context) (core.SessionsReport, error) {
		*calls++
		return fixture(), nil
	}
}

func run(t *testing.T, args []string, d Deps) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	d.Out, d.Err = &out, &errOut
	if d.Now == nil {
		d.Now = func() time.Time { return taken }
	}
	code := Run(args, d)
	return code, out.String(), errOut.String()
}

func TestOncePrintsOneSnapshotOfTheCompleteRead(t *testing.T) {
	calls := 0
	code, out, errOut := run(t, []string{"--once"}, Deps{Read: reader(&calls), Version: "test"})
	if code != core.ExitOK || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if calls != 1 {
		t.Fatalf("read %d times, want one complete read for one snapshot", calls)
	}
	for _, want := range []string{
		"abtop  1 sessions, 1 active",
		"invoice totals",
		"claude-sonnet-5",
		"130 tok",
		"5% ctx",
		"main",
		"ports 3000",
		"quota claude  5h 42%  30d 7%",
		"orphan port 3100  node  (invoices)",
		"unreadable copilot: store unreadable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshotOmitsFactsTheSourceDidNotRecord(t *testing.T) {
	rep := core.SessionsReport{
		Header: core.Header{Domain: core.DomainSessions, State: core.Connected, Source: core.SourceLive},
		Snapshot: agents.Snapshot{Taken: taken, Sessions: []agents.Session{
			{Harness: agents.HarnessCodex, PID: 7, CWD: "/home/dev/quiet", State: agents.StateWaiting},
			{Harness: agents.HarnessCodex, PID: 9},
		}},
		Quotas: []agents.Quota{{
			Source: agents.HarnessCodex, Updated: taken,
			Short: &agents.QuotaWindow{UsedPercent: 3},
		}},
	}
	code, out, _ := run(t, []string{"--once"}, Deps{Read: func(context.Context) (core.SessionsReport, error) { return rep, nil }})
	if code != core.ExitOK {
		t.Fatalf("code=%d", code)
	}
	for _, unwanted := range []string{"0 tok", "0% ctx", "ports ", "30d"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("drew a fact the source did not record (%q):\n%s", unwanted, out)
		}
	}
	for _, want := range []string{"waiting     codex", "quiet", "unknown", "codex:9", "quota codex  5h 3%"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestSnapshotMarksAStaleQuota(t *testing.T) {
	rep := fixture()
	rep.Quotas[0].Updated = time.Time{}
	_, out, _ := run(t, []string{"--once"}, Deps{Read: func(context.Context) (core.SessionsReport, error) { return rep, nil }})
	if !strings.Contains(out, "quota claude  5h 42%  30d 7%  (stale)") {
		t.Fatalf("a gauge with no reported time must read stale:\n%s", out)
	}
}

func TestJSONPrintsTheSameTypedReportTheCLIPrints(t *testing.T) {
	want, err := core.ReportJSON(fixture())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	code, out, errOut := run(t, []string{"--json"}, Deps{Read: reader(&calls), Version: "test"})
	if code != core.ExitOK || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if out != string(want) {
		t.Fatalf("--json is not the typed report `abacus sessions --json` prints:\ngot  %s\nwant %s", out, want)
	}
	if !strings.Contains(out, `"schema_version": 1`) || !strings.Contains(out, `"domain": "sessions"`) {
		t.Fatalf("json lost its version or domain:\n%s", out)
	}
}

func TestJSONWinsOverOnceAndStillReadsOnce(t *testing.T) {
	calls := 0
	code, out, _ := run(t, []string{"--once", "--json"}, Deps{Read: reader(&calls), Version: "test"})
	if code != core.ExitOK || calls != 1 {
		t.Fatalf("code=%d calls=%d", code, calls)
	}
	var got struct {
		SchemaVersion int    `json:"schema_version"`
		Domain        string `json:"domain"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if got.SchemaVersion != 1 || got.Domain != "sessions" {
		t.Fatalf("report = %+v", got)
	}
	if strings.Contains(out, "invoice totals  claude-sonnet-5") {
		t.Fatalf("both output modes ran:\n%s", out)
	}
}

func TestReadFailureFailsTheCommand(t *testing.T) {
	fail := func(context.Context) (core.SessionsReport, error) {
		return core.SessionsReport{}, errors.New("no process table")
	}
	for _, args := range [][]string{{"--once"}, {"--json"}} {
		code, out, errOut := run(t, args, Deps{Read: fail, Version: "test"})
		if code != core.ExitFail {
			t.Errorf("%v: code=%d, want failure", args, code)
		}
		if out != "" {
			t.Errorf("%v: wrote a report from a failed read:\n%s", args, out)
		}
		if !strings.Contains(errOut, "no process table") {
			t.Errorf("%v: the failure was not reported: %q", args, errOut)
		}
	}
}

func TestVersionNeedsNoRead(t *testing.T) {
	calls := 0
	code, out, errOut := run(t, []string{"--version"}, Deps{Read: reader(&calls), Version: "1.2.3"})
	if code != core.ExitOK || out != "abtop 1.2.3\n" || errOut != "" || calls != 0 {
		t.Fatalf("code=%d out=%q stderr=%q calls=%d", code, out, errOut, calls)
	}
}

func TestUsageForUnknownAndExtraArguments(t *testing.T) {
	calls := 0
	for _, args := range [][]string{{"--nope"}, {"extra"}, {"--interval", "0s"}} {
		code, out, errOut := run(t, args, Deps{Read: reader(&calls), Version: "test"})
		if code != core.ExitUsage {
			t.Errorf("%v: code=%d, want usage", args, code)
		}
		if out != "" {
			t.Errorf("%v: wrote output for a usage error:\n%s", args, out)
		}
		if !strings.Contains(errOut, "usage: abtop") {
			t.Errorf("%v: no usage on stderr: %q", args, errOut)
		}
	}
	if calls != 0 {
		t.Fatalf("usage errors read the machine %d times", calls)
	}
}

func TestNoFlagsStartsTheMonitor(t *testing.T) {
	var got tea.Model
	code, out, errOut := run(t, nil, Deps{
		Read:     func(context.Context) (core.SessionsReport, error) { return fixture(), nil },
		Version:  "test",
		StartTUI: func(m tea.Model) error { got = m; return nil },
	})
	if code != core.ExitOK || out != "" || errOut != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	if got == nil {
		t.Fatal("the monitor was not started")
	}
	cmd := got.Init()
	if cmd == nil {
		t.Fatal("the monitor did not start a read")
	}
	next, _ := got.Update(cmd())
	if view := next.View(); !strings.Contains(view, "invoice totals") {
		t.Fatalf("the monitor was not wired to the sessions read:\n%s", view)
	}
}

func TestMonitorRefreshesWithTheTickReadNotTheCompleteRead(t *testing.T) {
	var onceCalls, tickCalls int
	once := func(context.Context) (core.SessionsReport, error) { onceCalls++; return fixture(), nil }
	tick := func(context.Context) (core.SessionsReport, error) { tickCalls++; return fixture(), nil }
	var got tea.Model
	code, _, errOut := run(t, nil, Deps{
		Read:     once,
		Tick:     tick,
		Version:  "test",
		StartTUI: func(m tea.Model) error { got = m; return nil },
	})
	if code != core.ExitOK || errOut != "" || got == nil {
		t.Fatalf("code=%d stderr=%q started=%v", code, errOut, got != nil)
	}
	cmd := got.Init()
	if cmd == nil {
		t.Fatal("the monitor did not start a read")
	}
	next, cmd := got.Update(cmd())
	if cmd == nil {
		t.Fatal("a finished read must arm the next tick")
	}
	if tickCalls != 1 || onceCalls != 0 {
		t.Fatalf("the first frame used complete=%d bounded=%d, want the bounded read", onceCalls, tickCalls)
	}
	next, cmd = next.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("a tick must start the next read")
	}
	if _, cmd := next.Update(cmd()); cmd == nil {
		t.Fatal("a finished refresh must re-arm the tick")
	}
	if tickCalls != 2 || onceCalls != 0 {
		t.Fatalf("a refresh used complete=%d bounded=%d, want only the bounded read", onceCalls, tickCalls)
	}
}

func TestStartFailureFailsTheCommand(t *testing.T) {
	code, _, errOut := run(t, nil, Deps{
		Read:     func(context.Context) (core.SessionsReport, error) { return fixture(), nil },
		StartTUI: func(tea.Model) error { return errors.New("no terminal") },
	})
	if code != core.ExitFail || !strings.Contains(errOut, "no terminal") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestModelRendersTheReportAndQuits(t *testing.T) {
	m := newModel(func(context.Context) (core.SessionsReport, error) { return fixture(), nil }, time.Second, func() time.Time { return taken })
	if m.Init() == nil {
		t.Fatal("the first frame must start a read")
	}
	next, cmd := m.Update(reportMsg{rep: fixture()})
	if cmd == nil {
		t.Fatal("a completed read must schedule the next tick")
	}
	view := next.View()
	for _, want := range []string{"abtop", "invoice totals", "quota claude", "q quit  r reload"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
	quit, cmd := next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q ran %T instead of quitting", cmd())
	}
	if _, ok := quit.(*model); !ok {
		t.Fatalf("Update returned %T", quit)
	}
}

func TestModelKeepsTheLastFrameWhenALaterReadFails(t *testing.T) {
	m := newModel(nil, time.Second, nil)
	next, _ := m.Update(reportMsg{rep: fixture()})
	next, _ = next.Update(reportMsg{err: errors.New("io timeout")})
	view := next.View()
	if !strings.Contains(view, "invoice totals") || !strings.Contains(view, "read failed: io timeout") {
		t.Fatalf("a later failure replaced the last good frame:\n%s", view)
	}
}

func TestModelKeepsTheLastFrameWhenARefreshHasNoUsableRead(t *testing.T) {
	for _, state := range []core.Connection{core.Degraded, core.NotConnected} {
		m := newModel(nil, time.Second, func() time.Time { return taken })
		next, _ := m.Update(reportMsg{rep: fixture()})
		empty := core.SessionsReport{Header: core.Header{
			Domain: core.DomainSessions, State: state, Reason: "process table timed out",
		}}
		next, _ = next.Update(reportMsg{rep: empty})
		view := next.View()
		if !strings.Contains(view, "invoice totals") || !strings.Contains(view, "process table timed out") {
			t.Fatalf("%s refresh replaced the last good frame:\n%s", state, view)
		}
		if strings.Contains(view, "no agent sessions are running") {
			t.Fatalf("%s refresh claimed the machine is idle:\n%s", state, view)
		}
	}
}

func TestModelStillTakesADegradedReadThatCarriesSessions(t *testing.T) {
	m := newModel(nil, time.Second, func() time.Time { return taken })
	next, _ := m.Update(reportMsg{rep: fixture()})
	partial := core.SessionsReport{
		Header: core.Header{Domain: core.DomainSessions, State: core.Degraded, Reason: "one store unreadable"},
		Snapshot: agents.Snapshot{Sessions: []agents.Session{{
			Harness: agents.HarnessCodex, PID: 9, CWD: "/home/dev/partial", State: agents.StateWaiting,
		}}},
	}
	next, _ = next.Update(reportMsg{rep: partial})
	view := next.View()
	if !strings.Contains(view, "partial") || strings.Contains(view, "invoice totals") {
		t.Fatalf("a degraded read that still carries sessions did not replace the frame:\n%s", view)
	}
}

func TestNarrowFrameDoesNotPoisonALaterWideFrame(t *testing.T) {
	m := newModel(nil, time.Second, func() time.Time { return taken })
	next, _ := m.Update(reportMsg{rep: fixture()})
	narrow, _ := next.Update(tea.WindowSizeMsg{Width: 20, Height: 40})
	if view := narrow.View(); strings.Contains(view, "invoice totals") {
		t.Fatalf("a 20-column frame kept the full-width row:\n%s", view)
	}
	wide, _ := next.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	if view := wide.View(); !strings.Contains(view, "invoice totals") || !strings.Contains(view, "claude-sonnet-5") {
		t.Fatalf("a narrow frame changed what a wide frame draws:\n%s", view)
	}
}

func TestModelDoesNotStartASecondReadWhileBusy(t *testing.T) {
	calls := 0
	m := newModel(reader(&calls), time.Second, nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil || !m.busy {
		t.Fatal("r must start the one read and mark the monitor busy")
	}
	if _, cmd := m.Update(tickMsg{}); cmd == nil || !m.ticking {
		t.Fatal("an overrunning read must re-arm the timer rather than start a second read")
	}
	if calls != 0 {
		t.Fatalf("counted a read that never ran: %d", calls)
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}); cmd != nil {
		t.Fatal("a reload started a second read while one was in flight")
	}
}

func TestIntervalReachesTheModel(t *testing.T) {
	var got tea.Model
	code, _, errOut := run(t, []string{"--interval", "7s"}, Deps{
		Read:     func(context.Context) (core.SessionsReport, error) { return fixture(), nil },
		Version:  "test",
		StartTUI: func(m tea.Model) error { got = m; return nil },
	})
	if code != core.ExitOK || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if m, ok := got.(*model); !ok || m.interval != 7*time.Second {
		t.Fatalf("interval = %v, want the parsed 7s", got)
	}
}

func TestModelReadsAgainOnTickAndReloadWithOneTimer(t *testing.T) {
	calls := 0
	m := newModel(reader(&calls), time.Second, nil)
	_, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("a tick must start a read")
	}
	next, cmd := m.Update(cmd())
	if calls != 1 {
		t.Fatalf("tick read %d times", calls)
	}
	if next.(*model).busy {
		t.Fatal("a finished read left the monitor busy, so it would never read again")
	}
	if cmd == nil || !next.(*model).ticking {
		t.Fatal("a finished read must arm the next tick")
	}
	reloaded, cmd := next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("r must reload")
	}
	joined, cmd := reloaded.Update(cmd())
	if calls != 2 {
		t.Fatalf("reload read %d times", calls)
	}
	if cmd != nil {
		t.Fatal("a reload armed a second timer beside the pending tick")
	}
	if _, ok := joined.(*model); !ok {
		t.Fatalf("Update returned %T", joined)
	}
}

func TestModelShowsAFirstReadFailure(t *testing.T) {
	m := newModel(nil, time.Second, nil)
	next, _ := m.Update(reportMsg{err: errors.New("boom")})
	if view := next.View(); !strings.Contains(view, "cannot read the machine") || !strings.Contains(view, "boom") {
		t.Fatalf("a first failure must say so:\n%s", view)
	}
}

func TestSnapshotShowsTheNotConnectedStateAndReason(t *testing.T) {
	rep := core.SessionsReport{Header: core.Header{
		Domain: core.DomainSessions, State: core.NotConnected,
		Reason: "this machine does not expose its process table",
	}}
	code, out, _ := run(t, []string{"--once"}, Deps{Read: func(context.Context) (core.SessionsReport, error) { return rep, nil }})
	if code != core.ExitOK || !strings.Contains(out, "this machine does not expose its process table") {
		t.Fatalf("code=%d out:\n%s", code, out)
	}
	if !strings.Contains(out, "abtop  not connected") || strings.Contains(out, "0 sessions") {
		t.Fatalf("the header hid why the read is unavailable:\n%s", out)
	}
}

func TestSnapshotDoesNotCallAnUnavailableReadIdle(t *testing.T) {
	rep := core.SessionsReport{Header: core.Header{
		Domain: core.DomainSessions, State: core.NotConnected,
		Reason:       "this machine does not expose its process table",
		HowToConnect: "run abacus doctor",
	}}
	code, out, _ := run(t, []string{"--once"}, Deps{Read: func(context.Context) (core.SessionsReport, error) { return rep, nil }})
	if code != core.ExitOK {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(out, "no agent sessions are running") {
		t.Fatalf("an unavailable read was rendered as an idle machine:\n%s", out)
	}
	if !strings.Contains(out, "how to connect: run abacus doctor") {
		t.Fatalf("the hint the report carries was dropped:\n%s", out)
	}
}

func TestSnapshotCallsAConnectedEmptyReadIdle(t *testing.T) {
	rep := core.SessionsReport{Header: core.Header{Domain: core.DomainSessions, State: core.Connected}}
	code, out, _ := run(t, []string{"--once"}, Deps{Read: func(context.Context) (core.SessionsReport, error) { return rep, nil }})
	if code != core.ExitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "no agent sessions are running") {
		t.Fatalf("a usable empty read did not say the machine is idle:\n%s", out)
	}
}

func TestSnapshotSanitizesTranscriptText(t *testing.T) {
	rep := fixture()
	rep.Sessions[0].Title = "line one\r\nline two\x1b[31m"
	rep.Sessions[0].Model = "model\x1b]0;evil\x07"
	rep.Orphans[0].Command = "node\x1b[2J"
	_, out, _ := run(t, []string{"--once"}, Deps{Read: func(context.Context) (core.SessionsReport, error) { return rep, nil }})
	if strings.ContainsAny(out, "\x1b\r\x07") {
		t.Fatalf("control runes reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "line one  line two[31m") {
		t.Fatalf("the title was not flattened and kept printable:\n%s", out)
	}
}
