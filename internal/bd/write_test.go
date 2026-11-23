package bd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

const closeKey = "close tt-1 --json"

func writeMod(f *fakeRun, o Options) *Module {
	o.Runner, o.LookPath = f, found
	m := New(o)
	m.SetFound(core.Found{Bin: "/fake/bd"})
	return m
}

func runClose(t *testing.T, stdout string, exit int, runErr error) (core.TaskResult, error, *fakeRun) {
	t.Helper()
	f := &fakeRun{resp: map[string]string{closeKey: stdout}, exit: map[string]int{closeKey: exit}, errs: map[string]error{}}
	if runErr != nil {
		f.errs[closeKey] = runErr
	}
	res, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "close", "tt-1", "--json"})
	return res, err, f
}

const issueTT1 = `{"id":"tt-1","title":"t","status":"closed"}`

// A write counts as applied only when bd exited 0 and returned the issue that was asked for.
func TestRunTaskAcceptsABareObjectOrAOneElementArray(t *testing.T) {
	for name, body := range map[string]string{"object": issueTT1, "array": "[" + issueTT1 + "]"} {
		res, err, _ := runClose(t, body, 0, nil)
		if err != nil || res.ID != "tt-1" || res.Status != "closed" {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
}

func TestRunTaskExitZeroWithoutAnIssueIsAnError(t *testing.T) {
	bodies := map[string]string{
		"empty":             "",
		"whitespace":        " \n",
		"warning text":      "Warning: something odd\n",
		"error object":      `{"error":"x","schema_version":1}`,
		"empty array":       `[]`,
		"two issues":        `[` + issueTT1 + `,{"id":"tt-2"}]`,
		"object without id": `{"title":"t"}`,
		"json null":         `null`,
		"mismatched id":     `{"id":"tt-2","title":"other","status":"closed"}`,
		"mismatched array":  `[{"id":"tt-2"}]`,
	}
	for name, body := range bodies {
		res, err, _ := runClose(t, body, 0, nil)
		if err == nil || res.ID != "" {
			t.Errorf("%s: exit 0 with %q must be an error, got %+v %v", name, body, res, err)
		}
	}
	if _, err, _ := runClose(t, `{"error":"x boom","schema_version":1}`, 0, nil); err == nil || !strings.Contains(err.Error(), "x boom") {
		t.Errorf("bd's own error text must surface even on exit 0: %v", err)
	}
	_, err, _ := runClose(t, "", 0, nil)
	if !strings.Contains(err.Error(), "check the tracker") {
		t.Errorf("message must point at the tracker: %v", err)
	}
}

func TestRunTaskNonZeroExitIsAnErrorEvenWithAnIssueBody(t *testing.T) {
	if _, err, _ := runClose(t, issueTT1, 1, nil); err == nil {
		t.Fatal("exit 1 with a plausible body must still fail")
	}
	_, err, _ := runClose(t, `{"error":"no issue found","schema_version":1}`, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "no issue found") {
		t.Fatalf("bd's own error must surface: %v", err)
	}
}

func TestRunTaskTimeoutAndCancelAreAnUnknownOutcome(t *testing.T) {
	for _, e := range []error{fmt.Errorf("%w after 10s", core.ErrTimedOut), fmt.Errorf("%w: x", core.ErrCanceled)} {
		_, err, _ := runClose(t, "", 0, e)
		if !errors.Is(err, core.ErrOutcomeUnknown) || !strings.Contains(err.Error(), "result unknown") || !strings.Contains(err.Error(), "refresh before retrying") {
			t.Errorf("%v -> %v", e, err)
		}
	}
	// A command that could not even start did not run: a plain error, not unknown.
	_, err, _ := runClose(t, "", 0, errors.New("fork/exec: no such file"))
	if err == nil || errors.Is(err, core.ErrOutcomeUnknown) {
		t.Fatalf("%v", err)
	}
	// Killed by a signal (exit -1) may have written first.
	_, err, _ = runClose(t, "", -1, nil)
	if !errors.Is(err, core.ErrOutcomeUnknown) {
		t.Fatalf("%v", err)
	}
}

func TestRunTaskUpdateMustReturnTheRequestedID(t *testing.T) {
	k := "update tt-1 --json --title=x"
	f := &fakeRun{resp: map[string]string{k: `{"id":"tt-9","title":"x"}`}, exit: map[string]int{}}
	if _, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "update", "tt-1", "--json", "--title=x"}); err == nil {
		t.Fatal("an update that returns another issue is an error")
	}
}

