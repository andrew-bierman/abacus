package tui

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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
