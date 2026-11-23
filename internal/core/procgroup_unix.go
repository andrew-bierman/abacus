//go:build unix

package core

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group and makes a timeout or cancel kill
// the whole group, so a timed-out tool does not leave a child it started running.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
