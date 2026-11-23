package taskview

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

func iss(id, status string, prio int, deps ...core.TaskDep) core.TaskIssue {
	return core.TaskIssue{ID: id, Title: "title " + id, Status: status, Priority: prio, Deps: deps, CreatedAt: fixedNow.Add(-72 * time.Hour), UpdatedAt: fixedNow.Add(-time.Hour)}
}

func blk(to string) core.TaskDep    { return core.TaskDep{DependsOn: to, Type: "blocks"} }
func parent(to string) core.TaskDep { return core.TaskDep{DependsOn: to, Type: "parent-child"} }

func view(issues ...core.TaskIssue) *View {
	return Build(core.TaskSnapshot{Issues: issues, ReadAt: fixedNow}, fixedNow)
}

func getRow(t *testing.T, v *View, id string) core.TaskRow {
	t.Helper()
	r, ok := v.Row(id)
	if !ok {
		t.Fatalf("no row %s", id)
	}
	return r
}

func TestDeriveBlockedAndReady(t *testing.T) {
	deferred := iss("deferred", "open", 2)
	deferred.DeferUntil = fixedNow.Add(24 * time.Hour)
	pastDefer := iss("pastdefer", "open", 2)
	pastDefer.DeferUntil = fixedNow.Add(-time.Hour)
	v := view(
		iss("closed1", "closed", 1),
		iss("open1", "open", 1),
		iss("freed", "open", 1, blk("closed1")),             // its only blocker is finished
		iss("held", "open", 1, blk("open1")),                // held by an open task
		iss("held2", "open", 1, blk("open1"), blk("freed")), // two unfinished blockers
		iss("wip", "in_progress", 1),
		iss("stored", "blocked", 1),             // stored blocked, nothing in the way
		iss("ghost", "open", 1, blk("nowhere")), // blocker not in this tracker: never blocks
		iss("related", "open", 1, core.TaskDep{DependsOn: "open1", Type: "related"}),
		iss("unknowntype", "open", 1, core.TaskDep{DependsOn: "open1", Type: "something-new"}),
		iss("waits", "open", 1, core.TaskDep{DependsOn: "open1", Type: "waits-for"}),
		deferred, pastDefer,
	)
	type want struct {
		blockedBy, blocks int
		blocked, ready    bool
	}
	for id, w := range map[string]want{
		"closed1":     {0, 1, false, false}, // closed is never ready; blocks 1: "freed" is open and listed it
		"open1":       {0, 2, false, true},  // blocks held, held2
		"freed":       {0, 1, false, true},  // blocks held2
		"held":        {1, 0, true, false},
		"held2":       {2, 0, true, false},
		"wip":         {0, 0, false, false}, // in progress is not "ready" (bd ready agrees)
		"stored":      {0, 0, false, false}, // the stored status is not the derived state
		"ghost":       {0, 0, false, true},
		"related":     {0, 0, false, true},
		"unknowntype": {0, 0, false, true},
		"waits":       {0, 0, false, true},
		"deferred":    {0, 0, false, false},
		"pastdefer":   {0, 0, false, true},
	} {
		r := getRow(t, v, id)
		if got := (want{r.BlockedBy, r.Blocks, r.Blocked, r.Ready}); got != w {
			t.Errorf("%s: %+v, want %+v", id, got, w)
		}
	}
	if v.Ready() != 7 {
		t.Errorf("ready total = %d", v.Ready())
	}
}

func TestBlockerClosingFreesTheTask(t *testing.T) {
	before := view(iss("a", "open", 1), iss("b", "open", 1, blk("a")))
	after := view(iss("a", "closed", 1), iss("b", "open", 1, blk("a")))
	if !getRow(t, before, "b").Blocked || getRow(t, before, "b").Ready {
		t.Error("b is held while a is open")
	}
	if getRow(t, after, "b").Blocked || !getRow(t, after, "b").Ready {
		t.Error("b is ready once a is closed")
	}
}

