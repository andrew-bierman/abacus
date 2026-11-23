package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// wsMod is a module that also provides the optional workspace capability.
type wsMod struct {
	*modtest.Fake
	rep   core.WorkspaceReport
	err   error
	asked []core.WorkspaceItem
}

func (w *wsMod) Workspace(_ context.Context, items []core.WorkspaceItem) (core.WorkspaceReport, error) {
	w.asked = items
	return w.rep, w.err
}
func (w *wsMod) Open(core.Placement) core.OpenPlan { return core.OpenPlan{} }

func newWS(rep core.WorkspaceReport, err error) *wsMod {
	return &wsMod{Fake: &modtest.Fake{IDStr: "ws"}, rep: rep, err: err}
}

var someItems = []core.WorkspaceItem{{Kind: core.WorkspaceRun, ID: "ac-1", Host: "mbp"}}

func TestAbsentProviderIsAnEmptyNonErrorResult(t *testing.T) {
	for name, entries := range map[string][]core.Entry{
		"no modules":     nil,
		"not ready":      {entry(newWS(core.WorkspaceReport{Placements: []core.Placement{{ID: "x"}}}, nil), core.Incompatible, "x")},
		"absent":         {entry(newWS(core.WorkspaceReport{}, nil), core.Absent, "")},
		"unrelated only": {entry(&modtest.Fake{IDStr: "other"}, core.Ready, "")},
	} {
		rep := core.ReadWorkspace(context.Background(), entries, someItems)
		if !rep.Empty() || rep.State != "" || rep.Reason != "" {
			t.Errorf("%s: %+v, want the zero report", name, rep)
		}
	}
	// And it is omitted from the status JSON.
	d := core.ReadDomains(context.Background(), nil)
	if d.Workspace != nil {
		t.Fatal("no provider must leave Workspace nil")
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "workspace") {
		t.Fatalf("workspace must be omitted without a provider: %s", b)
	}
}

func TestProviderFailureIsDegradedNotAnError(t *testing.T) {
	m := newWS(core.WorkspaceReport{}, errors.New("boom \x1b[31mred"))
	rep := core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, someItems)
	if rep.State != core.Degraded || strings.ContainsRune(rep.Reason, 0x1b) || rep.Empty() {
		t.Fatalf("%+v", rep)
	}
}

func TestWorkspaceReportIsSanitizedAndBounded(t *testing.T) {
	hostile := "a\x1b]0;pwned\x07b‮c‍d" + strings.Repeat("中", 10000)
	m := newWS(core.WorkspaceReport{State: core.Connected, Placements: []core.Placement{
		{ID: hostile, Item: core.WorkspaceItem{Kind: "run", ID: hostile, Host: "mbp"}, Host: "mbp", Session: hostile, Tab: hostile, Label: hostile, Match: core.PlacementMatch(hostile)},
	}, Ambiguous: []core.Ambiguity{{Item: core.WorkspaceItem{Kind: "run", ID: "x"}, Candidates: make([]core.Placement, 50)}}}, nil)
	rep := core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, someItems)
	p := rep.Placements[0]
	for name, s := range map[string]string{"id": p.ID, "host": p.Host, "session": p.Session, "tab": p.Tab, "label": p.Label, "match": string(p.Match), "item": p.Item.ID} {
		for _, r := range s {
			if r < 0x20 || r == 0x7f || r == 0x202e || r == 0x200d {
				t.Errorf("%s keeps control or bidi rune %U", name, r)
			}
		}
		if n := len([]rune(s)); n > core.MaxIDRunes {
			t.Errorf("%s has %d runes, over the bound", name, n)
		}
	}
	if n := len([]rune(p.Label)); n > core.MaxLabelRunes || n == 0 {
		t.Errorf("label has %d runes, want 1..%d", n, core.MaxLabelRunes)
	}
	if got := len(rep.Ambiguous[0].Candidates); got != 8 {
		t.Errorf("candidates not bounded: %d", got)
	}
}

func TestItemsAreCappedBeforeTheProviderSeesThem(t *testing.T) {
	m := newWS(core.WorkspaceReport{}, nil)
	var items []core.WorkspaceItem
	for i := 0; i < 1000; i++ {
		items = append(items, core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "t"})
	}
	core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, items)
	if len(m.asked) != 200 {
		t.Fatalf("asked %d items", len(m.asked))
	}
}

