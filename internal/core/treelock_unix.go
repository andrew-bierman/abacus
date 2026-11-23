//go:build unix

package core

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// lockTree takes an exclusive advisory lock on path, polling for at most wait, so two abacus
// instances cannot interleave their read-modify-write of the fold state. A busy lock is an error
// (the fold state is then not saved), never a block of the screen.
func lockTree(path string, wait time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, errors.New("another abacus is saving the fold state")
			}
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
