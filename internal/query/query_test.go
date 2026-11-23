package query

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

// doc is a minimal Doc over a map, so the grammar is tested without any domain.
type doc struct {
	vals map[string]Value
	text []string
}

func (d doc) Field(n string) (Value, bool) { v, ok := d.vals[n]; return v, ok }
func (d doc) Text() []string               { return d.text }

func strs(s ...string) Value { return Value{Strs: s} }

func task(id, title, status string, prio int, who string, labels ...string) doc {
	v := map[string]Value{
		"id": strs(id), "title": strs(title), "status": strs(status), "label": {Strs: labels},
		"priority": {Num: prio, HasNum: true}, "type": strs("task"), "blocked": {Bool: false, HasBool: true}, "ready": {Bool: true, HasBool: true},
		"updated": {Time: now.Add(-48 * time.Hour), HasTime: true}, "created": {Time: now.Add(-10 * 24 * time.Hour), HasTime: true},
	}
	if who != "" {
		v["who"] = strs(who)
	} else {
		v["who"] = Value{}
	}
	return doc{v, append([]string{id, title}, labels...)}
}

func TestParseTable(t *testing.T) {
	cases := []struct {
		in   string
		want string // normalized form
	}{
		{"", ""},
		{"   \t ", ""},
		{"lease", "lease"},
		{"Lease Renewal", "lease renewal"},
		{`"lease renewal"`, `"lease renewal"`},
		{"status:open", "status:open"},
		{"STATUS:Open", "status:open"},
		{"status:open status:blocked", "status:open status:blocked"},
		{"status:open,blocked", "status:open,blocked"},
		{"!status:closed", "!status:closed"},
		{"-status:closed", "!status:closed"},
		{"assignee:ada", "who:ada"},
		{"who:none", "who:none"},
		{"priority:p1", "priority:p1"},
		{"priority:<=1", "priority<=1"},
		{"priority<2", "priority<2"},
		{"priority>=1 priority<=2", "priority>=1 priority<=2"},
		{"updated>7d", "updated>7d"},
		{"updated:>7d", "updated>7d"},
		{"created<2026-11-01", "created<2026-11-01"},
		{"blocked:true ready:no", "blocked:true ready:no"},
		{"is:blocked has:comments", "is:blocked has:comments"},
		{"in:tasks lease", "lease in:tasks"},
		{`title:"foo bar"`, `title:"foo bar"`},
		{"id:ac-4", "id:ac-4"},
		{"label:none", "label:none"},
		{"-dash", "-dash"},
		{"héllo wörld 日本語", "héllo wörld 日本語"},
	}
	for _, c := range cases {
		q, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if got := q.String(); got != c.want {
			t.Errorf("Parse(%q).String() = %q, want %q", c.in, got, c.want)
		}
		q2, err := Parse(q.String())
		if err != nil || q2.String() != q.String() {
			t.Errorf("normalized form %q does not round-trip: %v %v", q.String(), q2, err)
		}
	}
}

func TestParseErrorsCarryTheColumn(t *testing.T) {
	cases := []struct {
		in  string
		col int
		sub string
	}{
		{"foo:bar", 1, "unknown field"},
		{"lease foo:bar", 7, "unknown field"},
		{"!foo:bar", 2, "unknown field"},
		{"status:", 8, "missing value"},
		{"status:open,,blocked", 8, "empty value"},
		{"priority:9", 10, "priority must be"},
		{"priority:high", 10, "priority must be"},
		{"title>x", 7, "does not take"},
		{"status<open", 8, "does not take"},
		{"updated:7d", 9, "needs a comparison"},
		{"updated>soon", 9, "not a duration"},
		{"updated>1x", 9, "not a duration"},
		{"created<2026-13-45", 9, "not a duration"},
		{"blocked:maybe", 9, "true or false"},
		{"blocked>true", 9, "true or false"},
		{"in:phasma", 4, "not one of"},
		{"has:wings", 5, "not one of"},
		{"is>blocked", 4, "not a comparison"},
		{`"open quote`, 1, "unterminated"},
		{`lease "open`, 7, "unterminated"},
		{`""`, 1, "empty quoted"},
		{"!word", 1, "excludes"},
		{strings.Repeat("a", 257), 257, "longer than"},
	}
	for _, c := range cases {
		_, err := Parse(c.in)
		var qe *Error
		if !errors.As(err, &qe) {
			t.Errorf("Parse(%q): want a typed error, got %v", c.in, err)
			continue
		}
		if qe.Col != c.col || !strings.Contains(qe.Msg, c.sub) {
			t.Errorf("Parse(%q) = column %d %q, want column %d containing %q", c.in, qe.Col, qe.Msg, c.col, c.sub)
		}
	}
	if _, err := Parse(strings.Repeat("a", 256)); err != nil {
		t.Errorf("256 characters is the cap, not over it: %v", err)
	}
	if _, err := Parse(strings.Repeat("日", 256)); err != nil {
		t.Errorf("the cap counts characters, not bytes: %v", err)
	}
}