func TestUnknownKindAndMatchPassThroughJSONUnchanged(t *testing.T) {
	in := `{"state":"connected","placements":[{"id":"p1","item":{"kind":"hologram","id":"zz-1"},"session":"s","match":"telepathy","future":1}],` +
		`"ambiguous":[{"item":{"kind":"hologram","id":"zz-2"},"candidates":[{"id":"p2","item":{"kind":"hologram","id":"zz-2"}}]}]}`
	var r core.WorkspaceReport
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if r.Placements[0].Item.Kind.Known() || r.Placements[0].Match != "telepathy" {
		t.Fatalf("%+v", r.Placements[0])
	}
	m := newWS(r, nil)
	rep := core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, someItems)
	b, _ := json.Marshal(rep)
	for _, want := range []string{`"kind":"hologram"`, `"match":"telepathy"`, `"zz-2"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("lost %s: %s", want, b)
		}
	}
	for _, k := range []core.WorkspaceKind{core.WorkspaceRun, core.WorkspaceLease, core.WorkspaceTask, core.WorkspaceQueue} {
		if !k.Known() {
			t.Errorf("%s should be known", k)
		}
	}
}

func TestEmptyIsNothingToReportButDegradedIsReported(t *testing.T) {
	if !(core.WorkspaceReport{State: core.NotConnected, Reason: "no server"}).Empty() {
		t.Error("not connected is empty (omitted from JSON)")
	}
	if (core.WorkspaceReport{State: core.Degraded}).Empty() {
		t.Error("degraded must be reported")
	}
	if (core.WorkspaceReport{Ambiguous: []core.Ambiguity{{}}}).Empty() {
		t.Error("an ambiguity is a finding")
	}
}

func TestReadDomainsAddsWorkspaceOnlyWithAProviderAndFindings(t *testing.T) {
	it := core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "t-1"}
	ws := newWS(core.WorkspaceReport{State: core.Connected, Placements: []core.Placement{{ID: "p", Item: it, Session: "s"}}}, nil)
	tasks := provMod{Fake: &modtest.Fake{IDStr: "bd"}, tasks: func() (core.TasksReport, error) {
		return core.TasksReport{Recent: []core.Task{{ID: "t-1"}, {ID: "t-1"}}}, nil
	}}
	d := core.ReadDomains(context.Background(), []core.Entry{entry(tasks, core.Ready, ""), entry(ws, core.Ready, "")})
	if d.Workspace == nil || len(d.Workspace.Placements) != 1 {
		t.Fatalf("%+v", d.Workspace)
	}
	if len(ws.asked) != 1 || ws.asked[0] != it {
		t.Fatalf("items asked for: %+v (duplicates are dropped)", ws.asked)
	}
	if p, _ := d.Workspace.For(it); p == nil || p.Session != "s" {
		t.Fatalf("For: %+v", p)
	}
	// A provider with nothing to say adds nothing.
	d = core.ReadDomains(context.Background(), []core.Entry{entry(tasks, core.Ready, ""), entry(newWS(core.WorkspaceReport{State: core.NotConnected}, nil), core.Ready, "")})
	if d.Workspace != nil {
		t.Fatal("an empty report is omitted")
	}
}

func TestWorkspaceItemsCoverRunsQueueLeasesAndTasksWithTheRunsMachine(t *testing.T) {
	d := core.DomainsReport{
		Queues: core.QueuesReport{Runs: []core.QueueRun{{Issue: "r1", Machine: "mbp"}}, Queue: []core.QueueItem{{ID: "q1"}}},
		Leases: core.LeasesReport{Leases: []core.Lease{{Resource: "l1"}}},
		Tasks:  core.TasksReport{Recent: []core.Task{{ID: "t1"}, {ID: ""}}},
	}
	got := core.WorkspaceItems(d)
	want := []core.WorkspaceItem{{Kind: "run", ID: "r1", Host: "mbp"}, {Kind: "queue", ID: "q1"}, {Kind: "lease", ID: "l1"}, {Kind: "task", ID: "t1"}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestResolveProvidersExposesTheWorkspaceProvider(t *testing.T) {
	ws := newWS(core.WorkspaceReport{}, nil)
	if core.ResolveProviders([]core.Entry{entry(ws, core.Ready, "")}).Workspace == nil {
		t.Fatal("ready provider must be resolved")
	}
	if core.ResolveProviders([]core.Entry{entry(ws, core.Absent, "")}).Workspace != nil {
		t.Fatal("absent provider must not be")
	}
}

func TestShellJoinQuotesWhatNeedsIt(t *testing.T) {
	got := core.ShellJoin([]string{"rex", "-S", "my host", "it's", "", "a;b", "ok-1_2.3/x@y:z"})
	want := `rex -S 'my host' 'it'\''s' '' 'a;b' ok-1_2.3/x@y:z`
	if got != want {
		t.Fatalf("%q != %q", got, want)
	}
}

