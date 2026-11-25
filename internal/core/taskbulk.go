package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// A bulk change is one verb applied to up to MaxBulk marked tasks. It is planned as one listing of
// every task and its exact command, confirmed once, and applied one task at a time through the same
// single-use, audited gate as a single write. Nothing is atomic: each task is re-read right before
// its write and skipped (and reported) when it no longer fits, a failure does not stop the rest, and
// a timeout leaves the remaining tasks unknown.

// MaxBulk is the most tasks one confirm may cover.
const MaxBulk = 25

// BulkItemTimeout bounds the re-read and the write of one task of a batch; BulkTotalTimeout bounds the
// whole batch. Front ends hand ApplyBulk a context with the second.
const (
	BulkItemTimeout  = 20 * time.Second
	BulkTotalTimeout = 5 * time.Minute
)

// BulkSpec is what to do to every task of a batch. Op is one of close, update (a status change),
// priority, label-add, label-remove and assign; the field that op needs is the only one read.
type BulkSpec struct {
	Op       TaskOp
	Status   string  // update
	Priority *int    // priority
	Label    string  // label-add, label-remove
	Assignee *string // assign; "" unassigns
}

// Validate refuses a spec that no task could be planned from.
func (s BulkSpec) Validate() error {
	switch s.Op {
	case TaskClose:
	case TaskUpdate:
		if !slices.Contains(TaskStatuses, s.Status) {
			return fmt.Errorf("status must be one of %s (use close to close)", strings.Join(TaskStatuses, ", "))
		}
	case TaskPriority:
		if s.Priority == nil || *s.Priority < 0 || *s.Priority > 4 {
			return errors.New("priority must be 0-4")
		}
	case TaskLabelAdd, TaskLabelRemove:
		if !LabelRE.MatchString(s.Label) {
			return fmt.Errorf("invalid label %q (letters, digits and : . _ / -, up to 64 characters, no spaces or commas)", s.Label)
		}
	case TaskAssign:
		if s.Assignee == nil || !AssigneeOK(*s.Assignee) {
			return errors.New("invalid assignee name (empty to unassign)")
		}
	default:
		return fmt.Errorf("a batch can close, change the status or priority, add or remove a label, or assign (not %q)", s.Op)
	}
	return nil
}

// Summary is the one line that says what the batch does, without the count.
func (s BulkSpec) Summary() string {
	switch s.Op {
	case TaskClose:
		return "close"
	case TaskUpdate:
		return "set the status to " + s.Status + " on"
	case TaskPriority:
		if s.Priority == nil {
			return "set the priority of"
		}
		return fmt.Sprintf("set the priority to %d on", *s.Priority)
	case TaskLabelAdd:
		return "add the label " + s.Label + " to"
	case TaskLabelRemove:
		return "remove the label " + s.Label + " from"
	case TaskAssign:
		if s.Assignee == nil {
			return "assign"
		}
		if *s.Assignee == "" {
			return "unassign"
		}
		return "assign " + *s.Assignee + " to"
	}
	return string(s.Op)
}

// change is the single-task change this spec means for t, planned against t as it is now.
func (s BulkSpec) change(t Task) TaskChange {
	ch := TaskChange{Op: s.Op, ID: t.ID, Before: t, Expect: t.Status}
	switch s.Op {
	case TaskUpdate:
		ch.Status, ch.IfStatus = s.Status, t.Status
	case TaskPriority:
		ch.Priority = s.Priority
	case TaskLabelAdd, TaskLabelRemove:
		ch.Label = s.Label
	case TaskAssign:
		ch.Assignee, ch.IfAssignee = s.Assignee, t.Assignee
	}
	return ch
}

