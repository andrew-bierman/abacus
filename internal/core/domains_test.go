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
