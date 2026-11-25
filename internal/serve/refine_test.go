package serve_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
	"github.com/andrew-bierman/abacus/internal/demo"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

// ---- initialize carries the contract ----

func TestInitializeCarriesTheSameContractAsVersion(t *testing.T) {
	f := newFixture(t)
	c := startClient(t, f.cfg)
	ini := c.init()
	var v struct {
		Contract json.RawMessage `json:"contract"`
	}
	c.call("version", nil).into(t, &v)
	got, _ := json.Marshal(ini.Contract)
	if ini.Contract == nil || !bytes.Equal(got, v.Contract) {
		t.Fatalf("initialize contract %s, version contract %s", got, v.Contract)
	}
	l := ini.Capabilities.Limits
	if l.MaxInFlight == 0 || l.MaxQueued == 0 || l.PlanTTLSeconds != 600 || l.MaxRequestBytes == 0 {
		t.Errorf("limits: %+v", l)
	}
	for _, m := range []string{"plan.setup", "plan.config.set"} {
		if !slices.Contains(ini.Capabilities.Methods, m) {
			t.Errorf("%s is not announced", m)
		}
	}
	if !slices.Contains(ini.Capabilities.Notifications, "agents.changed") || !slices.Contains(ini.Capabilities.Topics, "agents") {
		t.Errorf("agents notifications are not announced: %+v", ini.Capabilities)
	}
}

// ---- a guided actor step, so setup has something to plan ----

type guideMod struct {
	*modtest.Fake
	cfg core.ConfigStore
}

func (guideMod) Domain() core.DomainID { return core.DomainLeases }
func (g guideMod) Guide(context.Context) *core.Guide {
	if c, _ := g.cfg.Load(); c.Actor != "" {
		return nil
	}
	return &core.Guide{Domain: core.DomainLeases, Step: core.StepActor, Message: "Leases need an actor name", Action: "choose one"}
}

func (f *fixture) withActorGuide() {
	g := guideMod{&modtest.Fake{IDStr: "actor-guide", Det: core.Detection{State: core.Ready, Version: "1"}}, f.cfg.Config}
	f.cfg.Build = func() *core.Registry {
		return core.NewRegistry(lambe.NewDemo(), f.leases, f.tasks, f.board, fixedAgents{demo.NewAgents()}, g)
	}
}

// ---- demo mode: every plan is made and marked, apply is refused ----

// Every plan.* method is listed here with a call that plans on the fixture, and says what demo mode does with
// it; a new plan method that is not classified fails the test, so it cannot reach demo mode unconsidered.
var demoPlanCalls = map[string]struct {
	params  any
	refused bool // no plan to show: refused in demo mode (none now: every plan method has a demo plan)
}{
	"plan.task.create":       {map[string]any{"title": "x"}, false},
	"plan.task.update":       {map[string]any{"id": "ac-12", "title": "x"}, false},
	"plan.task.close":        {map[string]any{"id": "ac-12"}, false},
	"plan.task.reopen":       {map[string]any{"id": "ac-03"}, false},
	"plan.task.label-add":    {map[string]any{"id": "ac-12", "label": "ui"}, false},
	"plan.task.label-remove": {map[string]any{"id": "ac-12", "label": "core"}, false},
	"plan.task.assign":       {map[string]any{"id": "ac-12", "assignee": "mei"}, false},
	"plan.task.priority":     {map[string]any{"id": "ac-12", "priority": 3}, false},
	"plan.task.dep-add":      {map[string]any{"id": "ac-30", "blocked_by": "ac-33"}, false},
	"plan.task.dep-remove":   {map[string]any{"id": "ac-16", "blocked_by": "ac-15"}, false},
	"plan.task.comment":      {map[string]any{"id": "ac-12", "text": "hi"}, false},
	"plan.tasks.bulk":        {map[string]any{"ids": []string{"ac-12"}, "op": "close"}, false},
	"plan.board.post":        {map[string]any{"channel": "dev", "text": "x"}, false},
	"plan.board.reply":       {map[string]any{"id": "2", "text": "x"}, false},
	"plan.setup":             {map[string]any{"step": "actor"}, false},
	"plan.config.set":        {map[string]any{"key": "actor", "value": "mei"}, false},
}