func TestMatchTable(t *testing.T) {
	a := task("ac-41", "Lease heartbeat misses on resume", "in_progress", 0, "ada", "core", "ui")
	b := task("ac-45", "Kanban cards show blockers", "open", 1, "", "ui")
	c := task("ac-12", "Tasks workspace epic", "closed", 3, "mei")
	b.vals["blocked"], b.vals["ready"] = Value{Bool: true, HasBool: true}, Value{HasBool: true}
	cases := []struct {
		q    string
		a, b bool // matches a, b
		c    bool
	}{
		{"", true, true, true},
		{"lease", true, false, false},
		{"LEASE", true, false, false},
		{"heartbeat", true, false, false},
		{"lease heartbeat", true, false, false}, // every word must match
		{"lease cards", false, false, false},
		{"hb", true, true, false}, // short word: subsequence of "heartbeat" and of "show blockers"
		{"hbt", true, false, false},
		{"hbat", true, false, false},                     // exactly four characters is still a short word: a subsequence of "heartbeat"
		{"hbtt", false, false, false},                    // but the letters must be in order                      // still a subsequence
		{"hrtbt", false, false, false},                   // five characters is a long word: contiguous only, though a subsequence of "heartbeat"
		{"heartbaet", false, false, false},               // long word: contiguous only
		{`"misses on"`, true, false, false},              // phrase is contiguous
		{`"on misses"`, false, false, false},             //
		{"ac-4", true, true, false},                      // id is searched
		{"ui", true, true, false},                        // labels are searched
		{"status:open", false, true, false},              //
		{"status:OPEN", false, true, false},              // values are case-insensitive
		{"status:open status:closed", false, true, true}, // repeated values OR
		{"status:open,closed", false, true, true},
		{"status:open label:ui", false, true, false}, // different fields AND
		{"!status:closed", true, true, false},
		{"-status:closed", true, true, false},
		{"!status:open !status:closed", true, false, false},
		{"status:open !label:ui", false, false, false},
		{"assignee:ada", true, false, false},
		{"who:ada,mei", true, false, true},
		{"assignee:none", false, true, false},
		{"!assignee:none", true, false, true},
		{"label:none", false, false, true},
		{"label:core", true, false, false}, // exact, not substring
		{"label:co", false, false, false},
		{"priority:0", true, false, false},
		{"priority:p1", false, true, false},
		{"priority:<=1", true, true, false},
		{"priority<1", true, false, false},
		{"priority>=1", false, true, true},
		{"priority>0 priority<3", false, true, false}, // comparisons AND: a range
		{"priority:0,3", true, false, true},
		{"!priority:0", false, true, true},
		{"id:ac-4", true, true, false}, // id is a prefix match
		{"id:c-4", false, false, false},
		{"title:kanban", false, true, false}, // title is a substring match
		{"title:ANBAN", false, true, false},
		{"blocked:true", false, true, false},
		{"blocked:false", true, false, true},
		{"ready:true", true, false, true},
		{"ready:no", false, true, false},
		{"updated>7d", true, true, true},    // touched in the last week (48h ago)
		{"updated>1d", false, false, false}, // 48h ago is before 1d ago
		{"updated<1d", true, true, true},    // untouched for a day
		{"updated<7d", false, false, false},
		{"created<2026-11-20", true, true, true},
		{"created>2026-11-20", false, false, false},
		{"created<=2026-11-13T12:00:00Z", true, true, true},
		{"created>1w", false, false, false}, // created 10 days ago, before a week ago
		{"priority:p0 lease !status:closed", true, false, false},
	}
	for _, tc := range cases {
		q, err := Parse(tc.q)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.q, err)
			continue
		}
		got := [3]bool{q.Match(a, now), q.Match(b, now), q.Match(c, now)}
		if want := [3]bool{tc.a, tc.b, tc.c}; got != want {
			t.Errorf("%q matched (ac-41, ac-45, ac-12) = %v, want %v", tc.q, got, want)
		}
	}
}

