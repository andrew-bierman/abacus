package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

func asBuild(t *testing.T, version string) {
	t.Helper()
	core.SetBuildVersion(version)
	t.Cleanup(func() { core.SetBuildVersion("1.0.0") })
}

func readyEntry(id string, src core.Source) core.Entry {
	return core.Entry{Module: &modtest.Fake{IDStr: id}, Detection: core.Detection{State: core.Ready, Version: "1.0.0", Source: src, Path: "/x/" + id}}
}

func absentEntry(id, detail string) core.Entry {
	return core.Entry{Module: &modtest.Fake{IDStr: id}, Detection: core.Detection{State: core.Absent, Detail: detail}}
}

// fourBundled is the four tools abacus ships, with bd serving Tasks so the domain is connected.
func fourBundled(src core.Source) []core.Entry {
	var es []core.Entry
	for _, id := range core.KnownTools {
		e := readyEntry(id, src)
		if id == "bd" {
			e.Module = tasksMod{Fake: &modtest.Fake{IDStr: "bd"}, rep: core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 1}}}
		}
		es = append(es, e)
	}
	return es
}

func TestABundledToolThatIsGoneIsAbacusProblemInAReleaseNotMissing(t *testing.T) {
	asBuild(t, "1.2.3")
	es := fourBundled(core.SourceBundled)
	es[2] = absentEntry("lambe", "lambe not found on PATH")
	o := core.BuildOverview(context.Background(), es)
	if o.Ready {
		t.Fatalf("an unusable bundled tool is not Ready: %+v", o)
	}
	want := "abacus's own copy of lambe is damaged or was moved. Re-extract the download and keep the folder together."
	if o.Headline != "Needs attention: "+want {
		t.Fatalf("Doctor must lead with what is wrong with abacus's copy: %q", o.Headline)
	}
	var tl core.ToolLine
	for _, x := range o.Tools {
		if x.ID == "lambe" {
			tl = x
		}
	}
	if tl.Label() != "unavailable" || tl.Note != want || tl.Where != "missing" {
		t.Fatalf("label %q note %q where %q", tl.Label(), tl.Note, tl.Where)
	}
	var shown []string
	for _, a := range o.Attention {
		if !strings.HasPrefix(a, "Board:") { // the optional Board line is allowed to say how to turn it on
			shown = append(shown, a)
		}
	}
	for _, s := range append(append([]string{o.Headline, tl.Label(), tl.Note, o.Bundled.Line}, shown...), core.HowToConnect[core.DomainQueues]) {
		if strings.Contains(strings.ToLower(s), "missing") || strings.Contains(s, "install") {
			t.Errorf("a release user must not be told a bundled tool is missing or to install it: %q", s)
		}
	}
	for _, a := range o.Attention {
		if strings.HasPrefix(a, "Queues:") {
			t.Errorf("the Queues line repeats the lambe line: %q", a)
		}
	}
	if o.Bundled.Line != "Bundled tools: 3 of 4 verified; 1 unusable (lambe)" {
		t.Fatalf("%q", o.Bundled.Line)
	}
}

func TestARefusedBundledToolSaysWhyInAReleaseAndWhatToDo(t *testing.T) {
	asBuild(t, "1.2.3")
	es := fourBundled(core.SourceBundled)
	es[1] = absentEntry("bv", "bv not found on PATH (bundled copy ignored: world-writable (/app/bundled/bv))")
	o := core.BuildOverview(context.Background(), es)
	want := "abacus's own copy of bv cannot be used (world-writable (/app/bundled/bv)). Re-extract the download and keep the folder together."
	if !strings.HasPrefix(o.Headline, "Needs attention: ") || !strings.Contains(o.Headline, want) {
		t.Fatalf("%q", o.Headline)
	}
}

func TestADevBuildSaysRunDevBundleEverywhere(t *testing.T) {
	asBuild(t, "dev")
	if !core.IsDevBuild() {
		t.Fatal("dev must read as a dev build")
	}
	es := fourBundled(core.SourceBundled)
	for i, id := range core.KnownTools {
		es[i] = absentEntry(id, id+" not found on PATH")
	}
	o := core.BuildOverview(context.Background(), es)
	if o.Ready || !strings.Contains(o.Headline, "scripts/dev-bundle.sh") || !strings.Contains(o.Headline, "bd, bv, lambe, beads-lease") {
		t.Fatalf("%q", o.Headline)
	}
	for id, s := range map[string]string{"tasks": core.HowToConnect[core.DomainTasks], "leases": core.HowToConnect[core.DomainLeases],
		"queues": core.HowToConnect[core.DomainQueues], "insights": core.HowToConnectInsights} {
		if !strings.Contains(s, "scripts/dev-bundle.sh") || strings.Contains(s, "download") {
			t.Errorf("%s: %q", id, s)
		}
	}
	// Setting a release version flips every text back.
	core.SetBuildVersion("0.4.0")
	if core.IsDevBuild() || !strings.Contains(core.HowToConnect[core.DomainTasks], "Re-extract the download") {
		t.Fatalf("%q", core.HowToConnect[core.DomainTasks])
	}
}