func TestDemoModePlansEveryWriteAndRefusesApply(t *testing.T) {
	f := newFixture(t)
	f.withActorGuide()
	f.cfg.Demo = true
	c := startClient(t, f.cfg)
	ini := c.init()
	realDigests := map[string]string{}
	for _, m := range ini.Capabilities.Methods {
		if !strings.HasPrefix(m, "plan.") || m == "plan.cancel" {
			continue
		}
		tc, ok := demoPlanCalls[m]
		if !ok {
			t.Errorf("%s is not classified for demo mode: add it to demoPlanCalls", m)
			continue
		}
		r := c.call(m, tc.params)
		if tc.refused {
			if e := r.wantErr(t, core.CodeRefused); !strings.Contains(e.Message, "demo") {
				t.Errorf("%s: %q", m, e.Message)
			}
			continue
		}
		if r.Error != nil {
			t.Errorf("%s in demo mode: %+v", m, r.Error)
			continue
		}
		if string(mustMap(t, r.Result)["demo"]) != "true" {
			t.Errorf("%s: the plan is not flagged demo: %s", m, r.Result)
		}
		var p core.ServePlan
		r.into(t, &p)
		realDigests[m] = p.PlanDigest
		// applying it is refused, and a plan that was made is not consumed by the refusal
		if e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeRefused); !strings.Contains(e.Message, "demo") {
			t.Errorf("apply of %s: %q", m, e.Message)
		}
	}
	for m := range demoPlanCalls {
		if !slices.Contains(ini.Capabilities.Methods, m) {
			t.Errorf("demoPlanCalls names %s, which the server does not have", m)
		}
	}
	if e := c.call("apply", map[string]any{"plan_id": "plan_" + strings.Repeat("0", 32), "expected_digest": strings.Repeat("0", 64)}).wantErr(t, core.CodeRefused); e == nil {
		t.Error("apply")
	}
	if f.tasks.runs() != 0 || f.board.runs() != 0 || f.audit.phases() != "" {
		t.Error("demo mode wrote or audited")
	}
	if c, err := f.cfg.Config.Load(); err != nil || c.Actor != "" {
		t.Errorf("demo mode changed the config: %+v %v", c, err)
	}
	// the digests are marked: a real server's plan for the same change has another digest
	g := newFixture(t)
	g.withActorGuide()
	rc := startClient(t, g.cfg)
	rc.init()
	var real core.ServePlan
	rc.call("plan.task.close", map[string]any{"id": "ac-12"}).into(t, &real)
	if real.PlanDigest == realDigests["plan.task.close"] || realDigests["plan.task.close"] != core.DemoDigest(real.PlanDigest) {
		t.Errorf("demo digest %s vs real %s", realDigests["plan.task.close"], real.PlanDigest)
	}
}

// The demo digest of a plan is what the command line prints for the same plan under --demo --dry-run.
func TestDemoPlanDigestMatchesTheCommandsDemoDryRun(t *testing.T) {
	f := newFixture(t)
	f.cfg.Demo = true
	c := startClient(t, f.cfg)
	c.init()
	var p core.ServePlan
	c.call("plan.task.close", map[string]any{"id": "ac-12"}).into(t, &p)
	code, out := cliRun(t, f, "tasks", "close", "ac-12", "--demo", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var dry core.WriteDryRun
	if err := json.Unmarshal([]byte(out), &dry); err != nil || dry.PlanDigest != p.PlanDigest {
		t.Errorf("the command's demo digest %q, the server's %q (%v)", dry.PlanDigest, p.PlanDigest, err)
	}
}

// ---- plan.setup ----

func TestPlanSetupAppliesOnceThroughTheGate(t *testing.T) {
	f := newFixture(t)
	f.withActorGuide()
	c := startClient(t, f.cfg)
	c.init()
	p := plan(t, c, "plan.setup", map[string]any{"step": "actor"})
	if p.Kind != "setup" || p.Op != "actor" || len(p.Lines) == 0 || len(p.Argv) == 0 {
		t.Fatalf("plan %+v", p)
	}
	if f.audit.phases() != "" {
		t.Fatal("planning audited or wrote")
	}
	if cfg, _ := f.cfg.Config.Load(); cfg.Actor != "" {
		t.Fatal("planning wrote the config")
	}
	a := apply(t, c, p.PlanID, p.PlanDigest)
	if a.Kind != "setup" || a.Outcome != "applied" || a.Setup == nil || a.Setup.Step != "actor" || a.Setup.Message == "" {
		t.Fatalf("applied %+v", a)
	}
	if cfg, _ := f.cfg.Config.Load(); cfg.Actor != "me-mac" {
		t.Errorf("actor %q", cfg.Actor)
	}
	if got := f.audit.phases(); got != "attempt,ok" {
		t.Errorf("audit phases %q", got)
	}
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAlreadyApplied)
	// the step is done now: nothing to plan
	c.call("plan.setup", map[string]any{"step": "actor"}).wantErr(t, core.CodeRefused)
}

