package taskview

import (
	"slices"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestSinceNamesNewChangedAndRemovedRowsSorted(t *testing.T) {
	old := view(iss("a", "open", 2), iss("b", "open", 2), iss("c", "open", 2), iss("z", "open", 2))
	edited := iss("b", "open", 2)
	edited.Title = "retitled"
	cur := view(edited, iss("a", "open", 2), iss("c", "open", 2), iss("n", "open", 2))
	changed, removed := cur.Since(old.State(), cur.State())
	if got := ids(changed); !slices.Equal(got, []string{"b", "n"}) {
		t.Errorf("changed %v", got)
	}
	if !slices.Equal(removed, []string{"z"}) {
		t.Errorf("removed %v", removed)
	}
	// nothing differs: both lists are empty, not nil (they encode as [])
	c2, r2 := old.Since(old.State(), old.State())
	if c2 == nil || r2 == nil || len(c2)+len(r2) != 0 {
		t.Errorf("%v %v", c2, r2)
	}
	// removed ids are sorted
	_, rm := view(iss("a", "open", 2)).Since(view(iss("z", "open", 2), iss("m", "open", 2), iss("a", "open", 2)).State(), view(iss("a", "open", 2)).State())
	if !slices.Equal(rm, []string{"m", "z"}) {
		t.Errorf("removed %v", rm)
	}
}

// The derived row counts: closing a blocker changes the row of the task it held, though that task's own issue
// did not change.
func TestSinceCountsDerivedRowChanges(t *testing.T) {
	before := view(iss("blocker", "open", 2), iss("held", "open", 2, blk("blocker")))
	after := view(iss("blocker", "closed", 2), iss("held", "open", 2, blk("blocker")))
	changed, _ := after.Since(before.State(), after.State())
	if got := ids(changed); !slices.Equal(got, []string{"blocker", "held"}) {
		t.Fatalf("changed %v", got)
	}
}

func TestStateFingerprint(t *testing.T) {
	a, b := iss("a", "open", 2), iss("b", "open", 2)
	f := view(a, b).State().Fingerprint()
	if len(f) != 32 || f != view(b, a).State().Fingerprint() { // an order of the export is not a change
		t.Errorf("fingerprint %q is not stable under order", f)
	}
	if f == view(a).State().Fingerprint() || f == view(a, iss("b", "open", 3)).State().Fingerprint() {
		t.Error("a changed state has the same fingerprint")
	}
	// age moves with the clock, not the task
	later := Build(core.TaskSnapshot{Issues: []core.TaskIssue{a, b}, ReadAt: fixedNow}, fixedNow.Add(5*time.Hour))
	if later.State().Fingerprint() != f {
		t.Error("the clock changed the fingerprint")
	}
	// a stored field the row does not show still counts (the detail the client may have cached)
	d := a
	d.Description = "more"
	if view(d, b).State().Fingerprint() == f {
		t.Error("a description change was not seen")
	}
}
