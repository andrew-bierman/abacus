package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/andrew-bierman/abacus/internal/core"
)

// leasesWith builds the Leases section over a scripted read; change *r between ticks.
func leasesWith(r *leasesRead) *leasesSection {
	return newLeasesSection(func(context.Context) leasesRead { return *r })
}

// leasesReport is a live report of the given leases.
func leasesReport(ls ...core.Lease) leasesRead {
	return leasesRead{report: core.LeasesReport{Header: core.Header{Source: core.SourceLive}, Leases: ls}}
}

// run executes a command and feeds each resulting message to the section; a Batch is flattened.
func run(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = run(t, m, c)
		}
	case nil:
	default:
		m, _ = m.Update(msg)
	}
	return m
}

func TestLeasesSectionRefetchesOnEveryTick(t *testing.T) {
	r := leasesReport(core.Lease{Resource: "ac-1", State: core.LeaseHeld, Holder: "alice"})
	s := leasesWith(&r)
	s.tick = func() tea.Cmd { return nil } // never sleep in a test
	var m tea.Model = s
	m = run(t, m, s.Init())
	if !strings.Contains(m.View(), "1 held, 0 stale, 0 free") {
		t.Fatalf("first load missing:\n%s", m.View())
	}
	r = leasesReport()
	m, cmd := m.Update(leasesTickMsg{})
	m = run(t, m, cmd)
	if !strings.Contains(m.View(), "0 held, 0 stale, 0 free") {
		t.Fatalf("a tick must re-fetch; still showing:\n%s", m.View())
	}
}

func TestLeasesSectionRecoversFromAFailedFirstRead(t *testing.T) {
	r := leasesRead{unavailable: "hub busy"}
	s := leasesWith(&r)
	s.tick = func() tea.Cmd { return nil }
	var m tea.Model = s
	m = run(t, m, s.Init())
	if !strings.Contains(m.View(), "state unavailable: hub busy") {
		t.Fatalf("view:\n%s", m.View())
	}
	r = leasesReport(core.Lease{Resource: "back", State: core.LeaseFree})
	m, cmd := m.Update(leasesTickMsg{})
	m = run(t, m, cmd)
	if !strings.Contains(m.View(), "back") {
		t.Fatalf("a transient failure must not stick until restart:\n%s", m.View())
	}
}

func TestLeasesPollIntervalIsGentleOnSharedBackends(t *testing.T) {
	// The Leases read shells out to a shared production hub.
	if leasesPollEvery < 30*time.Second {
		t.Fatalf("leasesPollEvery = %v, want at least 30s", leasesPollEvery)
	}
	if s := leasesWith(&leasesRead{}); s.tick == nil {
		t.Fatal("the production section must schedule its own refresh")
	}
}

func TestLeasesTickRearmsExactlyOnce(t *testing.T) {
	s := leasesWith(&leasesRead{})
	ticks := 0
	s.tick = func() tea.Cmd { ticks++; return nil }
	if _, cmd := s.Update(leasesTickMsg{}); cmd == nil || ticks != 1 {
		t.Fatalf("a tick must re-fetch and re-arm exactly once (cmd=%v, timers=%d)", cmd != nil, ticks)
	}
}

func TestReadLeasesMapsProviderOutcomes(t *testing.T) {
	ok := readLeases(context.Background(), fakeLeases{r: core.LeasesReport{Leases: []core.Lease{{Resource: "a"}}}})
	if len(ok.report.Leases) != 1 || ok.unavailable != "" {
		t.Fatalf("ok read = %+v", ok)
	}
	nc := readLeases(context.Background(), fakeLeases{err: &core.NotConnectedError{Reason: "no hub", HowToConnect: "set it"}})
	if nc.unavailable != "no hub" || nc.howToConnect != "set it" {
		t.Fatalf("not-connected read = %+v", nc)
	}
	bad := readLeases(context.Background(), fakeLeases{err: errors.New("boom")})
	if bad.unavailable != "boom" || bad.howToConnect != "" {
		t.Fatalf("error read = %+v", bad)
	}
}

type fakeLeases struct {
	r   core.LeasesReport
	err error
}

func (p fakeLeases) Leases(context.Context) (core.LeasesReport, error) { return p.r, p.err }

// The badge color follows the lease's own tone (core decides it). Explicit kinds, so a wrong
// mapping in either place fails, and the three states must render distinctly.
func TestLeaseBadgesUseTheLeaseTone(t *testing.T) {
	r := leasesReport(
		core.Lease{Resource: "a", State: core.LeaseHeld, Holder: "x"},
		core.Lease{Resource: "b", State: core.LeaseStale, Holder: "y"},
		core.Lease{Resource: "c", State: core.LeaseFree})
	s := leasesWith(&r)
	th := NewTheme(rendererFor(termenv.TrueColor, true))
	s.SetTheme(th)
	var m tea.Model = s
	m, _ = m.Update(leasesMsg{read: r})
	v := m.View()
	want := map[core.LeaseState]Kind{core.LeaseHeld: KindAccent, core.LeaseStale: KindWarn, core.LeaseFree: KindOK}
	seen := map[string]bool{}
	for st, kind := range want {
		b := th.Badge(kind, string(st))
		if !strings.Contains(v, b) {
			t.Errorf("%s: badge %q missing:\n%s", st, b, v)
		}
		seen[b] = true
	}
	if len(seen) != 3 {
		t.Fatalf("held, stale and free badges must be distinct: %v", seen)
	}
}

func TestStateKindFollowsCoreTone(t *testing.T) {
	for state, want := range map[string]Kind{
		"running": KindOK, "RUNNING": KindOK, "failed": KindFail, "deferred": KindWarn,
		"pending": KindMuted, "settled": KindMuted, "canceled": KindMuted, "": KindMuted, "from-a-newer-supervisor": KindMuted,
		"active": KindMuted, "error": KindMuted, // never reported by the supervisor: no color of their own
	} {
		if got := stateKind(core.ParseRunState(state)); got != want {
			t.Errorf("stateKind(%q) = %v, want %v", state, got, want)
		}
	}
}

// A held lease with a readable expiry shows what is left, from the typed time (no text parsing).
func TestLeaseDetailShowsTimeRemaining(t *testing.T) {
	soon := goldenNow.Add(12 * time.Minute).Format(time.RFC3339)
	past := goldenNow.Add(-3 * time.Hour).Format(time.RFC3339)
	for _, c := range []struct{ expiry, want string }{{soon, "in 12m"}, {past, "3h ago"}} {
		r := leasesReport(core.Lease{Resource: "a", State: core.LeaseHeld, Holder: "x", ExpiresAt: c.expiry})
		s := leasesWith(&r)
		var m tea.Model = s
		m, _ = m.Update(leasesMsg{read: r})
		if v := sized(m, 100, 20).View(); !strings.Contains(v, "remaining") || !strings.Contains(v, c.want) {
			t.Errorf("%s: missing %q:\n%s", c.expiry, c.want, v)
		}
	}
	r := leasesReport(core.Lease{Resource: "a", State: core.LeaseStale, Holder: "x", ExpiresAt: goldenNow.Format(time.RFC3339)})
	var m tea.Model = leasesWith(&r)
	m, _ = m.Update(leasesMsg{read: r})
	if strings.Contains(sized(m, 100, 20).View(), "remaining") {
		t.Error("only a held lease has time left")
	}
}
