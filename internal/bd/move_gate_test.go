package bd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

type recAudit struct{ lines []core.AuditEntry }

func (r *recAudit) Record(e core.AuditEntry) error { r.lines = append(r.lines, e); return nil }

const (
	moveKey   = "update tt-1 --json --status=in_progress --if-status=open"
	reopenKey = "reopen tt-1 --json --reason=back"
	showKey   = "--readonly show tt-1 --json"
)

func moveChange() core.TaskChange {
	return core.TaskChange{Op: core.TaskUpdate, ID: "tt-1", Status: "in_progress", IfStatus: "open", Before: core.Task{ID: "tt-1", Status: "open"}}
}

func issueIn(status string) string {
	return `[{"id":"tt-1","title":"t","status":"` + status + `"}]`
}

func moveFake(answer, reread string, exit int) *fakeRun {
	return &fakeRun{resp: map[string]string{moveKey: answer, showKey: reread, reopenKey: issueIn("open")}, exit: map[string]int{moveKey: exit}, errs: map[string]error{}}
}

func TestMoveBuildsTheExactGuardedArgv(t *testing.T) {
	got, err := New(Options{}).BuildTaskArgv(moveChange())
	if err != nil || strings.Join(got, " ") != "bd "+moveKey {
		t.Fatalf("%v %v", got, err)
	}
	got, err = New(Options{Dir: "/trk"}).BuildTaskArgv(core.TaskChange{Op: core.TaskReopen, ID: "tt-1", Reason: "back"})
	if err != nil || strings.Join(got, " ") != "bd -C /trk "+reopenKey {
		t.Fatalf("%v %v", got, err)
	}
}

// Unconfirmed never runs; confirmed runs once; the attempt is audited before the run and the
// outcome after it; a second apply of the same plan is refused.
func TestMoveGateUnconfirmedNeverRunsSingleUseAuditedBeforeAndAfter(t *testing.T) {
	f, au := moveFake(issueIn("in_progress"), issueIn("in_progress"), 0), &recAudit{}
	m := writeMod(f, Options{})
	ap := core.NewApplier(m, au)
	plan, err := core.PlanTaskChange(m, moveChange())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ap.Apply(context.Background(), plan); !errors.Is(err, core.ErrNotConfirmed) || len(f.calls) != 0 || len(au.lines) != 0 {
		t.Fatalf("unconfirmed: err=%v calls=%v audit=%v", err, f.calls, au.lines)
	}
	res, err := ap.Apply(context.Background(), plan.Confirm())
	if err != nil || res.ID != "tt-1" || res.Status != "in_progress" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(au.lines) != 2 || au.lines[0].Phase != "attempt" || au.lines[1].Phase != "ok" || au.lines[0].Verb != "update" ||
		strings.Join(au.lines[0].Argv, " ") != "bd "+moveKey {
		t.Fatalf("audit: %+v", au.lines)
	}
	if _, err := ap.Apply(context.Background(), plan.Confirm()); !errors.Is(err, core.ErrAlreadyApplied) {
		t.Fatalf("single use: %v", err)
	}
	if n := strings.Count(strings.Join(f.calls, "\n"), "update tt-1"); n != 1 {
		t.Fatalf("the write ran %d times: %v", n, f.calls)
	}
}

// bd's exit 13 means the guard did not hold: nothing was written, said as such, audited as an error.
func TestMoveWithAStaleGuardIsReportedAsChangedAndAuditedAsAnError(t *testing.T) {
	stale := `{"error":"1 of 1 issues failed to update","failed":[{"id":"tt-1","guard_mismatch":true}],"schema_version":1}`
	f, au := moveFake(stale, issueIn("blocked"), 13), &recAudit{}
	m := writeMod(f, Options{})
	plan, _ := core.PlanTaskChange(m, moveChange())
	_, err := core.NewApplier(m, au).Apply(context.Background(), plan.Confirm())
	if !errors.Is(err, core.ErrChanged) || errors.Is(err, core.ErrOutcomeUnknown) {
		t.Fatalf("err=%v", err)
	}
	if got := core.DescribeWriteError(err); !strings.Contains(got, "changed since you opened it") || !strings.Contains(got, "nothing was written") {
		t.Errorf("wording: %q", got)
	}
	if len(au.lines) != 2 || au.lines[1].Phase != "error" {
		t.Errorf("audit: %+v", au.lines)
	}
}

func TestMoveVerificationNeedsTheAskedForStatusInTheAnswerAndTheReread(t *testing.T) {
	// exit 0 and the right issue, but bd answered with another status
	f := moveFake(issueIn("open"), issueIn("open"), 0)
	if _, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "update", "tt-1", "--json", "--status=in_progress", "--if-status=open"}); err == nil || !strings.Contains(err.Error(), `wanted "in_progress"`) {
		t.Errorf("answer mismatch: %v", err)
	}
	// the answer is right but the re-read disagrees (another actor moved it again): not a success
	f = moveFake(issueIn("in_progress"), issueIn("blocked"), 0)
	if _, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "update", "tt-1", "--json", "--status=in_progress", "--if-status=open"}); err == nil || !strings.Contains(err.Error(), "re-read shows") {
		t.Errorf("re-read mismatch: %v", err)
	}
	// a re-read that cannot be made does not undo a write bd itself confirmed
	f = moveFake(issueIn("in_progress"), "", 0)
	delete(f.resp, showKey)
	if res, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "update", "tt-1", "--json", "--status=in_progress", "--if-status=open"}); err != nil || res.Status != "in_progress" {
		t.Errorf("unreadable re-read: %+v %v", res, err)
	}
	if !strings.Contains(strings.Join(f.calls, "|"), showKey) {
		t.Error("the task is re-read after the write")
	}
}

func TestMoveTimeoutOrCancelIsAnUnknownOutcome(t *testing.T) {
	for _, e := range []error{core.ErrTimedOut, core.ErrCanceled} {
		f := moveFake("", "", 0)
		f.errs[moveKey] = e
		_, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "update", "tt-1", "--json", "--status=in_progress", "--if-status=open"})
		if !errors.Is(err, core.ErrOutcomeUnknown) {
			t.Errorf("%v: %v", e, err)
		}
	}
}

func TestReopenRunsVerifiedAndRefusesMissingOrForcedForms(t *testing.T) {
	f := moveFake("", issueIn("open"), 0)
	res, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "reopen", "tt-1", "--json", "--reason=back"})
	if err != nil || res.Status != "open" {
		t.Fatalf("%+v %v", res, err)
	}
	for _, a := range [][]string{
		{"bd", "update", "tt-1", "--json", "--status=in_progress"},                                // no guard
		{"bd", "update", "tt-1", "--json", "--status=in_progress", "--if-status=open", "--force"}, // forced
		{"bd", "update", "tt-1", "--json", "--status=open", "--if-status=closed"},                 // out of closed
		{"bd", "reopen", "tt-1", "--json", "--force"},
	} {
		f := moveFake("", "", 0)
		if _, err := writeMod(f, Options{}).RunTask(context.Background(), a); err == nil || len(f.calls) != 0 {
			t.Errorf("%v: err=%v calls=%v", a, err, f.calls)
		}
	}
}