// BulkItemText is the short line of one task of a batch: its id, title and what changes.
func BulkItemText(ch TaskChange) string {
	what := ""
	switch ch.Op {
	case TaskClose:
		what = "close"
	case TaskUpdate:
		what = fmt.Sprintf("status %s -> %s", orNone(ch.Before.Status), ch.Status)
	case TaskPriority:
		what = fmt.Sprintf("priority %d -> %d", ch.Before.Priority, *ch.Priority)
	case TaskLabelAdd:
		what = "add label " + ch.Label
	case TaskLabelRemove:
		what = "remove label " + ch.Label
	case TaskAssign:
		what = fmt.Sprintf("assignee %s -> %s", orNone(ch.IfAssignee), orNone(*ch.Assignee))
	}
	return strings.TrimSpace(ch.ID+"  "+ch.Before.Title) + "  (" + what + ")"
}

func orNone(s string) string {
	if s == "" {
		return "nobody"
	}
	return s
}

// BulkSkip is a marked task left out of the plan, and why.
type BulkSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// BulkPlan is a validated batch: one WritePlan per task, in the order given, each with its exact argv.
// It cannot be applied until Confirm is called.
type BulkPlan struct {
	id        uint64
	Spec      BulkSpec
	Items     []WritePlan
	Skipped   []BulkSkip
	confirmed bool
}

// Confirm marks the whole batch as approved by the user's one explicit yes.
func (p BulkPlan) Confirm() BulkPlan { p.confirmed = true; return p }

// PlanBulk plans spec against targets (the tasks as they are now, with their labels). It refuses an
// empty batch, one larger than MaxBulk and a repeated id; a task the change no longer fits (already
// closed, already that priority, the label is there) is left out and named in Skipped, and a batch of
// nothing but those is refused.
func PlanBulk(w TaskWriter, spec BulkSpec, targets []Task) (BulkPlan, error) {
	if err := spec.Validate(); err != nil {
		return BulkPlan{}, err
	}
	if err := CheckBulkSize(len(targets)); err != nil {
		return BulkPlan{}, err
	}
	seen := map[string]bool{}
	p := BulkPlan{id: planSeq.Add(1), Spec: spec}
	for _, t := range targets {
		if seen[t.ID] {
			return BulkPlan{}, fmt.Errorf("%s is in the batch twice", Sanitize(t.ID))
		}
		seen[t.ID] = true
		ch := spec.change(t)
		var err error
		if t.Status == "closed" && spec.Op == TaskClose {
			err = errors.New("already closed")
		} else if err = CheckFresh(ch, t); err == nil {
			var wp WritePlan
			if wp, err = PlanTaskChange(w, ch); err == nil {
				p.Items = append(p.Items, wp)
				continue
			}
		}
		p.Skipped = append(p.Skipped, BulkSkip{ID: t.ID, Reason: skipReason(t.ID, err)})
	}
	if len(p.Items) == 0 {
		return BulkPlan{}, fmt.Errorf("nothing to change: %s", Sanitize(skippedText(p.Skipped)))
	}
	return p, nil
}

// skipReason is why a task was left out, without its id (the line already names it) or the refusal's tail.
func skipReason(id string, err error) string {
	r := strings.TrimSuffix(err.Error(), ": nothing was written; refresh and plan the change again")
	r = strings.TrimPrefix(strings.TrimPrefix(r, id+" "), "is ")
	return Sanitize(r)
}

// Plural is "1 task", "3 tasks": a count and its noun, for the lines that report a batch.
func Plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func skippedText(s []BulkSkip) string {
	var out []string
	for _, k := range s[:min(len(s), 3)] {
		out = append(out, k.ID+" "+k.Reason)
	}
	if len(s) > 3 {
		out = append(out, fmt.Sprintf("and %d more", len(s)-3))
	}
	return strings.Join(out, "; ")
}

// CheckBulkSize refuses an empty batch and one over MaxBulk; front ends call it before reading every task.
func CheckBulkSize(n int) error {
	switch {
	case n == 0:
		return errors.New("no tasks are marked")
	case n > MaxBulk:
		return fmt.Errorf("a batch is at most %d tasks (%d are marked): unmark some and apply it in parts", MaxBulk, n)
	}
	return nil
}