func TestPlacementIDIsStableSpecificAndTwelveHexCharsLong(t *testing.T) {
	id := core.PlacementID(core.WorkspaceItem{Kind: "run", ID: "x"}, "h", "s")
	if len(id) != len("pl-")+12 || !strings.HasPrefix(id, "pl-") {
		t.Fatalf("%q", id)
	}
	// the fields are separated: moving a character between neighbours must not collide
	a := core.PlacementID(core.WorkspaceItem{Kind: "run", ID: "ab"}, "c", "d")
	for _, b := range []string{core.PlacementID(core.WorkspaceItem{Kind: "run", ID: "a"}, "bc", "d"), core.PlacementID(core.WorkspaceItem{Kind: "run", ID: "a"}, "b", "cd"),
		core.PlacementID(core.WorkspaceItem{Kind: "runa", ID: "b"}, "c", "d")} {
		if a == b {
			t.Fatal("fields run together")
		}
	}
}

func TestPlacementIDIsStableAndSpecific(t *testing.T) {
	a := core.PlacementID(core.WorkspaceItem{Kind: "run", ID: "x"}, "h", "s1")
	if a != core.PlacementID(core.WorkspaceItem{Kind: "run", ID: "x"}, "h", "s1") {
		t.Fatal("not stable")
	}
	for _, b := range []string{core.PlacementID(core.WorkspaceItem{Kind: "task", ID: "x"}, "h", "s1"), core.PlacementID(core.WorkspaceItem{Kind: "run", ID: "x"}, "h2", "s1"), core.PlacementID(core.WorkspaceItem{Kind: "run", ID: "x"}, "h", "s2")} {
		if a == b {
			t.Fatal("collision across kind, host or session")
		}
	}
}

func TestClipRunesNeverSplitsARuneOrKeepsInvalidBytes(t *testing.T) {
	if got := core.ClipRunes("日本語日本語", 3); got != "日本語" {
		t.Fatal(got)
	}
	if got := core.ClipRunes("a\xffb", 10); got != "a�b" {
		t.Fatalf("%q", got)
	}
}

func TestOpenPlanRungIsDataAndRoundTrips(t *testing.T) {
	in := core.OpenPlan{Rungs: []core.OpenRung{{Name: "a-future-rung", Argv: []string{"x", "y"}}}, Copy: "x y"}
	b, _ := json.Marshal(in)
	var out core.OpenPlan
	if err := json.Unmarshal(b, &out); err != nil || out.Rungs[0].Name != "a-future-rung" || out.Copy != "x y" {
		t.Fatalf("%v %+v", err, out)
	}
}

func placements(n int) []core.Placement {
	var out []core.Placement
	for i := 0; i < n; i++ {
		out = append(out, core.Placement{ID: fmt.Sprintf("p%d", i), Item: core.WorkspaceItem{Kind: "run", ID: fmt.Sprintf("r%d", i)}})
	}
	return out
}

func read(rep core.WorkspaceReport) core.WorkspaceReport {
	return core.ReadWorkspace(context.Background(), []core.Entry{entry(newWS(rep, nil), core.Ready, "")}, someItems)
}

