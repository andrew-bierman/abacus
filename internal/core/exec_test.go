package core_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tool.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecRunnerReturnsOutputAndNonZeroExitWithoutError(t *testing.T) {
	r := core.ExecRunner{}
	out, exit, err := r.Run(context.Background(), writeScript(t, `echo hi; exit 3`))
	if err != nil || exit != 3 || string(out) != "hi\n" {
		t.Fatalf("out=%q exit=%d err=%v", out, exit, err)
	}
}

func TestExecRunnerMissingBinaryIsAnError(t *testing.T) {
	if _, _, err := (core.ExecRunner{}).Run(context.Background(), "/nonexistent/abacus-tool"); err == nil {
		t.Fatal("want an error for a missing binary")
	}
}

func TestExecRunnerEnforcesDeadlineOnAHungTool(t *testing.T) {
	r := core.ExecRunner{Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, _, err := r.Run(context.Background(), writeScript(t, `sleep 5`))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("hung tool not cut off (err=%v after %v)", err, time.Since(start))
	}
}

func TestExecRunnerDoesNotBlockOnAGrandchildHoldingStdout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	r := core.ExecRunner{Timeout: 200 * time.Millisecond}
	start := time.Now()
	// The background sleep inherits stdout and outlives the killed parent.
	_, _, _ = r.Run(context.Background(), writeScript(t, `sleep 5 &
sleep 5`))
	if time.Since(start) > 4*time.Second {
		t.Fatalf("blocked %v on a grandchild holding the pipe", time.Since(start))
	}
}

func TestExecRunnerReportsParentCancellationNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, _, err := core.ExecRunner{Timeout: 5 * time.Second}.Run(ctx, writeScript(t, `sleep 5`))
	if err == nil || strings.Contains(err.Error(), "timed out") || !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled run must not read as a timeout: %v", err)
	}
}
