package serve_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

var (
	planIDRE = regexp.MustCompile(`^plan_[0-9a-f]{32}$`)
	digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func plan(t *testing.T, c *client, method string, params any) core.ServePlan {
	t.Helper()
	var p core.ServePlan
	c.call(method, params).into(t, &p)
	if !planIDRE.MatchString(p.PlanID) || !digestRE.MatchString(p.PlanDigest) {
		t.Fatalf("plan ids: %+v", p)
	}
	return p
}

func apply(t *testing.T, c *client, id string, extra ...string) core.ServeApplied {
	t.Helper()
	params := map[string]any{"plan_id": id}
	if len(extra) > 0 {
		params["expected_digest"] = extra[0]
	}
	var a core.ServeApplied
	c.call("apply", params).into(t, &a)
	return a
}

// ---- the plan gate over the protocol ----

func TestEveryTaskVerbPlansThenAppliesOnce(t *testing.T) {
	c, f := readyClient(t)
	for _, tc := range []struct {
		method string
		params map[string]any
		line   string // a word the plan lines say
	}{
		{"plan.task.create", map[string]any{"title": "new thing", "priority": 2, "type": "task", "description": "d"}, "create a new task"},
		{"plan.task.update", map[string]any{"id": "ac-12", "title": "renamed", "status": "blocked"}, "ac-12"},
		{"plan.task.close", map[string]any{"id": "ac-12", "reason": "done"}, "ac-12"},
		{"plan.task.reopen", map[string]any{"id": "ac-03"}, "ac-03"},
		{"plan.task.label-add", map[string]any{"id": "ac-12", "label": "ui"}, "label"},
		{"plan.task.label-remove", map[string]any{"id": "ac-12", "label": "core"}, "label"},
		{"plan.task.assign", map[string]any{"id": "ac-12", "assignee": "mei"}, "assign"},
		{"plan.task.assign", map[string]any{"id": "ac-12", "clear": true}, "unassign"},
		{"plan.task.priority", map[string]any{"id": "ac-12", "priority": 3}, "priority"},
		{"plan.task.dep-add", map[string]any{"id": "ac-30", "blocked_by": "ac-33"}, "wait"},
		{"plan.task.dep-remove", map[string]any{"id": "ac-16", "blocked_by": "ac-15"}, "waiting"},
		{"plan.task.comment", map[string]any{"id": "ac-12", "text": "hello\nworld"}, "comment"},
	} {
		before := f.tasks.runs()
		p := plan(t, c, tc.method, tc.params)
		if f.tasks.runs() != before {
			t.Fatalf("%s: planning ran a write", tc.method)
		}
		if p.Kind != "task" || len(p.Argv) == 0 || !strings.Contains(strings.Join(p.Lines, "\n"), tc.line) {
			t.Errorf("%s: plan %+v", tc.method, p)
		}
		if exp, err := time.Parse(time.RFC3339, p.ExpiresAt); err != nil || !exp.Equal(fixedNow.Add(10*time.Minute)) {
			t.Errorf("%s: expires_at %q", tc.method, p.ExpiresAt)
		}
		a := apply(t, c, p.PlanID, p.PlanDigest)
		if a.Outcome != "applied" || a.Kind != "task" || a.Task == nil || a.PlanDigest != p.PlanDigest || a.PlanID != p.PlanID {
			t.Errorf("%s: applied %+v", tc.method, a)
		}
		if f.tasks.runs() != before+1 {
			t.Errorf("%s: %d writes", tc.method, f.tasks.runs()-before)
		}
		// single use
		c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAlreadyApplied)
		if f.tasks.runs() != before+1 {
			t.Errorf("%s: a second apply wrote again", tc.method)
		}
	}
	// every write was recorded before and after it ran
	if got, want := f.audit.phases(), strings.Repeat("attempt,ok,", 12); got+"," != want {
		t.Errorf("audit phases %q", got)
	}
}