func TestParentsChildrenAndProgress(t *testing.T) {
	v := view(
		iss("epic", "open", 1),
		iss("c1", "closed", 1, parent("epic")),
		iss("c2", "open", 1, parent("epic")),
		iss("c3", "in_progress", 1, parent("epic")),
		iss("orphan", "open", 1, parent("missing-parent")),
		iss("selfparent", "open", 1, parent("selfparent")),
		iss("cycA", "open", 1, parent("cycB")), iss("cycB", "open", 1, parent("cycA")),
	)
	e := getRow(t, v, "epic")
	if e.ChildTotal != 3 || e.ChildDone != 1 || e.Parent != "" {
		t.Errorf("epic: %+v", e)
	}
	if c := getRow(t, v, "c2"); c.Parent != "epic" {
		t.Errorf("c2: %+v", c)
	}
	if o := getRow(t, v, "orphan"); o.Parent != "missing-parent" || o.ChildTotal != 0 {
		t.Errorf("orphan keeps the parent id it names: %+v", o)
	}
	d, _ := v.Detail("orphan")
	if d.Parent == nil || d.Parent.Status != "missing" || d.Parent.ID != "missing-parent" {
		t.Errorf("an unknown parent is shown as missing: %+v", d.Parent)
	}
	getRow(t, v, "selfparent") // a parent cycle must not hang or panic
	getRow(t, v, "cycA")
}

func TestAgeLabels(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "now"}, {5 * time.Minute, "5m"}, {59 * time.Minute, "59m"}, {time.Hour, "1h"}, {23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"}, {99 * 24 * time.Hour, "99d"}, {100 * 24 * time.Hour, "3mo"}, {364 * 24 * time.Hour, "12mo"}, {800 * 24 * time.Hour, "2y"},
	} {
		if got := Age(fixedNow.Add(-c.d), fixedNow); got != c.want {
			t.Errorf("%v = %q, want %q", c.d, got, c.want)
		}
	}
	if Age(time.Time{}, fixedNow) != "" || Age(fixedNow.Add(time.Hour), fixedNow) != "" {
		t.Error("no age for an unknown or future time")
	}
}

// ---- sort ----

func sortedIDs(v *View, s Sort) []string {
	return ids(v.List(ListOptions{Preset: PresetAll, Sort: s}).Rows)
}

func TestSortEveryKeyAndDirection(t *testing.T) {
	mk := func(id string, mod func(*core.TaskIssue)) core.TaskIssue { i := iss(id, "open", 2); mod(&i); return i }
	t0 := fixedNow
	v := view(
		mk("c", func(i *core.TaskIssue) {
			i.Priority, i.Title, i.Type, i.Status = 2, "Banana", "task", "closed"
			i.CreatedAt = t0.Add(-1 * time.Hour)
			i.UpdatedAt = t0.Add(-3 * time.Hour)
		}),
		mk("a", func(i *core.TaskIssue) {
			i.Priority, i.Title, i.Type, i.Status = 0, "cherry", "bug", "in_progress"
			i.CreatedAt = t0.Add(-3 * time.Hour)
			i.UpdatedAt = t0.Add(-1 * time.Hour)
			i.DeferUntil = t0.Add(48 * time.Hour)
		}),
		mk("b", func(i *core.TaskIssue) {
			i.Priority, i.Title, i.Type, i.Status = 4, "apple", "epic", "open"
			i.CreatedAt = t0.Add(-2 * time.Hour)
			i.UpdatedAt = t0.Add(-2 * time.Hour)
			i.DeferUntil = t0.Add(24 * time.Hour)
		}),
		mk("d", func(i *core.TaskIssue) {
			i.Priority, i.Title, i.Type, i.Status = 2, "Apple pie", "task", "blocked"
			i.CreatedAt = time.Time{}
			i.UpdatedAt = time.Time{}
			i.Deps = []core.TaskDep{blk("a")}
		}),
	)
	for _, c := range []struct {
		s    Sort
		want string
	}{
		{Sort{SortPriority, false}, "a c d b"}, // P0 first; c and d tie on 2 and break by id
		{Sort{SortPriority, true}, "b c d a"},  // the tie still breaks by id ascending
		{Sort{SortCreated, false}, "a b c d"},  // oldest first, d (unknown) last
		{Sort{SortCreated, true}, "c b a d"},   // newest first, d (unknown) still last
		{Sort{SortUpdated, false}, "c b a d"},
		{Sort{SortUpdated, true}, "a b c d"},
		{Sort{SortTitle, false}, "b d c a"}, // case-insensitive: apple, Apple pie, Banana, cherry
		{Sort{SortTitle, true}, "a c d b"},
		{Sort{SortStatus, false}, "b a d c"}, // open, in_progress, blocked, closed
		{Sort{SortStatus, true}, "c d a b"},
		{Sort{SortType, false}, "a b c d"}, // bug, epic, task, task
		{Sort{SortType, true}, "c d b a"},
		{Sort{SortDeps, false}, "b c a d"}, // fewest blockers, then fewest dependents: b and c (0,0), a blocks d (0,1), d is held by a (1,0)
		{Sort{SortDeps, true}, "d a b c"},
		{Sort{SortDeferred, false}, "b a c d"}, // soonest first; no deferral last
		{Sort{SortDeferred, true}, "a b c d"},  // furthest first; no deferral still last
	} {
		if got := strings.Join(sortedIDs(v, c.s), " "); got != c.want {
			t.Errorf("sort %s desc=%v = %q, want %q", c.s.Key, c.s.Desc, got, c.want)
		}
	}
}

