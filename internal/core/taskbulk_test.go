package core_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// bulkWriter records every argv it runs and fails the ids in failOn.
type bulkWriter struct {
	fakeWriter
	failOn map[string]error
}

func (b *bulkWriter) RunTask(ctx context.Context, argv []string) (core.TaskResult, error) {
	b.ran = append(b.ran, argv)
	return core.TaskResult{ID: argv[2]}, b.failOn[argv[2]]
}

// mapReader answers a re-read from cur (by id) and counts the reads.
type mapReader struct {
	cur   map[string]core.Task
	errOn map[string]error
	reads []string
}

func (m *mapReader) ReadTask(_ context.Context, id string) (core.Task, error) {
	m.reads = append(m.reads, id)
	if err := m.errOn[id]; err != nil {
		return core.Task{}, err
	}
	return m.cur[id], nil
}

func tasksN(n int) []core.Task {
	var out []core.Task
	for i := 1; i <= n; i++ {
		out = append(out, core.Task{ID: fmt.Sprintf("t-%d", i), Title: fmt.Sprintf("task %d", i), Status: "open", Priority: 2})
	}
	return out
}

func readerOf(ts []core.Task) *mapReader {
	m := &mapReader{cur: map[string]core.Task{}, errOn: map[string]error{}}
	for _, t := range ts {
		m.cur[t.ID] = t
	}
	return m
}

var closeSpec = core.BulkSpec{Op: core.TaskClose}

func TestPlanBulkBoundsTheBatch(t *testing.T) {
	w := &fakeWriter{}
	if _, err := core.PlanBulk(w, closeSpec, nil); err == nil {
		t.Error("an empty batch is refused")
	}
	p, err := core.PlanBulk(w, closeSpec, tasksN(core.MaxBulk))
	if err != nil || len(p.Items) != core.MaxBulk {
		t.Fatalf("exactly the bound is allowed: %v, %d items", err, len(p.Items))
	}
	if _, err := core.PlanBulk(w, closeSpec, tasksN(core.MaxBulk+1)); err == nil || !strings.Contains(err.Error(), "at most 25") {
		t.Errorf("one over the bound is refused: %v", err)
	}
	if _, err := core.PlanBulk(w, closeSpec, append(tasksN(2), tasksN(1)...)); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a repeated id is refused: %v", err)
	}
}

func TestApplyBulkRechecksTheBound(t *testing.T) {
	// A plan built by hand (or grown after planning) over the bound never runs: ApplyBulk checks it again.
	w, a := &bulkWriter{}, &memAudit{}
	ap := core.NewApplier(w, a)
	ts := tasksN(core.MaxBulk)
	p, err := core.PlanBulk(w, closeSpec, ts)
	if err != nil {
		t.Fatal(err)
	}
	p.Items = append(p.Items, p.Items[0])
	if _, err := ap.ApplyBulk(context.Background(), p.Confirm(), readerOf(ts)); err == nil || len(w.ran) != 0 || len(a.lines) != 0 {
		t.Errorf("an oversized plan runs nothing: err=%v ran=%d audit=%d", err, len(w.ran), len(a.lines))
	}
}

func TestPlanBulkLeavesOutWhatNoLongerFits(t *testing.T) {
	w := &fakeWriter{}
	ts := tasksN(4)
	ts[1].Status = "closed"
	ts[2].Labels = []string{"ui"}
	p, err := core.PlanBulk(w, core.BulkSpec{Op: core.TaskLabelAdd, Label: "ui"}, ts)
	if err != nil || len(p.Items) != 3 || len(p.Skipped) != 1 || p.Skipped[0].ID != "t-3" {
		t.Fatalf("a task that has the label is skipped: %v %+v", err, p.Skipped)
	}
	p, err = core.PlanBulk(w, closeSpec, ts)
	if err != nil || len(p.Items) != 3 || p.Skipped[0].ID != "t-2" || !strings.Contains(p.Skipped[0].Reason, "already closed") {
		t.Fatalf("a closed task is skipped when closing: %v %+v", err, p.Skipped)
	}
	only := tasksN(1)
	only[0].Status = "closed"
	if _, err := core.PlanBulk(w, closeSpec, only); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Errorf("a batch of nothing to do is refused: %v", err)
	}
}