func TestReasonAndHowToConnectAreBounded(t *testing.T) {
	for _, tc := range []struct{ n, want int }{{299, 299}, {300, 300}, {301, 300}, {5000, 300}} {
		rep := read(core.WorkspaceReport{State: core.Degraded, Reason: strings.Repeat("r", tc.n)})
		if got := len([]rune(rep.Reason)); got != tc.want {
			t.Errorf("reason of %d runes became %d, want %d", tc.n, got, tc.want)
		}
	}
	for _, tc := range []struct{ n, want int }{{199, 199}, {200, 200}, {201, 200}} {
		rep := read(core.WorkspaceReport{State: core.NotConnected, HowToConnect: strings.Repeat("h", tc.n)})
		if got := len([]rune(rep.HowToConnect)); got != tc.want {
			t.Errorf("how-to of %d runes became %d, want %d", tc.n, got, tc.want)
		}
	}
	rep := read(core.WorkspaceReport{State: core.Degraded, Reason: "a\x1b[2Jb\u202ec", HowToConnect: "x\x07y"})
	if rep.Reason != "a[2Jbc" || rep.HowToConnect != "xy" {
		t.Errorf("%q %q", rep.Reason, rep.HowToConnect)
	}
}

func TestPlacementAndAmbiguityCapsHaveExactBoundaries(t *testing.T) {
	for _, tc := range []struct {
		n     int
		trunc bool
	}{{499, false}, {500, false}, {501, true}} {
		rep := read(core.WorkspaceReport{State: core.Connected, Placements: placements(tc.n)})
		if got := len(rep.Placements); got != min(tc.n, 500) || rep.Truncated != tc.trunc {
			t.Errorf("%d placements: kept %d, truncated %v", tc.n, got, rep.Truncated)
		}
		var amb []core.Ambiguity
		for i := 0; i < tc.n; i++ {
			amb = append(amb, core.Ambiguity{Item: core.WorkspaceItem{Kind: "run", ID: fmt.Sprintf("r%d", i)}})
		}
		rep = read(core.WorkspaceReport{State: core.Connected, Ambiguous: amb})
		if got := len(rep.Ambiguous); got != min(tc.n, 500) || rep.Truncated != tc.trunc {
			t.Errorf("%d ambiguities: kept %d, truncated %v", tc.n, got, rep.Truncated)
		}
	}
}

func TestCandidateCapMarksTruncationAtExactlyNine(t *testing.T) {
	for _, tc := range []struct {
		n     int
		trunc bool
	}{{7, false}, {8, false}, {9, true}} {
		rep := read(core.WorkspaceReport{State: core.Connected, Ambiguous: []core.Ambiguity{{Item: core.WorkspaceItem{Kind: "run", ID: "x"}, Candidates: placements(tc.n)}}})
		a := rep.Ambiguous[0]
		if len(a.Candidates) != min(tc.n, 8) || a.Truncated != tc.trunc {
			t.Errorf("%d candidates: kept %d, truncated %v", tc.n, len(a.Candidates), a.Truncated)
		}
	}
	// a provider's own marker passes through
	rep := read(core.WorkspaceReport{State: core.Connected, Ambiguous: []core.Ambiguity{{Item: core.WorkspaceItem{ID: "x"}, Truncated: true}}})
	if !rep.Ambiguous[0].Truncated {
		t.Error("the provider's truncation marker was lost")
	}
}

func TestCandidatesAreSanitizedLikePlacements(t *testing.T) {
	hostile := "a\x1b]0;pwned\x07b\u202ec" + strings.Repeat("中", 500)
	rep := read(core.WorkspaceReport{State: core.Connected, Ambiguous: []core.Ambiguity{{
		Item:       core.WorkspaceItem{Kind: "run", ID: hostile},
		Candidates: []core.Placement{{ID: hostile, Item: core.WorkspaceItem{Kind: core.WorkspaceKind(hostile), ID: hostile}, Session: hostile, Tab: hostile, Label: hostile, Match: core.PlacementMatch(hostile)}}}}})
	a := rep.Ambiguous[0]
	c := a.Candidates[0]
	for name, v := range map[string]string{"item id": a.Item.ID, "id": c.ID, "kind": string(c.Item.Kind), "session": c.Session, "tab": c.Tab, "label": c.Label, "match": string(c.Match)} {
		if strings.ContainsAny(v, "\x1b\x07\u202e") {
			t.Errorf("%s keeps a control or bidi rune: %q", name, v)
		}
		if n := len([]rune(v)); n > core.MaxIDRunes {
			t.Errorf("%s has %d runes", name, n)
		}
	}
	if n := len([]rune(c.Label)); n != core.MaxLabelRunes {
		t.Errorf("label %d", n)
	}
	if n := len([]rune(c.Tab)); n != core.MaxLabelRunes {
		t.Errorf("tab %d", n)
	}
}

