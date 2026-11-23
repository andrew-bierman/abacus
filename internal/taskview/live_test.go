package taskview

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func TestChangedTable(t *testing.T) {
	for _, c := range []struct {
		name, prev, cur string
		want            bool
	}{
		{"same", "abc", "abc", false},
		{"moved", "abc", "abd", true},
		{"first read after start", "", "abc", true},
		{"no fingerprint available never counts as a change", "abc", "", false},
		{"neither", "", "", false},
	} {
		if got := Changed(c.prev, c.cur); got != c.want {
			t.Errorf("%s: Changed(%q,%q) = %v, want %v", c.name, c.prev, c.cur, got, c.want)
		}
	}
}

func issue(id string, mod func(*core.TaskIssue)) core.TaskIssue {
	i := core.TaskIssue{ID: id, Title: "t", Status: "open", Priority: 2, Labels: []string{"a", "b"},
		Deps:      []core.TaskDep{{DependsOn: "x-1", Type: "blocks"}, {DependsOn: "x-2", Type: "related"}},
		UpdatedAt: time.Date(2026, 11, 23, 4, 0, 0, 0, time.UTC)}
	if mod != nil {
		mod(&i)
	}
	return i
}

// IssueHash is stable for an unchanged issue, whatever order labels and dependencies came in, and
// moves for every field the snapshot carries.
func TestIssueHashStableAndSensitiveToEveryField(t *testing.T) {
	base := IssueHash(issue("a-1", nil))
	if IssueHash(issue("a-1", func(i *core.TaskIssue) {
		i.Labels = []string{"b", "a"}
		i.Deps = []core.TaskDep{i.Deps[1], i.Deps[0]}
	})) != base {
		t.Error("label and dependency order must not change the hash")
	}
	mut := map[string]func(*core.TaskIssue){
		"ID": func(i *core.TaskIssue) { i.ID = "a-2" }, "Title": func(i *core.TaskIssue) { i.Title = "u" },
		"Description": func(i *core.TaskIssue) { i.Description = "d" }, "Design": func(i *core.TaskIssue) { i.Design = "d" },
		"Notes": func(i *core.TaskIssue) { i.Notes = "n" }, "Acceptance": func(i *core.TaskIssue) { i.Acceptance = "a" },
		"Status": func(i *core.TaskIssue) { i.Status = "closed" }, "Priority": func(i *core.TaskIssue) { i.Priority = 0 },
		"Type": func(i *core.TaskIssue) { i.Type = "bug" }, "Assignee": func(i *core.TaskIssue) { i.Assignee = "mei" },
		"Labels":    func(i *core.TaskIssue) { i.Labels = append(i.Labels, "c") },
		"CreatedAt": func(i *core.TaskIssue) { i.CreatedAt = i.UpdatedAt }, "UpdatedAt": func(i *core.TaskIssue) { i.UpdatedAt = i.UpdatedAt.Add(time.Second) },
		"ClosedAt": func(i *core.TaskIssue) { i.ClosedAt = i.UpdatedAt }, "DeferUntil": func(i *core.TaskIssue) { i.DeferUntil = i.UpdatedAt },
		"CloseReason": func(i *core.TaskIssue) { i.CloseReason = "done" },
		"Deps":        func(i *core.TaskIssue) { i.Deps = i.Deps[:1] },
		"Comments":    func(i *core.TaskIssue) { i.Comments = []core.TaskComment{{Author: "ada", Text: "hi"}} },
	}
	rt := reflect.TypeOf(core.TaskIssue{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i).Name
		if strings.HasPrefix(f, "Reported") || f == "HasReportedDeps" {
			continue // what the export said about itself, not content
		}
		m, ok := mut[f]
		if !ok {
			t.Errorf("field %s of TaskIssue has no hash sensitivity case: add one", f)
			continue
		}
		if IssueHash(issue("a-1", m)) == base {
			t.Errorf("changing %s does not change the hash", f)
		}
	}
}

func snap(is ...core.TaskIssue) core.TaskSnapshot { return core.TaskSnapshot{Issues: is} }

func TestDiffTable(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 4, 0, 0, 0, time.UTC)
	at := func(d time.Duration) func(*core.TaskIssue) {
		return func(i *core.TaskIssue) { i.UpdatedAt = t0.Add(d) }
	}
	title := func(s string, d time.Duration) func(*core.TaskIssue) {
		return func(i *core.TaskIssue) { i.Title = s; i.UpdatedAt = t0.Add(d) }
	}
	for _, c := range []struct {
		name                    string
		old, cur                core.TaskSnapshot
		added, removed, changed []string
		newest                  string
	}{
		{name: "identical", old: snap(issue("a", nil), issue("b", nil)), cur: snap(issue("b", nil), issue("a", nil))},
		{name: "one edited", old: snap(issue("a", nil), issue("b", nil)), cur: snap(issue("a", nil), issue("b", title("new", time.Minute))), changed: []string{"b"}, newest: "b"},
		{name: "added and removed", old: snap(issue("a", nil), issue("b", nil)), cur: snap(issue("a", nil), issue("c", at(time.Hour))), added: []string{"c"}, removed: []string{"b"}, newest: "c"},
		{name: "newest is the latest update", old: snap(issue("a", nil), issue("b", nil), issue("c", nil)),
			cur: snap(issue("a", title("x", time.Minute)), issue("b", title("y", 3*time.Minute)), issue("c", title("z", 2*time.Minute))), changed: []string{"a", "b", "c"}, newest: "b"},
		{name: "tie goes to the smaller id", old: snap(issue("a", nil), issue("b", nil)),
			cur: snap(issue("b", title("y", time.Minute)), issue("a", title("x", time.Minute))), changed: []string{"a", "b"}, newest: "a"},
		{name: "a removal alone has no newest", old: snap(issue("a", nil), issue("b", nil)), cur: snap(issue("a", nil)), removed: []string{"b"}},
		{name: "first read", old: snap(), cur: snap(issue("a", nil)), added: []string{"a"}, newest: "a"},
	} {
		d := Diff(c.old, c.cur)
		if !reflect.DeepEqual(append([]string(nil), d.Added...), append([]string(nil), c.added...)) || !reflect.DeepEqual(append([]string(nil), d.Removed...), append([]string(nil), c.removed...)) ||
			!reflect.DeepEqual(append([]string(nil), d.Changed...), append([]string(nil), c.changed...)) || d.Newest != c.newest {
			t.Errorf("%s: %+v, want added %v removed %v changed %v newest %q", c.name, d, c.added, c.removed, c.changed, c.newest)
		}
		if d.Empty() != (len(c.added)+len(c.removed)+len(c.changed) == 0) {
			t.Errorf("%s: Empty() = %v", c.name, d.Empty())
		}
	}
}