func TestPlanRefusesWhatCoreWouldRefuse(t *testing.T) {
	c, f := readyClient(t)
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"plan.task.close", map[string]any{"id": "bad id"}}, {"plan.task.close", map[string]any{}}, {"plan.task.update", map[string]any{"id": "ac-12", "status": "closed"}},
		{"plan.task.update", map[string]any{"id": "ac-12"}}, {"plan.task.priority", map[string]any{"id": "ac-12", "priority": 9}},
		{"plan.task.priority", map[string]any{"id": "ac-12"}}, {"plan.task.comment", map[string]any{"id": "ac-12", "text": "bad \x1b[31m"}},
		{"plan.task.label-add", map[string]any{"id": "ac-12", "label": "has space"}}, {"plan.task.assign", map[string]any{"id": "ac-12"}},
		{"plan.task.assign", map[string]any{"id": "ac-12", "assignee": "x", "clear": true}}, {"plan.task.assign", map[string]any{"id": "ac-12", "assignee": "  "}},
		{"plan.task.create", map[string]any{}}, {"plan.task.create", map[string]any{"title": "x", "type": "nosuch"}},
		{"plan.task.dep-add", map[string]any{"id": "ac-30"}}, {"plan.task.dep-add", map[string]any{"id": "ac-30", "blocks": "ac-1", "blocked_by": "ac-2"}},
		{"plan.task.dep-add", map[string]any{"id": "ac-12", "blocked_by": "ac-12"}},
		{"plan.task.reopen", map[string]any{"id": "ac-12"}}, // not closed
		// a field of another verb, or an argv, is refused rather than ignored
		{"plan.task.close", map[string]any{"id": "ac-12", "title": "x"}}, {"plan.task.close", map[string]any{"id": "ac-12", "argv": []string{"bd", "delete"}}},
		{"plan.board.post", map[string]any{"channel": "dev", "text": "x", "id": "1"}}, {"plan.board.reply", map[string]any{"id": "1", "channel": "dev", "text": "x"}},
		{"plan.board.post", map[string]any{"channel": "", "text": "x"}}, {"plan.board.post", map[string]any{"channel": "dev", "text": ""}},
	} {
		m := c.call(tc.method, tc.params)
		if m.Error == nil || (m.Error.Data.Code != core.CodeUsage && m.Error.Data.Code != core.CodeRefused) {
			t.Errorf("%s %v: %+v %s", tc.method, tc.params, m.Error, m.Result)
		}
	}
	if f.tasks.runs() != 0 || f.board.runs() != 0 {
		t.Error("a refused plan ran something")
	}
}

func TestApplyParamsAndStaleOrUnknownPlanIDs(t *testing.T) {
	f := newFixture(t)
	f.cfg.Limits.MaxPlans = 2
	c := startClient(t, f.cfg)
	c.init()
	for _, bad := range []map[string]any{{}, {"plan_id": ""}, {"plan_id": "plan_zz"}, {"plan_id": "../x"}, {"plan_id": 7}, {"plan_id": "plan_" + strings.Repeat("0", 32), "x": 1}} {
		c.call("apply", bad).wantErr(t, core.CodeUsage)
	}
	// well formed, never issued
	e := c.call("apply", map[string]any{"plan_id": "plan_" + strings.Repeat("0", 32)}).wantErr(t, core.CodeNotFound)
	if e.Data.Outcome != "not_applied" {
		t.Errorf("%+v", e)
	}
	// expired: the plan is only valid against the state it was read from
	p := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	f.advance(10*time.Minute + time.Second)
	e = c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeChanged)
	if e.Data.Outcome != "changed" || !e.Data.Retryable || !strings.Contains(e.Message, "expired") {
		t.Errorf("%+v", e)
	}
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeChanged) // and stays expired
	// the store is bounded: the oldest plan is pushed out by newer ones
	p1 := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	p2 := plan(t, c, "plan.task.close", map[string]any{"id": "ac-15"})
	p3 := plan(t, c, "plan.task.close", map[string]any{"id": "ac-16"})
	c.call("apply", map[string]any{"plan_id": p1.PlanID}).wantErr(t, core.CodeChanged)
	apply(t, c, p2.PlanID)
	apply(t, c, p3.PlanID)
	if f.tasks.runs() != 2 {
		t.Errorf("writes: %d", f.tasks.runs())
	}
}

func TestPlanCancel(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	var r struct {
		PlanID    string `json:"plan_id"`
		Cancelled bool   `json:"cancelled"`
	}
	c.call("plan.cancel", map[string]any{"plan_id": p.PlanID}).into(t, &r)
	if !r.Cancelled || r.PlanID != p.PlanID {
		t.Errorf("%+v", r)
	}
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeNotFound)
	c.call("plan.cancel", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeNotFound)
	c.call("plan.cancel", map[string]any{"plan_id": "x"}).wantErr(t, core.CodeUsage)
	if f.tasks.runs() != 0 {
		t.Error("a cancelled plan ran")
	}
}

