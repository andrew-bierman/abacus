package agents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

type fakeRun struct {
	out   string
	exit  int
	err   error
	calls [][]string
}

func (f *fakeRun) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, args)
	return []byte(f.out), f.exit, f.err
}

func monitorWith(r core.Runner) *Monitor {
	m := NewMonitor(MonitorOptions{Runner: r, Bin: "/fake/monitor"})
	m.SetFound(core.Found{Bin: "/fake/monitor", Src: core.SourceOverride})
	return m
}

func TestParseSnapshotPinnedShape(t *testing.T) {
	agents, extra, err := ParseSnapshot([]byte(fixture(t, "monitor-v0.5.5.json")))
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 3 { // the session without a pid or name is skipped
		t.Fatalf("%d agents: %+v", len(agents), agents)
	}
	a := agents[0]
	if a.Harness != core.HarnessClaude || a.PID != 4101 || a.State != core.AgentRunning || a.Source != core.EvidenceMonitor ||
		a.ContextPct == nil || *a.ContextPct != 72 || a.Tokens == nil || *a.Tokens != 150000 || a.Task != "Pear counter" || a.Branch != "feat/pears" {
		t.Fatalf("%+v", a)
	}
	if len(a.Children) != 2 || len(a.Ports) != 1 || a.Ports[0].Port != 5173 || a.Children[0].Port != 0 || len(a.Subs) != 1 || a.Subs[0].State != "working" {
		t.Fatalf("children/ports/subagents: %+v", a)
	}
	b := agents[1]
	if b.State != core.AgentWaiting || b.ContextPct != nil { // a zero context window means unknown, not 0%
		t.Fatalf("%+v", b)
	}
	if c := agents[2]; c.State != core.AgentUnknown {
		t.Fatalf("an activity word this build has not heard of must be unknown: %+v", c)
	}
	if len(extra.RateLimits) != 2 || extra.RateLimits[0].Harness != core.HarnessClaude || *extra.RateLimits[0].FiveHourPct != 35 ||
		extra.RateLimits[0].FiveHourResets == "" || extra.RateLimits[1].SevenDayPct != nil {
		t.Fatalf("%+v", extra.RateLimits)
	}
	if len(extra.OrphanPorts) != 1 || extra.OrphanPorts[0].Port != 4000 {
		t.Fatalf("%+v", extra.OrphanPorts)
	}
	if extra.TokenRate == nil || *extra.TokenRate != 300 {
		t.Fatalf("token rate per second: %v", extra.TokenRate)
	}
}

func TestParseSnapshotDrift(t *testing.T) {
	for name, in := range map[string]string{
		"not json":      `<html>`,
		"empty":         ``,
		"other shape":   `{"agents":[{"pid":1}]}`,
		"sessions null": `{"sessions":null}`,
		"sessions type": `{"sessions":"x"}`,
	} {
		if _, _, err := ParseSnapshot([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// extra and missing fields are fine
	if a, _, err := ParseSnapshot([]byte(`{"sessions":[{"agent_cli":"claude","pid":7,"status":"Done","brand_new":{"x":1}}],"future":true}`)); err != nil || len(a) != 1 || a[0].State != core.AgentStopped {
		t.Fatalf("%v %v", a, err)
	}
}

func TestMonitorRunsOnlyTheSnapshotArgv(t *testing.T) {
	f := &fakeRun{out: fixture(t, "monitor-v0.5.5.json")}
	m := monitorWith(f)
	if _, _, err := m.Agents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || strings.Join(f.calls[0], " ") != "--json" {
		t.Fatalf("%v", f.calls)
	}
	for _, bad := range [][]string{{"--setup"}, {"--update"}, {"--json", "--demo"}, {}, {"--once"}, {"update"}} {
		if _, _, err := m.run.Run(context.Background(), "x", bad...); err == nil {
			t.Errorf("argv %v was allowed", bad)
		}
	}
	if len(f.calls) != 1 {
		t.Fatalf("a refused argv reached the runner: %v", f.calls)
	}
}

func TestMonitorFailuresAreErrorsNotPanics(t *testing.T) {
	for name, f := range map[string]*fakeRun{
		"timeout": {err: core.ErrTimedOut}, "exit": {exit: 2}, "garbage": {out: "nope"}, "cannot run": {err: errors.New("exec format error")},
	} {
		if _, _, err := monitorWith(f).Agents(context.Background()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestMonitorEnrichAndDetectAbsent(t *testing.T) {
	var saw int
	m := NewMonitor(MonitorOptions{Runner: &fakeRun{out: fixture(t, "monitor-v0.5.5.json")}, Enrich: func(l []core.Agent) { saw = len(l) }})
	m.SetFound(core.Found{Bin: "/fake"})
	if _, _, err := m.Agents(context.Background()); err != nil || saw != 3 {
		t.Fatalf("%v %d", err, saw)
	}
	abs := NewMonitor(MonitorOptions{LookPath: func(string) (string, error) { return "", errors.New("no") }})
	if d := abs.Detect(context.Background()); d.State != core.Absent {
		t.Fatalf("%+v", d)
	}
}

func TestKeepEnvDropsEverythingButTheAllowlist(t *testing.T) {
	got := keepEnv([]string{"HOME=/h", "AWS_SECRET_ACCESS_KEY=x", "ANTHROPIC_API_KEY=y", "PATH=/bin", "BEADS_DIR=/z", "CODEX_HOME=/c"})
	if strings.Join(got, ",") != "HOME=/h,PATH=/bin,CODEX_HOME=/c" {
		t.Fatalf("%v", got)
	}
}
