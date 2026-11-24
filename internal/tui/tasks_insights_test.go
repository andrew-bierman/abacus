package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
)

// insightsAt is the Tasks tab on the Insights view over the demo tracker and the demo report.
func insightsAt(w, h int) *tasksSection {
	s, _, _ := editAt(w, h)
	s.Update(insightsMsg{report: fixtureInsights()})
	send(s, "V")
	return s
}

func TestInsightsShowsEverySectionOfTheReport(t *testing.T) {
	s := insightsAt(100, 60)
	v := s.View()
	for _, want := range []string{"Next picks", "Blockers to clear", "Bottlenecks · betweenness", "Critical path · impact depth", "Influence · centrality", "Cycles", "Parallel tracks",
		"ac-12", "unblocks 2", "4.50", "0.61", "ac-20 → ac-21 → ac-20", "track-A: ac-12 ac-09", "3 actionable · 4 blocked · 1 cycle"} {
		if !strings.Contains(v, want) {
			t.Errorf("lacks %q:\n%s", want, v)
		}
	}
}

func TestInsightsNamesNoTool(t *testing.T) {
	for _, w := range []int{60, 100} {
		v := strings.ToLower(insightsAt(w, 60).View())
		for _, tool := range []string{"bv ", "bd ", "beads", "lambe", "triage", "graph engine"} {
			if strings.Contains(v, tool) {
				t.Errorf("%d: the view names %q", w, tool)
			}
		}
	}
}

