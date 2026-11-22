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
