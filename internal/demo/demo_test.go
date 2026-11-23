package demo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The demo providers plug into the domains they stand in for.
var (
	_ core.TaskProvider      = Tasks{}
	_ core.InsightsProvider  = Tasks{}
	_ core.TaskWriter        = Tasks{}
	_ core.TaskReader        = Tasks{}
	_ core.LeaseProvider     = Leases{}
	_ core.WorkspaceProvider = Leases{}
	_ core.WorkspaceProvider = WorkspaceProvider{}
	_ core.BoardProvider     = Board{}
	_ core.Module            = Tasks{}
)

func TestEveryDemoProviderIsReadyAndSaysItIsDemoData(t *testing.T) {
	for _, m := range []interface {
		core.Module
		IsDemo() bool
	}{NewTasks(), NewLeases(), NewBoard()} {
		if d := m.Detect(context.Background()); d.State != core.Ready {
			t.Errorf("%s: detection %v", m.ID(), d.State)
		}
		if !m.IsDemo() {
			t.Errorf("%s must report itself as demo data", m.ID())
		}
		if cs := m.Doctor(context.Background()); len(cs) == 0 || cs[0].Status != core.StatusOK {
			t.Errorf("%s: doctor %v", m.ID(), cs)
		}
	}
}

// A demo write changes nothing and says so: there is no path from here to a tracker.
func TestDemoWritesApplyToNothing(t *testing.T) {
	w := NewTasks()
	argv, err := w.BuildTaskArgv(core.TaskChange{Op: core.TaskCreate, Title: "ship it"})
	if err != nil || strings.Join(argv, " ") != "bd create --title=ship it" {
		t.Fatalf("argv = %v, %v", argv, err)
	}
	res, err := w.RunTask(context.Background(), argv)
	if err != nil || res.ID != "demo-1" || !strings.Contains(res.Title, "nothing was written") {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if w.TrackerDir() != "" {
		t.Fatal("a demo has no tracker directory")
	}
}

func TestDemoReadTaskFindsFixturesAndRejectsOthers(t *testing.T) {
	if got, err := NewTasks().ReadTask(context.Background(), "ac-12"); err != nil || got.Title != "wire the lease heartbeat" {
		t.Fatalf("ReadTask(ac-12) = %+v, %v", got, err)
	}
	if _, err := NewTasks().ReadTask(context.Background(), "nope"); err == nil {
		t.Fatal("an unknown id must be an error")
	}
}

func TestDemoReportsCoverEveryStateTheTabsDraw(t *testing.T) {
	states := map[core.LeaseState]bool{}
	for _, l := range LeasesReport().Leases {
		states[l.State] = true
	}
	if !states[core.LeaseHeld] || !states[core.LeaseStale] || !states[core.LeaseFree] {
		t.Fatalf("lease fixtures must show held, stale and free: %v", states)
	}
	if b := BoardReport(); len(b.Channels) == 0 || len(b.Threads) == 0 || len(b.Tasks) == 0 {
		t.Fatalf("board fixtures must fill every section: %+v", b)
	}
	if i := InsightsReport(); len(i.TopPicks) == 0 || len(i.CriticalPath) == 0 || len(i.Cycles) == 0 {
		t.Fatalf("insight fixtures must fill every section: %+v", i)
	}
}

// A demo provider says it is demo data, so Doctor does not call it a missing tool.
func TestDemoProvidersDetectWithTheDemoSource(t *testing.T) {
	for _, m := range []core.Module{NewTasks(), NewLeases(), NewBoard()} {
		if d := m.Detect(context.Background()); d.Source != core.SourceDemo {
			t.Errorf("%s: source %q, want %q", m.ID(), d.Source, core.SourceDemo)
		}
	}
}

func TestDemoWorkspacePlacesSomeItemsAmbiguouslyAndRunsNothing(t *testing.T) {
	items := []core.WorkspaceItem{{Kind: core.WorkspaceRun, ID: "T-1", Host: "operators-mac"}, {Kind: core.WorkspaceRun, ID: "T-2"}, {Kind: core.WorkspaceLease, ID: "ac-1k2"}, {Kind: core.WorkspaceLease, ID: "ac-00h"}}
	rep, err := NewWorkspace().Workspace(context.Background(), items)
	if err != nil || rep.State != core.Connected || len(rep.Placements) != 2 || len(rep.Ambiguous) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if p, _ := rep.For(items[0]); p == nil || p.Match != core.MatchExact || p.Host != "operators-mac" {
		t.Fatalf("%+v", p)
	}
	if p, c := rep.For(items[1]); p != nil || c != nil {
		t.Fatal("T-2 has no session")
	}
	plan := NewWorkspace().Open(rep.Placements[0])
	if len(plan.Rungs) != 0 || plan.Copy != "rex -S operators-mac" {
		t.Fatalf("%+v", plan)
	}
}

// The demo's placements are exactly what the real matcher says about the demo sessions.
func TestDemoWorkspaceExactAndLabelMatchesAndTheThirdTask(t *testing.T) {
	want := map[string]core.PlacementMatch{"T-1": core.MatchExact, "ac-zoj": core.MatchExact, "ac-00h": core.MatchExact, "T-3": core.MatchLabel}
	var items []core.WorkspaceItem
	for id := range want {
		items = append(items, core.WorkspaceItem{Kind: core.WorkspaceTask, ID: id})
	}
	rep, err := NewWorkspace().Workspace(context.Background(), items)
	if err != nil || len(rep.Placements) != len(want) || len(rep.Ambiguous) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, it := range items {
		p, _ := rep.For(it)
		if p == nil || p.Match != want[it.ID] || p.Session == "" || p.Label == "" {
			t.Errorf("%s: %+v, want a %s match", it.ID, p, want[it.ID])
		}
	}
	if p, _ := rep.For(core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "T-3"}); p.Session != "demo:s3" || p.Label != "review T-3 notes" {
		t.Errorf("T-3: %+v", p)
	}
	amb := core.WorkspaceItem{Kind: core.WorkspaceLease, ID: "ac-1k2"}
	rep, _ = NewWorkspace().Workspace(context.Background(), []core.WorkspaceItem{amb})
	_, cands := rep.For(amb)
	if len(cands) != 2 || cands[0].Match != core.MatchExact || cands[1].Match != core.MatchLabel || cands[0].Session != "demo:s6" || cands[1].Session != "demo:s7" {
		t.Errorf("%+v", cands)
	}
	if rep, _ := NewWorkspace().Workspace(context.Background(), []core.WorkspaceItem{{Kind: core.WorkspaceRun, ID: "T-2"}, {Kind: core.WorkspaceRun, ID: "T-10"}}); len(rep.Placements)+len(rep.Ambiguous) != 0 {
		t.Errorf("T-2 and T-10 have no session: %+v", rep)
	}
}

func TestDemoOpenPlanHasAnEmptyNotNilRungListAndACopyForEveryone(t *testing.T) {
	w := NewWorkspace()
	for _, p := range []core.Placement{{Host: "operators-mac", Session: "demo:s1"}, {Session: "demo:s1"}} {
		plan := w.Open(p)
		if plan.Rungs == nil || len(plan.Rungs) != 0 {
			t.Errorf("%+v: rungs must be an empty list", plan)
		}
		if plan.Note == "" {
			t.Errorf("%+v: a note", plan)
		}
	}
	if got := w.Open(core.Placement{Host: "operators-mac"}).Copy; got != "rex -S operators-mac" {
		t.Errorf("%q", got)
	}
	if got := w.Open(core.Placement{}).Copy; got != "rex" {
		t.Errorf("a session on this machine still has a command to copy: %q", got)
	}
	rep := core.ReadWorkspaceFrom(context.Background(), w, []core.WorkspaceItem{{Kind: core.WorkspaceRun, ID: "T-1", Host: "operators-mac"}})
	b, _ := json.Marshal(rep)
	if !strings.Contains(string(b), `"open":{"rungs":[],"copy":"rex -S operators-mac"`) {
		t.Errorf("%s", b)
	}
}