func TestSortIsStableUnderShuffledInput(t *testing.T) {
	var issues []core.TaskIssue
	for i := 0; i < 60; i++ {
		issues = append(issues, iss(fmt.Sprintf("t-%02d", i), "open", i%3)) // many ties
	}
	want := sortedIDs(view(issues...), DefaultSort())
	r := rand.New(rand.NewSource(1))
	for n := 0; n < 20; n++ {
		r.Shuffle(len(issues), func(i, j int) { issues[i], issues[j] = issues[j], issues[i] })
		for _, k := range SortKeys {
			s := Sort{Key: k, Desc: n%2 == 0}
			a, b := sortedIDs(view(issues...), s), sortedIDs(view(slices.Clone(issues)...), s)
			if !slices.Equal(a, b) {
				t.Fatalf("sort %v is not deterministic", s)
			}
		}
		if got := sortedIDs(view(issues...), DefaultSort()); !slices.Equal(got, want) {
			t.Fatalf("input order changed the default order:\n%v\n%v", got, want)
		}
	}
}

func TestSortCycleAndReverse(t *testing.T) {
	s := DefaultSort()
	seen := map[SortKey]bool{}
	for i := 0; i < len(SortKeys); i++ {
		seen[s.Key] = true
		s = s.Next()
		if s.Desc != s.Key.DefaultDesc() {
			t.Errorf("%s starts in its natural direction", s.Key)
		}
	}
	if len(seen) != 8 || s.Key != SortPriority {
		t.Errorf("the cycle visits all eight keys and comes back: %v %v", seen, s)
	}
	if r := s.Reverse(); r.Key != s.Key || r.Desc == s.Desc {
		t.Errorf("reverse: %+v", r)
	}
	if (Sort{Key: "nope"}).Next() != DefaultSort() || SortKey("nope").Valid() || !SortTitle.Valid() {
		t.Error("an unknown key falls back to the default")
	}
}

// ---- list ----

