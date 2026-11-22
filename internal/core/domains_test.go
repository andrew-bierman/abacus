package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// provMod is a module that also provides some domains.
type provMod struct {
	*modtest.Fake
	tasks  func() (core.TasksReport, error)
	leases func() (core.LeasesReport, error)
}

func (p provMod) Tasks(context.Context) (core.TasksReport, error) {
	if p.tasks == nil {
		return core.TasksReport{}, nil
	}
	return p.tasks()
}
func (p provMod) Leases(context.Context) (core.LeasesReport, error) {
	if p.leases == nil {
		return core.LeasesReport{}, nil
	}
	return p.leases()
}

func entry(m core.Module, st core.State, detail string) core.Entry {
	return core.Entry{Module: m, Detection: core.Detection{State: st, Detail: detail}}
}

func TestNoProviderMeansNotConnectedWithHowToConnectNotMissing(t *testing.T) {
	d := core.ReadDomains(context.Background(), nil)
	for _, h := range []core.Header{d.Board.Header, d.Tasks.Header, d.Leases.Header, d.Queues.Header} {
		if h.State != core.NotConnected || h.HowToConnect == "" || h.Title == "" {
			t.Errorf("%s: %+v", h.Domain, h)
		}
	}
}

func TestReadyProviderIsConnectedAndTrackerSourceIsDegraded(t *testing.T) {
	m := provMod{Fake: &modtest.Fake{IDStr: "x"}, tasks: func() (core.TasksReport, error) {
		return core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 3, Open: 2, InProgress: 1, Ready: 2}}, nil
	}, leases: func() (core.LeasesReport, error) {
		return core.LeasesReport{Header: core.Header{Source: core.SourceTracker}, Leases: []core.Lease{{Resource: "r", State: core.LeaseHeld}}}, nil
	}}
	d := core.ReadDomains(context.Background(), []core.Entry{entry(m, core.Ready, "")})
	if d.Tasks.State != core.Connected || d.Tasks.Source != "live" || !strings.Contains(d.Tasks.Summary, "3 tasks") {
		t.Fatalf("tasks %+v", d.Tasks.Header)
	}
	if d.Leases.State != core.Degraded || d.Leases.Source != core.SourceTracker {
		t.Fatalf("leases %+v", d.Leases.Header)
	}
}

func TestBrokenProviderIsNotConnectedWithItsReason(t *testing.T) {
	m := provMod{Fake: &modtest.Fake{IDStr: "x"}}
	d := core.ReadDomains(context.Background(), []core.Entry{entry(m, core.Broken, "x exited 2\x1b[2J")})
	if d.Tasks.State != core.NotConnected || !strings.Contains(d.Tasks.Reason, "x exited 2") || strings.ContainsRune(d.Tasks.Reason, 0x1b) {
		t.Fatalf("%+v", d.Tasks.Header)
	}
}

func TestReadErrorIsDegradedAndSanitized(t *testing.T) {
	m := provMod{Fake: &modtest.Fake{IDStr: "x"}, tasks: func() (core.TasksReport, error) { return core.TasksReport{}, errors.New("no tracker\x07") }}
	d := core.ReadDomains(context.Background(), []core.Entry{entry(m, core.Ready, "")})
	if d.Tasks.State != core.Degraded || d.Tasks.Reason != "no tracker" || d.Tasks.HowToConnect == "" {
		t.Fatalf("%+v", d.Tasks.Header)
	}
}

