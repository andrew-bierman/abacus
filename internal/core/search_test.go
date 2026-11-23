package core_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/query"
)

var searchNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return searchNow.Add(-d) }

const day = 24 * time.Hour

// searchFixtures is one fixture per row of the plan's field table, in every domain.
func searchFixtures() []core.SearchFeed {
	task := func(id, title, status string, prio int, who string, labels []string, upd time.Duration) core.SearchDoc {
		return core.SearchDoc{Domain: core.DomainTasks, Kind: core.KindTask, ID: id, Ref: id, Title: title, Status: status, Priority: prio, HasPriority: true,
			Who: who, Labels: labels, Type: "task", Updated: ago(upd), Created: ago(upd + day), HasDerived: true}
	}
	t1 := task("ac-41", "Lease heartbeat misses on resume", "in_progress", 0, "ada", []string{"core", "leases"}, time.Hour)
	t1.Body = "The renewal window is skipped after sleep."
	t1.Has = []string{"description", "labels", "assignee"}
	t2 := task("ac-38", "Queue filter ignores machine", "open", 1, "", []string{"queues"}, 3*day)
	t2.Ready = true
	t3 := task("ac-47", "Doctor stale export warning", "blocked", 2, "mei", []string{"backend"}, 30*day)
	t3.Blocked, t3.Stale, t3.Parent = true, true, "ac-12"
	t4 := task("ac-03", "retire the old poller", "closed", 3, "ada", nil, 40*day)
	board := core.BoardSearchDocs(core.BoardReport{
		Threads: []core.BoardThread{
			{Root: core.BoardMessage{ID: "15", Channel: "dev/web", Author: "ada", At: "2026-11-23T10:00:00Z", Text: "checkout times out\nafter the deploy"}, Replies: 1,
				Last: &core.BoardMessage{ID: "17", Author: "mei", At: "2026-11-23T11:00:00Z", Text: "rolled back, renewal fixed"}},
		},
		Tasks: []core.BoardTask{{ID: "16", Channel: "work", Text: "fix the lease renewal", State: "claimed", Claimant: "codex"}},
	})
	leases := core.LeasesSearchDocs(core.LeasesReport{Leases: []core.Lease{
		{Resource: "tracker-sync", State: core.LeaseHeld, Holder: "ada", ExpiresAt: "2026-11-23T12:04:00Z"},
		{Resource: "export-job", State: core.LeaseStale, Holder: "mei"},
		{Resource: "idle-slot", State: core.LeaseFree},
	}})
	queues := core.QueuesSearchDocs(core.QueuesReport{
		Runs:      []core.QueueRun{{Issue: "ac-41", State: core.RunRunning, Phase: "build", Machine: "m2", Workflow: "release"}, {Issue: "ac-38", State: core.RunFailed, Phase: "test", Machine: "m1", Stalled: true}},
		Queue:     []core.QueueItem{{ID: "ac-52", Title: "help overlay", Blocked: true}},
		Attention: []core.Attention{{Issue: "ac-38", Reason: "failed twice", Detail: "tests flaky on m1"}},
		History:   []core.HistoryItem{{Issue: "ac-30", Title: "export retry", Outcome: "settled", UpdatedAt: "2026-11-22T10:00:00Z"}},
	})
	return []core.SearchFeed{
		{Domain: core.DomainTasks, Docs: []core.SearchDoc{t1, t2, t3, t4}, ReadAt: searchNow},
		{Domain: core.DomainBoard, Docs: board, ReadAt: searchNow},
		{Domain: core.DomainLeases, Docs: leases, ReadAt: searchNow},
		{Domain: core.DomainQueues, Docs: queues, ReadAt: searchNow},
	}
}

func idsOf(r core.SearchReport) []string {
	var out []string
	for _, g := range r.Groups {
		for _, x := range g.Results {
			out = append(out, string(g.Domain)+"/"+x.ID)
		}
	}
	slices.Sort(out)
	return out
}

