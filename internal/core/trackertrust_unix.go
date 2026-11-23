//go:build unix

package core

import (
	"fmt"
	"os"
	"syscall"
)

// UntrustedDir says why a directory should not be trusted as a tracker to switch to ("" when it can):
// it is writable by group or others, or owned by someone other than uid or root. Switching points the
// write gate (and so later bd commands) at that tracker, so another user must not be able to change it.
func UntrustedDir(fi os.FileInfo, uid int) string {
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Sprintf("is writable by other users (mode %04o): run chmod go-w on it first", fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid && st.Uid != 0 {
		return "is owned by another user"
	}
	return ""
}