func TestAnOptionalToolIsOptionalNeverARequirement(t *testing.T) {
	asBuild(t, "1.2.3")
	es := append(fourBundled(core.SourceBundled), absentEntry("board", "the Vercel Board CLI (board or vb) was not found on PATH"))
	o := core.BuildOverview(context.Background(), es)
	if !o.Ready {
		t.Fatalf("an absent optional tool never blocks Ready: %+v", o)
	}
	var tl core.ToolLine
	for _, x := range o.Tools {
		if x.ID == "board" {
			tl = x
		}
	}
	if tl.Label() != "optional" || !tl.Optional || tl.Bundled || !strings.Contains(tl.Note, "adds the Board tab") {
		t.Fatalf("%+v label %q", tl, tl.Label())
	}
	if !strings.HasPrefix(core.HowToConnect[core.DomainBoard], "The Board tab is optional") {
		t.Fatalf("%q", core.HowToConnect[core.DomainBoard])
	}
}

func TestBundledSummaryCountsVerifiedCopiesAndSaysWhatIsWrong(t *testing.T) {
	asBuild(t, "1.2.3")
	sum := func(es []core.Entry) core.BundledSummary {
		var mods []core.ModuleReport
		for _, e := range es {
			mods = append(mods, core.ModuleReport{ID: e.Module.ID(), Detection: e.Detection})
		}
		return core.SummarizeBundled(mods)
	}
	if b := sum(fourBundled(core.SourceBundled)); b.Line != "Bundled tools: 4 of 4 verified" || !b.Complete() || b.Verified != 4 {
		t.Fatalf("%+v", b)
	}
	es := fourBundled(core.SourceBundled)
	es[1] = readyEntry("bv", core.SourcePATH)
	if b := sum(es); b.Line != "Bundled tools: 3 of 4 verified; 1 using your own copy (bv)" || !b.Complete() {
		t.Fatalf("%+v", b)
	}
	es[3] = absentEntry("beads-lease", "x")
	if b := sum(es); b.Complete() || b.Line != "Bundled tools: 2 of 4 verified; 1 using your own copy (bv); 1 unusable (beads-lease)" {
		t.Fatalf("%+v", b)
	}
	if b := sum(fourBundled(core.SourceDemo)); b.Line != "Bundled tools: demo data, nothing to verify" || !b.Complete() {
		t.Fatalf("%+v", b)
	}
	// Nothing usable is not "demo data".
	none := []core.Entry{absentEntry("bd", "x"), absentEntry("bv", "x")}
	if b := sum(none); b.Line != "Bundled tools: 0 of 2 verified; 2 unusable (bd, bv)" {
		t.Fatalf("%+v", b)
	}
	// Tools that are not bundled never count.
	if b := sum([]core.Entry{readyEntry("board", core.SourcePATH)}); b.Total != 0 {
		t.Fatalf("%+v", b)
	}
}

func TestAPathCopyInPlaceOfTheBundledOneIsExplainedAndStillReady(t *testing.T) {
	asBuild(t, "1.2.3")
	es := fourBundled(core.SourceBundled)
	es[1] = readyEntry("bv", core.SourcePATH)
	o := core.BuildOverview(context.Background(), es)
	if !o.Ready {
		t.Fatalf("a usable PATH copy is still usable: %+v", o)
	}
	for _, tl := range o.Tools {
		if tl.ID == "bv" && !strings.Contains(tl.Note, "abacus's own copy was not found next to abacus; using the bv on your PATH") {
			t.Fatalf("%q", tl.Note)
		}
	}
}

func TestAnIncompatibleBundledToolIsNotReady(t *testing.T) {
	asBuild(t, "1.2.3")
	es := fourBundled(core.SourceBundled)
	es[1] = core.Entry{Module: &modtest.Fake{IDStr: "bv"}, Detection: core.Detection{State: core.Incompatible, Detail: "bv 9.9 is too new"}}
	o := core.BuildOverview(context.Background(), es)
	if o.Ready || o.Bundled.Complete() || o.Bundled.Line != "Bundled tools: 3 of 4 verified; 1 unusable (bv)" {
		t.Fatalf("Ready needs all four bundled tools usable: %+v", o)
	}
}

func TestTheOverviewAndVersionJSONAddBundledWithoutBumpingTheSchema(t *testing.T) {
	asBuild(t, "1.2.3")
	r := core.VersionReport("1.2.3", fourBundled(core.SourceBundled))
	b, _ := json.Marshal(r)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["schema_version"] != float64(1) {
		t.Fatalf("schema_version must stay 1: %v", m["schema_version"])
	}
	bundled, _ := m["bundled"].(map[string]any)
	if bundled["line"] != "Bundled tools: 4 of 4 verified" || bundled["verified"] != float64(4) || bundled["total"] != float64(4) {
		t.Fatalf("%v", m["bundled"])
	}
}