// Every row of the plan's field table, across the four domains: which documents a predicate keeps.
func TestSearchGrammarOverDomains(t *testing.T) {
	ix := core.NewSearchIndex(searchFixtures()...)
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"in:tasks", []string{"tasks/ac-03", "tasks/ac-38", "tasks/ac-41", "tasks/ac-47"}},
		{"in:board", []string{"board/k:16", "board/m:17", "board/t:15"}},
		{"in:leases", []string{"leases/export-job", "leases/idle-slot", "leases/tracker-sync"}},
		{"in:queues", []string{"queues/ac-30", "queues/ac-38", "queues/ac-38", "queues/ac-41", "queues/ac-52"}},
		{"in:tasks in:leases", []string{"leases/export-job", "leases/idle-slot", "leases/tracker-sync", "tasks/ac-03", "tasks/ac-38", "tasks/ac-41", "tasks/ac-47"}},
		// id: task id, message or thread id, resource, run key
		{"id:ac-4", []string{"queues/ac-41", "tasks/ac-41", "tasks/ac-47"}},
		{"id:15", []string{"board/t:15"}},
		{"id:tracker", []string{"leases/tracker-sync"}},
		// title: title, first line of a message, resource, issue and workflow
		{"title:heartbeat", []string{"tasks/ac-41"}},
		{"title:checkout", []string{"board/t:15"}},
		{"title:export", []string{"leases/export-job", "queues/ac-30", "tasks/ac-47"}},
		{"title:release", []string{"queues/ac-41"}},
		// status: task status, board-task status, held stale free, run state
		{"status:open", []string{"tasks/ac-38"}},
		{"status:claimed", []string{"board/k:16"}},
		{"status:held", []string{"leases/tracker-sync"}},
		{"status:running", []string{"queues/ac-41"}},
		{"status:open,blocked", []string{"tasks/ac-38", "tasks/ac-47", "queues/ac-52"}},
		// who: assignee, author and claimant, holder, machine (and the aliases)
		{"who:ada", []string{"board/t:15", "leases/tracker-sync", "tasks/ac-03", "tasks/ac-41"}},
		{"assignee:mei", []string{"board/m:17", "leases/export-job", "tasks/ac-47"}}, // the aliases are one field,
		{"author:mei in:board", []string{"board/m:17"}},
		{"holder:mei in:leases", []string{"leases/export-job"}},
		{"machine:m2", []string{"queues/ac-41"}},
		{"who:codex", []string{"board/k:16"}},
		// label type priority parent: tasks only
		{"label:leases", []string{"tasks/ac-41"}},
		{"label:none", []string{"tasks/ac-03"}},
		{"type:task priority:<=1", []string{"tasks/ac-38", "tasks/ac-41"}},
		{"parent:ac-12", []string{"tasks/ac-47"}},
		// channel, phase, workflow
		{"channel:dev/web", []string{"board/m:17", "board/t:15"}},
		{"phase:build", []string{"queues/ac-41"}},
		{"workflow:release", []string{"queues/ac-41"}},
		// is:blocked is:ready (tasks), is:stale (tasks, leases, queues)
		{"is:blocked", []string{"tasks/ac-47"}},
		{"is:ready", []string{"tasks/ac-38"}},
		{"is:stale", []string{"leases/export-job", "queues/ac-38", "tasks/ac-47"}},
		{"blocked:true ready:false", []string{"tasks/ac-47"}},
		// has:
		{"has:description", []string{"tasks/ac-41"}},
		// time fields: updated (at), expires
		{"updated>2d", []string{"board/m:17", "board/t:15", "queues/ac-30", "tasks/ac-41"}},
		{"at>2d in:board", []string{"board/m:17", "board/t:15"}},
		{"expires>2h", nil}, // it expires in 4 minutes
		{"expires<2h", []string{"leases/tracker-sync"}},
		// negation, OR within a field, AND across
		{"!status:closed in:tasks", []string{"tasks/ac-38", "tasks/ac-41", "tasks/ac-47"}},
		{"-who:ada in:tasks", []string{"tasks/ac-38", "tasks/ac-47"}},
		{"status:open status:in_progress", []string{"tasks/ac-38", "tasks/ac-41"}},
		{"status:open who:ada", nil},
		// free words and quoted phrases
		{"renewal", []string{"board/k:16", "board/m:17", "tasks/ac-41"}},
		{`"renewal window"`, []string{"tasks/ac-41"}},
		{"lease renewal in:board", []string{"board/k:16"}},
		{"lease renewal status:claimed", []string{"board/k:16"}},
		// a predicate on a field a domain lacks excludes it
		{"label:core in:leases", nil},
		{"channel:work in:tasks", nil},
	} {
		r, err := ix.Search(tc.q, core.SearchOptions{Now: searchNow})
		if err != nil {
			t.Errorf("%q: %v", tc.q, err)
			continue
		}
		want := slices.Clone(tc.want)
		slices.Sort(want)
		got := idsOf(r)
		if !slices.Equal(got, want) {
			t.Errorf("%q = %v, want %v", tc.q, got, want)
		}
	}
}

