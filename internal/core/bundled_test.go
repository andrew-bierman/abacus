package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/embedtools/embedtest"
)

// asBuild makes the process an embedded build (a fake tool set) or a development build.
func asBuild(t *testing.T, embedded bool) {
	t.Helper()
	if embedded {
		t.Cleanup(core.SetEmbeddedForTest(embedtest.Standard().FS))
	} else {
		t.Cleanup(core.SetEmbeddedForTest(nil))
	}
}

func readyEntry(id string, src core.Source) core.Entry {
	return core.Entry{Module: &modtest.Fake{IDStr: id}, Detection: core.Detection{State: core.Ready, Version: "1.0.0", Source: src, Path: "/x/" + id}}
}

func absentEntry(id, detail string) core.Entry {
	return core.Entry{Module: &modtest.Fake{IDStr: id}, Detection: core.Detection{State: core.Absent, Detail: detail}}
}

// builtIn is the four built-in tools, with bd serving Tasks so the domain is connected.
func builtIn(src core.Source) []core.Entry {
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

func idx(id string) int {
	for i, k := range core.KnownTools {
		if k == id {
			return i
		}
	}
	return -1
}

func TestAnEmbeddedBuildShowsEveryToolAsBuiltIn(t *testing.T) {
	asBuild(t, true)
	o := core.BuildOverview(context.Background(), builtIn(core.SourceEmbedded))
	if !o.Ready || o.Bundled.Line != "Built-in tools: 5 of 5 ready" || !o.Bundled.Embedded {
		t.Fatalf("%+v", o.Bundled)
	}
	for _, tl := range o.Tools {
		if tl.Label() != "built in" || tl.Where != "built-in" {
			t.Errorf("%s: label %q where %q", tl.ID, tl.Label(), tl.Where)
		}
	}
}

func TestAFailedBuiltInToolSaysWhatFailedAndTheOneFix(t *testing.T) {
	asBuild(t, true)
	es := builtIn(core.SourceEmbedded)
	es[idx("lambe")] = absentEntry("lambe", "lambe is not available (bundled copy ignored: could not write into /tools/x: permission denied)")
	o := core.BuildOverview(context.Background(), es)
	want := "The built-in lambe could not be set up: could not write into /tools/x: permission denied. Choose a folder you can write to with ABACUS_TOOLS_DIR=<folder>, then press r."
	if o.Ready || o.Headline != "Needs attention: "+want {
		t.Fatalf("%q", o.Headline)
	}
	if o.Bundled.Line != "Built-in tools: 4 of 5 ready; 1 unusable (lambe)" {
		t.Fatalf("%q", o.Bundled.Line)
	}
	for _, a := range o.Attention {
		if strings.HasPrefix(a, "Queues:") {
			t.Errorf("the Queues line repeats the lambe line: %q", a)
		}
	}
}

func TestADamagedBuiltInSaysDownloadItAgain(t *testing.T) {
	asBuild(t, true)
	es := builtIn(core.SourceEmbedded)
	es[idx("bd")] = absentEntry("bd", "bd is not available (bundled copy ignored: the built-in bd does not match what was built into abacus (its sha256 or length is different))")
	o := core.BuildOverview(context.Background(), es)
	if !strings.Contains(o.Headline, "This copy of abacus is damaged; download it again.") || strings.Contains(o.Headline, "ABACUS_TOOLS_DIR") {
		t.Fatalf("%q", o.Headline)
	}
}

func TestADevelopmentBuildSaysSoOnlyInTheSummaryLine(t *testing.T) {
	asBuild(t, false)
	es := builtIn(core.SourcePATH)
	es[idx("lambe")] = absentEntry("lambe", "lambe was not found")
	o := core.BuildOverview(context.Background(), es)
	if o.Bundled.Line != core.DevBuildLine || !strings.Contains(core.DevBuildLine, "tools are not built in; build with make build") {
		t.Fatalf("%q", o.Bundled.Line)
	}
	if o.Ready || o.Headline != "Needs attention: lambe is not available in this development build." {
		t.Fatalf("%q", o.Headline)
	}
	for _, s := range append([]string{core.HowToConnectInsights()}, core.HowToConnectFor(core.DomainQueues), core.HowToConnectFor(core.DomainTasks), core.HowToConnectFor(core.DomainBoard)) {
		if strings.Contains(s, "not built in") || strings.Contains(s, "make build") {
			t.Errorf("a domain tab never carries the development-build line: %q", s)
		}
	}
}

func TestEachBuiltInRequiredToolUnusableStillBlocksReady(t *testing.T) {
	for _, id := range []string{"bd", "lambe", "beads-lease"} {
		for _, embedded := range []bool{true, false} {
			asBuild(t, embedded)
			es := builtIn(core.SourceEmbedded)
			es[idx(id)] = absentEntry(id, id+" not found")
			if o := core.BuildOverview(context.Background(), es); o.Ready || !strings.HasPrefix(o.Headline, "Needs attention") {
				t.Errorf("%s (embedded %v) unusable must block Ready: %q", id, embedded, o.Headline)
			}
		}
	}
}

func TestTheBoardIsBuiltInButNeverARequirement(t *testing.T) {
	asBuild(t, true)
	es := builtIn(core.SourceEmbedded)
	es[idx("board")] = absentEntry("board", "board is not available (bundled copy ignored: x)")
	o := core.BuildOverview(context.Background(), es)
	if !o.Ready {
		t.Fatalf("an unusable Board never blocks Ready: %+v", o)
	}
	var tl core.ToolLine
	for _, x := range o.Tools {
		if x.ID == "board" {
			tl = x
		}
	}
	if tl.Label() != "unavailable" || !tl.Optional || !tl.Bundled {
		t.Fatalf("%+v label %q", tl, tl.Label())
	}
	found := false
	for _, a := range o.Attention {
		found = found || strings.HasPrefix(a, "The built-in board could not be set up")
	}
	if !found {
		t.Fatalf("%v", o.Attention)
	}
}

func TestBuiltInSummaryCountsReadyCopiesAndSaysWhatIsWrong(t *testing.T) {
	asBuild(t, true)
	sum := func(es []core.Entry) core.BundledSummary {
		var mods []core.ModuleReport
		for _, e := range es {
			mods = append(mods, core.ModuleReport{ID: e.Module.ID(), Detection: e.Detection})
		}
		return core.SummarizeBundled(mods)
	}
	if b := sum(builtIn(core.SourceEmbedded)); b.Line != "Built-in tools: 5 of 5 ready" || !b.Complete() || b.Verified != 5 {
		t.Fatalf("%+v", b)
	}
	es := builtIn(core.SourceEmbedded)
	es[idx("lambe")] = readyEntry("lambe", core.SourceOverride)
	if b := sum(es); b.Line != "Built-in tools: 4 of 5 ready; 1 using your own copy (lambe)" || !b.Complete() {
		t.Fatalf("%+v", b)
	}
	es[idx("beads-lease")] = absentEntry("beads-lease", "x")
	if b := sum(es); b.Complete() || b.Line != "Built-in tools: 3 of 5 ready; 1 using your own copy (lambe); 1 unusable (beads-lease)" {
		t.Fatalf("%+v", b)
	}
	if b := sum(builtIn(core.SourceDemo)); b.Line != "Built-in tools: demo data, nothing to check" || !b.Complete() {
		t.Fatalf("%+v", b)
	}
}

func TestAnOverrideIsExplainedAndStillReady(t *testing.T) {
	asBuild(t, true)
	es := builtIn(core.SourceEmbedded)
	es[idx("lambe")] = readyEntry("lambe", core.SourceOverride)
	o := core.BuildOverview(context.Background(), es)
	if !o.Ready {
		t.Fatalf("%+v", o)
	}
	for _, tl := range o.Tools {
		if tl.ID == "lambe" && !strings.Contains(tl.Note, "the copy you chose with the ABACUS_*_BIN setting") {
			t.Fatalf("%q", tl.Note)
		}
	}
}

func TestAnIncompatibleRequiredBuiltInToolIsNotReady(t *testing.T) {
	asBuild(t, true)
	es := builtIn(core.SourceEmbedded)
	es[idx("lambe")] = core.Entry{Module: &modtest.Fake{IDStr: "lambe"}, Detection: core.Detection{State: core.Incompatible, Detail: "lambe 9.9 is too new"}}
	o := core.BuildOverview(context.Background(), es)
	if o.Ready || o.Bundled.Complete() {
		t.Fatalf("%+v", o)
	}
}

func TestTheVersionJSONCarriesTheBuiltInSummaryWithoutBumpingTheSchema(t *testing.T) {
	asBuild(t, true)
	r := core.VersionReport("1.2.3", builtIn(core.SourceEmbedded))
	b, _ := json.Marshal(r)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	bundled, _ := m["bundled"].(map[string]any)
	if m["schema_version"] != float64(1) || bundled["line"] != "Built-in tools: 5 of 5 ready" || bundled["verified"] != float64(5) || bundled["embedded"] != true {
		t.Fatalf("%v", m)
	}
}
