//go:build unix

package core

import (
	"os"
	"syscall"
	"time"
)

// lockConfig takes an exclusive advisory lock beside the config file, so a TUI and a CLI (or two
// of either) cannot interleave their read-modify-write. The lock is released when unlock runs
// or the process dies.
func lockConfig(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// lockWithin is lockConfig that gives up after wait instead of blocking: it retries a
// non-blocking lock every few milliseconds and returns errLockBusy when the time is up.
func lockWithin(path string, wait time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK || !time.Now().Before(deadline) {
			f.Close()
			if err == syscall.EWOULDBLOCK {
				return nil, errLockBusy
			}
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