func TestPlanSetupRefusesWhatSetupWouldNotOffer(t *testing.T) {
	f := newFixture(t) // no guide: the actor step is unavailable, the queues step is done
	c := startClient(t, f.cfg)
	c.init()
	for _, step := range []string{"actor", "queues", "workspace", "repair"} {
		c.call("plan.setup", map[string]any{"step": step}).wantErr(t, core.CodeRefused)
	}
	for _, bad := range []map[string]any{{}, {"step": "nosuch"}, {"step": "actor", "argv": []string{"x"}}, {"step": 3}} {
		c.call("plan.setup", bad).wantErr(t, core.CodeUsage)
	}
	if f.audit.phases() != "" {
		t.Error("a refused plan audited")
	}
}

func TestPlanSetupIsChangedWhenTheStateMovesBeforeApply(t *testing.T) {
	f := newFixture(t)
	f.withActorGuide()
	c := startClient(t, f.cfg)
	c.init()
	p := plan(t, c, "plan.setup", map[string]any{"step": "actor"})
	if err := f.cfg.Config.Update(func(cfg *core.Config) error { return cfg.Set("actor", "someone-else") }); err != nil {
		t.Fatal(err)
	}
	e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeChanged)
	if e.Data.PlanID != p.PlanID || e.Data.Outcome != "changed" {
		t.Errorf("%+v", e.Data)
	}
	if cfg, _ := f.cfg.Config.Load(); cfg.Actor != "someone-else" || f.audit.phases() != "" {
		t.Errorf("a stale step wrote or audited: %+v %q", cfg, f.audit.phases())
	}
}

func TestPlanSetupFailsClosedWhenTheAuditCannotBeWritten(t *testing.T) {
	f := newFixture(t)
	f.withActorGuide()
	c := startClient(t, f.cfg)
	c.init()
	p := plan(t, c, "plan.setup", map[string]any{"step": "actor"})
	f.audit.err = context.DeadlineExceeded
	e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAuditFailed)
	if e.Data.Outcome != "not_applied" {
		t.Errorf("%+v", e.Data)
	}
	if cfg, _ := f.cfg.Config.Load(); cfg.Actor != "" {
		t.Error("the step ran without an audit line")
	}
}

// ---- plan.config.set ----

func TestPlanConfigSetAppliesOnceThroughTheGate(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.config.set", map[string]any{"key": "actor", "value": "mei"})
	if p.Kind != "config" || p.Op != "set actor" || !strings.Contains(strings.Join(p.Lines, "\n"), "mei") {
		t.Fatalf("plan %+v", p)
	}
	if cfg, _ := f.cfg.Config.Load(); cfg.Actor != "" || f.audit.phases() != "" {
		t.Fatal("planning wrote or audited")
	}
	// the same change has the same digest, another value another
	if q := plan(t, c, "plan.config.set", map[string]any{"key": "actor", "value": "mei"}); q.PlanDigest != p.PlanDigest || q.PlanID == p.PlanID {
		t.Errorf("digests %s %s", q.PlanDigest, p.PlanDigest)
	}
	if q := plan(t, c, "plan.config.set", map[string]any{"key": "actor", "value": "kai"}); q.PlanDigest == p.PlanDigest {
		t.Error("two values share a digest")
	}
	a := apply(t, c, p.PlanID, p.PlanDigest)
	if a.Kind != "config" || a.Outcome != "applied" || a.Config == nil || a.Config.Step != "actor" {
		t.Fatalf("applied %+v", a)
	}
	var shown core.ConfigShow
	c.call("config.show", nil).into(t, &shown)
	if shown.Config.Actor != "mei" {
		t.Errorf("config.show: %+v", shown)
	}
	if f.audit.phases() != "attempt,ok" {
		t.Errorf("audit phases %q", f.audit.phases())
	}
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAlreadyApplied)
}

