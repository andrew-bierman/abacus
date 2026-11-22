//go:build unix

package core

import (
	"os"
	"syscall"
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