// BulkOutcome is what became of one task of a batch.
type BulkOutcome string

const (
	// BulkApplied: the write landed.
	BulkApplied BulkOutcome = "applied"
	// BulkSkipped: nothing was written; the task changed since the plan, could not be re-read, or the
	// tracker refused the change.
	BulkSkipped BulkOutcome = "skipped"
	// BulkFailed: the write was refused or failed; the tracker says nothing was written.
	BulkFailed BulkOutcome = "failed"
	// BulkUnknown: the write may or may not have happened (timeout, cancel), or the batch ran out of
	// time before this task: refresh before trying again.
	BulkUnknown BulkOutcome = "unknown"
	// BulkNotRun: this task and the rest were never sent (the audit log could not be written, or the batch was interrupted).
	BulkNotRun BulkOutcome = "not_run"
)

// BulkItemResult is one task's outcome.
type BulkItemResult struct {
	ID      string      `json:"id"`
	Outcome BulkOutcome `json:"outcome"`
	Detail  string      `json:"detail,omitempty"`
	Argv    []string    `json:"argv,omitempty"`
}

// BulkReport is the typed result of a batch (schema_version 1).
type BulkReport struct {
	SchemaVersion int              `json:"schema_version"`
	BatchID       string           `json:"batch_id"`
	Op            string           `json:"op"`
	Applied       int              `json:"applied"`
	Skipped       int              `json:"skipped"`
	Failed        int              `json:"failed"`
	Unknown       int              `json:"unknown"`
	NotRun        int              `json:"not_run"`
	Items         []BulkItemResult `json:"items"`
	// PlanDigest is the digest of the plan this batch applied (additive; set by the front end that planned it).
	PlanDigest string `json:"plan_digest,omitempty"`
	// Error is the same error object every failing command prints, set (code unknown_outcome) when any task's
	// result is unknown, so a client reads one place. Additive.
	Error *ErrorBody `json:"error,omitempty"`
}

// Summary is the one line after a batch: "3 of 5 applied", then what else happened.
func (r BulkReport) Summary() string {
	out := fmt.Sprintf("%d of %d applied", r.Applied, len(r.Items))
	for _, c := range []struct {
		n    int
		what string
	}{{r.Skipped, "skipped"}, {r.Failed, "failed"}, {r.Unknown, "result unknown"}, {r.NotRun, "not run"}} {
		if c.n > 0 {
			out += fmt.Sprintf(", %d %s", c.n, c.what)
		}
	}
	return out
}

// AllApplied is whether every task of the batch was written; a task left out at plan time counts as
// not applied (it is in Items as skipped).
func (r BulkReport) AllApplied() bool { return r.Applied == len(r.Items) }

func (r *BulkReport) add(it BulkItemResult) {
	it.Detail = Sanitize(it.Detail)
	r.Items = append(r.Items, it)
	switch it.Outcome {
	case BulkApplied:
		r.Applied++
	case BulkSkipped:
		r.Skipped++
	case BulkFailed:
		r.Failed++
	case BulkUnknown:
		r.Unknown++
	case BulkNotRun:
		r.NotRun++
	}
}

// CheckBulkFresh compares a batch change with the task as it is now: the status it was planned
// against must still hold (close has no tracker-side guard), then the edit's own check.
func CheckBulkFresh(ch TaskChange, cur Task) error {
	if ch.Expect != "" && cur.Status != ch.Expect {
		return staleError{fmt.Sprintf("%s is now %s (it was %s)", ch.ID, cur.Status, ch.Expect)}
	}
	return CheckFresh(ch, cur)
}

func newBatchID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "batch-" + hex.EncodeToString(b)
}