func TestExpectedDigest(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.priority", map[string]any{"id": "ac-12", "priority": 3})
	other := strings.Repeat("a", 64)
	// a digest that is not the plan's is "changed": nothing is written, and the plan is still there
	e := c.call("apply", map[string]any{"plan_id": p.PlanID, "expected_digest": other}).wantErr(t, core.CodeChanged)
	if e.Data.Outcome != "changed" || e.Data.PlanDigest != p.PlanDigest || e.Data.PlanID != p.PlanID {
		t.Errorf("%+v", e)
	}
	if f.tasks.runs() != 0 || f.audit.phases() != "" {
		t.Errorf("a digest mismatch wrote or audited: runs=%d audit=%q", f.tasks.runs(), f.audit.phases())
	}
	// a malformed digest, and an empty one, are usage errors: never "no check"
	for _, bad := range []string{"", "xyz", strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		c.call("apply", map[string]any{"plan_id": p.PlanID, "expected_digest": bad}).wantErr(t, core.CodeUsage)
	}
	if f.tasks.runs() != 0 {
		t.Error("a malformed digest wrote")
	}
	// the right one, in any case and with spaces around it, applies
	a := apply(t, c, p.PlanID, " "+strings.ToUpper(p.PlanDigest)+" ")
	if a.Outcome != "applied" || f.tasks.runs() != 1 {
		t.Errorf("%+v", a)
	}
}

func TestAStalePlanIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.priority", map[string]any{"id": "ac-12", "priority": 3})
	f.tasks.mu.Lock()
	f.tasks.readHook = func(_ string, tk core.Task) core.Task { tk.Priority = 4; return tk } // someone else changed it
	f.tasks.mu.Unlock()
	e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeChanged)
	if e.Data.Outcome != "changed" {
		t.Errorf("%+v", e)
	}
	if f.tasks.runs() != 0 || f.audit.phases() != "" {
		t.Errorf("a stale plan wrote or audited: %d %q", f.tasks.runs(), f.audit.phases())
	}
	// the plan is spent: plan again
	e = c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeChanged)
	if !strings.Contains(e.Message, "nothing was written") {
		t.Errorf("%+v", e)
	}
	// and a reopen of a task that is no longer closed is refused the same way
	f.tasks.mu.Lock()
	f.tasks.readHook = nil
	f.tasks.mu.Unlock()
	rp := plan(t, c, "plan.task.reopen", map[string]any{"id": "ac-03"})
	f.tasks.mu.Lock()
	f.tasks.readHook = func(_ string, tk core.Task) core.Task { tk.Status = "open"; return tk }
	f.tasks.mu.Unlock()
	c.call("apply", map[string]any{"plan_id": rp.PlanID}).wantErr(t, core.CodeChanged)
	if f.tasks.runs() != 0 {
		t.Error("a stale reopen wrote")
	}
}

func TestWriteFailuresKeepTheirMeaning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		code    core.ErrorCode
		outcome string
		unknown bool
		phases  string
	}{
		{"unknown outcome", core.UnknownOutcome("bd timed out; refresh before retrying"), core.CodeUnknownOutcome, "unknown", true, "attempt,unknown"},
		{"applied but not what was asked", core.AppliedMismatch("written, but the status differs"), core.CodeAppliedMismatch, "applied_mismatch", false, "attempt,applied-mismatch"},
		{"plain failure", errors.New("bd said no"), core.CodeFailed, "not_applied", false, "attempt,error"},
		{"changed under the provider", core.Stale("moved"), core.CodeChanged, "changed", false, "attempt,error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := readyClient(t)
			p := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
			f.tasks.mu.Lock()
			f.tasks.runHook = func(context.Context, []string) error { return tc.err }
			f.tasks.mu.Unlock()
			e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, tc.code)
			if e.Data.Outcome != tc.outcome || e.Data.UnknownOutcome != tc.unknown || e.Data.PlanID != p.PlanID || e.Data.PlanDigest != p.PlanDigest {
				t.Errorf("%+v", e.Data)
			}
			if tc.unknown && (e.Data.Retryable || !strings.Contains(e.Message, "result unknown")) {
				t.Errorf("an unknown outcome is never retryable and never 'not applied': %+v", e)
			}
			if got := f.audit.phases(); got != tc.phases {
				t.Errorf("audit phases %q, want %q", got, tc.phases)
			}
			// the plan is spent either way: a retry is a new plan, after a refresh
			c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAlreadyApplied)
			if f.tasks.runs() != 1 {
				t.Errorf("writes: %d", f.tasks.runs())
			}
		})
	}
}