func TestPlanConfigSetAuditsTheAgentCommandAndRefusesWhatConfigSetRefuses(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.config.set", map[string]any{"key": "agent_command", "value": `["claude","{task_id}"]`})
	if len(p.Argv) != 2 || !strings.Contains(strings.Join(p.Lines, "\n"), "work on this will run") {
		t.Fatalf("plan %+v", p)
	}
	apply(t, c, p.PlanID)
	f.audit.mu.Lock()
	entries := slices.Clone(f.audit.entries)
	f.audit.mu.Unlock()
	if len(entries) != 2 || entries[0].Domain != "config" || entries[0].Verb != "set agent_command" || !slices.Equal(entries[0].Argv, []string{"claude", "{task_id}"}) {
		t.Errorf("audit %+v", entries)
	}
	for _, bad := range []map[string]any{{}, {"key": "actor"}, {"value": "x"}, {"key": "nosuch", "value": "x"}, {"key": "actor", "value": "bad name"},
		{"key": "tracker_dir", "value": "relative"}, {"key": "actor", "value": "x", "extra": 1}} {
		c.call("plan.config.set", bad).wantErr(t, core.CodeUsage)
	}
}

func TestPlanConfigSetIsChangedWhenTheKeyMovesAndFailsClosedOnAudit(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.config.set", map[string]any{"key": "actor", "value": "mei"})
	if err := f.cfg.Config.Update(func(cfg *core.Config) error { return cfg.Set("actor", "other") }); err != nil {
		t.Fatal(err)
	}
	e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeChanged)
	if e.Data.Outcome != "changed" {
		t.Errorf("%+v", e.Data)
	}
	if cfg, _ := f.cfg.Config.Load(); cfg.Actor != "other" || f.audit.phases() != "" {
		t.Errorf("a stale change wrote or audited: %+v", cfg)
	}
	// a plan made against the new value, with an audit log that cannot be written
	p = plan(t, c, "plan.config.set", map[string]any{"key": "actor", "value": "mei"})
	f.audit.err = context.DeadlineExceeded
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAuditFailed)
	if cfg, _ := f.cfg.Config.Load(); cfg.Actor != "other" {
		t.Errorf("the config changed without an audit line: %+v", cfg)
	}
}

func TestConfigSetOverServeLeavesTheCommandUnchanged(t *testing.T) {
	f := newFixture(t)
	if code, out := cliRun(t, f, "config", "set", "actor", "mei"); code != 0 || !strings.Contains(out, "actor set") {
		t.Fatalf("%d %q", code, out)
	}
	if f.audit.phases() != "" { // the command audits agent_command only, as before
		t.Errorf("config set audit %q", f.audit.phases())
	}
	if cfg, _ := f.cfg.Config.Load(); cfg.Actor != "mei" {
		t.Errorf("%+v", cfg)
	}
}

// ---- notifications that carry ids ----

func firstIssueID() string { return demo.TaskSnapshot(fixedNow).Issues[0].ID }

func noteParams(t *testing.T, c *client, method, topic string) core.ServeNotice {
	t.Helper()
	m, ok := c.note(3 * time.Second)
	if !ok || m.Method != method {
		t.Fatalf("no %s notification (got %+v)", method, m)
	}
	var n core.ServeNotice
	if err := json.Unmarshal(m.Params, &n); err != nil || n.Topic != topic {
		t.Fatalf("params %s: %v", m.Params, err)
	}
	return n
}

func TestTasksChangedNamesTheTasksThatChanged(t *testing.T) {
	c, f := readyClient(t)
	f.tasks.setFP("a", nil)
	subscribe(t, c, "tasks")
	time.Sleep(100 * time.Millisecond) // the baseline: the fingerprint and the tracker as they are
	f.tasks.mu.Lock()
	f.tasks.snapGen = 1 // the first task is edited
	f.tasks.mu.Unlock()
	f.tasks.setFP("b", nil)
	n := noteParams(t, c, "tasks.changed", "tasks")
	if n.Kind != "ids" || !slices.Equal(n.ChangedIDs, []string{firstIssueID()}) || n.Fingerprint != "b" || n.Reason != "fingerprint" {
		t.Errorf("%+v", n)
	}
	// the fingerprint moved but no task did (or could be seen to): the client refetches everything
	f.tasks.setFP("c", nil)
	n = noteParams(t, c, "tasks.changed", "tasks")
	if n.Kind != "full" || len(n.ChangedIDs) != 0 || n.Fingerprint != "c" {
		t.Errorf("%+v", n)
	}
}

