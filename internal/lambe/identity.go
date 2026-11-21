package lambe

import "os"

// ProcessIdentity is the platform process-incarnation id, ported (not imported) from
// Lambe's internal/proc. It is a var so tests can inject a fake.
var ProcessIdentity = processIdentity

// pidAlive reports whether signal 0 reaches the pid (EPERM still means alive).
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return signalZero(p) == nil
}
