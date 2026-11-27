package serve_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// ---- demo mode offers the setup steps and the settings as plans ----

func demoClient(t *testing.T) (*client, *fixture) {
	t.Helper()
	f := newFixture(t)
	f.cfg.Demo = true
	c := startClient(t, f.cfg)
	c.init()
	return c, f
}

func TestDemoSetupStateListsTheStepsOfAFirstRun(t *testing.T) {
	c, f := demoClient(t)
	var rep core.SetupReport
	c.call("setup.state", nil).into(t, &rep)
	got := map[core.StepID]core.StepState{}
	for _, s := range rep.Steps {
		got[s.Step] = s.State
	}
	want := map[core.StepID]core.StepState{core.StepWorkspace: core.StepNeeded, core.StepQueues: core.StepBlocked, core.StepActor: core.StepNeeded}
	if len(got) != len(want) || got[core.StepWorkspace] != want[core.StepWorkspace] || got[core.StepQueues] != want[core.StepQueues] || got[core.StepActor] != want[core.StepActor] {
		t.Fatalf("steps %v", got)
	}
	if rep.PlanDigest != core.DemoDigest(core.SetupDigest(core.DemoSetupStepPlans())) {
		t.Errorf("the digest is not a demo digest: %s", rep.PlanDigest)
	}
	// the same report as `setup --demo --json`, and as its --dry-run
	for _, args := range [][]string{{"setup", "--demo", "--json"}, {"setup", "--demo", "--dry-run", "--json"}} {
		code, out := cliRun(t, f, args...)
		if code != 0 {
			t.Fatalf("%v: %d %s", args, code, out)
		}
		var cli, srv map[string]json.RawMessage
		_ = json.Unmarshal([]byte(out), &cli)
		_ = json.Unmarshal(c.call("setup.state", nil).Result, &srv)
		if compact(t, string(cli["steps"])) != string(srv["steps"]) || string(cli["plan_digest"]) != string(srv["plan_digest"]) || string(cli["demo"]) != "true" {
			t.Errorf("%v differs from setup.state:\n%s\n%s", args, out, c.call("setup.state", nil).Result)
		}
	}
}

func TestDemoPlanSetupAndConfigAreHeldAndNeverApplied(t *testing.T) {
	c, f := demoClient(t)
	for _, step := range []string{"workspace", "actor"} {
		p := plan(t, c, "plan.setup", map[string]any{"step": step})
		_, _, raw, _ := core.DemoSetupPlan(core.StepID(step))
		if p.Kind != "setup" || p.Op != step || len(p.Lines) == 0 || len(p.Argv) == 0 || p.PlanDigest != core.DemoDigest(raw) {
			t.Errorf("%s: %+v", step, p)
		}
		e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeRefused)
		if !strings.Contains(e.Message, "demo") {
			t.Errorf("apply: %q", e.Message)
		}
	}
	for _, step := range []string{"queues", "repair"} { // blocked / nothing to repair: not offered, as for a real one
		c.call("plan.setup", map[string]any{"step": step}).wantErr(t, core.CodeRefused)
	}
	c.call("plan.setup", map[string]any{"step": "bogus"}).wantErr(t, core.CodeUsage)
	p := plan(t, c, "plan.config.set", map[string]any{"key": "actor", "value": "mei"})
	if p.Kind != "config" || p.Op != "set actor" {
		t.Errorf("%+v", p)
	}
	if e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeRefused); !strings.Contains(e.Message, "demo") {
		t.Errorf("apply: %q", e.Message)
	}
	// a bad value is still a usage error, and nothing was audited or written anywhere
	c.call("plan.config.set", map[string]any{"key": "actor", "value": "bad name"}).wantErr(t, core.CodeUsage)
	if n := len(f.audit.entries); n != 0 {
		t.Errorf("%d audit entries", n)
	}
	if _, err := os.Stat(f.cfg.Config.Path); err == nil {
		t.Error("the settings file was written")
	}
}

// A demo digest is never the digest of the same plan on real data.
func TestDemoConfigDigestIsNotTheRealOne(t *testing.T) {
	c, f := demoClient(t)
	// a real settings file that says otherwise is never read: the plan is made against an empty one
	if err := f.cfg.Config.Update(func(cfg *core.Config) error { return cfg.Set("actor", "someone-real") }); err != nil {
		t.Fatal(err)
	}
	d := plan(t, c, "plan.config.set", map[string]any{"key": "actor", "value": "mei"})
	r, _ := readyClient(t)
	real := plan(t, r, "plan.config.set", map[string]any{"key": "actor", "value": "mei"})
	if d.PlanDigest == real.PlanDigest || d.PlanDigest != core.DemoDigest(real.PlanDigest) {
		t.Errorf("demo %s real %s", d.PlanDigest, real.PlanDigest)
	}
}

// ---- plan.config.set takes a list for an argv key ----

