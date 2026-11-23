package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
)

type tasksMod struct {
	*modtest.Fake
	rep core.TasksReport
	err error
}

func (m tasksMod) Tasks(context.Context) (core.TasksReport, error) { return m.rep, m.err }

func TestOverviewLeadsWithReadyAndListsWhereEachToolCameFrom(t *testing.T) {
	bd := tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, rep: core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 3, Open: 3}}}
	entries := []core.Entry{
		{Module: bd, Detection: core.Detection{State: core.Ready, Version: "1.3.4", Source: core.SourceBundled, Path: "/app/bundled/bd"}},
		{Module: &modtest.Fake{IDStr: "beads-lease"}, Detection: core.Detection{State: core.Ready, Version: "0.1.0", Source: core.SourcePATH, Path: "/opt/beads-lease"}},
		{Module: &modtest.Fake{IDStr: "lambe"}, Detection: core.Detection{State: core.Absent, Detail: "lambe was not found"}},
	}
	o := core.BuildOverview(context.Background(), entries)
	// abacus's own lambe is unavailable: that is abacus's problem, so it is not Ready.
	if o.Ready || o.Headline != "Needs attention: lambe is not available in this development build." {
		t.Fatalf("%+v", o)
	}
	var tools []string
	for _, tl := range o.Tools {
		tools = append(tools, tl.ID+"="+tl.Where)
	}
	if got := strings.Join(tools, " "); got != "bd=bundled beads-lease=PATH lambe=missing" {
		t.Fatalf("%s", got)
	}
	if o.Tools[0].Version != "1.3.4" || o.Tools[0].Path != "/app/bundled/bd" {
		t.Fatalf("%+v", o.Tools[0])
	}
}

func TestOverviewNeedsAttentionWhenTasksHasNoTracker(t *testing.T) {
	g := core.Guide{Domain: core.DomainTasks, Step: core.StepWorkspace, Message: "No tracker here", Action: "create your fleet workspace (bd init)"}
	bd := tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, err: &core.NeedsSetupError{Guide: g}}
	o := core.BuildOverview(context.Background(), []core.Entry{{Module: bd, Detection: core.Detection{State: core.Ready}}})
	if o.Ready || !strings.HasPrefix(o.Headline, "Needs attention") || len(o.Attention) == 0 || !strings.Contains(o.Attention[0], "No tracker here") {
		t.Fatalf("%+v", o)
	}
}

func TestOverviewEmptyDomainsExplainThemselves(t *testing.T) {
	bd := tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, rep: core.TasksReport{Header: core.Header{Source: core.SourceLive}}}
	o := core.BuildOverview(context.Background(), []core.Entry{{Module: bd, Detection: core.Detection{State: core.Ready}}})
	by := map[core.DomainID]core.DomainLine{}
	for _, d := range o.Domains {
		by[d.Domain] = d
	}
	if l := by[core.DomainBoard].Line; strings.Contains(l, "board-beads") || strings.Contains(l, "b9s") || strings.Contains(l, "npm") || !strings.Contains(l, "board") {
		t.Errorf("board: %q", l)
	}
	if !strings.Contains(by[core.DomainTasks].Line, "no tasks yet") {
		t.Errorf("tasks: %q", by[core.DomainTasks].Line)
	}
	if !o.Ready {
		t.Errorf("an empty tracker is ready: %+v", o)
	}
}

func TestOverviewSaysWhenABundledToolWasIgnoredAndWhatRanInstead(t *testing.T) {
	bd := tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, rep: core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 1}}}
	e := core.Entry{Module: bd, Detection: core.Detection{State: core.Ready, Version: "1.3.0", Source: core.SourcePATH, Path: "/usr/local/bin/bd",
		Ignored: "world-writable (/app/bundled/bd)"}}
	o := core.BuildOverview(context.Background(), []core.Entry{e})
	want := "the copy next to abacus was not used (world-writable (/app/bundled/bd))"
	if o.Tools[0].Note != want {
		t.Fatalf("note %q, want %q", o.Tools[0].Note, want)
	}
}

func TestOverviewNotesAVersionThatDiffersFromTheBuiltInOneWithoutBlocking(t *testing.T) {
	t.Cleanup(core.SetEmbeddedForTest(embedtest.New("n", nil, embedtest.Tool{Name: "bd", Version: "1.3.4", Body: "x"}).FS))
	bd := tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, rep: core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 1}}}
	for ver, want := range map[string]string{"1.3.5": "version 1.3.5 differs from the built-in 1.3.4 (informational)", "v1.3.4": ""} {
		o := core.BuildOverview(context.Background(), []core.Entry{{Module: bd, Detection: core.Detection{State: core.Ready, Version: ver, Source: core.SourceEmbedded}}})
		if o.Tools[0].Note != want || !o.Ready {
			t.Errorf("%s: note %q ready=%v", ver, o.Tools[0].Note, o.Ready)
		}
	}
}

func TestOverviewHeadlineCountsOptionalThingsToSetUp(t *testing.T) {
	bd := tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, rep: core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 1}}}
	o := core.BuildOverview(context.Background(), []core.Entry{{Module: bd, Detection: core.Detection{State: core.Ready}}})
	// Board, Leases and Queues have no provider here: three optional things, Tasks is fine.
	if o.Headline != "Ready — 3 optional things to set up" {
		t.Fatalf("%q", o.Headline)
	}
}
