package core

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Runner executes a command. A non-zero exit is reported through the exit code, not err;
// err means the command could not run or was cut off.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (out []byte, exit int, err error)
}

// DefaultExecTimeout bounds every tool subprocess so a hung tool cannot hang abacus.
const DefaultExecTimeout = 10 * time.Second

// waitDelay lets a killed command's pipes drain, so a grandchild that inherited stdout
// cannot block the caller after the kill.
const waitDelay = 2 * time.Second

// ExecRunner is the production Runner. A zero Timeout means DefaultExecTimeout.
type ExecRunner struct{ Timeout time.Duration }

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, -1, fmt.Errorf("timed out after %s", timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, ee.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, err
	}
	return out, 0, nil
}
