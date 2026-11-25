//go:build !windows

package cli

import (
	"os/signal"
	"syscall"
)

// ignoreSIGPIPE makes a write to a pipe the client closed an error (which ends the server cleanly)
// instead of a signal that kills the process mid-response. The Go runtime already does this for fds
// other than 1 and 2; stdout is fd 1.
func ignoreSIGPIPE() { signal.Ignore(syscall.SIGPIPE) }
