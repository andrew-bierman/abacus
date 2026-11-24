//go:build unix

package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// lockWorkspace takes the exclusive, non-blocking lock bd keeps on .beads.gate.lock while it
// works in a workspace (a running init holds it; confirmed against bd 1.3.4). A held lock means
// another process is working there: refuse. No lock file means nobody has started. The caller
// releases it before init runs, since init takes the same lock.
func lockWorkspace(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, ".beads.gate.lock"), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another process is working in the workspace right now (its lock is held); nothing was changed, try again when it is done")
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
