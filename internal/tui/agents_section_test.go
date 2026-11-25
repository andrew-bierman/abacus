package tui

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The Agents tab names harnesses (its subject) and nothing else: no tool abacus composes, no
// library, no command that gathers the data.
var agentsBanned = regexp.MustCompile(`(?i)\b(abtop|gopsutil|lambe|beads-lease|board-beads|b9s|bv|bd|rex|lsof|ps)\b`)

func TestAgentsTabNamesNoTool(t *testing.T) {
	files, _ := filepath.Glob("testdata/golden/agents*.golden")
	if len(files) < 8 {
		t.Fatalf("expected the Agents goldens, found %d", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, ln := range strings.Split(string(b), "\n") {
			if m := agentsBanned.FindString(ln); m != "" {
				t.Errorf("%s: %q in %q", filepath.Base(f), m, ln)
			}
		}
	}
}

func agentsModel(t *testing.T, r core.AgentsReport) tea.Model {
	m := sized(New(nil, WithTheme(asciiTheme()), WithFullView(noFullView)), 120, 36)
	m, _ = m.Update(agentsMsg{report: r})
	return press(m, "5")
}

func agentsSec(m tea.Model) *agentsSection { return m.(*Model).tabs[4].sec.(*agentsSection) }

func TestAgentsGuessesAreMarkedAndMonitorFactsAreNot(t *testing.T) {
	v := agentsModel(t, fixtureAgents()).View()
	if !strings.Contains(v, "idle~") || !strings.Contains(v, "running ") || strings.Contains(v, "running~") || strings.Contains(v, "waiting~") {
		t.Fatalf("a state learned from files carries ~ and one the monitor reported does not:\n%s", v)
	}
}

func TestAgentsFiltersSortAndSelectionFollowTheAgent(t *testing.T) {
	m := agentsModel(t, fixtureAgents())
	s := agentsSec(m)
	if len(s.visible()) != 6 {
		t.Fatal(len(s.visible()))
	}
	m = press(press(m, "j"), "j") // the running orchard agent
	sel := s.ids()[s.cursor]
	// a new read that reorders rows keeps the selection on the same agent
	r := fixtureAgents()
	for i, j := 0, len(r.Agents)-1; i < j; i, j = i+1, j-1 {
		r.Agents[i], r.Agents[j] = r.Agents[j], r.Agents[i]
	}
	m, _ = m.Update(agentsMsg{report: r})
	if got := agentsSec(m).ids()[agentsSec(m).cursor]; got != sel {
		t.Fatalf("selection moved from %s to %s", sel, got)
	}
	m = press(m, "H") // harness filter: first harness in the report order
	if len(agentsSec(m).visible()) == 6 || agentsSec(m).harness == "" {
		t.Fatal("H did not narrow")
	}
	m = press(m, "esc") // peels the harness filter
	if agentsSec(m).harness != "" || len(agentsSec(m).visible()) != 6 {
		t.Fatal("esc did not clear the harness filter")
	}
	for range agentSorts {
		m = press(m, "s")
	}
	if agentsSec(m).sortKey != 0 {
		t.Fatal("sort keys cycle")
	}
}

func TestAgentsNeverPollsWhileHiddenOrTwiceAtOnce(t *testing.T) {
	calls := 0
	s := newAgentsSection(func(_ context.Context) core.AgentsReport { calls++; return fixtureAgents() })
	if s.Init() != nil {
		t.Fatal("a hidden tab reads nothing")
	}
	cmd := s.activate()
	if cmd == nil || !s.busy {
		t.Fatal("first visit reads")
	}
	gen := s.gen
	if _, c := s.Update(agentsTickMsg{gen}); c == nil || calls != 0 { // busy: only re-arms the tick
		t.Fatal("a tick while a read is in flight must not start a second read")
	}
	s.deactivate()
	if _, c := s.Update(agentsTickMsg{gen}); c != nil {
		t.Fatal("a tick from an earlier visit is dropped")
	}
}

func TestAgentWhereIsTheRepoOrTheDirectoryName(t *testing.T) {
	for _, c := range []struct {
		a    core.Agent
		want string
	}{
		{core.Agent{Repo: "orchard", Branch: "main", Cwd: "/work/x"}, "orchard@main"},
		{core.Agent{Cwd: "/Users/someone/projects/barn"}, "barn"},
		{core.Agent{}, ""},
	} {
		if got := agentWhere(c.a); got != c.want {
			t.Errorf("%+v: %q, want %q", c.a, got, c.want)
		}
	}
}

func TestAgentItemIsNamedByWorktreeOrRepoAndNeverGuessed(t *testing.T) {
	for _, c := range []struct {
		a    core.Agent
		want string
	}{
		{core.Agent{Repo: "orchard"}, "orchard"},
		{core.Agent{Repo: "cellar", Worktree: "cellar-barrels"}, "cellar-barrels"},
		{core.Agent{Cwd: "/work/barn"}, ""},   // no repository: nothing is guessed from a path
		{core.Agent{Repo: "x; rm -rf /"}, ""}, // never an id a command line could misread
	} {
		it, ok := agentItem(c.a)
		if ok != (c.want != "") || it.ID != c.want || (ok && it.Kind != core.WorkspaceAgent) {
			t.Errorf("%+v: %+v %v, want %q", c.a, it, ok, c.want)
		}
	}
}

// The tab defends itself too: a report that skipped core's cleaning still reaches the screen safe.
func TestAgentsHostileTextNeverReachesTheScreenEvenUncleaned(t *testing.T) {
	r := core.AgentsReport{Header: core.Header{Domain: core.DomainAgents, Title: "Agents", State: core.Connected, Source: core.SourceLive},
		Agents: hostileAgentsRaw(), RateLimits: []core.RateLimit{{Harness: hostileText, FiveHourPct: ptrF(50), Stale: true}}}
	for _, w := range []int{40, 60, 120} {
		m := sized(New(nil, WithTheme(asciiTheme()), WithFullView(noFullView)), w, 30)
		m, _ = m.Update(agentsMsg{report: r})
		m = press(m, "5")
		hostileOK(t, "list", m.View(), w)
		hostileOK(t, "detail", press(m, "enter").View(), w)
	}
}

func TestAgentsDetailShowsContextTurnsChangesAndStaleLimits(t *testing.T) {
	v := agentsModel(t, fixtureAgents())
	strip := v.View()
	if !strings.Contains(strip, "7d") || !strings.Contains(strip, "↻") || !strings.Contains(strip, "30d") || !strings.Contains(strip, "stale") {
		t.Fatalf("a fresh limit has a countdown, a long window its own name, an old one is marked:\n%s", strip)
	}
	for _, ln := range strings.Split(strip, "\n") {
		if strings.Contains(ln, "Codex") && strings.Contains(ln, "↻") {
			t.Fatalf("a stale limit drops its countdown: %q", ln)
		}
	}
	m := press(press(v, "j"), "j") // the running agent
	d := press(m, "enter").View()
	if !strings.Contains(d, "144k/200k") || !strings.Contains(d, "turns") || !strings.Contains(d, "peak 21k") || !strings.Contains(d, "2 added  5 modified") {
		t.Fatalf("detail:\n%s", d)
	}
}

func TestSparkAndTokenText(t *testing.T) {
	if got := spark([]int64{0, 5, 10}, 8); got != "▁▄█" {
		t.Fatalf("%q", got)
	}
	if got := spark([]int64{1, 2, 3, 4}, 2); got != "▅█" && got != "▆█" {
		t.Fatalf("only the newest points fit: %q", got)
	}
	if spark(nil, 5) != "" || spark([]int64{1}, 0) != "" || spark([]int64{0, 0}, 4) != "▁▁" {
		t.Fatal("empty and flat series")
	}
	for n, want := range map[int64]string{950: "950", 1200: "1.2k", 84_000: "84k", 1_500_000: "1.5M"} {
		if tokenText(n) != want {
			t.Errorf("%d: %s", n, tokenText(n))
		}
	}
	if shortDuration(90*time.Second) != "1m" || shortDuration(3*time.Hour) != "3h" || shortDuration(72*time.Hour) != "3d" || shortDuration(time.Second) != "1m" {
		t.Fatal("durations")
	}
}

func TestSparkNeverPanicsOnExtremePoints(t *testing.T) {
	for _, pts := range [][]int64{{2e18, 1, 2e18}, {math.MaxInt64, math.MaxInt64, 0}, {math.MinInt64, 5, math.MaxInt64}, {-1, -2}, {math.MaxInt64}} {
		if got := spark(pts, 8); len([]rune(got)) != len(pts) {
			t.Errorf("%v: %q", pts, got)
		}
	}
	if got := spark([]int64{2e18, 1, 2e18}, 8); got != "█▁█" {
		t.Fatalf("%q", got)
	}
	// and the whole tab, with a report that skipped core's door
	a := hostileAgentsRaw()[0]
	a.TurnTokens = []int64{math.MaxInt64, 1, math.MaxInt64}
	big := int64(math.MaxInt64)
	a.ContextTokens, a.ContextWindow, a.Tokens = &big, &big, &big
	m := sized(New(nil, WithTheme(asciiTheme()), WithFullView(noFullView)), 100, 30)
	m, _ = m.Update(agentsMsg{report: core.AgentsReport{Header: core.Header{Domain: core.DomainAgents, Title: "Agents", State: core.Connected, Source: core.SourceLive}, Agents: []core.Agent{a}}})
	_ = press(press(m, "5"), "enter").View()
}