func TestTasksChangedWithoutAFingerprintNamesTheTasksToo(t *testing.T) {
	f := newFixture(t)
	nf := noFPTasks{f.tasks}
	f.cfg.Build = func() *core.Registry {
		return core.NewRegistry(lambe.NewDemo(), f.leases, nf, f.board, fixedAgents{demo.NewAgents()})
	}
	c := startClient(t, f.cfg)
	c.init()
	subscribe(t, c, "tasks")
	time.Sleep(100 * time.Millisecond)
	f.tasks.mu.Lock()
	f.tasks.snapGen = 1
	f.tasks.mu.Unlock()
	n := noteParams(t, c, "tasks.changed", "tasks")
	if n.Kind != "ids" || n.Reason != "snapshot" || n.Fingerprint == "" || !slices.Equal(n.ChangedIDs, []string{firstIssueID()}) {
		t.Errorf("%+v", n)
	}
}

// togglingAgents drops its first agent when told to.
type togglingAgents struct {
	demo.AgentsFeed
	drop *atomic.Bool
}

func (a togglingAgents) Agents(context.Context) ([]core.Agent, core.AgentsExtra, error) {
	as, extra, err := demo.AgentsAt(fixedNow)
	if a.drop.Load() && len(as) > 0 {
		as = as[1:]
	}
	return as, extra, err
}

func TestAgentsChangedFollowsThePoll(t *testing.T) {
	f := newFixture(t)
	var drop atomic.Bool
	f.cfg.Build = func() *core.Registry {
		return core.NewRegistry(lambe.NewDemo(), f.leases, f.tasks, f.board, togglingAgents{demo.NewAgents(), &drop})
	}
	c := startClient(t, f.cfg)
	c.init()
	if s := subscribe(t, c, "agents"); len(s.Active) != 1 || s.Active[0] != "agents" {
		t.Fatalf("%+v", s)
	}
	noNote(t, c, 100*time.Millisecond) // the baseline is not a change; a steady agent list is quiet
	var before core.AgentsReport
	c.call("agents", nil).into(t, &before)
	drop.Store(true)
	var after core.AgentsReport
	c.call("agents", nil).into(t, &after)
	var gone []string
	for _, a := range before.Agents {
		if !slices.ContainsFunc(after.Agents, func(b core.Agent) bool { return b.ID == a.ID }) {
			gone = append(gone, a.ID)
		}
	}
	if len(gone) != 1 {
		t.Fatalf("the fixture dropped %v", gone)
	}
	n := noteParams(t, c, "agents.changed", "agents")
	if n.Kind != "ids" || n.Reason != "poll" || n.Fingerprint == "" || !slices.Equal(n.ChangedIDs, gone) {
		t.Errorf("%+v (dropped %v)", n, gone)
	}
	noNote(t, c, 100*time.Millisecond)
}

// ---- review fixes ----

