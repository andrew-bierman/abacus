package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
		{ID: hostile, Item: core.WorkspaceItem{Kind: "run", ID: hostile, Host: hostile}, Host: hostile, Session: hostile, Tab: hostile, Label: hostile, Match: core.PlacementMatch(hostile)},
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