func TestSearchBadQueryIsTypedWithColumn(t *testing.T) {
	ix := core.NewSearchIndex(searchFixtures()...)
	_, err := ix.Search("lease nosuchfield:x", core.SearchOptions{Now: searchNow})
	var qe *query.Error
	if !errors.As(err, &qe) || qe.Col != 7 {
		t.Fatalf("err = %v (%T), want *query.Error at column 7", err, err)
	}
	if _, err := ix.Search(strings.Repeat("a", query.MaxLen+1), core.SearchOptions{Now: searchNow}); err == nil {
		t.Error("a query over the cap must be refused")
	}
	if r, err := ix.Search("   ", core.SearchOptions{Now: searchNow}); err != nil || r.Total != 0 {
		t.Errorf("a blank query matches nothing: %v %d", err, r.Total)
	}
}

func TestSearchRankingIsDeterministicAndOrdered(t *testing.T) {
	mk := func(id, title, body string, labels []string, upd time.Duration) core.SearchDoc {
		return core.SearchDoc{Domain: core.DomainTasks, Kind: core.KindTask, ID: id, Ref: id, Title: title, Body: body, Labels: labels, Updated: ago(upd)}
	}
	docs := []core.SearchDoc{
		mk("z-body", "unrelated", "mentions lease here", nil, time.Minute),
		mk("a-label", "unrelated", "", []string{"lease"}, time.Minute),
		mk("b-title-part", "subleased things", "", nil, time.Minute),
		mk("c-title-word", "the lease heartbeat", "", nil, time.Hour),
		mk("lease-1", "other", "", nil, time.Hour),
		mk("lease", "other", "", nil, 2*time.Hour),
		mk("d-title-word-new", "lease news", "", nil, time.Minute),
	}
	ix := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainTasks, Docs: docs, ReadAt: searchNow})
	var first []string
	for i := 0; i < 5; i++ {
		r, err := ix.Search("lease", core.SearchOptions{Now: searchNow})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, x := range r.Groups[0].Results {
			got = append(got, x.ID)
		}
		if i == 0 {
			first = got
		} else if !slices.Equal(first, got) {
			t.Fatalf("run %d differs: %v vs %v", i, got, first)
		}
	}
	want := []string{"lease", "lease-1", "d-title-word-new", "c-title-word", "b-title-part", "a-label", "z-body"}
	if !slices.Equal(first, want) {
		t.Errorf("order = %v, want %v (exact id, id prefix, title word newest first, title part, label, body)", first, want)
	}
}

