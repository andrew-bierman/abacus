package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

type queueMod struct {
	*modtest.Fake
	r   core.QueuesReport
	err error
}

func (m queueMod) Queues(context.Context) (core.QueuesReport, error) { return m.r, m.err }

type insightsMod struct {
	*modtest.Fake
	r core.InsightsReport
}

func (m insightsMod) Insights(context.Context) (core.InsightsReport, error) { return m.r, nil }

// A queue provider's status is built from the queues domain that was already read: the summary,
// one line per run, and the tracker-subset badge only for that source.
func TestFleetStatusTakesAQueueProvidersStatusFromTheDomain(t *testing.T) {
	live := core.QueuesReport{
		Header: core.Header{State: core.Connected, Source: core.SourceLive},
		Runs:   []core.QueueRun{{Issue: "T-1", State: "running", Phase: "build"}, {Issue: "T-2", State: "pending", Stalled: true}},
		Queue:  []core.QueueItem{{ID: "T-3"}},
	}
	m := queueMod{Fake: &modtest.Fake{IDStr: "q", Stat: core.Status{Summary: "own"}}, r: live}
	st := *core.FleetStatusReport(context.Background(), "v", []core.Entry{entry(m, core.Ready, "")}).Modules[0].Status
	if st.Summary != live.Summarize() || st.Summary == "own" || st.Source != "" || len(st.Lines) != 2 ||
		st.Lines[0] != "T-1  running  build" || st.Lines[1] != "T-2  pending  ?  (stalled)" {
		t.Fatalf("%+v", st)
	}
	m.r.Header.Source = core.SourceTracker
	if st := *core.FleetStatusReport(context.Background(), "v", []core.Entry{entry(m, core.Ready, "")}).Modules[0].Status; st.Source != core.SourceTracker {
		t.Errorf("the tracker subset must be badged: %+v", st)
	}
}

// A provider that cannot be read says why instead of showing an empty summary.
func TestFleetStatusShowsAnUnreadableQueueProviderAsUnavailable(t *testing.T) {
	m := queueMod{Fake: &modtest.Fake{IDStr: "q"}, err: errors.New("no supervisor")}
	st := *core.FleetStatusReport(context.Background(), "v", []core.Entry{entry(m, core.Ready, "")}).Modules[0].Status
	if st.Unavailable != "no supervisor" || st.Summary != "" || len(st.Lines) != 0 {
		t.Fatalf("%+v", st)
	}
}

// Insights is computed in process: its status is the one already read, badged with the graph source.
func TestFleetStatusTakesInsightsStatusFromTheTasksDomain(t *testing.T) {
	im := core.InsightsReport{Header: core.Header{State: core.Connected, Source: core.SourceGraph}, TotalActionable: 2, TotalBlocked: 1}
	m := insightsMod{Fake: &modtest.Fake{IDStr: "g", Stat: core.Status{Summary: "own"}}, r: im}
	tasks := provMod{Fake: &modtest.Fake{IDStr: "t"}, tasks: func() (core.TasksReport, error) {
		return core.TasksReport{Header: core.Header{State: core.Connected}, Insights: &im}, nil
	}}
	r := core.FleetStatusReport(context.Background(), "v", []core.Entry{entry(tasks, core.Ready, ""), entry(m, core.Ready, "")})
	read := r.Domains.Tasks.Insights
	if read == nil {
		t.Fatalf("the insights were not read: %+v", r.Domains.Tasks)
	}
	if st := *r.Modules[1].Status; st.Summary != read.Summary || st.Summary == "own" || st.Source != core.SourceGraph {
		t.Fatalf("%+v", st)
	}
}
