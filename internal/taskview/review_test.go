package taskview

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func edgeView(t *testing.T) *View {
	t.Helper()
	s, err := Parse(bytes.NewReader(fixture(t, "export-edges.jsonl")), fixedNow, "edges")
	if err != nil {
		t.Fatal(err)
	}
	return Build(s, fixedNow)
}

func idsOf(t *testing.T, name string) []string {
	t.Helper()
	var rs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(fixture(t, name), &rs); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	slices.Sort(out)
	return out
}

// bd 1.3.4 is the reference (see testdata/README.md for how each case behaves): the ready and the
// blocked sets this reader derives are the ones bd prints, edge cases included.
func TestReadyAndBlockedAgreeWithBdOnEveryDependencyKind(t *testing.T) {
	v := edgeView(t)
	var ready, blocked []string
	for _, r := range v.List(ListOptions{Preset: PresetAll}).Rows {
		if r.Ready {
			ready = append(ready, r.ID)
		}
		if r.Blocked {
			blocked = append(blocked, r.ID)
		}
	}
	slices.Sort(ready)
	slices.Sort(blocked)
	if want := idsOf(t, "ready-edges.json"); !slices.Equal(ready, want) {
		t.Errorf("ready: this reader %v, bd ready %v", ready, want)
	}
	if want := idsOf(t, "blocked-edges.json"); !slices.Equal(blocked, want) {
		t.Errorf("blocked: this reader %v, bd blocked %v", blocked, want)
	}
}

func TestEdgeCasesOneByOne(t *testing.T) {
	v := edgeView(t)
	for id, want := range map[string]struct{ blocked, ready bool }{
		"ed-1ax":     {true, false},  // blocks
		"ed-pmq":     {true, false},  // conditional-blocks holds like blocks
		"ed-oyx":     {false, true},  // waits-for does not hold
		"ed-dfu":     {false, true},  // related
		"ed-7fn":     {false, true},  // discovered-from
		"ed-5op":     {false, true},  // external reference never holds
		"ed-6pr":     {true, false},  // the blocked parent
		"ed-6pr.1":   {true, false},  // child of a blocked parent
		"ed-6pr.1.1": {true, false},  // and its child
		"ed-jxj.1":   {false, true},  // child of an unblocked epic
		"ed-5ui":     {false, false}, // closed with an open blocker: neither
	} {
		r := getRow(t, v, id)
		if r.Blocked != want.blocked || r.Ready != want.ready {
			t.Errorf("%s: blocked=%v ready=%v, want %v", id, r.Blocked, r.Ready, want)
		}
	}
	if r := getRow(t, v, "ed-5ui"); r.BlockedBy != 0 {
		t.Errorf("closed work is held by nothing: %+v", r)
	}
	if r := getRow(t, v, "ed-6pr.1"); r.BlockedBy != 0 || r.HeldBy != "ed-6pr" {
		t.Errorf("an inherited hold names the parent and has no direct blockers: %+v", r)
	}
	// Freeing the blocker frees the whole branch.
	s := edgeSnapshotWithBaseClosed(t)
	for _, id := range []string{"ed-6pr", "ed-6pr.1", "ed-6pr.1.1", "ed-pmq"} {
		if r := getRow(t, Build(s, fixedNow), id); r.Blocked || !r.Ready {
			t.Errorf("%s after the blocker closed: %+v", id, r)
		}
	}
}

func edgeSnapshotWithBaseClosed(t *testing.T) core.TaskSnapshot {
	t.Helper()
	s, _ := Parse(bytes.NewReader(fixture(t, "export-edges.jsonl")), fixedNow, "")
	for i := range s.Issues {
		if s.Issues[i].ID == "ed-ubk" {
			s.Issues[i].Status = "closed"
		}
	}
	return s
}

func TestAClosedTaskIsNeverInTheBlockedPreset(t *testing.T) {
	v := edgeView(t)
	for _, r := range v.List(ListOptions{Preset: PresetBlocked}).Rows {
		if r.Status == "closed" {
			t.Errorf("closed %s is in the blocked preset", r.ID)
		}
	}
}