func TestDomainJSONShape(t *testing.T) {
	b, _ := json.Marshal(core.ReadDomains(context.Background(), nil))
	for _, want := range []string{`"board":`, `"tasks":`, `"leases":`, `"queues":`, `"state":"not-connected"`, `"how_to_connect":`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}

func TestStatusReportDomainsAreAdditiveAndOmittedWhenUnset(t *testing.T) {
	b, _ := json.Marshal(core.StatusReport(context.Background(), "v", nil))
	if strings.Contains(string(b), "domains") || !strings.Contains(string(b), `"schema_version":1`) {
		t.Fatalf("%s", b)
	}
}

type insMod struct {
	*modtest.Fake
	rep core.InsightsReport
	err error
}

func (m insMod) Insights(context.Context) (core.InsightsReport, error) { return m.rep, m.err }

func TestInsightsNotConnectedNamesHowToInstallAndDoesNotHideTheDomain(t *testing.T) {
	d := core.ReadDomains(context.Background(), nil)
	for _, in := range []*core.InsightsReport{d.Tasks.Insights, d.Board.Insights} {
		if in == nil || in.State != core.NotConnected || !strings.Contains(in.HowToConnect, "brew install") {
			t.Fatalf("%+v", in)
		}
	}
}

func TestInsightsConnectedPartialAndFailed(t *testing.T) {
	ok := insMod{Fake: &modtest.Fake{IDStr: "g"}, rep: core.InsightsReport{Header: core.Header{Source: core.SourceGraph}, TotalActionable: 2, Cycles: [][]string{{"a", "b", "a"}}}}
	in := core.ReadDomains(context.Background(), []core.Entry{entry(ok, core.Ready, "")}).Tasks.Insights
	if in.State != core.Connected || in.Source != "graph analysis" || !strings.Contains(in.Summary, "1 cycles") {
		t.Fatalf("%+v", in.Header)
	}
	ok.rep.Reason = "plan unavailable"
	if in := core.ReadDomains(context.Background(), []core.Entry{entry(ok, core.Ready, "")}).Tasks.Insights; in.State != core.Degraded {
		t.Fatalf("partial must be degraded: %+v", in.Header)
	}
	bad := insMod{Fake: &modtest.Fake{IDStr: "g"}, err: errors.New("exit 2\x1b[2J")}
	in = core.ReadDomains(context.Background(), []core.Entry{entry(bad, core.Ready, "")}).Tasks.Insights
	if in.State != core.Degraded || strings.ContainsRune(in.Reason, 0x1b) {
		t.Fatalf("%+v", in.Header)
	}
}

type countMod struct {
	*modtest.Fake
	tasks, leases, queues, board, insights int
}

func (m *countMod) Tasks(context.Context) (core.TasksReport, error) {
	m.tasks++
	return core.TasksReport{Header: core.Header{Source: core.SourceLive}}, nil
}
func (m *countMod) Leases(context.Context) (core.LeasesReport, error) {
	m.leases++
	return core.LeasesReport{Header: core.Header{Source: core.SourceLive}}, nil
}
func (m *countMod) Queues(context.Context) (core.QueuesReport, error) {
	m.queues++
	return core.QueuesReport{Header: core.Header{Source: core.SourceLive}}, nil
}
func (m *countMod) Board(context.Context) (core.BoardReport, error) {
	m.board++
	return core.BoardReport{Header: core.Header{Source: core.SourceLive}}, nil
}
func (m *countMod) Insights(context.Context) (core.InsightsReport, error) {
	m.insights++
	return core.InsightsReport{Header: core.Header{Source: core.SourceGraph}}, nil
}

func TestReadDomainReadsOnlyThatDomainsProvider(t *testing.T) {
	cases := map[core.DomainID][5]int{ // tasks, leases, queues, board, insights
		core.DomainTasks:  {1, 0, 0, 0, 1},
		core.DomainBoard:  {0, 0, 0, 1, 1},
		core.DomainLeases: {0, 1, 0, 0, 0},
		core.DomainQueues: {0, 0, 1, 0, 0},
	}
	for id, want := range cases {
		m := &countMod{Fake: &modtest.Fake{IDStr: "c"}}
		d := core.ReadDomain(context.Background(), []core.Entry{entry(m, core.Ready, "")}, id)
		if got := [5]int{m.tasks, m.leases, m.queues, m.board, m.insights}; got != want {
			t.Errorf("%s read %v, want %v", id, got, want)
		}
		if d.Tasks.Domain == "" && id == core.DomainTasks {
			t.Errorf("the requested domain must be filled")
		}
	}
	m := &countMod{Fake: &modtest.Fake{IDStr: "c"}}
	core.ReadDomains(context.Background(), []core.Entry{entry(m, core.Ready, "")})
	if [5]int{m.tasks, m.leases, m.queues, m.board, m.insights} != [5]int{1, 1, 1, 1, 1} {
		t.Errorf("all domains read once each: %+v", m)
	}
}

func TestEmptyDomainsSerializeStably(t *testing.T) {
	b, _ := json.Marshal(core.ReadDomains(context.Background(), nil))
	s := string(b)
	for _, want := range []string{`"leases":[]`, `"counts":{"total":0,"open":0,"in_progress":0,"blocked":0,"deferred":0,"closed":0,"ready":0}`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `"leases":null`) {
		t.Errorf("null leases: %s", s)
	}
	// A degraded read keeps the same shape.
	m := provMod{Fake: &modtest.Fake{IDStr: "x"}, leases: func() (core.LeasesReport, error) { return core.LeasesReport{}, errors.New("boom") }}
	b, _ = json.Marshal(core.ReadDomains(context.Background(), []core.Entry{entry(m, core.Ready, "")}))
	if !strings.Contains(string(b), `"leases":[]`) {
		t.Errorf("degraded leases must be []: %s", b)
	}
}

func TestReasonOnAConnectedSourceMakesTheDomainDegraded(t *testing.T) {
	m := provMod{Fake: &modtest.Fake{IDStr: "x"}, leases: func() (core.LeasesReport, error) {
		return core.LeasesReport{Header: core.Header{Source: core.SourceSnapshot, Reason: "demo data"}}, nil
	}}
	d := core.ReadDomains(context.Background(), []core.Entry{entry(m, core.Ready, "")})
	if d.Leases.State != core.Degraded || d.Leases.Reason != "demo data" {
		t.Fatalf("%+v", d.Leases.Header)
	}
}

func TestSummarizeLivesOnTheReports(t *testing.T) {
	if got := (core.TasksReport{Counts: core.TaskCounts{Total: 3, Open: 1, InProgress: 1, Blocked: 1, Ready: 2}}).Summarize(); got != "3 tasks: 1 open, 1 in progress, 1 blocked, 2 ready" {
		t.Error(got)
	}
	q := core.QueuesReport{Runs: []core.QueueRun{{Issue: "a"}}, Queue: []core.QueueItem{{ID: "q"}, {ID: "r"}}, Attention: []core.Attention{{Issue: "a"}}}
	if got := q.Summarize(); got != "1 runs, 2 queued, 1 need attention" {
		t.Error(got)
	}
	q.Header.Source = core.SourceTracker
	if got := q.Summarize(); got != "1 runs, 1 need attention (tracker subset, no live daemon)" || strings.Contains(got, "queued") {
		t.Errorf("tracker subset has no queue: %q", got)
	}
	l := core.LeasesReport{Leases: []core.Lease{{State: core.LeaseHeld}, {State: core.LeaseStale}, {State: core.LeaseFree}, {State: core.LeaseFree}}}
	if got := l.Summarize(); got != "1 held, 1 stale, 2 free" {
		t.Error(got)
	}
	if got := (core.QueueRun{Issue: "T-1", State: "running", Stalled: true}).Line(); got != "T-1  running  ?  (stalled)" {
		t.Error(got)
	}
}

func TestLeaseHelpersShapeRowsOnce(t *testing.T) {
	r := core.LeasesReport{Header: core.Header{Source: core.SourceTracker}, Leases: []core.Lease{
		{Resource: "a", State: core.LeaseHeld, Holder: "h", ExpiresAt: "T"},
		{Resource: "b", State: core.LeaseHeld, Holder: "h"},
		{Resource: "c", State: core.LeaseStale, Holder: "old"},
		{Resource: "d", State: core.LeaseFree},
		{Resource: "e", State: "weird"}}}
	st := r.Status()
	if st.Summary != "2 held, 1 stale, 1 free" || st.Source != core.SourceTracker || len(st.Rows) != 5 || len(st.Lines) != 5 {
		t.Fatalf("%+v", st)
	}
	wantDetail := []string{"by h (expires T)", "by h (expires ?)", "(last holder old)", "", ""}
	wantTone := []core.Tone{core.ToneInfo, core.ToneInfo, core.ToneWarn, core.ToneOK, core.ToneMuted}
	for i, row := range st.Rows {
		if row.Detail != wantDetail[i] || row.Tone != wantTone[i] || r.Leases[i].Detail() != wantDetail[i] {
			t.Errorf("row %d = %+v", i, row)
		}
	}
	if st.Lines[0] != "a  held by h (expires T)" || st.Lines[3] != "d  free" {
		t.Errorf("%q", st.Lines)
	}
	live := core.LeasesReport{Header: core.Header{Source: core.SourceLive}}
	if live.Status().Source != "" {
		t.Error("a live source is not a badge")
	}
}
