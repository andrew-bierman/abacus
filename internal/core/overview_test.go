package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
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
		{Module: &modtest.Fake{IDStr: "bv"}, Detection: core.Detection{State: core.Ready, Version: "0.27.0", Source: core.SourcePATH, Path: "/opt/bv"}},
		{Module: &modtest.Fake{IDStr: "lambe"}, Detection: core.Detection{State: core.Absent, Detail: "lambe not found on PATH"}},
	}
	o := core.BuildOverview(context.Background(), entries)
	if !o.Ready || !strings.HasPrefix(o.Headline, "Ready") {
		t.Fatalf("%+v", o)
	}
	var tools []string
	for _, tl := range o.Tools {
		tools = append(tools, tl.ID+"="+tl.Where)
	}
	if got := strings.Join(tools, " "); got != "bd=bundled bv=PATH lambe=missing" {
		t.Fatalf("%s", got)
	}
	if o.Tools[0].Version != "1.3.4" || o.Tools[0].Path != "/app/bundled/bd" {
		t.Fatalf("%+v", o.Tools[0])
	}
}

func TestOverviewNeedsAttentionWhenTasksHasNoTracker(t *testing.T) {
	g := core.Guide{Domain: core.DomainTasks, Step: core.StepWorkspace, Key: "i", Message: "No tracker here", Action: "create your fleet workspace (bd init)"}
	bd := tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, err: &core.NeedsSetupError{Guide: g}}
	o := core.BuildOverview(context.Background(), []core.Entry{{Module: bd, Detection: core.Detection{State: core.Ready}}})
	if o.Ready || !strings.HasPrefix(o.Headline, "Needs attention") || len(o.Attention) == 0 || !strings.Contains(o.Attention[0], "No tracker here") {
		t.Fatalf("%+v", o)
	}
}

func TestOverviewBoardKeepsTheUpstreamDetailAndEmptyDomainsExplainThemselves(t *testing.T) {
	bd := tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, rep: core.TasksReport{Header: core.Header{Source: core.SourceLive}}}
	o := core.BuildOverview(context.Background(), []core.Entry{{Module: bd, Detection: core.Detection{State: core.Ready}}})
	by := map[core.DomainID]core.DomainLine{}
	for _, d := range o.Domains {
		by[d.Domain] = d
	}
	if !strings.Contains(by[core.DomainBoard].Line, "board-beads issue 15") || !strings.Contains(by[core.DomainBoard].Line, "press o") {
		t.Errorf("board: %q", by[core.DomainBoard].Line)
	}
	if !strings.Contains(by[core.DomainTasks].Line, "no tasks yet") {
		t.Errorf("tasks: %q", by[core.DomainTasks].Line)
	}
	if !o.Ready {
		t.Errorf("an empty tracker is ready: %+v", o)
	}
}
