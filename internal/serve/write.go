package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// The writes: plan.* builds exactly what core's gate would run and holds it under a server-made plan id;
// apply claims that id once. The client never sends an argv. Everything past the id is core's gate:
// the confirm check, the single-use claim, the audit line written before the write (a failed audit
// blocks it) and the outcome recorded after, with a timeout or cancel being "result unknown".

// taskFields are the params each plan.task.* accepts: a field of another verb is refused, not ignored.
var taskFields = map[core.TaskOp][]string{
	core.TaskCreate:      {"title", "description", "priority", "type"},
	core.TaskUpdate:      {"id", "title", "description", "priority", "status"},
	core.TaskClose:       {"id", "reason"},
	core.TaskReopen:      {"id", "reason"},
	core.TaskLabelAdd:    {"id", "label"},
	core.TaskLabelRemove: {"id", "label"},
	core.TaskPriority:    {"id", "priority"},
	core.TaskAssign:      {"id", "assignee", "clear"},
	core.TaskDepAdd:      {"id", "blocked_by", "blocks"},
	core.TaskDepRemove:   {"id", "blocked_by", "blocks"},
	core.TaskAddComment:  {"id", "text"},
}

type taskParams struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Reason      string  `json:"reason"`
	Type        string  `json:"type"`
	Priority    *int    `json:"priority"`
	Label       string  `json:"label"`
	Assignee    *string `json:"assignee"`
	Clear       bool    `json:"clear"`
	BlockedBy   string  `json:"blocked_by"`
	Blocks      string  `json:"blocks"`
	Text        string  `json:"text"`
}

func (e *Engine) hold(h *heldPlan, p core.ServePlan) core.ServePlan {
	h.id = newPlanID()
	p.PlanID, p.PlanDigest, p.Kind = h.id, h.digest, h.kind
	h.expires = e.plans.add(h)
	p.ExpiresAt = h.expires.UTC().Format(time.RFC3339)
	return p
}

// gateErr is the error of a write the gate was handed: a timeout or a cancel there is a result
// unknown, never "not applied".
func gateErr(err error, id, digest string) *apiError {
	code := core.CodeFor(err)
	msg := core.DescribeWriteError(err)
	if code == core.CodeTimeout {
		code, msg = core.CodeUnknownOutcome, "result unknown: the request ended before the write was confirmed; refresh before retrying"
	}
	return &apiError{rpc: rpcServer, code: code, msg: msg, planID: id, digest: digest, outcome: outcomeOf(code)}
}

