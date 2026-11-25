//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The real binary, a fake slow bd and a real SIGTERM: the audit log ends with an "unknown" line, the
// JSON error document says unknown_outcome, the exit code is non-zero, and the tool is gone. Nothing
// here touches a real tracker or the real audit log (isolated HOME and XDG, a temporary tracker).
func TestSIGTERMDuringApplyIsRecordedAsUnknown(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	bin := filepath.Join(root, "abacus")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	tracker := filepath.Join(root, "tracker")
	if err := os.MkdirAll(filepath.Join(tracker, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tracker, ".beads", "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	started, childPID := filepath.Join(root, "started"), filepath.Join(root, "child.pid")
	fake := filepath.Join(root, "bd")
	script := `#!/bin/sh
for a in "$@"; do
  if [ "$a" = version ]; then echo '{"version":"9.9.9","schema_version":1}'; exit 0; fi
done
/bin/sleep 60 &
echo $! > ` + childPID + `
echo started > ` + started + `
wait
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "tasks", "close", "ac-12", "--tracker", tracker, "--yes", "--json")
	cmd.Env = []string{"HOME=" + root, "XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"), "ABACUS_BD_BIN=" + fake, "PATH=/usr/bin:/bin"}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("the fake bd never started the write: %s %s", out.String(), errOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, err := os.ReadFile(childPID)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid fixture child PID %q", b)
	}
	if runtime.GOOS == "linux" && !fixtureChildRunning(pid) {
		t.Fatalf("the tool's child %d was not running before SIGTERM", pid)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var werr error
	select {
	case werr = <-waited:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("abacus did not exit after SIGTERM")
	}
	if ee, ok := werr.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Errorf("exit: %v (stderr %s)", werr, errOut.String())
	}
	var doc struct {
		Error struct {
			Code           string `json:"code"`
			UnknownOutcome bool   `json:"unknown_outcome"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.Error.Code != "unknown_outcome" || !doc.Error.UnknownOutcome {
		t.Errorf("stdout %q (%v)", out.String(), err)
	}
	log, err := os.ReadFile(filepath.Join(root, "state", "abacus", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var phases []string
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		var e struct{ Phase string }
		if json.Unmarshal([]byte(l), &e) == nil {
			phases = append(phases, e.Phase)
		}
	}
	if strings.Join(phases, ",") != "attempt,unknown" {
		t.Errorf("audit phases %v, want attempt,unknown:\n%s", phases, log)
	}
	time.Sleep(200 * time.Millisecond)
	if fixtureChildRunning(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the tool's child %d is still running", pid)
	}
}

func fixtureChildRunning(pid int) bool {
	if runtime.GOOS == "linux" {
		// Signal zero also succeeds for an exited child that has yet to be reaped.
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		// The command name can contain spaces and parentheses. State follows its last ')'.
		if end := strings.LastIndexByte(string(stat), ')'); err == nil && end >= 0 && end+2 < len(stat) {
			return stat[end+2] != 'Z' && stat[end+2] != 'X'
		}
	}
	return syscall.Kill(pid, 0) == nil
}