// ApplyBulk runs a confirmed batch once. Before each task is written it is read again and checked
// (CheckBulkFresh); a task that no longer fits, or cannot be read, is skipped and reported, never
// written. A failure does not stop the rest. A timeout or cancel makes that task and every task after
// it unknown, and an audit log that cannot be written stops the batch (nothing is sent unrecorded).
// Every audit line of the batch carries the same batch id. An error means nothing was done at all.
func (a *Applier) ApplyBulk(ctx context.Context, p BulkPlan, r TaskReader) (BulkReport, error) {
	rep, err := a.applyBulk(ctx, p, r)
	if err != nil {
		return rep, err
	}
	for _, k := range p.Skipped { // left out at plan time: in the report, so a batch is not "applied" while any task is not
		rep.add(BulkItemResult{ID: k.ID, Outcome: BulkSkipped, Detail: k.Reason + ": left out of the plan"})
	}
	return rep, nil
}

func (a *Applier) applyBulk(ctx context.Context, p BulkPlan, r TaskReader) (BulkReport, error) {
	switch {
	case !p.confirmed:
		return BulkReport{}, ErrNotConfirmed
	case r == nil:
		return BulkReport{}, ErrCannotVerify
	}
	if err := CheckBulkSize(len(p.Items)); err != nil {
		return BulkReport{}, err
	}
	if err := a.g.claim(p.id); err != nil {
		return BulkReport{}, err
	}
	rep := BulkReport{SchemaVersion: 1, BatchID: newBatchID(), Op: string(p.Spec.Op)}
	for i, it := range p.Items {
		id := it.Change.ID
		res := BulkItemResult{ID: id, Argv: it.Argv}
		if ctx.Err() != nil { // nothing more is sent
			for _, rest := range p.Items[i:] {
				if errors.Is(ctx.Err(), context.Canceled) { // interrupted: these were never started, so nothing was sent for them
					rep.add(BulkItemResult{ID: rest.Change.ID, Outcome: BulkNotRun, Argv: rest.Argv, Detail: "not started: interrupted; nothing was sent for this task"})
					continue
				}
				rep.add(BulkItemResult{ID: rest.Change.ID, Outcome: BulkUnknown, Argv: rest.Argv, Detail: "not started: the time ran out; refresh before trying again"})
			}
			return rep, nil
		}
		ictx, cancel := context.WithTimeout(ctx, BulkItemTimeout)
		cur, err := r.ReadTask(ictx, id)
		if err != nil {
			cancel()
			res.Outcome, res.Detail = BulkSkipped, "could not be read again, so nothing was written ("+err.Error()+")"
			rep.add(res)
			continue
		}
		if err := CheckBulkFresh(it.Change, cur); err != nil {
			cancel()
			res.Outcome, res.Detail = BulkSkipped, strings.TrimSuffix(err.Error(), ": nothing was written; refresh and plan the change again")+": nothing was written"
			rep.add(res)
			continue
		}
		_, ran, err := a.apply(ictx, it.Confirm(), rep.BatchID)
		cancel()
		switch {
		case err == nil:
			res.Outcome = BulkApplied
		case !ran: // refused before the write: the audit log, or a reused plan
			for _, rest := range p.Items[i:] {
				rep.add(BulkItemResult{ID: rest.Change.ID, Outcome: BulkNotRun, Argv: rest.Argv, Detail: "not sent: " + err.Error()})
			}
			return rep, nil
		case errors.Is(err, ErrAppliedMismatch):
			res.Outcome, res.Detail = BulkApplied, err.Error()
		case errors.Is(err, ErrOutcomeUnknown):
			res.Outcome, res.Detail = BulkUnknown, err.Error()
			rep.add(res)
			for _, rest := range p.Items[i+1:] {
				rep.add(BulkItemResult{ID: rest.Change.ID, Outcome: BulkUnknown, Argv: rest.Argv, Detail: "not started: the one before it has an unknown result; refresh before trying again"})
			}
			return rep, nil
		case errors.Is(err, ErrChanged):
			res.Outcome, res.Detail = BulkSkipped, "changed since the plan: nothing was written"
		default:
			res.Outcome, res.Detail = BulkFailed, err.Error()
		}
		rep.add(res)
	}
	return rep, nil
}