func (e *Engine) planTask(op core.TaskOp) handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var keys map[string]json.RawMessage
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &keys)
		}
		for k := range keys {
			if !slices.Contains(taskFields[op], k) {
				return nil, usageErr("%q is not a field of plan.task.%s (it takes: %s)", core.Sanitize(truncate(k, 40)), op, strings.Join(taskFields[op], ", "))
			}
		}
		var p taskParams
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		pr := core.ResolveProviders(e.entries(ctx))
		w, r := pr.Writer, pr.Reader
		if w == nil {
			return nil, failErr(core.CodeNotConnected, "tasks: not connected. %s", core.HowToConnectFor(core.DomainTasks))
		}
		needRead := func() error {
			if r == nil {
				return failErr(core.CodeUnsupported, "plan.task.%s: cannot read the task's current state, so it cannot be changed safely", op)
			}
			return nil
		}
		read := func(id string) (core.Task, error) {
			cur, err := r.ReadTask(ctx, id)
			if err != nil {
				return cur, failErr(core.CodeFor(err), "could not read %s first: %s", core.Sanitize(id), readText(err))
			}
			return cur, nil
		}
		ch := core.TaskChange{Op: op, ID: p.ID, Title: p.Title, Description: p.Description, Reason: p.Reason, Type: p.Type, Priority: p.Priority}
		if op != core.TaskCreate && !core.TaskIDRE.MatchString(p.ID) {
			return nil, usageErr("invalid issue id %q", core.Sanitize(truncate(p.ID, 80)))
		}
		switch op {
		case core.TaskUpdate:
			ch.Status = p.Status
			if ch.Status != "" {
				if !slices.Contains(core.TaskStatuses, ch.Status) {
					return nil, usageErr("status must be one of %s (use plan.task.close to close)", strings.Join(core.TaskStatuses, ", "))
				}
				if err := needRead(); err != nil {
					return nil, err
				}
				cur, err := read(ch.ID) // a status change names the status the task has now, so a change by someone else is refused
				if err != nil {
					return nil, err
				}
				ch.IfStatus, ch.Before = cur.Status, cur
				if ch.Status == cur.Status && (ch.Title != "" || ch.Description != "" || ch.Priority != nil) {
					ch.Status, ch.IfStatus = "", ""
				}
			}
		case core.TaskReopen:
			if err := needRead(); err != nil {
				return nil, err
			}
			cur, err := read(ch.ID)
			if err != nil {
				return nil, err
			}
			if cur.ID != ch.ID {
				return nil, usageErr("%s is not a whole task id (it resolves to %s): use the full id", core.Sanitize(ch.ID), core.Sanitize(cur.ID))
			}
			if cur.Status != "closed" {
				return nil, failErr(core.CodeRefused, "%s is not closed (it is %s), so there is nothing to reopen", core.Sanitize(ch.ID), core.Sanitize(cur.Status))
			}
			ch.Before, ch.Expect = cur, cur.Status
		case core.TaskLabelAdd, core.TaskLabelRemove, core.TaskPriority, core.TaskAssign, core.TaskAddComment:
			switch op {
			case core.TaskLabelAdd, core.TaskLabelRemove:
				ch.Label = p.Label
			case core.TaskAddComment:
				ch.Text = p.Text
			case core.TaskAssign:
				if (p.Assignee != nil) == p.Clear {
					return nil, usageErr("assign needs exactly one of assignee and clear")
				}
				who := ""
				if p.Assignee != nil {
					who = *p.Assignee
					if strings.TrimSpace(who) == "" {
						return nil, usageErr("the name is empty (use clear to unassign)")
					}
				}
				ch.Assignee = &who
			}
			if err := needRead(); err != nil {
				return nil, err
			}
			cur, err := read(ch.ID)
			if err != nil {
				return nil, err
			}
			ch.Before, ch.Expect = cur, cur.Status
			if op == core.TaskAssign {
				ch.IfAssignee = cur.Assignee
			}
		case core.TaskDepAdd, core.TaskDepRemove:
			if (p.Blocks == "") == (p.BlockedBy == "") {
				return nil, usageErr("a dependency needs exactly one of blocks and blocked_by")
			}
			if pr.Source == nil {
				return nil, failErr(core.CodeUnsupported, "cannot read the whole tracker, so the dependency cannot be checked")
			}
			if err := needRead(); err != nil {
				return nil, err
			}
			if _, err := read(ch.ID); err != nil {
				return nil, err
			}
			snap, err := pr.Source.Snapshot(ctx)
			if err != nil {
				return nil, failErr(core.CodeFor(err), "could not read the tracker: %s", readText(err))
			}
			held, blocker := ch.ID, p.Blocks
			if p.BlockedBy != "" {
				blocker = p.BlockedBy
			} else { // ID blocks OTHER: OTHER is the held task
				held, blocker = p.Blocks, ch.ID
			}
			if !core.TaskIDRE.MatchString(held) || !core.TaskIDRE.MatchString(blocker) {
				return nil, usageErr("invalid issue id")
			}
			var err2 error
			if ch, err2 = taskview.Build(snap, e.cfg.Now()).PlanDep(op == core.TaskDepAdd, held, blocker); err2 != nil {
				return nil, usageErr("%v", err2)
			}
		}
		plan, err := core.PlanTaskChange(w, ch)
		if err != nil {
			return nil, usageErr("%v", err)
		}
		src, now := pr.Source, e.cfg.Now
		h := &heldPlan{kind: "task", digest: plan.Digest()}
		h.apply = func(ctx context.Context) (*core.ServeApplied, bool, error) {
			if op == core.TaskReopen { // someone else may have reopened it, or changed it, since the plan
				cur, err := r.ReadTask(ctx, ch.ID)
				if err != nil {
					return nil, false, fromErr(fmt.Errorf("%w (%s)", core.ErrCannotVerify, core.Sanitize(err.Error())))
				}
				if cur.ID != ch.ID || cur.Status != ch.Expect {
					return nil, false, fromErr(core.Stale(fmt.Sprintf("%s is now %s (it was %s)", ch.ID, cur.Status, ch.Expect)))
				}
			} else if err := core.FreshDep(ctx, r, plan.Change, taskview.RecheckDep(src, now)); err != nil {
				return nil, false, fromErr(err) // nothing was written: the plan no longer fits
			}
			res, err := core.NewApplier(w, e.audit()).Apply(ctx, plan.Confirm())
			if err != nil {
				var ae *core.AuditError
				return nil, !errors.As(err, &ae), gateErr(err, h.id, h.digest)
			}
			return &core.ServeApplied{PlanID: h.id, PlanDigest: h.digest, Kind: "task", Outcome: "applied", Task: &res}, true, nil
		}
		return e.hold(h, core.ServePlan{Op: string(op), Lines: plan.Lines, Argv: plan.Argv}), nil
	}
}

