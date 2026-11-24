//go:build unix

package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// bd holds this lock while it works in a workspace: a repair must not move files under it.
func TestRepairRefusesWhileAnotherProcessHoldsTheWorkspaceLock(t *testing.T) {
	e := halfEnv(t)
	lock := filepath.Join(e.ws, ".beads.gate.lock")
	write(t, lock, "")
	f, err := os.OpenFile(lock, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	p, err := e.s.Plan(core.StepRepair)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err == nil || !strings.Contains(err.Error(), "lock is held") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(e.ws, ".beads", ".gitignore")); err != nil || len(e.bd.ran) != 0 {
		t.Fatal("nothing may move while the lock is held")
	}
	// Released, the same kind of plan goes through (the lock is dropped before init runs).
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	p, _ = e.s.Plan(core.StepRepair)
	if _, err := e.s.Apply(context.Background(), p.Confirm()); err != nil {
		t.Fatalf("%v", err)
	}
}
