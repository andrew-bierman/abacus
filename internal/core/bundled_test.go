package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
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

// threeBundled is the three tools abacus ships, with bd serving Tasks so the domain is connected.
func threeBundled(src core.Source) []core.Entry {
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
	es := threeBundled(core.SourceBundled)
	es[1] = absentEntry("lambe", "lambe not found on PATH")
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
	for _, s := range append(append([]string{o.Headline, tl.Label(), tl.Note, o.Bundled.Line}, shown...), core.HowToConnectFor(core.DomainQueues)) {
		if strings.Contains(strings.ToLower(s), "missing") || strings.Contains(s, "install") {
			t.Errorf("a release user must not be told a bundled tool is missing or to install it: %q", s)
		}
	}
	for _, a := range o.Attention {
		if strings.HasPrefix(a, "Queues:") {
			t.Errorf("the Queues line repeats the lambe line: %q", a)
		}
	}
	if o.Bundled.Line != "Bundled tools: 2 of 3 verified; 1 unusable (lambe)" {
		t.Fatalf("%q", o.Bundled.Line)
	}
}

func TestARefusedBundledToolSaysWhyInAReleaseAndWhatToDo(t *testing.T) {
	asBuild(t, "1.2.3")
	es := threeBundled(core.SourceBundled)
	es[1] = absentEntry("lambe", "lambe not found on PATH (bundled copy ignored: world-writable (/app/bundled/lambe))")
	o := core.BuildOverview(context.Background(), es)
	want := "abacus's own copy of lambe cannot be used (world-writable (/app/bundled/lambe)). Re-extract the download and keep the folder together."
	if !strings.HasPrefix(o.Headline, "Needs attention: ") || !strings.Contains(o.Headline, want) {
		t.Fatalf("%q", o.Headline)
	}
}

func TestEachOtherBundledToolUnusableStillBlocksReady(t *testing.T) {
	for _, id := range []string{"bd", "lambe", "beads-lease"} {
		for _, build := range []string{"1.2.3", "dev"} {
			asBuild(t, build)
			es := threeBundled(core.SourceBundled)
			for i, k := range core.KnownTools {
				if k == id {
					es[i] = absentEntry(id, id+" not found on PATH")
				}
			}
			if o := core.BuildOverview(context.Background(), es); o.Ready || !strings.HasPrefix(o.Headline, "Needs attention") {
				t.Errorf("%s (%s) unusable must block Ready: %q", id, build, o.Headline)
			}
		}
	}
}

func TestADevBuildSaysRunDevBundleEverywhere(t *testing.T) {
	asBuild(t, "dev")
	if !core.IsDevBuild() {
		t.Fatal("dev must read as a dev build")
	}
	es := threeBundled(core.SourceBundled)
	for i, id := range core.KnownTools {
		es[i] = absentEntry(id, id+" not found on PATH")
	}
	o := core.BuildOverview(context.Background(), es)
	if o.Ready || !strings.Contains(o.Headline, "scripts/dev-bundle.sh") || !strings.Contains(o.Headline, "(bd, lambe, beads-lease)") {
		t.Fatalf("%q", o.Headline)
	}
	for id, s := range map[string]string{"tasks": core.HowToConnectFor(core.DomainTasks), "leases": core.HowToConnectFor(core.DomainLeases),
		"queues": core.HowToConnectFor(core.DomainQueues), "insights": core.HowToConnectInsights()} {
		if !strings.Contains(s, "scripts/dev-bundle.sh") || strings.Contains(s, "download") {
			t.Errorf("%s: %q", id, s)
		}
	}
	// Setting a release version flips every text back.
	core.SetBuildVersion("0.4.0")
	if core.IsDevBuild() || !strings.Contains(core.HowToConnectFor(core.DomainTasks), "Re-extract the download") {
		t.Fatalf("%q", core.HowToConnectFor(core.DomainTasks))
	}
}