// Applies that are still waiting (for a slot, or for their turn to write) when shutdown comes never run: only
// the write that already started gets the grace.
func TestQueuedAppliesNeverRunAfterShutdown(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxInFlight, f.cfg.Limits.MaxQueued = 8, 32
	f.cfg.WriteGrace = 5 * time.Second
	entered, release := make(chan struct{}, 64), make(chan struct{})
	f.tasks.runHook = func(ctx context.Context, _ []string) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c := startClient(t, f.cfg)
	c.init()
	var plans []core.ServePlan
	for i := 0; i < 28; i++ {
		plans = append(plans, plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"}))
	}
	var ids []string
	for _, p := range plans {
		ids = append(ids, c.send("apply", map[string]any{"plan_id": p.PlanID}))
	}
	<-entered // the first write is running; seven wait for their turn and twenty for a slot
	time.Sleep(100 * time.Millisecond)
	sd := c.send("shutdown", nil)
	c.call("version", nil).wantErr(t, core.CodeRefused) // the shutdown has been read: the gate is shut
	for _, id := range ids[8:] {                        // the ones waiting for a slot are cancelled at once, not when a slot frees
		c.await(id).wantErr(t, core.CodeTimeout)
	}
	close(release)
	applied := 0
	for _, id := range ids[:8] {
		if m := c.await(id); m.Error == nil {
			applied++
		}
	}
	c.await(sd)
	if applied != 1 || f.tasks.runs() != 1 {
		t.Errorf("%d applies succeeded and %d writes ran; only the one that had started may", applied, f.tasks.runs())
	}
}

// Requests past the in-flight limit are served strictly in the order they arrived.
func TestQueuedRequestsAreServedInArrivalOrder(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxInFlight, f.cfg.Limits.MaxQueued = 1, 10
	c := startClient(t, f.cfg)
	c.init()
	var log []string
	c.mu.Lock()
	c.log = &log
	c.mu.Unlock()
	started := slowTasks(f)
	slow := c.send("tasks.list", nil)
	<-started
	var ids []string
	for i := 0; i < 8; i++ {
		ids = append(ids, c.send("version", nil))
		time.Sleep(10 * time.Millisecond)
	}
	c.cancel(slow)
	for _, id := range ids {
		c.await(id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var got []string
	for _, l := range log {
		var m rpcMsg
		if json.Unmarshal([]byte(l), &m) == nil && m.Result != nil {
			got = append(got, string(m.ID))
		}
	}
	if !slices.Equal(got, ids) {
		t.Errorf("answered in order %v, arrived in order %v", got, ids)
	}
}

// A feed that failed leaves its agents out of the report with a note: that is not every agent going away.
type flakyAgents struct {
	demo.AgentsFeed
	fail *atomic.Bool
}

func (a flakyAgents) Agents(context.Context) ([]core.Agent, core.AgentsExtra, error) {
	if a.fail.Load() {
		return nil, core.AgentsExtra{}, context.DeadlineExceeded
	}
	return demo.AgentsAt(fixedNow)
}

func TestAgentsWatcherIgnoresAFailedFeedAndStartsAtSubscribe(t *testing.T) {
	f := newFixture(t)
	f.cfg.SlowBase = 40 * time.Millisecond
	var fail atomic.Bool
	f.cfg.Build = func() *core.Registry {
		return core.NewRegistry(lambe.NewDemo(), f.leases, f.tasks, f.board, flakyAgents{demo.NewAgents(), &fail})
	}
	c := startClient(t, f.cfg)
	c.init()
	subscribe(t, c, "agents")
	time.Sleep(60 * time.Millisecond)
	fail.Store(true) // the feed fails for a while, then recovers
	time.Sleep(200 * time.Millisecond)
	fail.Store(false)
	noNote(t, c, 250*time.Millisecond)
}

func TestWatchersTakeTheirBaselineAtSubscribe(t *testing.T) {
	f := newFixture(t)
	f.cfg.PollBase, f.cfg.PollMax = 300*time.Millisecond, 300*time.Millisecond // the first poll is far away
	c := startClient(t, f.cfg)
	c.init()
	f.tasks.setFP("a", nil)
	subscribe(t, c, "tasks")
	f.tasks.setFP("b", nil) // changed before the first poll
	n := noteParams(t, c, "tasks.changed", "tasks")
	if n.Fingerprint != "b" {
		t.Errorf("%+v", n)
	}
}

// bigTasks is a tracker with more tasks than the watcher keeps.
type bigTasks struct{ *tasksMod }

func (b bigTasks) Snapshot(ctx context.Context) (core.TaskSnapshot, error) {
	s, err := b.tasksMod.Snapshot(ctx)
	for i := 0; i < 5001; i++ {
		is := s.Issues[1] // not the one the test edits
		is.ID = fmt.Sprintf("big-%d", i)
		s.Issues = append(s.Issues, is)
	}
	return s, err
}

func TestAHugeTrackerIsWatchedByDigestAndEveryChangeIsFull(t *testing.T) {
	f := newFixture(t)
	f.cfg.Build = func() *core.Registry {
		return core.NewRegistry(lambe.NewDemo(), f.leases, bigTasks{f.tasks}, f.board, fixedAgents{demo.NewAgents()})
	}
	c := startClient(t, f.cfg)
	c.init()
	f.tasks.setFP("a", nil)
	subscribe(t, c, "tasks")
	time.Sleep(100 * time.Millisecond)
	f.tasks.mu.Lock()
	f.tasks.snapGen = 1
	f.tasks.mu.Unlock()
	f.tasks.setFP("b", nil)
	if n := noteParams(t, c, "tasks.changed", "tasks"); n.Kind != "full" || len(n.ChangedIDs) != 0 {
		t.Errorf("%+v", n)
	}
}