func TestAuditFailsClosed(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	f.audit.mu.Lock()
	f.audit.err = errors.New("disk full")
	f.audit.mu.Unlock()
	e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAuditFailed)
	if e.Data.Outcome != "not_applied" || f.tasks.runs() != 0 {
		t.Errorf("a write the audit could not record ran: %+v runs=%d", e.Data, f.tasks.runs())
	}
	// nothing was written, so the plan is not "applied": plan again once the log works
	f.audit.mu.Lock()
	f.audit.err = nil
	f.audit.mu.Unlock()
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeChanged)
	p2 := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	if a := apply(t, c, p2.PlanID); a.Outcome != "applied" {
		t.Errorf("%+v", a)
	}
}

func TestACancelledWriteIsAResultUnknown(t *testing.T) {
	for name, hookErr := range map[string]func(context.Context) error{
		"provider reports unknown": func(ctx context.Context) error { return core.UnknownOutcome("cancelled") },
		"provider returns ctx err": func(ctx context.Context) error { return ctx.Err() },
	} {
		t.Run(name, func(t *testing.T) {
			c, f := readyClient(t)
			p := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
			started := make(chan struct{}, 1)
			f.tasks.mu.Lock()
			f.tasks.runHook = func(ctx context.Context, _ []string) error { started <- struct{}{}; <-ctx.Done(); return hookErr(ctx) }
			f.tasks.mu.Unlock()
			id := c.send("apply", map[string]any{"plan_id": p.PlanID})
			<-started
			c.cancel(id)
			e := c.await(id).wantErr(t, core.CodeUnknownOutcome)
			if !e.Data.UnknownOutcome || e.Data.Outcome != "unknown" || e.Data.Retryable {
				t.Errorf("%+v", e.Data)
			}
			c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAlreadyApplied)
		})
	}
}

func TestADeadlineOnAWriteIsAResultUnknown(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	f.tasks.mu.Lock()
	f.tasks.runHook = func(ctx context.Context, _ []string) error { <-ctx.Done(); return ctx.Err() }
	f.tasks.mu.Unlock()
	e := c.call("apply", map[string]any{"plan_id": p.PlanID}, 80).wantErr(t, core.CodeUnknownOutcome)
	if !strings.Contains(e.Message, "result unknown") {
		t.Errorf("%+v", e)
	}
}

func TestACancelBeforeTheClaimLeavesThePlanAppliable(t *testing.T) {
	c, f := readyClient(t)
	first := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	second := plan(t, c, "plan.task.close", map[string]any{"id": "ac-15"})
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.tasks.mu.Lock()
	f.tasks.runHook = func(ctx context.Context, _ []string) error { started <- struct{}{}; <-release; return nil }
	f.tasks.mu.Unlock()
	id1 := c.send("apply", map[string]any{"plan_id": first.PlanID})
	<-started
	id2 := c.send("apply", map[string]any{"plan_id": second.PlanID}) // waits for the write in flight
	time.Sleep(40 * time.Millisecond)
	c.cancel(id2)
	c.await(id2).wantErr(t, core.CodeTimeout)
	close(release)
	c.await(id1)
	if f.tasks.runs() != 1 {
		t.Fatalf("writes: %d", f.tasks.runs())
	}
	if a := apply(t, c, second.PlanID); a.Outcome != "applied" || f.tasks.runs() != 2 {
		t.Errorf("%+v", a)
	}
}

