//go:build unix

package core

import (
	"os"
	"syscall"
)

// ownerUID is the uid that owns fi; ok is false when the platform cannot say.
func ownerUID(fi os.FileInfo) (uid int, ok bool) {
	st, is := fi.Sys().(*syscall.Stat_t)
	if !is {
		return 0, false
	}
	return int(st.Uid), true
}