func TestRunTaskCreateNeedsAnIDButNotAMatchingOne(t *testing.T) {
	k := "create --json --title=x"
	f := &fakeRun{resp: map[string]string{k: `{"id":"tt-5","title":"x","status":"open"}`}, exit: map[string]int{}}
	res, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "create", "--json", "--title=x"})
	if err != nil || res.ID != "tt-5" {
		t.Fatalf("%+v %v", res, err)
	}
	f.resp[k] = `{"title":"x"}`
	if _, err := writeMod(f, Options{}).RunTask(context.Background(), []string{"bd", "create", "--json", "--title=x"}); err == nil {
		t.Fatal("create without an id is an error")
	}
}

func TestRunTaskRefusesADisallowedArgvAndNeverCallsTheRunner(t *testing.T) {
	bad := [][]string{
		{"bd", "delete", "tt-1", "--json"},
		{"bd", "close", "tt-1", "--json", "--force=true"},
		{"bd", "update", "--force", "--json", "--title=x"},
		{"sh", "close", "tt-1", "--json"},
		{"bd"},
		nil,
		{"bd", "--readonly", "list", "--json"},
		{"bd", "-C", "/other", "close", "tt-1", "--json"},
	}
	for _, a := range bad {
		f := &fakeRun{resp: map[string]string{}, exit: map[string]int{}}
		if _, err := writeMod(f, Options{}).RunTask(context.Background(), a); err == nil || len(f.calls) != 0 {
			t.Errorf("%v: err=%v calls=%v", a, err, f.calls)
		}
	}
}

func TestBuildTaskArgvRefusalsAndExactArgv(t *testing.T) {
	m := New(Options{})
	p2 := 2
	refused := []core.TaskChange{
		{Op: "delete", ID: "tt-1"},
		{Op: core.TaskUpdate, ID: "--force", Title: "x"},
		{Op: core.TaskUpdate, ID: "tt-1", Status: "closed"},
		{Op: core.TaskUpdate, ID: "tt-1", Status: "blocked"},                  // no guard
		{Op: core.TaskUpdate, ID: "tt-1", Status: "open", IfStatus: "closed"}, // out of closed is reopen
		{Op: core.TaskReopen, ID: "a b"},
		{Op: core.TaskClose, ID: "a b"},
		{Op: core.TaskCreate},
	}
	for _, ch := range refused {
		if a, err := m.BuildTaskArgv(ch); err == nil {
			t.Errorf("%+v built %v", ch, a)
		}
	}
	got, err := m.BuildTaskArgv(core.TaskChange{Op: core.TaskCreate, Title: "T", Description: "D", Priority: &p2, Type: "bug"})
	if err != nil || strings.Join(got, "|") != "bd|create|--json|--title=T|--description=D|--priority=2|--type=bug" {
		t.Fatalf("%v %v", got, err)
	}
	got, _ = m.BuildTaskArgv(core.TaskChange{Op: core.TaskUpdate, ID: "tt-1", Title: "T", Status: "blocked", IfStatus: "open"})
	if strings.Join(got, "|") != "bd|update|tt-1|--json|--title=T|--status=blocked|--if-status=open" {
		t.Fatalf("%v", got)
	}
	got, _ = New(Options{Dir: "/trk"}).BuildTaskArgv(core.TaskChange{Op: core.TaskClose, ID: "tt-1", Reason: "done"})
	if strings.Join(got, "|") != "bd|-C|/trk|close|tt-1|--json|--reason=done" {
		t.Fatalf("%v", got)
	}
}
