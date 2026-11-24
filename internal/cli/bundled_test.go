package cli_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
)

// builtInRegistry is the four built-in tools with lambe unusable.
func builtInRegistry() []core.Module {
	var mods []core.Module
	for _, id := range core.KnownTools {
		det := core.Detection{State: core.Ready, Version: "1.0.0", Source: core.SourceEmbedded, Path: "/cache/" + id}
		if id == "lambe" {
			det = core.Detection{State: core.Absent, Detail: "lambe is not available (bundled copy ignored: could not write into /x: permission denied)"}
		}
		if id == "bd" {
			mods = append(mods, newTaskMod2(det))
			continue
		}
		mods = append(mods, &modtest.Fake{IDStr: id, Det: det})
	}
	return mods
}

func newTaskMod2(det core.Detection) *taskMod {
	return &taskMod{Fake: &modtest.Fake{IDStr: "bd", Det: det}}
}

func runBuild(t *testing.T, embedded bool, mods []core.Module, args ...string) string {
	t.Helper()
	if embedded {
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
	} else {
		t.Cleanup(core.SetEmbeddedForTest(nil))
	}
	var o, e bytes.Buffer
	cli.Run(args, cli.Deps{Out: &o, Err: &e, Version: "1.2.3",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(mods...) }})
	return o.String()
}

func TestDoctorInAnEmbeddedBuildSaysWhatFailedAndTheOneFix(t *testing.T) {
	out := runBuild(t, true, builtInRegistry(), "doctor")
	for _, want := range []string{
		"Needs attention: The built-in lambe could not be set up: could not write into /x: permission denied. Choose a folder you can write to with ABACUS_TOOLS_DIR=<folder>, then press r.",
		"Built-in tools: 4 of 5 ready; 1 unusable (lambe)",
		"lambe        unavailable",
		"bd           built in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "development build") || strings.Contains(out, "dev-bundle") {
		t.Errorf("an embedded build is not a development build:\n%s", out)
	}
}

func TestDoctorInADevelopmentBuildSaysToolsAreNotBuiltInOnce(t *testing.T) {
	out := runBuild(t, false, builtInRegistry(), "doctor")
	if got := strings.Count(out, "Development build: tools are not built in; build with make build"); got != 1 {
		t.Errorf("the development-build line appears %d times:\n%s", got, out)
	}
	if !strings.Contains(out, "Needs attention: lambe is not available in this development build.") {
		t.Errorf("%s", out)
	}
}

func TestVersionSaysTheBuiltInToolsLine(t *testing.T) {
	out := runBuild(t, true, builtInRegistry(), "version")
	if !strings.Contains(out, "Built-in tools: 4 of 5 ready; 1 unusable (lambe)") || !strings.Contains(out, "lambe      unavailable") || strings.Contains(out, "bundled copy ignored") {
		t.Errorf("%s", out)
	}
	js := runBuild(t, true, builtInRegistry(), "version", "--json")
	var d struct {
		SchemaVersion int `json:"schema_version"`
		Bundled       struct {
			Total, Verified int
			Unusable        []string
			Embedded        bool
		}
	}
	if err := json.Unmarshal([]byte(js), &d); err != nil || d.SchemaVersion != 1 || d.Bundled.Total != 5 || d.Bundled.Verified != 4 || len(d.Bundled.Unusable) != 1 || !d.Bundled.Embedded {
		t.Errorf("%v %+v\n%s", err, d, js)
	}
}
