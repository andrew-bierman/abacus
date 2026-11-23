//go:build unix

package bd

import (
	"errors"
	"syscall"
)

// openNoBlockNoFollow: never block on open and never follow a symlink at the last path element.
const openNoBlockNoFollow = syscall.O_NONBLOCK | syscall.O_NOFOLLOW

// isSymlinkRefusal is the error O_NOFOLLOW gives for a symlink.
func isSymlinkRefusal(err error) bool { return errors.Is(err, syscall.ELOOP) }
