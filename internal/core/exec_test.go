package core_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestExecRunnerRunsInTheConfiguredDir(t *testing.T) {
	dir := t.TempDir()
	out, exit, err := core.ExecRunner{Dir: dir}.Run(context.Background(), "pwd")
	if err != nil || exit != 0 || !strings.HasSuffix(strings.TrimSpace(string(out)), filepath.Base(dir)) {
		t.Fatalf("%q %d %v", out, exit, err)
	}
}

func TestTimeoutAndCancelAreTypedErrors(t *testing.T) {
	p := writeScript(t, "sleep 5")
	_, _, err := core.ExecRunner{Timeout: 100 * time.Millisecond}.Run(context.Background(), p)
	if !errors.Is(err, core.ErrTimedOut) {
		t.Fatalf("timeout err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, _, err = core.ExecRunner{}.Run(ctx, p)
	if !errors.Is(err, core.ErrCanceled) {
		t.Fatalf("cancel err = %v", err)
	}
}

func TestExecRunnerScrubsTheInheritedEnvironmentBeforeAddingItsOwn(t *testing.T) {
	t.Setenv("BEADS_DIR", "/real/tracker")
	t.Setenv("BD_SOCKET", "/real/sock")
	t.Setenv("BEADS_ACTOR", "kept")
	t.Setenv("KEEP_ME", "yes")
	r := core.ExecRunner{Scrub: core.ScrubBeadsEnv, Env: func() []string { return []string{"BEADS_ACTOR=added"} }}
	out, _, err := r.Run(context.Background(), writeScript(t, `env`))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"BEADS_DIR=", "BD_SOCKET="} {
		if strings.Contains(s, bad) {
			t.Errorf("%s leaked into the child environment", bad)
		}
	}
	if !strings.Contains(s, "KEEP_ME=yes") || !strings.Contains(s, "BEADS_ACTOR=added") {
		t.Errorf("unrelated or explicitly added variables must survive:\n%s", s)
	}
}

func TestExecRunnerKillsTheWholeProcessGroupOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := writeScript(t, `sleep 30 &
echo $! > `+pidFile+`
wait`)
	// The command must have started its child before it is stopped, or the test checks nothing.
	// A fixed short timeout races the script's start-up on a loaded machine, so stop it by
	// cancelling once the pid file exists; the timeout is only a safety net.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := core.ExecRunner{Timeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() {
		_, _, err := r.Run(ctx, script)
		done <- err
	}()
	var b []byte
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(data)) != "" {
			b = data
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(b) == 0 {
		cancel()
		t.Fatal("the command never started its child")
	}
	pid := strings.TrimSpace(string(b))
	if n, err := strconv.Atoi(pid); err != nil || n <= 0 {
		t.Fatalf("invalid fixture grandchild PID %q", b)
	}
	if runtime.GOOS == "linux" && !fixtureGrandchildRunning(pid) {
		t.Fatalf("the grandchild %s was not running before cancellation", pid)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want the stopped command to report an error")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the cancelled command did not return")
	}
	time.Sleep(200 * time.Millisecond)
	if fixtureGrandchildRunning(pid) {
		exec.Command("kill", "-9", pid).Run()
		t.Fatalf("the grandchild %s outlived the stopped command", pid)
	}
}

func fixtureGrandchildRunning(pid string) bool {
	if runtime.GOOS == "linux" {
		// Signal zero also succeeds for an exited child that has yet to be reaped.
		stat, err := os.ReadFile("/proc/" + pid + "/stat")
		// The command name can contain spaces and parentheses. State follows its last ')'.
		if end := strings.LastIndexByte(string(stat), ')'); err == nil && end >= 0 && end+2 < len(stat) {
			return stat[end+2] != 'Z' && stat[end+2] != 'X'
		}
	}
	return exec.Command("kill", "-0", pid).Run() == nil
}
