package cli_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// bundledRegistry is the four tools abacus ships with lambe gone, plus an absent Board CLI.
func bundledRegistry() []core.Module {
	var mods []core.Module
	for _, id := range core.KnownTools {
		det := core.Detection{State: core.Ready, Version: "1.0.0", Source: core.SourceBundled, Path: "/app/bundled/" + id}
		if id == "lambe" {
			det = core.Detection{State: core.Absent, Detail: "lambe not found on PATH"}
		}
		if id == "bd" {
			mods = append(mods, newTaskMod2(det))
			continue
		}
		mods = append(mods, &modtest.Fake{IDStr: id, Det: det})
	}
	return append(mods, &modtest.Fake{IDStr: "board", Det: core.Detection{State: core.Absent, Detail: "the Vercel Board CLI was not found on PATH"}})
}

func newTaskMod2(det core.Detection) *taskMod {
	return &taskMod{Fake: &modtest.Fake{IDStr: "bd", Det: det}}
}

func runVersion(t *testing.T, version string, mods []core.Module, args ...string) string {
	t.Helper()
	var o, e bytes.Buffer
	cli.Run(args, cli.Deps{Out: &o, Err: &e, Version: version,
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(mods...) }})
	t.Cleanup(func() { core.SetBuildVersion("t") })
	return o.String()
}

func TestDoctorInAReleaseLeadsWithAbacusOwnCopyAndNeverSaysMissing(t *testing.T) {
	out := runVersion(t, "1.2.3", bundledRegistry(), "doctor")
	for _, want := range []string{
		"Needs attention: abacus's own copy of lambe is damaged or was moved. Re-extract the download and keep the folder together.",
		"Bundled tools: 3 of 4 verified; 1 unusable (lambe)",
		"lambe        unavailable",
		"board        optional",
		"optional: adds the Board tab",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "missing") || strings.Contains(out, "dev-bundle") {
		t.Errorf("a release user must not see \"missing\" or the dev command:\n%s", out)
	}
}

func TestDoctorInADevBuildSaysRunDevBundle(t *testing.T) {
	out := runVersion(t, "dev", bundledRegistry(), "doctor")
	if !strings.Contains(out, "Needs attention: abacus's own tools are not set up for this build (lambe): run scripts/dev-bundle.sh") {
		t.Errorf("%s", out)
	}
	if strings.Contains(out, "Re-extract") {
		t.Errorf("a dev build has no archive to re-extract:\n%s", out)
	}
}

func TestVersionSaysTheBundledToolsLine(t *testing.T) {
	out := runVersion(t, "1.2.3", bundledRegistry(), "version")
	if !strings.Contains(out, "Bundled tools: 3 of 4 verified; 1 unusable (lambe)") || !strings.Contains(out, "lambe      unavailable") || strings.Contains(out, "not found on PATH") {
		t.Errorf("%s", out)
	}
	js := runVersion(t, "1.2.3", bundledRegistry(), "version", "--json")
	var d struct {
		SchemaVersion int `json:"schema_version"`
		Bundled       struct {
			Total, Verified int
			Unusable        []string
		}
	}
	if err := json.Unmarshal([]byte(js), &d); err != nil || d.SchemaVersion != 1 || d.Bundled.Total != 4 || d.Bundled.Verified != 3 || len(d.Bundled.Unusable) != 1 {
		t.Errorf("%v %+v\n%s", err, d, js)
	}
}
