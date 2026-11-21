//go:build darwin

package lambe

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func signalZero(p *os.Process) error {
	err := p.Signal(syscall.Signal(0))
	if err == syscall.EPERM {
		return nil
	}
	return err
}

func processIdentity(pid int) (string, error) {
	if pid <= 1 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if int(info.Proc.P_pid) != pid || info.Proc.P_starttime.Sec == 0 {
		return "", fmt.Errorf("process %d unavailable", pid)
	}
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil || boot == "" {
		return "", fmt.Errorf("read boot identity: %w", err)
	}
	return fmt.Sprintf("darwin:%s:%d:%d", boot, info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}
