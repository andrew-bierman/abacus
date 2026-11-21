package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

func run(t *testing.T, mods []core.Module, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = cli.Run(args, cli.Deps{
		Out: &o, Err: &e, Version: "test",
		Build:  func(cli.Options) *core.Registry { return core.NewRegistry(mods...) },
		RunTUI: func([]core.Entry) error { return nil },
	})
	return code, o.String(), e.String()
}

func ready(id string, checks ...core.Check) *modtest.Fake {
	return &modtest.Fake{IDStr: id, Det: core.Detection{State: core.Ready, Version: "v1"}, Checks: checks,
		Stat: core.Status{Summary: "1 runs", Lines: []string{"T-1 running"}}}
}

type doc struct {
	Version string `json:"version"`
	Modules []struct {
		ID        string         `json:"id"`
		Detection core.Detection `json:"detection"`
		Checks    []core.Check   `json:"checks"`
		Status    *core.Status   `json:"status"`
	} `json:"modules"`
}

func parse(t *testing.T, s string) doc {
	t.Helper()
	var d doc
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatalf("invalid JSON %q: %v", s, err)
	}
	return d
}

func TestDoctorOKExitsZeroWithJSON(t *testing.T) {
	code, out, _ := run(t, []core.Module{ready("a", core.Check{Name: "c", Status: core.StatusOK})}, "doctor", "--json")
	d := parse(t, out)
	if code != 0 || len(d.Modules) != 1 || d.Modules[0].Checks[0].Status != "ok" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestFailExitsOneWarnAndSkipDoNot(t *testing.T) {
	mods := func(s string) []core.Module { return []core.Module{ready("a", core.Check{Name: "c", Status: s})} }
	for s, want := range map[string]int{core.StatusFail: 1, core.StatusWarn: 0, core.StatusSkip: 0} {
		if code, _, _ := run(t, mods(s), "doctor"); code != want {
			t.Errorf("%s: exit %d, want %d", s, code, want)
		}
	}
}

func TestNothingDetectedIsExitZeroAndEmptyModules(t *testing.T) {
	absent := &modtest.Fake{IDStr: "a", Det: core.Detection{State: core.Absent}}
	for _, cmd := range []string{"doctor", "status"} {
		code, out, _ := run(t, []core.Module{absent}, cmd)
		if code != 0 || !strings.Contains(out, "no Beads tools detected") {
			t.Fatalf("%s text: code=%d out=%q", cmd, code, out)
		}
		code, out, _ = run(t, []core.Module{absent}, cmd, "--json")
		if d := parse(t, out); code != 0 || d.Modules == nil || len(d.Modules) != 0 {
			t.Fatalf("%s json: code=%d out=%s", cmd, code, out)
		}
	}
}

func TestVersionJSONListsEveryModuleState(t *testing.T) {
	absent := &modtest.Fake{IDStr: "b", Det: core.Detection{State: core.Absent}}
	code, out, _ := run(t, []core.Module{ready("a"), absent}, "version", "--json")
	d := parse(t, out)
	if code != 0 || d.Version != "test" || len(d.Modules) != 2 || d.Modules[0].Detection.Version != "v1" || d.Modules[1].Detection.State != core.Absent {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestBadFlagAndUnknownCommandExitTwo(t *testing.T) {
	for _, args := range [][]string{{"doctor", "--nope"}, {"frobnicate"}, {}} {
		if code, _, _ := run(t, nil, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestIncompatibleIsWarnAndExitZero(t *testing.T) {
	inc := &modtest.Fake{IDStr: "a", Det: core.Detection{State: core.Incompatible, Detail: "api_version 2 unsupported"}}
	code, out, _ := run(t, []core.Module{inc}, "doctor", "--json")
	d := parse(t, out)
	if code != 0 || len(d.Modules) != 1 || d.Modules[0].Checks[0].Status != "warn" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestStatusUnavailableCarriesReasonAndExitsZero(t *testing.T) {
	m := ready("a")
	m.Stat = core.Status{Unavailable: "state API disabled"}
	code, out, _ := run(t, []core.Module{m}, "status", "--json")
	d := parse(t, out)
	if code != 0 || d.Modules[0].Status.Unavailable != "state API disabled" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if code, out, _ = run(t, []core.Module{m}, "status"); code != 0 || !strings.Contains(out, "state unavailable: state API disabled") {
		t.Fatalf("text: code=%d out=%q", code, out)
	}
}

type scripted struct{ resp map[string]string }

func (s scripted) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	k := strings.Join(args, " ")
	exit := 0
	if strings.HasPrefix(k, "doctor") {
		exit = 1
	}
	return []byte(s.resp[k]), exit, nil
}

// An unconfigured Lambe reports a failing config check; the umbrella passes it through.
func TestLambeConfigFailPassesThroughAndExitsOne(t *testing.T) {
	lm := lambe.New(lambe.Options{
		Runner:   scripted{resp: map[string]string{"version --json": `{"version":"v0.10.0","api_version":1}`, "doctor -json": `[{"name":"config","status":"fail","detail":"no config"}]`}},
		LookPath: func(string) (string, error) { return "/fake/lambe", nil },
	})
	code, out, _ := run(t, []core.Module{lm}, "doctor")
	if code != 1 || !strings.Contains(out, "no config") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestTUICommandRunsShellWithReadyModulesOnly(t *testing.T) {
	var got []core.Entry
	absent := &modtest.Fake{IDStr: "b", Det: core.Detection{State: core.Absent}}
	var o, e bytes.Buffer
	code := cli.Run([]string{"tui"}, cli.Deps{Out: &o, Err: &e, Version: "t",
		Build:  func(cli.Options) *core.Registry { return core.NewRegistry(ready("a"), absent) },
		RunTUI: func(en []core.Entry) error { got = en; return nil }})
	if code != 0 || len(got) != 1 || got[0].Module.ID() != "a" {
		t.Fatalf("code=%d entries=%v", code, got)
	}
}

func TestTUIFailureAndMissingRunnerExitOne(t *testing.T) {
	build := func(cli.Options) *core.Registry { return core.NewRegistry(ready("a")) }
	var o, e bytes.Buffer
	code := cli.Run([]string{"tui"}, cli.Deps{Out: &o, Err: &e, Version: "t", Build: build,
		RunTUI: func([]core.Entry) error { return errors.New("no terminal") }})
	if code != 1 || !strings.Contains(e.String(), "tui: no terminal") {
		t.Fatalf("runner error: code=%d stderr=%q", code, e.String())
	}
	if code := cli.Run([]string{"tui"}, cli.Deps{Out: &o, Err: &e, Version: "t", Build: build}); code != 1 {
		t.Fatalf("nil runner: code=%d, want 1", code)
	}
}