// The numbers shown are the report's, in its order (parity with the engine: the view adds nothing and drops nothing).
func TestInsightsRowsAreTheEnginesReportInItsOrder(t *testing.T) {
	snap := fixtureSnapshot()
	rep := engineInsights(t, snap)
	s, _, _ := editAt(120, 200)
	s.Update(insightsMsg{report: rep})
	send(s, "V")
	var want []string
	for _, p := range rep.TopPicks {
		want = append(want, p.ID)
	}
	have := flatInsights(s.insightSections())
	var got []string
	for _, e := range have {
		if strings.HasPrefix(e.key, "next/") {
			got = append(got, e.id)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("next picks %v, the engine says %v", got, want)
	}
	n := len(rep.TopPicks) + len(rep.Blockers) + len(rep.Bottlenecks) + len(rep.CriticalPath) + len(rep.Influencers) + len(rep.Cycles) + len(rep.Tracks)
	if len(have) != n {
		t.Errorf("%d rows for %d report entries", len(have), n)
	}
	v := s.View()
	for _, b := range rep.Bottlenecks {
		if !strings.Contains(v, b.ID) || !strings.Contains(v, metric(b.Value)) {
			t.Errorf("bottleneck %s %v is not shown", b.ID, b.Value)
		}
	}
}

func TestInsightsCursorMovesAcrossSectionsAndStaysOnItsTaskThroughARefresh(t *testing.T) {
	s := insightsAt(100, 60)
	for i := 0; i < 4; i++ {
		send(s, "j")
	}
	sel := s.insNav.sel
	if !strings.HasPrefix(sel, "blocker/") {
		t.Fatalf("the cursor crossed into the second section: %q", sel)
	}
	rep := fixtureInsights()
	rep.TopPicks = append([]core.Pick{{ID: "ac-36", Title: "verify tool flags against help"}}, rep.TopPicks...) // a new pick above the cursor
	s.Update(insightsMsg{report: rep})
	_ = s.View()
	if s.insNav.sel != sel || !strings.HasPrefix(flatInsights(s.insightSections())[s.insNav.cur].key, "blocker/") {
		t.Errorf("the cursor follows its task, not its row number: %q cur %d", s.insNav.sel, s.insNav.cur)
	}
	send(s, "k", "k", "k", "k", "k", "k", "k", "k", "k")
	if s.insNav.cur != 0 {
		t.Errorf("k stops at the top: %d", s.insNav.cur)
	}
}

func TestInsightsEnterOpensTheTaskInTheListAndGShowsItsGraph(t *testing.T) {
	s := insightsAt(100, 40)
	send(s, "j") // ac-15, the second pick
	send(s, "enter")
	cur, _ := s.current()
	if s.list.cur != viewList || cur.ID != "ac-15" || !s.list.detailFocus {
		t.Fatalf("enter opens the task: view %v on %q detail %v", s.list.cur, cur.ID, s.list.detailFocus)
	}
	s = insightsAt(100, 40)
	send(s, "j", "g")
	if s.list.cur != viewGraph || s.graph.focus != "ac-15" {
		t.Errorf("g shows the graph of the task: view %v focus %q", s.list.cur, s.graph.focus)
	}
	s = insightsAt(60, 40) // stacked: the detail takes the screen
	send(s, "j", "enter")
	if !s.drilled {
		t.Error("stacked, enter drills into the detail as the list does")
	}
}

func TestInsightsEnterOnATaskTheListHidesClearsTheFilterAndSaysSo(t *testing.T) {
	s := insightsAt(100, 40)
	rep := fixtureInsights()
	rep.Blockers = append(rep.Blockers, core.Pick{ID: "ac-03", Title: "retire the old poller"}) // closed: the default quick filter hides it
	s.Update(insightsMsg{report: rep})
	for i := 0; i < 20; i++ {
		send(s, "j")
		if strings.HasSuffix(s.insNav.sel, "/ac-03") {
			break
		}
	}
	send(s, "enter")
	if cur, _ := s.current(); cur.ID != "ac-03" || !strings.Contains(s.notice, "filter cleared") {
		t.Errorf("the closed task is shown: %q notice %q", cur.ID, s.notice)
	}
}

func TestInsightsSortOrdersRowsAndKeepsTheCursorOnItsTask(t *testing.T) {
	s := insightsAt(100, 60)
	rank := func() string {
		var ids []string
		for _, e := range flatInsights(s.insightSections()) {
			if strings.HasPrefix(e.key, "critical/") {
				ids = append(ids, e.id)
			}
		}
		return strings.Join(ids, ",")
	}
	if rank() != "ac-12,ac-09,ac-15,ac-16" {
		t.Fatalf("rank order %s", rank())
	}
	send(s, "s")
	if rank() != "ac-09,ac-12,ac-15,ac-16" || !strings.Contains(s.View(), "sort: id ↑") {
		t.Errorf("s sorts by id: %s", rank())
	}
	send(s, "S")
	if rank() != "ac-16,ac-15,ac-12,ac-09" || !strings.Contains(s.View(), "sort: id ↓") {
		t.Errorf("S reverses: %s", rank())
	}
	send(s, "s")
	if !strings.Contains(s.View(), "sort: title") {
		t.Errorf("s again sorts by title")
	}
	send(s, "s")
	send(s, "S") // rank again, ascending
	if rank() != "ac-12,ac-09,ac-15,ac-16" || strings.Contains(s.View(), "sort:") {
		t.Errorf("back to the engine's order: %s", rank())
	}
	// cycles and tracks keep the engine's order whatever the sort
	send(s, "s")
	for _, e := range flatInsights(s.insightSections()) {
		if strings.HasPrefix(e.key, "track/") && e.id != "ac-12" && e.id != "ac-15" {
			t.Errorf("track %q", e.key)
		}
	}
}

func TestInsightsEmptySectionsAreSaidNotHidden(t *testing.T) {
	s, _, _ := editAt(100, 40)
	rep := fixtureInsights()
	rep.TopPicks, rep.Blockers, rep.Bottlenecks, rep.CriticalPath, rep.Influencers, rep.Tracks = nil, nil, nil, nil, nil, nil
	rep.CriticalPathNote = "critical path skipped: the graph has a dependency cycle"
	s.Update(insightsMsg{report: rep})
	send(s, "V")
	v := s.View()
	for _, want := range []string{"Critical path · impact depth", "critical path skipped: the graph has a dependency cycle", "Cycles", "ac-20 → ac-21 → ac-20"} {
		if !strings.Contains(v, want) {
			t.Errorf("a cycle: lacks %q:\n%s", want, v)
		}
	}
	s.Update(insightsMsg{report: core.InsightsReport{Header: core.Header{Source: core.SourceGraph, State: core.Connected}, TotalActionable: 3}})
	if v := s.View(); !strings.Contains(v, "nothing to rank yet") {
		t.Errorf("no rows and no reason says why:\n%s", v)
	}
	e := core.EmptyInsights()
	s.Update(insightsMsg{report: e})
	if v := s.View(); !strings.Contains(v, "no tasks yet") {
		t.Errorf("an empty tracker:\n%s", v)
	}
	s.Update(insightsMsg{report: core.InsightsReport{Header: core.Header{State: core.NotConnected, HowToConnect: core.HowToConnectInsights()}}})
	if v := s.View(); !strings.Contains(v, "not connected") {
		t.Errorf("not connected:\n%s", v)
	}
	send(s, "j", "enter", "g") // no rows: the keys do nothing and do not panic
}

func TestInsightsExplanationsAreOnTheHelpOverlayOfThatViewOnly(t *testing.T) {
	m := press(press(pm(t, 100, 80), "2"), "V")
	m = press(m, "?")
	v := m.View()
	for _, want := range []string{"What the Insights numbers mean", "betweenness", "PageRank-style", "impact depth", "unblocks N"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q:\n%s", want, v)
		}
	}
	m = press(press(pm(t, 100, 80), "2"), "?")
	if strings.Contains(m.View(), "What the Insights numbers mean") {
		t.Error("the list view has no insights notes")
	}
}