func TestPlanBulkBuildsOneExactArgvPerTask(t *testing.T) {
	w := &fakeWriter{}
	one := 1
	for name, spec := range map[string]core.BulkSpec{
		"status":   {Op: core.TaskUpdate, Status: "blocked"},
		"priority": {Op: core.TaskPriority, Priority: &one},
		"assign":   {Op: core.TaskAssign, Assignee: sp("ada")},
		"unassign": {Op: core.TaskAssign, Assignee: sp("")},
		"remove":   {Op: core.TaskLabelRemove, Label: "ui"},
	} {
		ts := tasksN(3)
		for i := range ts {
			ts[i].Labels, ts[i].Assignee = []string{"ui"}, "bob"
		}
		p, err := core.PlanBulk(w, spec, ts)
		if err != nil || len(p.Items) != 3 {
			t.Fatalf("%s: %v", name, err)
		}
		for i, it := range p.Items {
			if it.Change.ID != ts[i].ID || it.Argv[2] != ts[i].ID || it.Change.Expect != "open" {
				t.Errorf("%s: item %d = %+v", name, i, it)
			}
		}
	}
	for name, spec := range map[string]core.BulkSpec{
		"empty":        {},
		"status":       {Op: core.TaskUpdate, Status: "closed"},
		"priority":     {Op: core.TaskPriority},
		"priority 9":   {Op: core.TaskPriority, Priority: ip(9)},
		"label":        {Op: core.TaskLabelAdd, Label: "a b"},
		"assign":       {Op: core.TaskAssign},
		"delete":       {Op: "delete"},
		"create":       {Op: core.TaskCreate},
		"comment":      {Op: core.TaskAddComment},
		"flag label":   {Op: core.TaskLabelAdd, Label: "--force"},
		"hidden label": {Op: core.TaskLabelAdd, Label: "a​b"},
	} {
		if _, err := core.PlanBulk(w, spec, tasksN(2)); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

func TestApplyBulkNeedsTheConfirmAndRunsOnce(t *testing.T) {
	w, a := &bulkWriter{}, &memAudit{}
	ap := core.NewApplier(w, a)
	ts := tasksN(3)
	p, _ := core.PlanBulk(w, closeSpec, ts)
	if _, err := ap.ApplyBulk(context.Background(), p, readerOf(ts)); !errors.Is(err, core.ErrNotConfirmed) || len(w.ran) != 0 {
		t.Fatalf("unconfirmed never runs: %v ran=%d", err, len(w.ran))
	}
	if _, err := ap.ApplyBulk(context.Background(), p.Confirm(), nil); !errors.Is(err, core.ErrCannotVerify) || len(w.ran) != 0 {
		t.Fatalf("without a reader nothing is written: %v ran=%d", err, len(w.ran))
	}
	rep, err := ap.ApplyBulk(context.Background(), p.Confirm(), readerOf(ts))
	if err != nil || rep.Applied != 3 || !rep.AllApplied() || rep.SchemaVersion != 1 || len(w.ran) != 3 {
		t.Fatalf("one confirm applies the whole batch: %v %+v ran=%d", err, rep, len(w.ran))
	}
	if _, err := ap.ApplyBulk(context.Background(), p.Confirm(), readerOf(ts)); !errors.Is(err, core.ErrAlreadyApplied) || len(w.ran) != 3 {
		t.Errorf("a batch is single use: %v ran=%d", err, len(w.ran))
	}
}

func TestApplyBulkAuditsEveryTaskUnderOneBatchID(t *testing.T) {
	w, a := &bulkWriter{failOn: map[string]error{"t-2": errors.New("boom")}}, &memAudit{}
	ap := core.NewApplier(w, a)
	ts := tasksN(3)
	p, _ := core.PlanBulk(w, closeSpec, ts)
	rep, _ := ap.ApplyBulk(context.Background(), p.Confirm(), readerOf(ts))
	if rep.BatchID == "" || len(a.lines) != 6 { // an attempt and an outcome per task
		t.Fatalf("batch %q audit lines %d", rep.BatchID, len(a.lines))
	}
	for _, l := range a.lines {
		if l.Batch != rep.BatchID {
			t.Errorf("audit line without the batch id: %+v", l)
		}
	}
	phases := ""
	for _, l := range a.lines {
		phases += l.Phase + " "
	}
	if phases != "attempt ok attempt error attempt ok " {
		t.Errorf("phases = %s", phases)
	}
	p2, _ := core.PlanBulk(w, closeSpec, ts)
	rep2, _ := ap.ApplyBulk(context.Background(), p2.Confirm(), readerOf(ts))
	if rep2.BatchID == rep.BatchID {
		t.Error("each batch has its own id")
	}
}

func TestApplyBulkSkipsATaskThatChangedAndNeverWritesIt(t *testing.T) {
	w, a := &bulkWriter{}, &memAudit{}
	ap := core.NewApplier(w, a)
	ts := tasksN(3)
	p, _ := core.PlanBulk(w, closeSpec, ts)
	now := readerOf(ts)
	c := now.cur["t-2"]
	c.Status = "in_progress" // someone moved it after the plan
	now.cur["t-2"] = c
	rep, err := ap.ApplyBulk(context.Background(), p.Confirm(), now)
	if err != nil || rep.Applied != 2 || rep.Skipped != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
	for _, argv := range w.ran {
		if argv[2] == "t-2" {
			t.Fatalf("the changed task was written: %v", w.ran)
		}
	}
	if it := rep.Items[1]; it.ID != "t-2" || it.Outcome != core.BulkSkipped || !strings.Contains(it.Detail, "now in_progress") {
		t.Errorf("the skip is reported plainly: %+v", it)
	}
	if len(a.lines) != 4 {
		t.Errorf("a skipped task writes no audit line: %d", len(a.lines))
	}
}

func TestApplyBulkSkipsWhatTheEditNoLongerFits(t *testing.T) {
	w := &bulkWriter{}
	ap := core.NewApplier(w, &memAudit{})
	ts := tasksN(3)
	spec := core.BulkSpec{Op: core.TaskPriority, Priority: ip(1)}
	p, _ := core.PlanBulk(w, spec, ts)
	now := readerOf(ts)
	c := now.cur["t-1"]
	c.Priority = 1 // already there
	now.cur["t-1"] = c
	c = now.cur["t-3"]
	c.Priority = 4 // moved by someone else
	now.cur["t-3"] = c
	rep, _ := ap.ApplyBulk(context.Background(), p.Confirm(), now)
	if rep.Applied != 1 || rep.Skipped != 2 || len(w.ran) != 1 || w.ran[0][2] != "t-2" {
		t.Fatalf("%+v ran=%v", rep, w.ran)
	}
}

func TestApplyBulkSkipsATaskItCannotReadAgain(t *testing.T) {
	w := &bulkWriter{}
	ap := core.NewApplier(w, &memAudit{})
	ts := tasksN(3)
	p, _ := core.PlanBulk(w, closeSpec, ts)
	now := readerOf(ts)
	now.errOn["t-1"] = errors.New("gone")
	rep, _ := ap.ApplyBulk(context.Background(), p.Confirm(), now)
	if rep.Applied != 2 || rep.Skipped != 1 || rep.Items[0].Outcome != core.BulkSkipped || len(w.ran) != 2 {
		t.Fatalf("%+v ran=%v", rep, w.ran)
	}
}

func TestApplyBulkContinuesPastAFailureAndReportsIt(t *testing.T) {
	w := &bulkWriter{failOn: map[string]error{"t-2": errors.New("bd said no"), "t-4": core.ErrChanged}}
	ap := core.NewApplier(w, &memAudit{})
	ts := tasksN(5)
	p, _ := core.PlanBulk(w, closeSpec, ts)
	rep, err := ap.ApplyBulk(context.Background(), p.Confirm(), readerOf(ts))
	if err != nil || len(w.ran) != 5 || rep.Applied != 3 || rep.Failed != 1 || rep.Skipped != 1 {
		t.Fatalf("%v %+v ran=%d", err, rep, len(w.ran))
	}
	if got := rep.Summary(); got != "3 of 5 applied, 1 skipped, 1 failed" {
		t.Errorf("summary = %q", got)
	}
	if rep.Items[1].Outcome != core.BulkFailed || !strings.Contains(rep.Items[1].Detail, "bd said no") {
		t.Errorf("the failure is named: %+v", rep.Items[1])
	}
}

func TestApplyBulkAnUnknownResultLeavesTheRestUnknown(t *testing.T) {
	w := &bulkWriter{failOn: map[string]error{"t-2": core.UnknownOutcome("timed out")}}
	a := &memAudit{}
	ap := core.NewApplier(w, a)
	ts := tasksN(4)
	p, _ := core.PlanBulk(w, closeSpec, ts)
	rep, _ := ap.ApplyBulk(context.Background(), p.Confirm(), readerOf(ts))
	if len(w.ran) != 2 || rep.Applied != 1 || rep.Unknown != 3 || rep.Failed != 0 {
		t.Fatalf("%+v ran=%d", rep, len(w.ran))
	}
	if last := a.lines[len(a.lines)-1]; last.Phase != "unknown" {
		t.Errorf("the audit says unknown, not error: %+v", last)
	}
}

func TestApplyBulkADeadContextSendsNothing(t *testing.T) {
	w := &bulkWriter{}
	ap := core.NewApplier(w, &memAudit{})
	ts := tasksN(3)
	p, _ := core.PlanBulk(w, closeSpec, ts)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, _ := ap.ApplyBulk(ctx, p.Confirm(), readerOf(ts))
	if len(w.ran) != 0 || rep.Unknown != 3 {
		t.Fatalf("%+v ran=%d", rep, len(w.ran))
	}
}

func TestApplyBulkFailsClosedWhenTheAuditCannotBeWritten(t *testing.T) {
	w, a := &bulkWriter{}, &memAudit{err: errors.New("disk full")}
	ap := core.NewApplier(w, a)
	ts := tasksN(3)
	p, _ := core.PlanBulk(w, closeSpec, ts)
	rep, _ := ap.ApplyBulk(context.Background(), p.Confirm(), readerOf(ts))
	if len(w.ran) != 0 || rep.NotRun != 3 || rep.Applied != 0 {
		t.Fatalf("nothing unrecorded is written: %+v ran=%d", rep, len(w.ran))
	}
}
