package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestBrokenToolFailsDoctorAndIsListedInEveryReport(t *testing.T) {
	ctx := context.Background()
	broken := &modtest.Fake{IDStr: "b", Det: core.Detection{State: core.Broken, Detail: "version could not run (exit 2)"}}
	en := core.NewRegistry(broken).Detect(ctx)
	d := core.DoctorReport(ctx, "t", en)
	if d.ExitCode() != core.ExitFail || len(d.Modules) != 1 || d.Modules[0].Checks[0].Status != core.StatusFail {
		t.Fatalf("broken must fail doctor: %+v", d)
	}
	if s := core.StatusReport(ctx, "t", en); len(s.Modules) != 1 || s.Modules[0].Status != nil {
		t.Fatalf("broken must appear in status without a state read: %+v", s)
	}
}

func TestRegistryDetectSanitizesToolSuppliedVersionAndDetail(t *testing.T) {
	hostile := &modtest.Fake{IDStr: "h", Det: core.Detection{State: core.Ready,
		Version: "1.0\x1b]0;pwned\x07", Detail: "d\x1b[2Jetail"}}
	e := core.NewRegistry(hostile).Detect(context.Background())[0].Detection
	for label, s := range map[string]string{"version": e.Version, "detail": e.Detail} {
		for _, r := range s {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				t.Errorf("%s still contains control rune %U: %q", label, r, s)
			}
		}
	}
	if e.Version != "1.0]0;pwned" {
		t.Fatalf("version = %q", e.Version)
	}
}

func TestStatusRowsAreAdditiveAndOmittedWhenEmpty(t *testing.T) {
	b, _ := json.Marshal(core.Status{Summary: "s"})
	if strings.Contains(string(b), "rows") {
		t.Fatalf("rows must be omitempty so existing JSON is unchanged: %s", b)
	}
	if core.SchemaVersion != 1 {
		t.Fatal("an additive field must not bump the schema version")
	}
}

type slowMod struct {
	*modtest.Fake
	d time.Duration
}

func (s slowMod) Detect(context.Context) core.Detection {
	time.Sleep(s.d)
	return core.Detection{State: core.Ready}
}

func TestDetectRunsModulesConcurrentlyUnderOneDeadline(t *testing.T) {
	old := core.DetectDeadline
	core.DetectDeadline = 400 * time.Millisecond
	defer func() { core.DetectDeadline = old }()
	reg := core.NewRegistry(
		slowMod{&modtest.Fake{IDStr: "a"}, 250 * time.Millisecond},
		slowMod{&modtest.Fake{IDStr: "b"}, 250 * time.Millisecond},
		slowMod{&modtest.Fake{IDStr: "c"}, 3 * time.Second},
	)
	start := time.Now()
	es := reg.Detect(context.Background())
	if time.Since(start) > 900*time.Millisecond {
		t.Fatalf("detection was not concurrent / bounded: %v", time.Since(start))
	}
	if es[0].Detection.State != core.Ready || es[1].Detection.State != core.Ready || es[0].Module.ID() != "a" || es[2].Module.ID() != "c" {
		t.Fatalf("order or answers wrong: %+v", es)
	}
	if es[2].Detection.State != core.Broken || !strings.Contains(es[2].Detection.Detail, "timed out") {
		t.Fatalf("a module past the deadline is reported, not waited for: %+v", es[2].Detection)
	}
}
