//go:build !linux && !darwin

package lambe

import (
	"fmt"
	"os"
)

// Fail closed: without a process identity, a descriptor that carries one is skipped.
func processIdentity(pid int) (string, error) {
	return "", fmt.Errorf("process identity is unsupported for pid %d", pid)
}

func signalZero(*os.Process) error { return fmt.Errorf("liveness probe unsupported") }
