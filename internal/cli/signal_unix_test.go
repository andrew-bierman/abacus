//go:build unix

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/core/modtest"
)

// recAudit keeps every audit entry, safe for the goroutine that applies.
type recAudit struct {
	mu sync.Mutex
	e  []core.AuditEntry
}

func (a *recAudit) Record(e core.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.e = append(a.e, e)
	return nil
}

func (a *recAudit) phases() (p []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.e {
		p = append(p, e.Phase)
	}
	return p
}

// hangMod is a Tasks writer whose write hangs until its context ends, then answers the way the real
// adapter does for a cancelled write: result unknown.
type hangMod struct {
	*taskMod
	started chan struct{}
}

func (m hangMod) RunTask(ctx context.Context, _ []string) (core.TaskResult, error) {
	close(m.started)
	<-ctx.Done()
	return core.TaskResult{}, core.UnknownOutcome("the write was cancelled")
}

// A real SIGTERM (and SIGINT) to the process while a write is applying: the command is not killed, the
// tool is told to stop, the gate writes the outcome line as unknown, the JSON document says so, and the
// exit code is non-zero.
func TestSignalDuringApplyFinishesTheAuditRecordAsUnknown(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		au := &recAudit{}
		m := hangMod{&taskMod{Fake: &modtest.Fake{IDStr: "bd", Det: core.Detection{State: core.Ready, Version: "1"}}}, make(chan struct{})}
		var o, e bytes.Buffer
		done := make(chan int)
		go func() {
			done <- cli.Run([]string{"tasks", "close", "ac-12", "--yes", "--json"}, cli.Deps{Out: &o, Err: &e, Audit: au, Version: "t",
				Build: func(cli.Options) *core.Registry { return core.NewRegistry(m) }})
		}()
		select {
		case <-m.started:
		case <-time.After(10 * time.Second):
			t.Fatal("the write never started")
		}
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		var code int
		select {
		case code = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%v: the command did not finish after the signal", sig)
		}
		var doc core.ErrorDoc
		if code != 1 || json.Unmarshal(o.Bytes(), &doc) != nil || doc.Error.Code != core.CodeUnknownOutcome || !doc.Error.UnknownOutcome || doc.Error.Retryable {
			t.Errorf("%v: exit %d, stdout %s", sig, code, o.String())
		}
		if p := au.phases(); len(p) != 2 || p[0] != "attempt" || p[1] != "unknown" {
			t.Errorf("%v: audit phases %v, want [attempt unknown]", sig, p)
		}
	}
}

// A write that finishes with no signal is untouched, and leaves no handler behind.
func TestApplyWithoutASignalIsUnchanged(t *testing.T) {
	au := &recAudit{}
	m := newTaskMod()
	var o, e bytes.Buffer
	code := cli.Run([]string{"tasks", "close", "ac-12", "--yes", "--json"}, cli.Deps{Out: &o, Err: &e, Audit: au, Version: "t",
		Build: func(cli.Options) *core.Registry { return core.NewRegistry(m) }})
	if code != 0 || len(m.ran) != 1 || len(au.phases()) != 2 || au.phases()[1] != "ok" {
		t.Fatalf("%d %s %v", code, o.String(), au.phases())
	}
}