func TestUnknownStatusesSortAfterTheKnownOnes(t *testing.T) {
	v := view(iss("a", "pinned", 1), iss("b", "closed", 1), iss("c", "hooked", 1), iss("d", "open", 1))
	if got := sortedIDs(v, Sort{Key: SortStatus}); !slices.Equal(got, []string{"d", "b", "c", "a"}) {
		t.Errorf("open, closed, then hooked and pinned by name: %v", got)
	}
}

func TestFromRecentKnowsNothingAboutDependencies(t *testing.T) {
	rec := []core.Task{{ID: "a", Title: "a", Status: "open", Priority: 1}, {ID: "b", Title: "b", Status: "closed", Priority: 2}}
	snap := FromRecent(rec, fixedNow)
	if !snap.DepsUnknown || len(snap.Issues) != 2 {
		t.Fatalf("%+v", snap)
	}
	v := Build(snap, fixedNow)
	rep := v.List(ListOptions{Preset: DefaultPreset(snap)})
	if rep.Preset != "all" || rep.Matched != 2 || !rep.DepsUnknown || rep.Ready != 0 {
		t.Errorf("a recent-changes list is all of them, with unknown dependencies: %+v", rep)
	}
	for _, r := range rep.Rows {
		if r.Ready || r.Blocked || r.BlockedBy != 0 {
			t.Errorf("nothing is claimed ready or blocked without dependencies: %+v", r)
		}
	}
	if got := PresetOpen.Next(true); got != PresetClosed {
		t.Errorf("with unknown dependencies the cycle skips ready and blocked, open goes to closed: %q", got)
	}
	if got := PresetOpen.Next(false); got != PresetReady {
		t.Errorf("with dependencies known open goes to ready: %q", got)
	}
	if DefaultPreset(core.TaskSnapshot{}) != PresetOpen {
		t.Error("a real snapshot defaults to open")
	}
	if rep := v.List(ListOptions{Preset: PresetReady}); rep.Matched != 0 {
		t.Errorf("the ready preset is empty, not everything, when dependencies are unknown: %d", rep.Matched)
	}
}

func TestCountsComeFromTheSnapshot(t *testing.T) {
	c := realView(t).Counts()
	want := core.TaskCounts{Total: 9, Open: 5, InProgress: 1, Blocked: 1, Deferred: 1, Closed: 1, Ready: 4}
	if c != want {
		t.Errorf("counts %+v, want %+v", c, want)
	}
	if c.Ready != realView(t).List(ListOptions{}).Ready {
		t.Error("the strip, the list and the counts agree")
	}
}

// A per-row Ready() scan would make a list quadratic: 20,000 tasks must stay fast.
func TestTwentyThousandTasksAreNotQuadratic(t *testing.T) {
	v := bigView(20000)
	start := time.Now()
	for i := 0; i < 5; i++ {
		v.List(ListOptions{Preset: PresetAll})
		v.Counts()
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("5 lists and counts of 20,000 tasks took %v", d)
	}
}

func TestDuplicateIdsAreCountedInTheWarnings(t *testing.T) {
	in := `{"id":"a-1","title":"old"}` + "\n" + `{"id":"a-2","title":"two"}` + "\n" + `{"id":"a-1","title":"new"}` + "\n" + `{"id":"a\u001b-2","title":"two again"}`
	s, err := Parse(bytes.NewReader([]byte(in)), fixedNow, "")
	if err != nil || len(s.Issues) != 2 {
		t.Fatalf("%v %d", err, len(s.Issues))
	}
	if len(s.Warnings) != 1 || s.Warnings[0] != "2 duplicate ids: the later record of each was used (the order of an export is not stable)" {
		t.Errorf("warnings: %q", s.Warnings)
	}
	if c, _ := Parse(bytes.NewReader(fixture(t, "export.jsonl")), fixedNow, ""); len(c.Warnings) != 0 {
		t.Errorf("a clean export has no warnings: %v", c.Warnings)
	}
}