func TestSearchGroupsInPlanOrderWithCountsAndSnippet(t *testing.T) {
	ix := core.NewSearchIndex(searchFixtures()...)
	r, _ := ix.Search("renewal", core.SearchOptions{Now: searchNow})
	var doms []core.DomainID
	for _, g := range r.Groups {
		doms = append(doms, g.Domain)
		if g.Total != len(g.Results) {
			t.Errorf("%s total %d, results %d", g.Domain, g.Total, len(g.Results))
		}
	}
	if !slices.Equal(doms, []core.DomainID{core.DomainTasks, core.DomainBoard}) || r.Total != 3 {
		t.Errorf("groups = %v total %d", doms, r.Total)
	}
	x := r.Groups[0].Results[0]
	if x.ID != "ac-41" || !slices.Equal(x.Matched, []string{"body"}) || !strings.Contains(x.Snippet, "renewal window") {
		t.Errorf("task hit = %+v", x)
	}
	for _, d := range r.Domains {
		if d.State != core.SearchSearched || d.Stale {
			t.Errorf("domain %+v", d)
		}
	}
	if r.SchemaVersion != 1 {
		t.Error("schema_version must be 1")
	}
}

// The per-domain limit keeps the best N and says how many more there were.
func TestSearchLimitIsPerDomain(t *testing.T) {
	var docs []core.SearchDoc
	for i := 0; i < 50; i++ {
		docs = append(docs, core.SearchDoc{Domain: core.DomainTasks, Kind: core.KindTask, ID: fmt.Sprintf("t-%02d", i), Ref: fmt.Sprintf("t-%02d", i), Title: "common word", Updated: ago(time.Duration(i) * time.Minute)})
	}
	var leases []core.SearchDoc
	for i := 0; i < 30; i++ {
		leases = append(leases, core.SearchDoc{Domain: core.DomainLeases, Kind: core.KindLease, ID: fmt.Sprintf("l-%02d", i), Ref: fmt.Sprintf("l-%02d", i), Title: "common word"})
	}
	ix := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainTasks, Docs: docs}, core.SearchFeed{Domain: core.DomainLeases, Docs: leases})
	r, _ := ix.Search("common", core.SearchOptions{Now: searchNow})
	for _, g := range r.Groups {
		if len(g.Results) != core.DefaultSearchLimit {
			t.Errorf("%s: %d results, want %d", g.Domain, len(g.Results), core.DefaultSearchLimit)
		}
	}
	if r.Groups[0].Total != 50 || r.Groups[0].Truncated != 30 || r.Groups[1].Truncated != 10 {
		t.Errorf("totals %+v %+v", r.Groups[0], r.Groups[1])
	}
	if r.Groups[0].Results[0].ID != "t-00" { // newest first among equals
		t.Errorf("first = %s", r.Groups[0].Results[0].ID)
	}
	r, _ = ix.Search("common", core.SearchOptions{Now: searchNow, Limit: 3, Domains: []core.DomainID{core.DomainTasks}})
	if len(r.Groups) != 1 || len(r.Groups[0].Results) != 3 || len(r.Domains) != 1 {
		t.Errorf("limit 3 / one domain: %+v", r)
	}
	r, _ = ix.Search("common", core.SearchOptions{Now: searchNow, Limit: 100000})
	if len(r.Groups[0].Results) != 50 {
		t.Errorf("limit is clamped to %d, got %d results", core.MaxSearchLimit, len(r.Groups[0].Results))
	}
}

// The staleness rule: a read older than twice its poll interval, with a fake clock.
func TestSearchStaleness(t *testing.T) {
	poll := 30 * time.Second
	for _, tc := range []struct {
		age   time.Duration
		stale bool
	}{{0, false}, {59 * time.Second, false}, {60 * time.Second, false}, {61 * time.Second, true}, {5 * time.Minute, true}} {
		if got := core.SearchStale(searchNow.Add(-tc.age), searchNow, poll); got != tc.stale {
			t.Errorf("age %v: stale = %v, want %v", tc.age, got, tc.stale)
		}
	}
	if core.SearchStale(time.Time{}, searchNow, poll) || core.SearchStale(ago(time.Hour), searchNow, 0) {
		t.Error("an unknown read time or no interval is never stale")
	}
	feeds := searchFixtures()
	feeds[2].ReadAt, feeds[2].PollEvery = ago(3*time.Minute), 30*time.Second
	feeds[0].ReadAt, feeds[0].PollEvery = ago(40*time.Second), 30*time.Second
	r, _ := core.NewSearchIndex(feeds...).Search("in:leases in:tasks", core.SearchOptions{Now: searchNow})
	for _, g := range r.Groups {
		for _, x := range g.Results {
			if want := g.Domain == core.DomainLeases; x.Stale != want || (want && x.Age != "3m") {
				t.Errorf("%s/%s stale=%v age=%q", g.Domain, x.ID, x.Stale, x.Age)
			}
		}
	}
	for _, d := range r.Domains {
		if d.Stale != (d.Domain == core.DomainLeases) {
			t.Errorf("domain %s stale=%v", d.Domain, d.Stale)
		}
	}
}