func TestPresetsAndQueryOverTheRealExport(t *testing.T) {
	v := realView(t)
	list := func(preset Preset, q string) []string {
		pq, err := query.Parse(q)
		if err != nil {
			t.Fatal(err)
		}
		return ids(v.List(ListOptions{Preset: preset, Query: pq, Sort: Sort{Key: SortTitle}}).Rows)
	}
	for _, c := range []struct {
		preset Preset
		q      string
		want   string
	}{
		{PresetAll, "", "tk-wiq tk-1rz tk-x5n tk-0at.3 tk-tbr tk-0at.2 tk-o4w tk-0at.1 tk-0at"},
		{PresetOpen, "", "tk-wiq tk-1rz tk-x5n tk-0at.3 tk-tbr tk-0at.2 tk-o4w tk-0at"},
		{PresetClosed, "", "tk-0at.1"},
		{PresetReady, "", "tk-1rz tk-0at.2 tk-o4w tk-0at"},
		{PresetBlocked, "", "tk-wiq tk-0at.3"}, // stored blocked, and held by tk-0at.2
		{PresetOpen, "blocked:true", "tk-0at.3"},
		{PresetAll, "is:ready label:ui", "tk-0at.2"},
		{PresetAll, "assignee:ada", "tk-tbr"},
		{PresetAll, "who:none priority<=1", "tk-0at.1 tk-0at"},
		{PresetAll, "has:comments", "tk-0at.2"},
		{PresetAll, "has:children", "tk-0at"},
		{PresetAll, "has:parent type:feature", "tk-0at.3 tk-0at.2"},
		{PresetAll, "parent:tk-0at status:open", "tk-0at.3 tk-0at.2"},
		{PresetAll, "type:bug,epic", "tk-1rz tk-tbr tk-0at"},
		{PresetAll, "heartbeat", "tk-tbr"},
		{PresetAll, "label:later", "tk-0at.3"},
		{PresetAll, "!label:ui !status:closed", "tk-wiq tk-1rz tk-x5n tk-tbr tk-o4w tk-0at"},
		{PresetAll, "updated>1d", "tk-wiq tk-1rz tk-x5n tk-0at.3 tk-tbr tk-0at.2 tk-o4w tk-0at.1 tk-0at"},
		{PresetAll, "created<2026-11-23", ""},
		{PresetAll, "nosuchword", ""},
	} {
		if got := strings.Join(list(c.preset, c.q), " "); got != c.want {
			t.Errorf("%s %q = %q, want %q", c.preset, c.q, got, c.want)
		}
	}
	rep := v.List(ListOptions{})
	if rep.Preset != "open" || rep.Sort != "priority" || rep.Matched != 8 || rep.Total != 9 || rep.Ready != 4 || rep.Tracker != "tracker" || rep.Header.Domain != core.DomainTasks {
		t.Errorf("default report: %+v", rep)
	}
}

// bd ready is the reference for "ready": every task this reader calls ready is in its answer,
// and nothing else is (the two differ by design only for parent gates, which the fixture lacks).
func TestReadyAgreesWithBdReady(t *testing.T) {
	var bdReady []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(fixture(t, "ready.json"), &bdReady); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, r := range bdReady {
		want = append(want, r.ID)
	}
	var got []string
	for _, r := range realView(t).List(ListOptions{Preset: PresetReady}).Rows {
		got = append(got, r.ID)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("ready: this reader %v, bd ready %v", got, want)
	}
}

func TestListIgnoresInputOrderOfTheExport(t *testing.T) {
	// bd's export order is not stable: a reversed export must list identically.
	lines := strings.Split(strings.TrimSpace(string(fixture(t, "export.jsonl"))), "\n")
	slices.Reverse(lines)
	s, err := Parse(strings.NewReader(strings.Join(lines, "\n")), fixedNow, "tracker")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(Build(s, fixedNow).List(ListOptions{Preset: PresetAll}))
	b, _ := json.Marshal(realView(t).List(ListOptions{Preset: PresetAll}))
	if string(a) != string(b) {
		t.Errorf("export order leaks into the list:\n%s\n%s", a, b)
	}
}

func TestDetailOverTheRealExport(t *testing.T) {
	v := realView(t)
	d, ok := v.Detail("tk-0at.2")
	if !ok {
		t.Fatal("no detail")
	}
	if d.Row.ID != "tk-0at.2" || d.Description != "Render list." || len(d.Comments) != 2 || d.Comments[0].Author != "ada" {
		t.Errorf("detail: %+v", d)
	}
	if d.Parent == nil || d.Parent.ID != "tk-0at" || d.Parent.Title != "Tasks workspace" {
		t.Errorf("parent: %+v", d.Parent)
	}
	if len(d.BlockedBy) != 1 || d.BlockedBy[0] != (core.TaskRef{ID: "tk-0at.1", Title: "Snapshot reader", Status: "closed"}) {
		t.Errorf("a finished blocker is still listed: %+v", d.BlockedBy)
	}
	if len(d.Blocks) != 1 || d.Blocks[0].ID != "tk-0at.3" {
		t.Errorf("blocks: %+v", d.Blocks)
	}
	e, _ := v.Detail("tk-0at")
	if ids2 := []string{e.Children[0].ID, e.Children[1].ID, e.Children[2].ID}; !slices.Equal(ids2, []string{"tk-0at.1", "tk-0at.2", "tk-0at.3"}) || e.Row.ChildDone != 1 || e.Row.ChildTotal != 3 {
		t.Errorf("children %v, progress %d/%d", ids2, e.Row.ChildDone, e.Row.ChildTotal)
	}
	r, _ := v.Detail("tk-o4w")
	if r.Design != "Design **notes**" || r.Notes != "Some notes" || r.Acceptance != "It works" {
		t.Errorf("rich: %+v", r)
	}
	if c, _ := v.Detail("tk-0at.1"); c.CloseReason != "done" || c.ClosedAt == "" {
		t.Errorf("closed: %+v", c)
	}
	if _, ok := v.Detail("nope"); ok {
		t.Error("an unknown id has no detail")
	}
	g := view(iss("a", "open", 1, blk("gone")))
	if gd, _ := g.Detail("a"); len(gd.BlockedBy) != 1 || gd.BlockedBy[0].Status != "missing" {
		t.Errorf("a dependency outside the tracker is shown as missing: %+v", gd.BlockedBy)
	}
}