func TestDiffOverTheRealExport(t *testing.T) {
	s := realSnapshot(t)
	if d := Diff(s, s); !d.Empty() {
		t.Errorf("a snapshot against itself: %+v", d)
	}
	edited := core.TaskSnapshot{Issues: append([]core.TaskIssue(nil), s.Issues...)}
	edited.Issues[3].Notes += " more"
	if d := Diff(s, edited); len(d.Changed) != 1 || d.Changed[0] != s.Issues[3].ID {
		t.Errorf("one edit: %+v", d)
	}
}

// ---- poller, on a fake clock ----

func TestPollerIntervalAndNoOverlap(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewPoller(5*time.Second, 60*time.Second, time.Second)
	if !p.Due(now) || !p.Start(now) {
		t.Fatal("the first check is due at once")
	}
	if p.Due(now.Add(time.Hour)) || p.Start(now.Add(time.Hour)) {
		t.Fatal("a second check must never start while one is in flight, however late")
	}
	p.Done(now.Add(100*time.Millisecond), 100*time.Millisecond, true)
	for _, c := range []struct {
		after time.Duration
		due   bool
	}{{0, false}, {4 * time.Second, false}, {5*time.Second + 100*time.Millisecond, true}} {
		if got := p.Due(now.Add(100*time.Millisecond + c.after)); got != c.due {
			t.Errorf("%v after a quick check: Due = %v, want %v", c.after, got, c.due)
		}
	}
	if p.Wait() != 5*time.Second {
		t.Errorf("a quick check keeps the base wait, got %v", p.Wait())
	}
}

func TestPollerBacksOffOnSlowOrFailedAndRecovers(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewPoller(5*time.Second, 40*time.Second, time.Second)
	var waits []time.Duration
	for i := 0; i < 5; i++ { // slow checks double the wait up to the cap
		if !p.Start(now) {
			t.Fatalf("check %d not due", i)
		}
		now = now.Add(3 * time.Second)
		p.Done(now, 3*time.Second, true)
		waits = append(waits, p.Wait())
		now = now.Add(p.Wait())
	}
	want := []time.Duration{10, 20, 40, 40, 40}
	for i, w := range waits {
		if w != want[i]*time.Second {
			t.Errorf("wait after slow check %d = %v, want %v", i, w, want[i]*time.Second)
		}
	}
	p.Start(now)
	p.Done(now, time.Millisecond, false) // a failed check backs off too
	if p.Wait() != 40*time.Second {
		t.Errorf("failure at the cap stays at the cap, got %v", p.Wait())
	}
	now = now.Add(40 * time.Second)
	p.Start(now)
	p.Done(now, time.Millisecond, true)
	if p.Wait() != 5*time.Second {
		t.Errorf("a quick success returns to the base, got %v", p.Wait())
	}
	if p.Due(now.Add(4*time.Second)) || !p.Due(now.Add(5*time.Second)) {
		t.Error("after recovery the next check is one base wait away")
	}
}

func TestPollerPausesWhileHiddenAndResumesAtOnce(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewPoller(5*time.Second, 60*time.Second, time.Second)
	p.Start(now)
	p.Done(now, time.Millisecond, true)
	p.SetHidden(true)
	if p.Due(now.Add(time.Hour)) || p.Start(now.Add(time.Hour)) {
		t.Fatal("a hidden tab never checks")
	}
	p.SetHidden(false)
	if !p.Due(now.Add(time.Second)) {
		t.Fatal("showing the tab again checks at once: the tracker may have moved")
	}
	p.SetHidden(false) // showing an already shown tab keeps the schedule
	p.Start(now.Add(time.Second))
	p.Done(now.Add(time.Second), time.Millisecond, true)
	p.SetHidden(false)
	if p.Due(now.Add(2 * time.Second)) {
		t.Fatal("an idempotent show must not reset the wait")
	}
}

func TestPollerDeferAndAbort(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewPoller(5*time.Second, 60*time.Second, time.Second)
	p.Defer(now.Add(10 * time.Second))
	if p.Due(now.Add(9 * time.Second)) {
		t.Error("deferred")
	}
	p.Defer(now.Add(time.Second)) // an earlier deferral never shortens a later one
	if p.Due(now.Add(9 * time.Second)) {
		t.Error("an earlier Defer shortened the wait")
	}
	p.Start(now.Add(10 * time.Second))
	p.Abort()
	if !p.Due(now.Add(10 * time.Second)) {
		t.Error("an aborted check frees the poller")
	}
}
