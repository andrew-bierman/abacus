package cli_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/status-no-workspace.golden.json")

// fleetMod provides Tasks, Leases and Queues from fixed data.
type fleetMod struct{ *modtest.Fake }

func (fleetMod) Tasks(context.Context) (core.TasksReport, error) {
	return core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 2, Open: 1, Ready: 1},
		Recent: []core.Task{{ID: "t-1", Title: "one", Status: "open", Priority: 1}}}, nil
}
func (fleetMod) Leases(context.Context) (core.LeasesReport, error) {
	return core.LeasesReport{Header: core.Header{Source: core.SourceLive}, Leases: []core.Lease{{Resource: "t-1", State: core.LeaseHeld, Holder: "a"}}}, nil
}
func (fleetMod) Queues(context.Context) (core.QueuesReport, error) {
	return core.QueuesReport{Header: core.Header{Source: core.SourceLive}, Runs: []core.QueueRun{{Issue: "t-1", State: core.RunRunning, Machine: "mbp"}}}, nil
}

type fleetWS struct {
	fleetMod
	rep core.WorkspaceReport
}

func (w fleetWS) Workspace(context.Context, []core.WorkspaceItem) (core.WorkspaceReport, error) {
	return w.rep, nil
}
func (fleetWS) Open(core.Placement) core.OpenPlan { return core.OpenPlan{} }

func newFleet() fleetMod {
	return fleetMod{&modtest.Fake{IDStr: "fleet", Det: core.Detection{State: core.Ready, Version: "v1"}}}
}

// With no workspace provider (or one with nothing to report) status --json is byte-identical to
// what it was before the workspace capability existed: the golden was generated on main, before
// this change, and a mutant that always emits "workspace" must fail here.
func TestStatusJSONIsByteIdenticalWithoutAWorkspaceProvider(t *testing.T) {
	golden := filepath.Join("testdata", "status-no-workspace.golden.json")
	_, got, _ := run(t, []core.Module{newFleet()}, "status", "--json")
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("status --json changed without a workspace provider:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "workspace") {
		t.Fatal("workspace must be omitted")
	}
	// A provider that is merely not connected adds nothing either.
	_, got2, _ := run(t, []core.Module{newFleet(), fleetWS{newFleet(), core.WorkspaceReport{State: core.NotConnected, Reason: "none"}}}, "status", "--json")
	if strings.Contains(got2, `"workspace"`) {
		t.Fatalf("an empty report must be omitted:\n%s", got2)
	}
}

func TestStatusJSONGainsWorkspaceAdditivelyWhenThereAreFindings(t *testing.T) {
	ws := fleetWS{fleetMod{&modtest.Fake{IDStr: "ws", Det: core.Detection{State: core.Ready, Version: "v1"}}},
		core.WorkspaceReport{State: core.Connected, Placements: []core.Placement{{ID: "p1", Item: core.WorkspaceItem{Kind: "run", ID: "t-1", Host: "mbp"}, Host: "mbp", Session: "s1", Label: "Lambe · t-1", Match: core.MatchExact}}}}
	_, got, _ := run(t, []core.Module{newFleet(), ws}, "status", "--json")
	for _, want := range []string{`"schema_version": 1`, `"workspace": {`, `"placements"`, `"session": "s1"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, `"domains"`) {
		t.Fatal("domains lost")
	}
}