func TestTheSamePlanAppliedConcurrentlyRunsOnce(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	var wg sync.WaitGroup
	var ok, dup atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := c.call("apply", map[string]any{"plan_id": p.PlanID, "expected_digest": p.PlanDigest})
			switch {
			case m.Error == nil:
				ok.Add(1)
			case m.Error.Data.Code == core.CodeAlreadyApplied:
				dup.Add(1)
			default:
				t.Errorf("%+v", m.Error)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || dup.Load() != 5 || f.tasks.runs() != 1 {
		t.Errorf("ok=%d already_applied=%d writes=%d", ok.Load(), dup.Load(), f.tasks.runs())
	}
}

func TestWritesAreSerialized(t *testing.T) {
	c, f := readyClient(t)
	var ids []string
	for _, id := range []string{"ac-12", "ac-15", "ac-16", "ac-30"} {
		ids = append(ids, plan(t, c, "plan.task.close", map[string]any{"id": id}).PlanID)
	}
	var cur, max atomic.Int32
	f.tasks.mu.Lock()
	f.tasks.runHook = func(context.Context, []string) error {
		n := cur.Add(1)
		for {
			m := max.Load()
			if n <= m || max.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		cur.Add(-1)
		return nil
	}
	f.tasks.mu.Unlock()
	var sent []string
	for _, id := range ids {
		sent = append(sent, c.send("apply", map[string]any{"plan_id": id}))
	}
	for _, s := range sent {
		if m := c.await(s); m.Error != nil {
			t.Errorf("%+v", m.Error)
		}
	}
	if max.Load() != 1 || f.tasks.runs() != 4 {
		t.Errorf("writes overlapped (max %d) or were lost (%d)", max.Load(), f.tasks.runs())
	}
}

// ---- bulk ----

func TestBulkPlanThenApply(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.tasks.bulk", map[string]any{"ids": []string{"ac-30", "ac-33"}, "op": "priority", "value": "1"})
	if p.Kind != "bulk" || len(p.Items) != 2 || len(p.Argv) != 0 || p.Items[0].Argv == nil {
		t.Fatalf("%+v", p)
	}
	var a core.ServeApplied
	c.call("apply", map[string]any{"plan_id": p.PlanID, "expected_digest": p.PlanDigest}).into(t, &a)
	if a.Outcome != "applied" || a.Bulk == nil || a.Bulk.Applied != 2 || a.Bulk.PlanDigest != p.PlanDigest || a.Bulk.BatchID == "" {
		t.Fatalf("%+v", a)
	}
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAlreadyApplied)
	if f.tasks.runs() != 2 || f.audit.phases() != "attempt,ok,attempt,ok" {
		t.Errorf("runs=%d audit=%q", f.tasks.runs(), f.audit.phases())
	}
	for _, e := range f.audit.entries {
		if e.Batch != a.Bulk.BatchID {
			t.Errorf("audit line without the batch id: %+v", e)
		}
	}
}

func TestBulkPartialUnknownAndRefusals(t *testing.T) {
	c, f := readyClient(t)
	// ac-12 is priority 0: asking for 0 leaves it out of the plan, so the batch is partial
	p := plan(t, c, "plan.tasks.bulk", map[string]any{"ids": []string{"ac-12", "ac-30"}, "op": "priority", "value": "0"})
	if len(p.Skipped) != 1 {
		t.Fatalf("%+v", p)
	}
	var a core.ServeApplied
	c.call("apply", map[string]any{"plan_id": p.PlanID}).into(t, &a)
	if a.Outcome != "partial" || a.Bulk.Applied != 1 || a.Bulk.Skipped != 1 {
		t.Errorf("%+v", a.Bulk)
	}
	// an unknown outcome in a batch is reported as such and stops the rest
	p = plan(t, c, "plan.tasks.bulk", map[string]any{"ids": []string{"ac-30", "ac-33"}, "op": "priority", "value": "1"})
	f.tasks.mu.Lock()
	f.tasks.runHook = func(context.Context, []string) error { return core.UnknownOutcome("timed out") }
	f.tasks.mu.Unlock()
	c.call("apply", map[string]any{"plan_id": p.PlanID}).into(t, &a)
	if a.Outcome != "unknown" || a.Bulk.Unknown != 2 {
		t.Errorf("%+v", a.Bulk)
	}
	ids := make([]string, 26)
	for i := range ids {
		ids[i] = "ac-" + strings.Repeat("1", i+1)
	}
	for _, bad := range []map[string]any{
		{"ids": ids, "op": "close"}, {"ids": []string{}, "op": "close"}, {"ids": []string{"ac-30", "ac-30"}, "op": "close"}, {"ids": []string{"bad id"}, "op": "close"},
		{"ids": []string{"ac-30"}, "op": "delete"}, {"ids": []string{"ac-30"}, "op": "priority", "value": "x"}, {"ids": []string{"ac-30"}, "op": "close", "value": "x"},
		{"ids": []string{"ac-30"}, "op": "assign"}, {"ids": []string{"ac-30"}, "op": "assign", "value": "a", "clear": true}, {"ids": []string{"ac-30"}, "op": "close", "clear": true},
		{"ids": []string{"nosuch-1"}, "op": "close"}, {"ids": []string{"ac-30"}, "op": "status", "value": "closed"},
	} {
		if m := c.call("plan.tasks.bulk", bad); m.Error == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

// ---- board ----

func TestBoardPostAndReplyThroughTheGate(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.board.post", map[string]any{"channel": "dev", "text": "hello"})
	if p.Kind != "board" || len(p.Argv) == 0 || len(p.Context) == 0 || !strings.Contains(strings.Join(p.Argv, " "), "--idempotency-key") {
		t.Fatalf("%+v", p)
	}
	// the same content plans to the same digest (the key has no clock in it)
	if again := plan(t, c, "plan.board.post", map[string]any{"channel": "dev", "text": "hello"}); again.PlanDigest != p.PlanDigest || again.PlanID == p.PlanID {
		t.Errorf("digest %s vs %s", again.PlanDigest, p.PlanDigest)
	}
	var a core.ServeApplied
	c.call("apply", map[string]any{"plan_id": p.PlanID, "expected_digest": p.PlanDigest}).into(t, &a)
	if a.Outcome != "applied" || a.Board == nil || a.Board.ID != "9" || f.board.runs() != 1 {
		t.Fatalf("%+v", a)
	}
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAlreadyApplied)
	// a post that was just made is not planned again without again:true (the audit log says so)
	if e := c.call("plan.board.post", map[string]any{"channel": "dev", "text": "hello"}).wantErr(t, core.CodeRefused); !strings.Contains(e.Message, "again") {
		t.Errorf("%+v", e)
	}
	if again := plan(t, c, "plan.board.post", map[string]any{"channel": "dev", "text": "hello", "again": true}); again.PlanDigest == p.PlanDigest {
		t.Error("a post made again must get a key the log has never recorded")
	}
	rp := plan(t, c, "plan.board.reply", map[string]any{"id": "2", "text": "thanks"})
	if a := apply(t, c, rp.PlanID); a.Board == nil || f.board.runs() != 2 {
		t.Errorf("%+v", a)
	}
	// a failed post is a failed post
	f.board.mu.Lock()
	f.board.err = core.UnknownOutcome("posted but could not be re-read")
	f.board.mu.Unlock()
	up := plan(t, c, "plan.board.post", map[string]any{"channel": "work", "text": "x"})
	e := c.call("apply", map[string]any{"plan_id": up.PlanID}).wantErr(t, core.CodeUnknownOutcome)
	if !e.Data.UnknownOutcome {
		t.Errorf("%+v", e)
	}
}

func TestBoardPlanFailsClosedOnAnUnreadableAudit(t *testing.T) {
	f := newFixture(t)
	f.cfg.Audit = brokenAudit{}
	c := startClient(t, f.cfg)
	c.init()
	c.call("plan.board.post", map[string]any{"channel": "dev", "text": "x"}).wantErr(t, core.CodeAuditFailed)
	if f.board.runs() != 0 {
		t.Error("ran")
	}
}

type brokenAudit struct{}

func (brokenAudit) Record(core.AuditEntry) error        { return errors.New("no") }
func (brokenAudit) Entries() ([]core.AuditEntry, error) { return nil, errors.New("unreadable") }

func TestAPanicInTheWriterIsAResultUnknownWithItsOutcomeRecorded(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.close", map[string]any{"id": "ac-12"})
	f.tasks.mu.Lock()
	f.tasks.runHook = func(context.Context, []string) error { panic("provider bug with secret task text") }
	f.tasks.mu.Unlock()
	e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeUnknownOutcome)
	if !e.Data.UnknownOutcome || e.Data.Outcome != "unknown" || e.Data.PlanID != p.PlanID || strings.Contains(e.Message, "secret") {
		t.Errorf("%+v", e)
	}
	if got := f.audit.phases(); got != "attempt,unknown" {
		t.Errorf("the audit must end with the outcome, got %q", got)
	}
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeAlreadyApplied)
	if m := c.call("version", nil); m.Error != nil {
		t.Fatal("the server did not survive")
	}
	// a batch whose writer panics reports the task unknown and stops, with its outcome in the audit
	bp := plan(t, c, "plan.tasks.bulk", map[string]any{"ids": []string{"ac-30", "ac-33"}, "op": "priority", "value": "1"})
	var a core.ServeApplied
	c.call("apply", map[string]any{"plan_id": bp.PlanID}).into(t, &a)
	if a.Outcome != "unknown" || a.Bulk.Unknown != 2 {
		t.Errorf("%+v", a.Bulk)
	}
}