// A host is never clipped or altered: clipping can turn one host into another, so a host that is
// too long or would change is rejected along with its placement.
func TestHostsAreRejectedNeverClipped(t *testing.T) {
	long := strings.Repeat("a", 253)
	ok := core.Placement{ID: "ok", Item: core.WorkspaceItem{Kind: "run", ID: "x", Host: long}, Host: long}
	rep := read(core.WorkspaceReport{State: core.Connected, Placements: []core.Placement{ok}})
	if len(rep.Placements) != 1 || rep.Placements[0].Host != long || rep.Placements[0].Item.Host != long || rep.Truncated {
		t.Fatalf("a 253-character host is kept whole: %+v", rep)
	}
	tooLong := long + "b"
	for name, p := range map[string]core.Placement{
		"host":      {ID: "p", Item: core.WorkspaceItem{ID: "x"}, Host: tooLong},
		"item host": {ID: "p", Item: core.WorkspaceItem{ID: "x", Host: tooLong}},
		"control":   {ID: "p", Item: core.WorkspaceItem{ID: "x"}, Host: "mbp\x1b[2J"},
		"bidi":      {ID: "p", Item: core.WorkspaceItem{ID: "x"}, Host: "mbp\u202e"},
		"newline":   {ID: "p", Item: core.WorkspaceItem{ID: "x"}, Host: "mbp\n"},
		"bad utf8":  {ID: "p", Item: core.WorkspaceItem{ID: "x"}, Host: "mbp\xff"},
	} {
		rep := read(core.WorkspaceReport{State: core.Connected, Placements: []core.Placement{p, ok}})
		if len(rep.Placements) != 1 || rep.Placements[0].ID != "ok" || !rep.Truncated {
			t.Errorf("%s: the placement must be dropped and the report marked: %+v", name, rep)
		}
		rep = read(core.WorkspaceReport{State: core.Connected, Ambiguous: []core.Ambiguity{{Item: p.Item, Candidates: []core.Placement{p, ok}}}})
		if p.Item.Host != "" {
			if len(rep.Ambiguous) != 0 || !rep.Truncated {
				t.Errorf("%s: an ambiguity about a bad host is dropped: %+v", name, rep)
			}
			continue
		}
		if len(rep.Ambiguous) != 1 || len(rep.Ambiguous[0].Candidates) != 1 || !rep.Ambiguous[0].Truncated {
			t.Errorf("%s: a bad candidate is dropped and marked: %+v", name, rep.Ambiguous)
		}
	}
}

func TestStaleAndTruncatedPassThroughAndMakeTheReportWorthReporting(t *testing.T) {
	rep := read(core.WorkspaceReport{State: core.Connected, Stale: true, Truncated: true})
	if !rep.Stale || !rep.Truncated || rep.Empty() {
		t.Fatalf("%+v", rep)
	}
	if rep := read(core.WorkspaceReport{State: core.Connected}); rep.Stale || rep.Truncated {
		t.Fatalf("%+v", rep)
	}
	if !(core.WorkspaceReport{State: core.NotConnected}).Empty() || (core.WorkspaceReport{Truncated: true}).Empty() {
		t.Fatal("Empty must count a cut read")
	}
}

type planMod struct {
	*wsMod
	plan func(core.Placement) core.OpenPlan
}

func (p planMod) Open(pl core.Placement) core.OpenPlan { return p.plan(pl) }

