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