// ---- JSON contract ----

func TestJSONContract(t *testing.T) {
	v := realView(t)
	golden(t, "list.golden.json", v.List(ListOptions{Preset: PresetAll}))
	d, _ := v.Detail("tk-0at.2")
	golden(t, "detail.golden.json", d)
	// Additive-only contract: these keys must stay, with these meanings.
	b, _ := json.Marshal(v.List(ListOptions{}))
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"domain", "title", "state", "preset", "sort", "rows", "matched", "total", "ready"} {
		if _, ok := m[k]; !ok {
			t.Errorf("list report lost the key %q", k)
		}
	}
	var rows []map[string]json.RawMessage
	_ = json.Unmarshal(m["rows"], &rows)
	for _, k := range []string{"id", "title", "status", "priority"} {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("row lost the key %q", k)
		}
	}
	// Empty optional fields are omitted, so adding a field never changes existing output.
	if _, ok := rows[0]["assignee"]; ok && string(rows[0]["assignee"]) == `""` {
		t.Error("an empty assignee must be omitted, not empty")
	}
}

// ---- performance ----

func bigView(n int) *View {
	issues := make([]core.TaskIssue, n)
	r := rand.New(rand.NewSource(7))
	statuses := []string{"open", "open", "in_progress", "blocked", "deferred", "closed"}
	for i := range issues {
		it := iss(fmt.Sprintf("p-%05d", i), statuses[r.Intn(len(statuses))], r.Intn(5))
		it.Title = fmt.Sprintf("Task number %d about lease renewal and queue %d", i, r.Intn(50))
		it.Labels = []string{fmt.Sprintf("l%d", r.Intn(20)), "common"}
		it.Assignee = []string{"", "ada", "mei"}[r.Intn(3)]
		if i > 10 && r.Intn(3) == 0 {
			it.Deps = append(it.Deps, blk(fmt.Sprintf("p-%05d", r.Intn(i))))
		}
		if i > 10 && r.Intn(5) == 0 {
			it.Deps = append(it.Deps, parent(fmt.Sprintf("p-%05d", r.Intn(i))))
		}
		issues[i] = it
	}
	return view(issues...)
}

func TestFiveThousandTasksStayFast(t *testing.T) {
	const n = 5000
	start := time.Now()
	v := bigView(n)
	build := time.Since(start)
	q, _ := query.Parse(`lease status:open,blocked !label:l3 priority<=3 updated>1d heartbeat`)
	q2, _ := query.Parse("rnw")
	start = time.Now()
	for _, k := range SortKeys {
		v.List(ListOptions{Preset: PresetAll, Sort: Sort{Key: k}})
	}
	v.List(ListOptions{Query: q})
	v.List(ListOptions{Query: q2, Preset: PresetReady})
	ops := time.Since(start)
	// Generous bounds (a slow CI machine is several times slower than a laptop): the real figures
	// are a few milliseconds per operation, and a quadratic regression is three orders above these.
	if build > 2*time.Second {
		t.Errorf("building %d tasks took %v", n, build)
	}
	if per := ops / 10; per > 500*time.Millisecond {
		t.Errorf("list/sort/filter over %d tasks took %v each", n, per)
	}
	if rep := v.List(ListOptions{Preset: PresetAll}); rep.Total != n || len(rep.Rows) != n {
		t.Fatalf("rows: %d", len(rep.Rows))
	}
	t.Logf("build %v, ten list/sort/filter operations %v", build, ops)
}