func TestAnOptionalToolIsOptionalNeverARequirement(t *testing.T) {
	asBuild(t, "1.2.3")
	es := append(threeBundled(core.SourceBundled), absentEntry("board", "the Vercel Board CLI (board or vb) was not found on PATH"))
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
	if !strings.HasPrefix(core.HowToConnectFor(core.DomainBoard), "The Board tab is optional") {
		t.Fatalf("%q", core.HowToConnectFor(core.DomainBoard))
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
	if b := sum(threeBundled(core.SourceBundled)); b.Line != "Bundled tools: 3 of 3 verified" || !b.Complete() || b.Verified != 3 {
		t.Fatalf("%+v", b)
	}
	es := threeBundled(core.SourceBundled)
	es[1] = readyEntry("lambe", core.SourcePATH)
	if b := sum(es); b.Line != "Bundled tools: 2 of 3 verified; 1 using your own copy (lambe)" || !b.Complete() {
		t.Fatalf("%+v", b)
	}
	es[2] = absentEntry("beads-lease", "x")
	if b := sum(es); b.Complete() || b.Line != "Bundled tools: 1 of 3 verified; 1 using your own copy (lambe); 1 unusable (beads-lease)" {
		t.Fatalf("%+v", b)
	}
	if b := sum(threeBundled(core.SourceDemo)); b.Line != "Bundled tools: demo data, nothing to verify" || !b.Complete() {
		t.Fatalf("%+v", b)
	}
	// Nothing usable is not "demo data".
	none := []core.Entry{absentEntry("bd", "x"), absentEntry("lambe", "x")}
	if b := sum(none); b.Line != "Bundled tools: 0 of 2 verified; 2 unusable (bd, lambe)" {
		t.Fatalf("%+v", b)
	}
	// Tools that are not bundled never count.
	if b := sum([]core.Entry{readyEntry("board", core.SourcePATH)}); b.Total != 0 {
		t.Fatalf("%+v", b)
	}
}

func TestAPathCopyInPlaceOfTheBundledOneIsExplainedAndStillReady(t *testing.T) {
	asBuild(t, "1.2.3")
	es := threeBundled(core.SourceBundled)
	es[1] = readyEntry("lambe", core.SourcePATH)
	o := core.BuildOverview(context.Background(), es)
	if !o.Ready {
		t.Fatalf("a usable PATH copy is still usable: %+v", o)
	}
	for _, tl := range o.Tools {
		if tl.ID == "lambe" && !strings.Contains(tl.Note, "abacus's own copy was not found next to abacus; using the lambe on your PATH") {
			t.Fatalf("%q", tl.Note)
		}
	}
}

func TestAnIncompatibleRequiredBundledToolIsNotReady(t *testing.T) {
	asBuild(t, "1.2.3")
	es := threeBundled(core.SourceBundled)
	es[1] = core.Entry{Module: &modtest.Fake{IDStr: "lambe"}, Detection: core.Detection{State: core.Incompatible, Detail: "lambe 9.9 is too new"}}
	o := core.BuildOverview(context.Background(), es)
	if o.Ready || o.Bundled.Complete() || o.Bundled.Line != "Bundled tools: 2 of 3 verified; 1 unusable (lambe)" {
		t.Fatalf("Ready needs bd, lambe and beads-lease usable: %+v", o)
	}
}

func TestTheOverviewAndVersionJSONAddBundledWithoutBumpingTheSchema(t *testing.T) {
	asBuild(t, "1.2.3")
	r := core.VersionReport("1.2.3", threeBundled(core.SourceBundled))
	b, _ := json.Marshal(r)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["schema_version"] != float64(1) {
		t.Fatalf("schema_version must stay 1: %v", m["schema_version"])
	}
	bundled, _ := m["bundled"].(map[string]any)
	if bundled["line"] != "Bundled tools: 3 of 3 verified" || bundled["verified"] != float64(3) || bundled["total"] != float64(3) {
		t.Fatalf("%v", m["bundled"])
	}
}

// TestBuildKindTextsAreSafeToReadWhileTheBuildVersionIsSet runs readers of every how-to-connect
// text against SetBuildVersion; `go test -race` fails on any shared mutable text.
func TestBuildKindTextsAreSafeToReadWhileTheBuildVersionIsSet(t *testing.T) {
	t.Cleanup(func() { core.SetBuildVersion("1.0.0") })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				for _, d := range []core.DomainID{core.DomainTasks, core.DomainLeases, core.DomainQueues, core.DomainBoard} {
					if core.HowToConnectFor(d) == "" {
						t.Errorf("no text for %s", d)
					}
				}
				if !strings.HasPrefix(core.HowToConnectInsights(), "Graph insights are computed inside abacus") {
					t.Error("insights text")
				}
			}
		}()
	}
	for n := 0; n < 200; n++ {
		core.SetBuildVersion([]string{"dev", "1.2.3"}[n%2])
	}
	wg.Wait()
}

func TestHowToConnectFollowsTheBuildKindWithoutRefreshing(t *testing.T) {
	asBuild(t, "dev")
	if !strings.Contains(core.HowToConnectFor(core.DomainQueues), "scripts/dev-bundle.sh") || !strings.Contains(core.HowToConnectInsights(), "scripts/dev-bundle.sh") {
		t.Fatalf("dev build: %q / %q", core.HowToConnectFor(core.DomainQueues), core.HowToConnectInsights())
	}
	core.SetBuildVersion("1.2.3")
	if strings.Contains(core.HowToConnectFor(core.DomainQueues), "dev-bundle") || !strings.Contains(core.HowToConnectInsights(), "Re-extract") {
		t.Fatalf("release build: %q / %q", core.HowToConnectFor(core.DomainQueues), core.HowToConnectInsights())
	}
}