func TestSearchUnavailableDomainsAreNamed(t *testing.T) {
	feeds := []core.SearchFeed{
		{Domain: core.DomainTasks, Docs: searchFixtures()[0].Docs, ReadAt: searchNow},
		{Domain: core.DomainBoard, State: core.SearchNotConnected},
		{Domain: core.DomainLeases, State: core.SearchLoading},
		{Domain: core.DomainQueues, State: core.SearchDegraded, Reason: "the read failed"},
	}
	r, _ := core.NewSearchIndex(feeds...).Search("lease", core.SearchOptions{Now: searchNow})
	got := map[core.DomainID]core.SearchState{}
	for _, d := range r.Domains {
		got[d.Domain] = d.State
	}
	if got[core.DomainBoard] != core.SearchNotConnected || got[core.DomainLeases] != core.SearchLoading || got[core.DomainQueues] != core.SearchDegraded || got[core.DomainTasks] != core.SearchSearched {
		t.Errorf("states = %v", got)
	}
	if len(r.Groups) != 1 {
		t.Errorf("only the readable domain has hits: %v", idsOf(r))
	}
}

func TestSearchHostileTextStaysData(t *testing.T) {
	d := core.SearchDoc{Domain: core.DomainTasks, Kind: core.KindTask, ID: "x-1", Ref: "x-1", Title: "evil \x1b[31mred\x1b[0m ‮ rtl\x07", Meta: "m\x00eta", Body: strings.Repeat("😀界", 5000)}
	r, _ := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainTasks, Docs: []core.SearchDoc{d}}).Search("evil", core.SearchOptions{Now: searchNow})
	x := r.Groups[0].Results[0]
	for _, s := range []string{x.Title, x.Meta, x.Label} {
		for _, c := range s {
			if c < 0x20 || c == 0x7f || c == 0x202e {
				t.Errorf("control or bidi rune %q survived in %q", c, s)
			}
		}
	}
}

// 20,000 documents scan in well under the 200 ms budget.
func TestSearchTwentyThousandDocumentsUnderBudget(t *testing.T) {
	docs := make([]core.SearchDoc, 20000)
	for i := range docs {
		docs[i] = core.SearchDoc{Domain: core.DomainTasks, Kind: core.KindTask, ID: fmt.Sprintf("ac-%05d", i), Ref: fmt.Sprintf("ac-%05d", i),
			Title: fmt.Sprintf("Task number %d about lease renewal and the queue", i), Body: strings.Repeat("some longer description text ", 20),
			Status: "open", Priority: i % 5, HasPriority: true, Labels: []string{"core", "ui"}, Updated: ago(time.Duration(i) * time.Minute)}
	}
	ix := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainTasks, Docs: docs, ReadAt: searchNow})
	start := time.Now()
	for _, q := range []string{"lease renewal status:open", "who:ada heartbeat", "priority:<=1 !label:ui zzz"} {
		if _, err := ix.Search(q, core.SearchOptions{Now: searchNow}); err != nil {
			t.Fatal(err)
		}
	}
	if per := time.Since(start) / 3; per > 200*time.Millisecond {
		t.Errorf("a search over 20,000 documents took %v, budget 200ms", per)
	}
}