func TestAPanicInTheFreshnessCheckKeepsThePlanAndOutcome(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.priority", map[string]any{"id": "ac-12", "priority": 3})
	f.tasks.mu.Lock()
	f.tasks.readHook = func(string, core.Task) core.Task { panic("boom") }
	f.tasks.mu.Unlock()
	e := c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeUnknownOutcome)
	if e.Data.PlanID != p.PlanID || e.Data.Outcome != "unknown" {
		t.Errorf("%+v", e)
	}
}

func TestTwoBoardPlansMadeBeforeEitherIsAppliedPostOnce(t *testing.T) {
	c, f := readyClient(t)
	p1 := plan(t, c, "plan.board.post", map[string]any{"channel": "dev", "text": "same"})
	p2 := plan(t, c, "plan.board.post", map[string]any{"channel": "dev", "text": "same"})
	if p1.PlanDigest != p2.PlanDigest {
		t.Fatal("setup: the two plans share a key")
	}
	apply(t, c, p1.PlanID)
	e := c.call("apply", map[string]any{"plan_id": p2.PlanID}).wantErr(t, core.CodeChanged)
	if e.Data.PlanID != p2.PlanID || e.Data.Outcome != "changed" || f.board.runs() != 1 {
		t.Errorf("%+v runs=%d", e.Data, f.board.runs())
	}
	// again:true is a new key, so it is a new plan that applies
	p3 := plan(t, c, "plan.board.post", map[string]any{"channel": "dev", "text": "same", "again": true})
	apply(t, c, p3.PlanID)
	if f.board.runs() != 2 {
		t.Errorf("runs %d", f.board.runs())
	}
}