func TestEveryPlacementCarriesItsCleanedOpenPlanInJSON(t *testing.T) {
	var asked []core.Placement
	m := planMod{newWS(core.WorkspaceReport{State: core.Connected,
		Placements: []core.Placement{{ID: "p1", Item: core.WorkspaceItem{Kind: "run", ID: "x"}, Host: "mbp", Label: "l\x1b[2J"}},
		Ambiguous:  []core.Ambiguity{{Item: core.WorkspaceItem{Kind: "run", ID: "y"}, Candidates: []core.Placement{{ID: "p2", Host: "mbp"}, {ID: "p3"}}}}}, nil),
		func(p core.Placement) core.OpenPlan {
			asked = append(asked, p)
			if p.ID == "p3" {
				return core.OpenPlan{} // nil Rungs
			}
			return core.OpenPlan{Rungs: []core.OpenRung{{Name: "handoff", Argv: []string{"rex", "-S", p.Host}, Interactive: true}}, Copy: "rex -S " + p.Host, Note: "n\x1b[31m"}
		}}
	rep := core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, someItems)
	p := rep.Placements[0]
	if p.Open == nil || len(p.Open.Rungs) != 1 || p.Open.Rungs[0].Argv[2] != "mbp" || !p.Open.Rungs[0].Interactive || p.Open.Copy != "rex -S mbp" || p.Open.Note != "n[31m" {
		t.Fatalf("%+v", p.Open)
	}
	if asked[0].Label != "l[2J" {
		t.Fatalf("the provider's Open must see the cleaned placement: %+v", asked[0])
	}
	cands := rep.Ambiguous[0].Candidates
	if cands[0].Open == nil || cands[1].Open == nil || cands[1].Open.Rungs == nil {
		t.Fatalf("candidates carry plans too: %+v", cands)
	}
	b, _ := json.Marshal(rep)
	for _, want := range []string{`"open":{"rungs":[{"name":"handoff","argv":["rex","-S","mbp"],"interactive":true}],"copy":"rex -S mbp"`, `"open":{"rungs":[]}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	if strings.Contains(string(b), `null`) {
		t.Errorf("no null in %s", b)
	}
}

func TestAProviderCannotSmuggleItsOwnOpenPlan(t *testing.T) {
	evil := core.OpenPlan{Rungs: []core.OpenRung{{Name: "x", Argv: []string{"sh", "-c", "evil"}}}}
	m := newWS(core.WorkspaceReport{State: core.Connected, Placements: []core.Placement{{ID: "p", Item: core.WorkspaceItem{ID: "x"}, Open: &evil}}}, nil)
	rep := core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, someItems)
	if rep.Placements[0].Open == nil || len(rep.Placements[0].Open.Rungs) != 0 {
		t.Fatalf("core builds the plan from the provider's Open, not from the report: %+v", rep.Placements[0].Open)
	}
}

func TestCleanPlanSanitizesEveryStringAndDropsInsteadOfAltering(t *testing.T) {
	long := strings.Repeat("x", 257)
	m := planMod{newWS(core.WorkspaceReport{State: core.Connected, Placements: []core.Placement{{ID: "p", Item: core.WorkspaceItem{ID: "x"}}}}, nil),
		func(core.Placement) core.OpenPlan {
			return core.OpenPlan{
				Rungs: []core.OpenRung{
					{Name: "good\x1b[2J" + strings.Repeat("n", 300), Argv: []string{"a", "b"}},
					{Name: "ctrl-arg", Argv: []string{"a", "b\x1b[2J"}},
					{Name: "bidi-arg", Argv: []string{"a\u202eb"}},
					{Name: "newline-arg", Argv: []string{"a\nb"}},
					{Name: "long-arg", Argv: []string{long}},
					{Name: "empty", Argv: nil},
					{Name: "edge", Argv: []string{strings.Repeat("y", 256)}},
				},
				Copy: "copy\x1b[2J", Note: strings.Repeat("N", 400)}
		}}
	plan := *core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, someItems).Placements[0].Open
	if len(plan.Rungs) != 2 || plan.Rungs[0].Argv[1] != "b" || len([]rune(plan.Rungs[1].Argv[0])) != 256 {
		t.Fatalf("only clean rungs survive, unchanged: %+v", plan.Rungs)
	}
	if got := plan.Rungs[0].Name; strings.ContainsRune(got, 0x1b) || len([]rune(got)) != core.MaxIDRunes {
		t.Errorf("name: %q", got)
	}
	if plan.Copy != "" {
		t.Errorf("a copy line that would change is dropped, not altered: %q", plan.Copy)
	}
	if len([]rune(plan.Note)) != 300 {
		t.Errorf("note: %d", len([]rune(plan.Note)))
	}
	// Copy limits
	for n, want := range map[int]string{512: strings.Repeat("c", 512), 513: ""} {
		m.plan = func(core.Placement) core.OpenPlan { return core.OpenPlan{Copy: strings.Repeat("c", n)} }
		if got := core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, someItems).Placements[0].Open.Copy; got != want {
			t.Errorf("copy of %d runes: got %d runes", n, len(got))
		}
	}
	// at most eight rungs
	m.plan = func(core.Placement) core.OpenPlan {
		var rs []core.OpenRung
		for i := 0; i < 12; i++ {
			rs = append(rs, core.OpenRung{Name: fmt.Sprint(i), Argv: []string{"a"}})
		}
		return core.OpenPlan{Rungs: rs}
	}
	if got := core.ReadWorkspace(context.Background(), []core.Entry{entry(m, core.Ready, "")}, someItems).Placements[0].Open.Rungs; len(got) != 8 {
		t.Errorf("%d rungs", len(got))
	}
}

func TestOpenPlanJSONNeverHasNullRungsOrArgv(t *testing.T) {
	b, _ := json.Marshal(core.OpenPlan{})
	if string(b) != `{"rungs":[]}` {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(core.OpenPlan{Rungs: []core.OpenRung{{Name: "n"}}})
	if string(b) != `{"rungs":[{"name":"n","argv":[],"interactive":false}]}` {
		t.Fatal(string(b))
	}
}

// ReadWorkspaceFrom gives the provider a whole budget of one second, however many hosts it reads.
func TestWorkspaceReadBudgetIsOneSecond(t *testing.T) {
	var left time.Duration
	p := budgetProvider(func(ctx context.Context) {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Error("no deadline")
		}
		left = time.Until(dl)
	})
	core.ReadWorkspaceFrom(context.Background(), p, someItems)
	if left > time.Second || left < 900*time.Millisecond {
		t.Fatalf("budget %s, want about one second", left)
	}
	// a caller's shorter deadline still wins
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	core.ReadWorkspaceFrom(ctx, p, someItems)
	if left > 100*time.Millisecond {
		t.Fatalf("%s", left)
	}
}

type budgetProvider func(context.Context)

func (b budgetProvider) Workspace(ctx context.Context, _ []core.WorkspaceItem) (core.WorkspaceReport, error) {
	b(ctx)
	return core.WorkspaceReport{}, nil
}
func (budgetProvider) Open(core.Placement) core.OpenPlan { return core.OpenPlan{} }

func TestNoItemsAndNoProviderNeverCallTheProvider(t *testing.T) {
	m := newWS(core.WorkspaceReport{State: core.Connected, Placements: placements(1)}, nil)
	if rep := core.ReadWorkspaceFrom(context.Background(), m, nil); !rep.Empty() || rep.State != "" || m.asked != nil {
		t.Fatalf("%+v asked %v", rep, m.asked)
	}
	if rep := core.ReadWorkspaceFrom(context.Background(), m, []core.WorkspaceItem{}); !rep.Empty() || m.asked != nil {
		t.Fatalf("%+v", rep)
	}
	if rep := core.ReadWorkspaceFrom(context.Background(), nil, someItems); !rep.Empty() || rep.State != "" {
		t.Fatalf("%+v", rep)
	}
}

func TestProviderErrorIsDegradedWithABoundedReason(t *testing.T) {
	m := newWS(core.WorkspaceReport{}, errors.New("boom \x1b[31m"+strings.Repeat("r", 500)))
	rep := core.ReadWorkspaceFrom(context.Background(), m, someItems)
	if rep.State != core.Degraded || !strings.HasPrefix(rep.Reason, "boom [31mrrr") || len([]rune(rep.Reason)) != 200 || rep.Empty() {
		t.Fatalf("%q", rep.Reason)
	}
}

func TestForFindsByKindIDAndHostNotJustID(t *testing.T) {
	run := core.WorkspaceItem{Kind: core.WorkspaceRun, ID: "x", Host: "mbp"}
	task := core.WorkspaceItem{Kind: core.WorkspaceTask, ID: "x"}
	other := core.WorkspaceItem{Kind: core.WorkspaceRun, ID: "x", Host: "other"}
	rep := core.WorkspaceReport{Placements: []core.Placement{{ID: "pt", Item: task}, {ID: "pr", Item: run}},
		Ambiguous: []core.Ambiguity{{Item: other, Candidates: []core.Placement{{ID: "c1"}, {ID: "c2"}}}}}
	if p, c := rep.For(run); p == nil || p.ID != "pr" || c != nil {
		t.Fatalf("%+v %+v", p, c)
	}
	if p, _ := rep.For(task); p == nil || p.ID != "pt" {
		t.Fatalf("%+v", p)
	}
	if p, c := rep.For(other); p != nil || len(c) != 2 {
		t.Fatalf("%+v %+v", p, c)
	}
	if p, c := rep.For(core.WorkspaceItem{Kind: core.WorkspaceLease, ID: "x"}); p != nil || c != nil {
		t.Fatalf("a lease of the same id is nowhere: %+v %+v", p, c)
	}
}

func TestShellJoinQuotesEqualsAndPercent(t *testing.T) {
	got := core.ShellJoin([]string{"FOO=bar", "=x", "a=b", "%1", "100%", "a%b", "-S", "ok"})
	want := `'FOO=bar' '=x' 'a=b' '%1' '100%' 'a%b' -S ok`
	if got != want {
		t.Fatalf("%q != %q", got, want)
	}
}

func TestClipRunesBoundaries(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{{"abc", 3, "abc"}, {"abc", 2, "ab"}, {"abc", 4, "abc"}, {"abc", 0, ""}, {"", 0, ""}, {"日本語", 3, "日本語"}, {"日本語", 2, "日本"}, {"a\xffb", 2, "a\ufffd"}} {
		if got := core.ClipRunes(tc.in, tc.n); got != tc.want {
			t.Errorf("ClipRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// With the workspace read overlapping the others, a provider that waits for the board read to
// start (and a board that waits for the workspace read to start) can only finish if both run at
// once.
type rendezvousMod struct {
	*modtest.Fake
	ws, board chan struct{}
}

func (m rendezvousMod) Board(context.Context) (core.BoardReport, error) {
	close(m.board)
	select {
	case <-m.ws:
	case <-time.After(3 * time.Second):
		return core.BoardReport{}, errors.New("the workspace read did not start while the board was read")
	}
	return core.BoardReport{}, nil
}
func (m rendezvousMod) Queues(context.Context) (core.QueuesReport, error) {
	return core.QueuesReport{Runs: []core.QueueRun{{Issue: "r1"}}}, nil
}
func (m rendezvousMod) Workspace(context.Context, []core.WorkspaceItem) (core.WorkspaceReport, error) {
	close(m.ws)
	select {
	case <-m.board:
	case <-time.After(3 * time.Second):
		return core.WorkspaceReport{}, errors.New("the board read did not start while the workspace was read")
	}
	return core.WorkspaceReport{State: core.Connected, Placements: []core.Placement{{ID: "p", Item: core.WorkspaceItem{Kind: "run", ID: "r1"}}}}, nil
}
func (rendezvousMod) Open(core.Placement) core.OpenPlan { return core.OpenPlan{} }

func TestTheWorkspaceReadOverlapsTheOtherReads(t *testing.T) {
	m := rendezvousMod{Fake: &modtest.Fake{IDStr: "x"}, ws: make(chan struct{}), board: make(chan struct{})}
	d := core.ReadDomains(context.Background(), []core.Entry{entry(m, core.Ready, "")})
	if d.Board.State == core.NotConnected || d.Workspace == nil || len(d.Workspace.Placements) != 1 || d.Workspace.State != core.Connected {
		t.Fatalf("board %+v workspace %+v", d.Board.Header, d.Workspace)
	}
	if strings.Contains(d.Board.Reason, "did not start") {
		t.Fatalf("%s", d.Board.Reason)
	}
}

func TestASlowWorkspaceProviderCannotStallDomainsPastItsBudget(t *testing.T) {
	m := hangMod{Fake: &modtest.Fake{IDStr: "x"}}
	start := time.Now()
	d := core.ReadDomains(context.Background(), []core.Entry{entry(m, core.Ready, "")})
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("took %s", el)
	}
	if d.Workspace == nil || d.Workspace.State != core.Degraded {
		t.Fatalf("%+v", d.Workspace)
	}
}

type hangMod struct{ *modtest.Fake }

func (hangMod) Queues(context.Context) (core.QueuesReport, error) {
	return core.QueuesReport{Runs: []core.QueueRun{{Issue: "r1"}}}, nil
}
func (hangMod) Workspace(ctx context.Context, _ []core.WorkspaceItem) (core.WorkspaceReport, error) {
	<-ctx.Done()
	return core.WorkspaceReport{}, ctx.Err()
}
func (hangMod) Open(core.Placement) core.OpenPlan { return core.OpenPlan{} }
