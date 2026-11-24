package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// freshBd is a Tasks provider with no tracker yet that can create the workspace.
type freshBd struct {
	*modtest.Fake
	mu     sync.Mutex
	made   bool
	ran    [][]string
	fail   error
	dir    string // InitDir when set
	repair bool   // the workspace is half-created: the guide offers the repair
}

func (f *freshBd) Domain() core.DomainID { return core.DomainTasks }
func (f *freshBd) Guide(context.Context) *core.Guide {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.made {
		return nil
	}
	if f.repair {
		return &core.Guide{Domain: core.DomainTasks, Step: core.StepRepair, Message: "Your fleet workspace was only partly created", Action: "repair it (nothing is deleted)"}
	}
	return &core.Guide{Domain: core.DomainTasks, Step: core.StepWorkspace, Message: "No tracker here", Action: "create your fleet workspace (bd init)", Detail: "It will be created in /data/abacus/workspace."}
}
func (f *freshBd) Tasks(context.Context) (core.TasksReport, error) {
	if g := f.Guide(context.Background()); g != nil {
		return core.TasksReport{}, &core.NeedsSetupError{Guide: *g}
	}
	return core.TasksReport{Header: core.Header{Source: core.SourceLive}}, nil
}
func (f *freshBd) TrackerDir() string { return "" }
func (f *freshBd) InitDir() string {
	if f.dir != "" {
		return f.dir
	}
	return "/data/abacus/workspace"
}
func (f *freshBd) BuildTaskArgv(core.TaskChange) ([]string, error) {
	return []string{"bd", "init", "--non-interactive", "--quiet", "--skip-agents", "--skip-hooks", "--prefix=abacus"}, nil
}
func (f *freshBd) RunTask(_ context.Context, argv []string) (core.TaskResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, argv)
	if f.fail != nil {
		return core.TaskResult{}, f.fail
	}
	f.made = true
	return core.TaskResult{Title: "fleet workspace in /data/abacus/workspace", Status: "initialized"}, nil
}

type tuiAudit struct{ n int }

func (a *tuiAudit) Record(core.AuditEntry) error { a.n++; return nil }

func guidedRig(t *testing.T, bd *freshBd) (tea.Model, *tuiAudit) {
	t.Helper()
	au := &tuiAudit{}
	en := []core.Entry{{Module: bd, Detection: core.Detection{State: core.Ready}}}
	setup := core.NewSetup(en, core.SetupOptions{Audit: au})
	m := New(en, WithTheme(asciiTheme()), WithAudit(au), WithSetup(setup))
	var tm tea.Model = sized(m, 100, 30)
	// Not Init(): it starts poll ticks, which a test must never sleep through.
	tm, _ = tm.Update(guideMsg{domain: core.DomainTasks, g: bd.Guide(context.Background())})
	tm, _ = tm.Update(tasksMsg{err: &core.NeedsSetupError{Guide: *bd.Guide(context.Background())}})
	return press(tm, "2"), au
}

func TestTasksWithNoTrackerShowsOneCallToActionNotAWallOfErrors(t *testing.T) {
	m, _ := guidedRig(t, &freshBd{Fake: &modtest.Fake{IDStr: "bd"}})
	v := m.View()
	if !strings.Contains(v, "No tracker here — press i to create your fleet workspace (bd init)") {
		t.Fatalf("missing the call to action:\n%s", v)
	}
	for _, bad := range []string{"unavailable", "exited", "state unavailable", "Insights"} {
		if strings.Contains(v, bad) {
			t.Errorf("a guided state must not show %q:\n%s", bad, v)
		}
	}
}

func TestSetupKeyOpensAConfirmScreenAndNothingRunsUntilY(t *testing.T) {
	bd := &freshBd{Fake: &modtest.Fake{IDStr: "bd"}}
	m, au := guidedRig(t, bd)
	m = press(m, "i")
	v := m.View()
	for _, want := range []string{"Confirm this setup step", "exact command", "bd init --non-interactive"} {
		if !strings.Contains(v, want) {
			t.Fatalf("confirm screen missing %q:\n%s", want, v)
		}
	}
	if len(bd.ran) != 0 || au.n != 0 {
		t.Fatal("opening the confirm screen must not run or audit anything")
	}
	m = press(m, "n")
	if len(bd.ran) != 0 || !strings.Contains(m.View(), "cancelled: nothing was changed") {
		t.Fatalf("n must cancel:\n%s", m.View())
	}
	m = press(m, "i")
	m = pressSettle(t, m, "y")
	if len(bd.ran) != 1 || au.n != 2 {
		t.Fatalf("y applies exactly once, audited: ran=%d audit=%d", len(bd.ran), au.n)
	}
	if v := m.View(); strings.Contains(v, "No tracker here") || !strings.Contains(v, "created your fleet workspace") {
		t.Fatalf("after setup the guide is gone and the result is shown:\n%s", v)
	}
}

