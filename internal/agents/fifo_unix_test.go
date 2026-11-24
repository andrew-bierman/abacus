//go:build !windows

package agents

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// A FIFO named like a session file, or standing in for .git/HEAD, must not hang a scan: opening one
// for reading blocks until a writer shows up.
func TestFifosNeverHangAScan(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "-work-orchard")
	repo := filepath.Join(home, "work", "orchard")
	for _, d := range []string{proj, filepath.Join(repo, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(proj, "x.jsonl"), filepath.Join(repo, ".git", "HEAD")} {
		if err := syscall.Mkfifo(f, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l := NewLocal(Options{FS: rootFS{root: "/"}, Home: home, Now: func() time.Time { return time.Now() },
		Procs: func(context.Context) ([]Proc, error) {
			return []Proc{{PID: 9, PPID: 1, Name: "claude", Cwd: repo, StartUnix: time.Now().Add(-time.Hour).Unix()}}, nil
		},
		Panes: func(context.Context) ([]Pane, error) { return nil, nil }})
	done := make(chan []core.Agent, 1)
	go func() { a, _, _ := l.Agents(context.Background()); done <- a }()
	select {
	case a := <-done:
		if len(a) != 1 || a[0].Source != core.EvidenceProcess || a[0].Branch != "" {
			t.Fatalf("a FIFO gave facts: %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the scan hung on a FIFO")
	}
}

func TestOpenRegularRefusesNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "p")
	if err := syscall.Mkfifo(f, 0o600); err != nil {
		t.Fatal(err)
	}
	fsys := rootFS{root: dir}
	if _, _, err := openRegular(fsys, "p"); err == nil {
		t.Fatal("a FIFO was opened")
	}
	if err := os.WriteFile(filepath.Join(dir, "r"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, _, err := openRegular(fsys, "r")
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	// a symlink to a FIFO is as bad as the FIFO
	if err := os.Symlink(f, filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRegular(fsys, "l"); err == nil {
		t.Fatal("a link to a FIFO was opened")
	}
}
