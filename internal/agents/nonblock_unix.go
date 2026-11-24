//go:build !windows

package agents

import "syscall"

// nonblockFlag makes opening a FIFO for reading return at once instead of waiting for a writer.
const nonblockFlag = syscall.O_NONBLOCK