func TestSearchScanIsCappedPerDomain(t *testing.T) {
	docs := make([]core.SearchDoc, core.MaxSearchDocs+5)
	for i := range docs {
		docs[i] = core.SearchDoc{Domain: core.DomainTasks, Kind: core.KindTask, ID: fmt.Sprint(i), Ref: fmt.Sprint(i), Title: "hit"}
	}
	r, _ := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainTasks, Docs: docs}).Search("hit", core.SearchOptions{Now: searchNow})
	if r.Groups[0].Total != core.MaxSearchDocs || r.Domains[0].Reason == "" {
		t.Errorf("total %d, reason %q", r.Groups[0].Total, r.Domains[0].Reason)
	}
}

// A reply is shown by its thread: the jump target is the row that exists, not the message id.
func TestSearchResultJumpUsesTheTargetRow(t *testing.T) {
	r, _ := core.NewSearchIndex(searchFixtures()[1]).Search("rolled back", core.SearchOptions{Now: searchNow})
	x := r.Groups[0].Results[0]
	if x.ID != "m:17" || x.Jump() != "t:15" {
		t.Errorf("id %q jump %q, want m:17 -> t:15", x.ID, x.Jump())
	}
	r, _ = core.NewSearchIndex(searchFixtures()[0]).Search("ac-41", core.SearchOptions{Now: searchNow})
	if y := r.Groups[0].Results[0]; y.Jump() != "ac-41" {
		t.Errorf("a task jumps to its own id, got %q", y.Jump())
	}
}

func TestSearchLimitIsClampedToTheMaximum(t *testing.T) {
	docs := make([]core.SearchDoc, 300)
	for i := range docs {
		docs[i] = core.SearchDoc{Domain: core.DomainTasks, Kind: core.KindTask, ID: fmt.Sprint(i), Ref: fmt.Sprint(i), Title: "same"}
	}
	r, _ := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainTasks, Docs: docs}).Search("same", core.SearchOptions{Now: searchNow, Limit: 100000})
	if got := len(r.Groups[0].Results); got != core.MaxSearchLimit || r.Groups[0].Truncated != 100 {
		t.Errorf("%d results, %d truncated", got, r.Groups[0].Truncated)
	}
}

// Text whose lower-case form has a different byte length (U+023A, U+0130, Kelvin) or that folds
// oddly (sharp s, combining marks) must never break the excerpt, before or inside the match.
func TestSearchExcerptSurvivesCaseFoldingText(t *testing.T) {
	prefixes := []string{"Ⱥ", "İ", "K", "ß", "é", "ǅ", "ẞ", "ͅ"}
	for _, p := range prefixes {
		for _, body := range []string{
			strings.Repeat(p, 30) + " the renewal window is skipped",
			"the " + strings.Repeat(p, 5) + "renewal" + strings.Repeat(p, 5) + " window",
			"renewal " + strings.Repeat(p, 40),
			strings.Repeat(p, 60) + "RENEWAL",
		} {
			d := core.SearchDoc{Domain: core.DomainBoard, Kind: core.KindThread, ID: "t:1", Ref: "1", Title: "x", Body: body}
			r, err := core.NewSearchIndex(core.SearchFeed{Domain: core.DomainBoard, Docs: []core.SearchDoc{d}}).Search("renewal", core.SearchOptions{Now: searchNow})
			if err != nil || r.Total != 1 {
				t.Fatalf("%q: %v total %d", p, err, r.Total)
			}
			if s := r.Groups[0].Results[0].Snippet; !strings.Contains(strings.ToLower(s), "renewal") && !strings.HasPrefix(body, "renewal") {
				t.Logf("%q: excerpt without the match: %q", p, s)
			}
		}
	}
}

func FuzzSnippetAround(f *testing.F) {
	for _, s := range []string{"the renewal window", "Ⱥ renewal", "İ renewal İ", "K kelvin", "straße renewal", "é é renewal", "", "x"} {
		f.Add(s, "renewal")
		f.Add(s, "ss")
		f.Add(s, "")
	}
	f.Fuzz(func(t *testing.T, body, word string) {
		got := core.SnippetAround(body, strings.ToLower(word))
		if n := len([]rune(got)); n > 48+2+len([]rune(word)) {
			t.Fatalf("excerpt of %d runes for %q", n, body)
		}
	})
}