func TestAFailedSetupStaysGuidedAndSaysWhy(t *testing.T) {
	bd := &freshBd{Fake: &modtest.Fake{IDStr: "bd"}, fail: errors.New("bd init exited 1: nothing was set up")}
	m, _ := guidedRig(t, bd)
	m = press(m, "i")
	m = pressSettle(t, m, "y")
	v := m.View()
	if !strings.Contains(v, "not applied: bd init exited 1") || !strings.Contains(v, "No tracker here") {
		t.Fatalf("%s", v)
	}
}

func TestOtherKeysDoNothingWhileGuided(t *testing.T) {
	bd := &freshBd{Fake: &modtest.Fake{IDStr: "bd"}}
	m, _ := guidedRig(t, bd)
	before := m.View()
	for _, k := range []string{"n", "e", "x", "y"} {
		m = press(m, k)
	}
	if m.View() != before || len(bd.ran) != 0 {
		t.Fatalf("keys other than the setup key must not act on the hidden section:\n%s", m.View())
	}
}

// settle runs a command and, recursively, every command its results produce (a Batch is
// flattened). Only for flows that start no poll tick.
func settle(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = settle(t, m, c)
		}
	case nil:
	default:
		var next tea.Cmd
		m, next = m.Update(msg)
		m = settle(t, m, next)
	}
	return m
}

func pressSettle(t *testing.T, m tea.Model, k string) tea.Model {
	t.Helper()
	m, cmd := m.Update(runeOrKey(k))
	return settle(t, m, cmd)
}

// countingBd counts how often its guide is asked.
type countingBd struct {
	*freshBd
	n int
}

func (c *countingBd) Guide(ctx context.Context) *core.Guide { c.n++; return c.freshBd.Guide(ctx) }

func TestAHiddenGuidedTabNeverProbesAndTheVisibleOneDoes(t *testing.T) {
	bd := &countingBd{freshBd: &freshBd{Fake: &modtest.Fake{IDStr: "bd"}}}
	au := &tuiAudit{}
	en := []core.Entry{{Module: bd, Detection: core.Detection{State: core.Ready}}}
	setup := core.NewSetup(en, core.SetupOptions{Audit: au})
	mod := New(en, WithTheme(asciiTheme()), WithAudit(au), WithSetup(setup))
	var tm tea.Model = sized(mod, 100, 30)
	sec := mod.tabs[1].sec.(*guidedSection)
	sec.tickFn = func() tea.Cmd { return nil } // never sleep in a test
	// A tick while the Tasks tab is hidden re-arms nothing and probes nothing.
	if _, cmd := sec.Update(guideTickMsg{domain: core.DomainTasks}); cmd != nil {
		t.Fatal("a hidden tab must not probe on its tick")
	}
	// Showing the tab probes, and the tick then probes too.
	tm, cmd := tm.Update(runeOrKey("2"))
	if !sec.active || cmd == nil {
		t.Fatal("the tab became active and must probe")
	}
	if _, cmd := sec.Update(guideTickMsg{domain: core.DomainTasks}); cmd == nil {
		t.Fatal("a visible tab probes on its tick")
	}
	// Leaving it deactivates.
	tm.Update(runeOrKey("1"))
	if sec.active {
		t.Fatal("deactivated on leaving")
	}
}

func TestAHalfCreatedWorkspaceOffersARepairWithTheExactPlan(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(filepath.Join(ws, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	bd := &freshBd{Fake: &modtest.Fake{IDStr: "bd"}, dir: ws, repair: true}
	m, au := guidedRig(t, bd)
	if v := m.View(); !strings.Contains(v, "only partly created — press r to repair it") {
		t.Fatalf("missing the repair call to action:\n%s", v)
	}
	m = press(m, "r")
	v := m.View()
	for _, want := range []string{"Confirm this setup step", ".beads.incomplete-", "nothing is deleted"} {
		if !strings.Contains(v, want) {
			t.Fatalf("repair confirm screen missing %q:\n%s", want, v)
		}
	}
	if len(bd.ran) != 0 || au.n != 0 {
		t.Fatal("opening the repair plan must not run or audit anything")
	}
	m = pressSettle(t, m, "y")
	if _, err := os.Stat(filepath.Join(ws, ".beads")); err == nil || len(bd.ran) != 1 {
		t.Fatalf("y repairs once: ran=%d\n%s", len(bd.ran), m.View())
	}
}