func TestInsightsStaysInsideItsPanelAtEverySizeWithHostileText(t *testing.T) {
	bad := "x\x1b[2J\u202etxt\u200b\u200d\ufeff\x07y\x00日本語"
	rep := fixtureInsights()
	rep.TopPicks[0].Title, rep.TopPicks[0].Reasons = bad, []string{bad, bad}
	rep.Cycles = [][]string{{"ac-20", bad, "ac-20"}}
	rep.Reason = bad
	for _, sz := range [][2]int{{40, 12}, {60, 24}, {100, 32}, {120, 40}, {20, 8}} {
		s, _, _ := editAt(sz[0], sz[1])
		s.Update(insightsMsg{report: rep})
		send(s, "V")
		for _, k := range []string{"", "j", "j", "s", "S", "j"} {
			if k != "" {
				send(s, k)
			}
			v := s.View()
			hostileOK(t, "insights", v, sz[0])
			if n := len(strings.Split(strings.TrimRight(v, "\n"), "\n")); sz[1] > 0 && n > sz[1] {
				t.Errorf("%dx%d: %d lines", sz[0], sz[1], n)
			}
		}
	}
	_ = lipgloss.Width
}

func TestInsightsKeysHaveOneOwnerAndAreInTheFooter(t *testing.T) {
	s := insightsAt(100, 40)
	got := map[string]string{}
	for _, b := range s.Bindings() {
		got[b.Help().Key] = b.Help().Desc
	}
	for k, d := range map[string]string{"enter": "open task", "g": "graph", "s/S": "sort", "v": "view"} {
		if got[k] != d {
			t.Errorf("binding %s = %q, want %q", k, got[k], d)
		}
	}
}

func TestInsightsRendersInfluencersWhenDegradedWithAReason(t *testing.T) {
	s, _, _ := editAt(100, 40)
	s.Update(insightsMsg{report: core.InsightsReport{Header: core.Header{Source: core.SourceGraph, State: core.Degraded, Reason: "partial analysis"},
		Influencers: []core.Ranked{{ID: "ac-12", Value: 0.5}}}})
	send(s, "V")
	if v := s.View(); !strings.Contains(v, "Influence · centrality") || !strings.Contains(v, "0.50") {
		t.Errorf("a degraded report still shows what it has:\n%s", v)
	}
}