func TestACancelAfterTheClaimNamesTheSpentPlan(t *testing.T) {
	c, f := readyClient(t)
	p := plan(t, c, "plan.task.priority", map[string]any{"id": "ac-12", "priority": 3})
	started := make(chan struct{}, 1)
	f.tasks.mu.Lock()
	f.tasks.readHook = func(string, core.Task) core.Task { return core.Task{} }
	f.tasks.mu.Unlock()
	_ = started
	// make the freshness re-read wait for the context, as a slow tracker would
	f.tasks.mu.Lock()
	f.tasks.readHook = nil
	f.tasks.snapHook = nil
	f.tasks.mu.Unlock()
	slow := make(chan struct{}, 1)
	f.tasks.mu.Lock()
	f.tasks.readSlow = func(ctx context.Context) error { slow <- struct{}{}; <-ctx.Done(); return ctx.Err() }
	f.tasks.mu.Unlock()
	id := c.send("apply", map[string]any{"plan_id": p.PlanID})
	<-slow
	c.cancel(id)
	e := c.await(id).wantErr(t, core.CodeTimeout)
	if e.Code != -32800 || e.Data.PlanID != p.PlanID || e.Data.Outcome == "" || !strings.Contains(e.Message, "nothing was written") || f.tasks.runs() != 0 {
		t.Errorf("%+v runs=%d", e, f.tasks.runs())
	}
	c.call("apply", map[string]any{"plan_id": p.PlanID}).wantErr(t, core.CodeChanged)
}

func TestSecondRequestIdSpelledDifferentlyIsStillADuplicate(t *testing.T) {
	c, f := readyClient(t)
	started := slowTasks(f)
	id := c.expect(`"a"`)
	c.raw(`{"jsonrpc":"2.0","id":"a","method":"tasks.list"}`)
	<-started
	dup := c.expect(`"\u0061"`)
	c.raw(`{"jsonrpc":"2.0","id":"\u0061","method":"version"}`)
	c.await(dup).wantErr(t, core.CodeUsage)
	c.raw(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":"\u0061"}}`) // cancels the original
	c.await(id).wantErr(t, core.CodeTimeout)
}