func TestAFieldTheDocumentLacksExcludesIt(t *testing.T) {
	lease := doc{vals: map[string]Value{"id": strs("tracker-sync"), "who": strs("ada")}, text: []string{"tracker-sync", "ada"}}
	for q, want := range map[string]bool{
		"tracker":     true,
		"who:ada":     true,
		"label:x":     false, // a lease has no label
		"!label:x":    false, // and a negation does not bring it back
		"priority<=1": false,
	} {
		p, err := Parse(q)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Match(lease, now); got != want {
			t.Errorf("lease vs %q = %v, want %v", q, got, want)
		}
	}
}

func TestUnicodeWideAndHostileText(t *testing.T) {
	d := task("ac-1", "日本語のタイトル \x1b[31mred\x1b[0m café", "open", 2, "ada")
	for q, want := range map[string]bool{"日本語": true, "café": true, `"タイトル"`: true, "title:タイトル": true, "ｆｕｌｌ": false} {
		p, err := Parse(q)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if got := p.Match(d, now); got != want {
			t.Errorf("%q = %v, want %v", q, got, want)
		}
	}
	// Hostile query text never panics and either parses or gives a typed error.
	for _, s := range []string{"\x00", "\x1b[2J", "status:\x1b", "‮status:open", "!!!", ":::", "a:b:c", "<<<>>>", `"`, `\`, "!", "-", "status:<", "priority:<=", "updated>"} {
		if _, err := Parse(s); err != nil {
			var qe *Error
			if !errors.As(err, &qe) {
				t.Errorf("Parse(%q): untyped error %v", s, err)
			}
		}
	}
}

func TestEmptyAndRepeatedAreStable(t *testing.T) {
	q, _ := Parse("  ")
	if !q.Empty() || !q.Match(task("a", "b", "open", 1, ""), now) {
		t.Fatal("whitespace is the empty query, which matches everything")
	}
	a, _ := Parse("status:open lease !label:x priority<=2")
	b, _ := Parse(a.String())
	if !reflect.DeepEqual(a.Words, b.Words) || len(a.Preds) != len(b.Preds) {
		t.Fatalf("normalizing changes the query: %+v vs %+v", a, b)
	}
}

// A duration so large it overflows time.Duration used to wrap to a time in the future: nothing
// matched or everything did. Above 100 years it is an error with a column.
func TestHugeDurationsAreRejected(t *testing.T) {
	for _, in := range []string{"updated>999999d", "updated>3000000d", "updated>36501d", "updated<5300w", "updated>9999999999h", "created>99999999999999999999m"} {
		_, err := Parse(in)
		var qe *Error
		if !errors.As(err, &qe) || qe.Col != strings.IndexAny(in, "<>")+2 {
			t.Errorf("Parse(%q) = %v, want a typed error at the value", in, err)
		}
	}
	for _, in := range []string{"updated>36500d", "updated>5200w", "updated>876000h", "updated>0d", "updated>1m"} {
		if _, err := Parse(in); err != nil {
			t.Errorf("Parse(%q): %v", in, err)
		}
	}
	q, _ := Parse("updated>36500d")
	if !q.Match(task("a", "b", "open", 1, ""), now) {
		t.Error("a hundred years ago is long before this task was updated: it matches")
	}
}