var bulkOps = map[string]core.TaskOp{
	"close": core.TaskClose, "status": core.TaskUpdate, "priority": core.TaskPriority,
	"label-add": core.TaskLabelAdd, "label-remove": core.TaskLabelRemove, "assign": core.TaskAssign,
}

func (e *Engine) planBulk(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		IDs   []string `json:"ids"`
		Op    string   `json:"op"`
		Value string   `json:"value"`
		Clear bool     `json:"clear"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	taskOp, ok := bulkOps[p.Op]
	if !ok {
		return nil, usageErr("op must be one of close, status, priority, label-add, label-remove, assign")
	}
	spec := core.BulkSpec{Op: taskOp}
	switch taskOp {
	case core.TaskUpdate:
		spec.Status = p.Value
	case core.TaskPriority:
		n, err := strconv.Atoi(p.Value)
		if err != nil {
			return nil, usageErr("value must be a priority 0-4")
		}
		spec.Priority = &n
	case core.TaskLabelAdd, core.TaskLabelRemove:
		spec.Label = p.Value
	case core.TaskAssign:
		if p.Clear == (p.Value != "") {
			return nil, usageErr("assign needs exactly one of value (a name) and clear")
		}
		spec.Assignee = &p.Value
	}
	if taskOp == core.TaskClose && p.Value != "" {
		return nil, usageErr("close takes no value")
	}
	if p.Clear && taskOp != core.TaskAssign {
		return nil, usageErr("clear is for assign")
	}
	if err := spec.Validate(); err != nil {
		return nil, usageErr("%v", err)
	}
	if err := core.CheckBulkSize(len(p.IDs)); err != nil {
		return nil, usageErr("%v", err)
	}
	for _, id := range p.IDs {
		if !core.TaskIDRE.MatchString(id) {
			return nil, usageErr("invalid issue id %q", core.Sanitize(truncate(id, 80)))
		}
	}
	pr := core.ResolveProviders(e.entries(ctx))
	w, r := pr.Writer, pr.Reader
	if w == nil {
		return nil, failErr(core.CodeNotConnected, "tasks: not connected. %s", core.HowToConnectFor(core.DomainTasks))
	}
	if r == nil {
		return nil, failErr(core.CodeUnsupported, "cannot read the tasks' current state, so they cannot be changed safely")
	}
	targets := make([]core.Task, 0, len(p.IDs))
	for _, id := range p.IDs {
		t, err := r.ReadTask(ctx, id)
		if err != nil {
			return nil, failErr(core.CodeFor(err), "could not read %s first: %s", core.Sanitize(id), readText(err))
		}
		if t.ID != id { // the tracker resolved a prefix or alias: only whole ids go in a batch
			return nil, usageErr("%s is not a whole task id (it resolves to %s): use the full id", core.Sanitize(id), core.Sanitize(t.ID))
		}
		targets = append(targets, t)
	}
	plan, err := core.PlanBulk(w, spec, targets)
	if err != nil {
		return nil, usageErr("%v", err)
	}
	items := make([]core.BulkDryRunItem, len(plan.Items))
	for i, it := range plan.Items {
		items[i] = core.BulkDryRunItem{ID: it.Change.ID, Title: it.Change.Before.Title, Change: core.BulkItemText(it.Change), Argv: it.Argv}
	}
	h := &heldPlan{kind: "bulk", digest: plan.Digest()}
	h.apply = func(ctx context.Context) (*core.ServeApplied, bool, error) {
		rep, err := core.NewApplier(w, e.audit()).ApplyBulk(ctx, plan.Confirm(), r)
		if err != nil { // an error means nothing was done at all
			return nil, false, gateErr(err, h.id, h.digest)
		}
		rep.PlanDigest = h.digest
		out := "applied"
		switch {
		case rep.Unknown > 0:
			out = "unknown"
		case !rep.AllApplied():
			out = "partial"
		}
		return &core.ServeApplied{PlanID: h.id, PlanDigest: h.digest, Kind: "bulk", Outcome: out, Bulk: &rep}, true, nil
	}
	return e.hold(h, core.ServePlan{Op: p.Op, Lines: []string{plan.Spec.Summary() + " " + core.Plural(len(plan.Items), "task")}, Items: items, Skipped: plan.Skipped}), nil
}

func (e *Engine) planBoard(op core.BoardOp) handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Channel string `json:"channel"`
			ID      string `json:"id"`
			Text    string `json:"text"`
			Again   bool   `json:"again"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if (op == core.BoardPost && p.ID != "") || (op == core.BoardReply && p.Channel != "") {
			return nil, usageErr("plan.board.%s takes %s", op, map[core.BoardOp]string{core.BoardPost: "channel and text", core.BoardReply: "id and text"}[op])
		}
		w := core.ResolveProviders(e.entries(ctx)).BoardWriter
		if w == nil {
			return nil, failErr(core.CodeNotConnected, "board: not connected. %s", core.HowToConnectFor(core.DomainBoard))
		}
		ch := core.BoardChange{Op: op, Channel: p.Channel, ID: p.ID, Text: p.Text}
		au := e.audit()
		// The key comes from the content, not the clock; the audit log says whether this is a repeat.
		nonce, repeat, err := core.BoardCLINonce(au, w.Target(), ch, core.BoardKeyWindow, time.Now())
		if err != nil {
			return nil, failErr(core.CodeAuditFailed, "board %s: %v", op, err)
		}
		ch.Nonce = nonce
		if repeat && !p.Again {
			return nil, failErr(core.CodeRefused, "the same message was posted, or tried, from this machine in the last %d minutes (the board may already have it: refresh it first); nothing was planned. again:true plans it anyway", int(core.BoardKeyWindow/time.Minute))
		}
		plan, err := core.PlanBoardChange(w, ch)
		if err != nil {
			return nil, usageErr("%v", err)
		}
		dctx, cancel := context.WithTimeout(ctx, 2*core.DefaultExecTimeout)
		defer cancel()
		h := &heldPlan{kind: "board", digest: plan.Digest()}
		h.apply = func(ctx context.Context) (*core.ServeApplied, bool, error) {
			// the repeat guard ran at plan time; run it again now, so two plans made before either was applied
			// cannot both post the same message under the same key
			if r, ok := au.(core.AuditReader); ok {
				entries, err := r.Entries()
				if err != nil {
					return nil, false, failErr(core.CodeAuditFailed, "board %s: %v", op, err)
				}
				for _, en := range entries {
					if en.Domain == string(core.DomainBoard) && slices.Contains(en.Argv, plan.Key) {
						return nil, false, &apiError{rpc: rpcServer, code: core.CodeChanged, msg: "this message was posted, or tried, since the plan was made (the board may already have it: refresh it first); nothing was written. Plan it again with again:true to post it anyway"}
					}
				}
			}
			res, err := core.NewBoardApplier(w, au).Apply(ctx, plan.Confirm())
			if err != nil {
				var ae *core.AuditError
				return nil, !errors.As(err, &ae), gateErr(err, h.id, h.digest)
			}
			return &core.ServeApplied{PlanID: h.id, PlanDigest: h.digest, Kind: "board", Outcome: "applied", Board: &res}, true, nil
		}
		return e.hold(h, core.ServePlan{Op: string(op), Lines: plan.Lines, Context: w.Describe(dctx, ch), Argv: plan.Argv}), nil
	}
}

