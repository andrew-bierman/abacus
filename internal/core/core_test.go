package core_test

import (
	"context"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

func TestRegistryReportsEachStateInOrderAndDetectsOnce(t *testing.T) {
	ready := &modtest.Fake{IDStr: "a", Det: core.Detection{State: core.Ready}}
	absent := &modtest.Fake{IDStr: "b", Det: core.Detection{State: core.Absent}}
	incompat := &modtest.Fake{IDStr: "c", Det: core.Detection{State: core.Incompatible}}
	entries := core.NewRegistry(ready, absent, incompat).Detect(context.Background())

	want := []core.State{core.Ready, core.Absent, core.Incompatible}
	for i, e := range entries {
		if e.Detection.State != want[i] {
			t.Fatalf("entry %d state = %s, want %s", i, e.Detection.State, want[i])
		}
	}
	for _, f := range []*modtest.Fake{ready, absent, incompat} {
		if f.DetectCalls != 1 {
			t.Fatalf("%s detected %d times, want 1", f.IDStr, f.DetectCalls)
		}
	}
	if got := core.ReadyOnly(entries); len(got) != 1 || got[0].Module.ID() != "a" {
		t.Fatalf("ReadyOnly() = %v, want only module a", got)
	}
}

func TestReportsAndExitCodeRules(t *testing.T) {
	ctx := context.Background()
	failing := &modtest.Fake{IDStr: "f", Det: core.Detection{State: core.Ready}, Checks: []core.Check{{Name: "c", Status: core.StatusFail}}}
	warn := &modtest.Fake{IDStr: "w", Det: core.Detection{State: core.Ready}, Checks: []core.Check{{Name: "c", Status: core.StatusWarn}, {Name: "d", Status: core.StatusSkip}}}
	absent := &modtest.Fake{IDStr: "a", Det: core.Detection{State: core.Absent}}
	incompat := &modtest.Fake{IDStr: "i", Det: core.Detection{State: core.Incompatible, Detail: "api_version 2 unsupported"}}

	entries := core.NewRegistry(warn, absent, incompat).Detect(ctx)
	doctor := core.DoctorReport(ctx, "t", entries)
	if doctor.ExitCode() != core.ExitOK || len(doctor.Modules) != 2 || doctor.Modules[1].Checks[0].Status != core.StatusWarn {
		t.Fatalf("warn/skip/absent/incompatible must not fail: %+v", doctor)
	}
	if core.DoctorReport(ctx, "t", core.NewRegistry(failing).Detect(ctx)).ExitCode() != core.ExitFail {
		t.Fatal("a failed check must exit 1")
	}
	if v := core.VersionReport("t", entries); len(v.Modules) != 3 {
		t.Fatalf("version lists every module, absent included: %+v", v)
	}
	if s := core.StatusReport(ctx, "t", entries); len(s.Modules) != 2 || s.Modules[0].Status == nil || s.Modules[1].Status != nil {
		t.Fatalf("status only for ready modules: %+v", s)
	}
}

func TestDoctorReportNormalizesUndeclaredStatusToWarn(t *testing.T) {
	ctx := context.Background()
	odd := &modtest.Fake{IDStr: "o", Det: core.Detection{State: core.Ready},
		Checks: []core.Check{{Name: "a", Status: "error"}, {Name: "b", Status: core.StatusOK}, {Name: "c", Status: ""}}}
	r := core.DoctorReport(ctx, "t", core.NewRegistry(odd).Detect(ctx))
	got := []core.CheckStatus{r.Modules[0].Checks[0].Status, r.Modules[0].Checks[1].Status, r.Modules[0].Checks[2].Status}
	want := []core.CheckStatus{core.StatusWarn, core.StatusOK, core.StatusWarn}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("check %d status = %q, want %q", i, got[i], want[i])
		}
	}
	if r.ExitCode() != core.ExitOK {
		t.Fatal("a normalized warn must not fail the run")
	}
}

func TestEveryReportCarriesSchemaVersion(t *testing.T) {
	ctx := context.Background()
	en := core.NewRegistry(&modtest.Fake{IDStr: "a", Det: core.Detection{State: core.Ready}}).Detect(ctx)
	for name, r := range map[string]core.Report{
		"version": core.VersionReport("t", en), "doctor": core.DoctorReport(ctx, "t", en), "status": core.StatusReport(ctx, "t", en),
	} {
		if r.SchemaVersion != core.SchemaVersion || core.SchemaVersion != 1 {
			t.Errorf("%s report schema version = %d", name, r.SchemaVersion)
		}
	}
}
