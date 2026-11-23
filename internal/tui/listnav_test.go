package tui

import "testing"

var abc = []string{"a", "b", "c"}

func TestListNavMovesAndClamps(t *testing.T) {
	var n listNav
	n.up(abc)
	if n.cursor != 0 || n.selected != "a" {
		t.Fatalf("up at the top stays on the first row: %+v", n)
	}
	n.down(abc)
	n.down(abc)
	n.down(abc) // past the end
	if n.cursor != 2 || n.selected != "c" {
		t.Fatalf("down stops on the last row and remembers it: %+v", n)
	}
	n.up(abc)
	if n.cursor != 1 || n.selected != "b" {
		t.Fatalf("up moves one row and remembers it: %+v", n)
	}
}

func TestListNavEmptyList(t *testing.T) {
	n := listNav{selected: "keep"}
	n.down(nil)
	n.up(nil)
	if n.cursor != 0 || n.selected != "keep" {
		t.Fatalf("with no rows the cursor stays 0 and the selection is untouched: %+v", n)
	}
}

func TestListNavRemember(t *testing.T) {
	n := listNav{cursor: 1}
	n.remember(abc)
	if n.selected != "b" {
		t.Fatalf("remember records the row under the cursor: %q", n.selected)
	}
	n.cursor = 5
	n.remember(abc)
	if n.selected != "b" {
		t.Fatalf("a cursor past the end leaves the selection alone: %q", n.selected)
	}
}

func TestListNavReanchorFindsSelectedRow(t *testing.T) {
	for _, follow := range []bool{false, true} {
		n := listNav{cursor: 0, selected: "c"}
		n.reanchor([]string{"x", "c", "y"}, follow)
		if n.cursor != 1 || n.selected != "c" {
			t.Errorf("follow=%v: the cursor follows the selected row to its new index: %+v", follow, n)
		}
	}
}

func TestListNavReanchorPinsWhenRowVanishes(t *testing.T) {
	n := listNav{cursor: 2, selected: "c"}
	n.reanchor([]string{"a", "b"}, false)
	if n.cursor != 1 {
		t.Errorf("the cursor clamps to the last row: %d", n.cursor)
	}
	if n.selected != "c" {
		t.Errorf("pinned: the selection stays on the vanished row: %q", n.selected)
	}
	n.reanchor([]string{"a", "b", "c"}, false)
	if n.cursor != 2 {
		t.Errorf("the vanished row comes back and is selected again: %d", n.cursor)
	}
}

func TestListNavReanchorFollowsWhenRowVanishes(t *testing.T) {
	n := listNav{cursor: 2, selected: "c"}
	n.reanchor([]string{"a", "b"}, true)
	if n.cursor != 1 || n.selected != "b" {
		t.Errorf("follow: the clamped row becomes the selection: %+v", n)
	}
	n.reanchor([]string{"a", "b", "c"}, true)
	if n.cursor != 1 {
		t.Errorf("follow: the old row coming back does not steal the selection: %d", n.cursor)
	}
}

func TestListNavReanchorFirstSelection(t *testing.T) {
	for _, follow := range []bool{false, true} {
		n := listNav{cursor: 1}
		n.reanchor(abc, follow)
		if n.selected != "b" {
			t.Errorf("follow=%v: with no selection yet, the row under the cursor is chosen: %q", follow, n.selected)
		}
	}
	n := listNav{cursor: 3, selected: "gone"}
	n.reanchor(nil, false)
	if n.cursor != 0 || n.selected != "gone" {
		t.Errorf("an empty list clamps the cursor and keeps a pinned selection: %+v", n)
	}
	n = listNav{cursor: 3, selected: "gone"}
	n.reanchor(nil, true)
	if n.cursor != 0 || n.selected != "gone" {
		t.Errorf("an empty list has no row to follow: %+v", n)
	}
}

func TestListNavEnterOnlyWhenStackedWithRows(t *testing.T) {
	var n listNav
	n.enter(bpWide-1, 3)
	if !n.drilled {
		t.Error("stacked with rows: enter drills")
	}
	n = listNav{}
	n.enter(bpWide, 3)
	if n.drilled {
		t.Error("side by side: there is nothing to drill into")
	}
	n = listNav{}
	n.enter(bpWide-1, 0)
	if n.drilled {
		t.Error("no rows: nothing to drill into")
	}
	n = listNav{drilled: true}
	n.enter(bpWide, 3)
	if n.drilled {
		t.Error("enter recomputes the drill from the layout")
	}
}

func TestListNavBackReportsWhetherItCleared(t *testing.T) {
	n := listNav{drilled: true}
	if !n.back() || n.drilled {
		t.Error("back from the detail clears it and says so")
	}
	if n.back() {
		t.Error("back with nothing to leave reports false, so the section can peel the next layer")
	}
}

func TestListNavFitAndSettle(t *testing.T) {
	n := listNav{drilled: true}
	n.fit(bpWide - 1)
	if !n.drilled {
		t.Error("still stacked: the drill stays")
	}
	n.fit(bpWide)
	if n.drilled {
		t.Error("widened to side by side: the drill is dropped")
	}
	n = listNav{drilled: true}
	n.settle(1)
	if !n.drilled {
		t.Error("rows remain: the drill stays")
	}
	n.settle(0)
	if n.drilled {
		t.Error("no rows left: the drill is dropped")
	}
	n = listNav{}
	n.fit(bpWide - 1)
	n.settle(3)
	if n.drilled {
		t.Error("fit and settle never start a drill")
	}
}

func TestListNavBindings(t *testing.T) {
	var n listNav
	enter, back := n.bindings(bpWide-1, 2)
	if !enter.Enabled() || back.Enabled() {
		t.Errorf("stacked, not drilled: enter on, back off (enter=%v back=%v)", enter.Enabled(), back.Enabled())
	}
	if enter, _ := n.bindings(bpWide, 2); enter.Enabled() {
		t.Error("side by side: enter is not offered")
	}
	if enter, _ := n.bindings(bpWide-1, 0); enter.Enabled() {
		t.Error("no rows: enter is not offered")
	}
	n.drilled = true
	enter, back = n.bindings(bpWide-1, 2)
	if enter.Enabled() || !back.Enabled() {
		t.Errorf("drilled: enter off, back on (enter=%v back=%v)", enter.Enabled(), back.Enabled())
	}
}

// No selection yet is not the selection "": a row whose id is empty must not capture the cursor.
func TestListNavReanchorEmptySelectionMatchesNothing(t *testing.T) {
	n := listNav{cursor: 1}
	n.reanchor([]string{"", "x"}, false)
	if n.cursor != 1 || n.selected != "x" {
		t.Fatalf("an unset selection is not matched against an empty id: %+v", n)
	}
}

// The package-level bindings must not be mutated: bindings hands out copies.
func TestListNavBindingsDoNotMutateTheGlobals(t *testing.T) {
	var n listNav
	n.bindings(bpWide, 0)
	n.drilled = true
	n.bindings(bpWide-1, 3)
	if !keyEnter.Enabled() || !keyBack.Enabled() {
		t.Error("keyEnter and keyBack stay enabled; only copies are disabled")
	}
}