func TestPlanConfigSetTakesAListForAgentCommand(t *testing.T) {
	c, f := readyClient(t)
	words := []string{"claude", "Work on {task_id}: {title} & more"}
	list := plan(t, c, "plan.config.set", map[string]any{"key": "agent_command", "value": words})
	text := plan(t, c, "plan.config.set", map[string]any{"key": "agent_command", "value": `["claude","Work on {task_id}: {title} & more"]`})
	if !slices.Equal(list.Argv, words) || list.PlanDigest != text.PlanDigest || !slices.Equal(list.Lines, text.Lines) {
		t.Fatalf("list %+v\ntext %+v", list, text)
	}
	apply(t, c, list.PlanID, list.PlanDigest)
	if cfg, _ := f.cfg.Config.Load(); !slices.Equal(cfg.AgentCommand, words) {
		t.Errorf("config: %+v", cfg)
	}
}

func TestPlanConfigSetListValueIsValidatedAndOnlyForArgvKeys(t *testing.T) {
	c, _ := readyClient(t)
	long := make([]string, 17)
	for i := range long {
		long[i] = "a"
	}
	for name, bad := range map[string]map[string]any{
		"empty list":       {"key": "agent_command", "value": []string{}},
		"too many words":   {"key": "agent_command", "value": long},
		"empty program":    {"key": "agent_command", "value": []string{"", "x"}},
		"unknown holder":   {"key": "agent_command", "value": []string{"claude", "{nope}"}},
		"list for a text":  {"key": "actor", "value": []string{"mei"}},
		"list for a path":  {"key": "tracker_dir", "value": []string{"/x"}},
		"null word":        {"key": "agent_command", "value": []any{"claude", nil}},
		"null word text":   {"key": "agent_command", "value": `["claude",null]`},
		"non-string words": {"key": "agent_command", "value": []any{"claude", 1}},
		"number":           {"key": "actor", "value": 3},
		"object":           {"key": "agent_command", "value": map[string]any{"a": "b"}},
		"bool":             {"key": "search_history", "value": false},
		"null":             {"key": "actor", "value": nil},
	} {
		if r := c.call("plan.config.set", bad); r.Error == nil || r.Error.Data.Code != core.CodeUsage {
			t.Errorf("%s: %+v %s", name, r.Error, r.Result)
		}
	}
	// the text form still clears
	p := plan(t, c, "plan.config.set", map[string]any{"key": "agent_command", "value": ""})
	if len(p.Argv) != 0 {
		t.Errorf("%+v", p)
	}
}

// config.show in demo mode is the demo store: the real settings file is never read, and its path never shown.
func TestDemoConfigShowNeverReadsTheRealFile(t *testing.T) {
	c, f := demoClient(t)
	if err := f.cfg.Config.Update(func(cfg *core.Config) error { return cfg.Set("actor", "real-actor-name") }); err != nil {
		t.Fatal(err)
	}
	r := c.call("config.show", nil)
	var got core.ConfigShow
	r.into(t, &got)
	if got.Path != core.DemoConfigPath || got.Actor != "" || strings.Contains(r.Raw, "real-actor-name") || strings.Contains(r.Raw, f.cfg.Config.Path) {
		t.Errorf("%s", r.Raw)
	}
	if string(mustMap(t, r.Result)["demo"]) != "true" {
		t.Errorf("not flagged demo: %s", r.Raw)
	}
}

// The list form and every spelling of the same words as text are one plan: one digest, one stored value.
func TestPlanConfigSetAgentCommandIsCanonical(t *testing.T) {
	c, f := readyClient(t)
	list := plan(t, c, "plan.config.set", map[string]any{"key": "agent_command", "value": []string{"claude", "a & {task_id}"}})
	for _, text := range []string{`["claude","a & {task_id}"]`, ` [ "claude" ,  "a & {task_id}" ] `} {
		p := plan(t, c, "plan.config.set", map[string]any{"key": "agent_command", "value": text})
		if p.PlanDigest != list.PlanDigest || !slices.Equal(p.Lines, list.Lines) {
			t.Errorf("%q: %+v vs %+v", text, p, list)
		}
	}
	apply(t, c, list.PlanID)
	var shown core.ConfigShow
	c.call("config.show", nil).into(t, &shown)
	if got, _ := shown.Config.Get("agent_command"); got != `["claude","a & {task_id}"]` {
		t.Errorf("stored %q", got)
	}
	_ = f
	// only "" clears (a blank string too): an empty list is refused
	if a, b := plan(t, c, "plan.config.set", map[string]any{"key": "agent_command", "value": ""}), plan(t, c, "plan.config.set", map[string]any{"key": "agent_command", "value": "  "}); a.PlanDigest != b.PlanDigest {
		t.Error("blank and empty are different clears")
	}
	c.call("plan.config.set", map[string]any{"key": "agent_command", "value": []string{}}).wantErr(t, core.CodeUsage)
}