func parsePlanID(s string) (string, error) {
	if !planIDRE.MatchString(s) {
		return "", usageErr("plan_id is a plan id from a plan.* result")
	}
	return s, nil
}

func (e *Engine) planCancel(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		PlanID string `json:"plan_id"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	id, err := parsePlanID(p.PlanID)
	if err != nil {
		return nil, err
	}
	if err := e.plans.cancel(id); err != nil {
		return nil, err
	}
	return struct {
		PlanID    string `json:"plan_id"`
		Cancelled bool   `json:"cancelled"`
	}{id, true}, nil
}

// apply claims a plan once and runs it. Writes are serialized: the next apply waits for this one.
func (e *Engine) apply(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		PlanID         string  `json:"plan_id"`
		ExpectedDigest *string `json:"expected_digest"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	id, err := parsePlanID(p.PlanID)
	if err != nil {
		return nil, err
	}
	expect := ""
	if p.ExpectedDigest != nil { // given, it must be a digest: an empty one is an error, never "no check"
		var ok bool
		if expect, ok = core.ParseDigest(*p.ExpectedDigest); !ok {
			return nil, usageErr("expected_digest takes the 64 hex digits of a plan_digest (it was empty or malformed, so nothing was checked or written)")
		}
	}
	select {
	case e.writeMu <- struct{}{}:
		defer func() { <-e.writeMu }()
	case <-ctx.Done(): // before the claim: nothing was written and the plan is still held
		return nil, ctx.Err()
	}
	h, err := e.plans.take(id, expect)
	if err != nil {
		return nil, err
	}
	res, wrote, err := e.runHeld(ctx, h)
	if wrote {
		e.invalidate()
	}
	if err != nil {
		if !wrote {
			e.plans.unwritten(id)
		}
		var ae *apiError
		if errors.As(err, &ae) { // say which plan, and what became of it, whichever way it failed
			if ae.planID == "" {
				ae.planID, ae.digest = id, h.digest
			}
			if !wrote && ctx.Err() != nil && ae.code == core.CodeRefused { // the re-read could not finish: the request ended first
				ae.code = core.CodeTimeout
			}
			if ae.outcome == "" {
				ae.outcome = outcomeOf(ae.code)
			}
		}
		return nil, err
	}
	return res, nil
}

// runHeld runs a claimed plan. A panic that gets past the gate's own recovery (which makes a write that
// panicked a result unknown) is still a result unknown with the plan named, never a bare internal error,
// because the plan is spent and the write may have been sent.
func (e *Engine) runHeld(ctx context.Context, h *heldPlan) (res *core.ServeApplied, wrote bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, wrote = nil, true
			err = &apiError{rpc: rpcServer, code: core.CodeUnknownOutcome, msg: "result unknown: the write failed unexpectedly; refresh before retrying", planID: h.id, digest: h.digest, outcome: "unknown"}
		}
	}()
	return h.apply(ctx)
}
