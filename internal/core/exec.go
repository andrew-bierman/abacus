package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Runner executes a command. A non-zero exit is reported through the exit code, not err;
// err means the command could not run or was cut off.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (out []byte, exit int, err error)
}

// ErrTimedOut and ErrCanceled are what a Runner returns when it cut a command off. The
// command may have run to completion first, so for a write they mean the outcome is unknown.
var (
	ErrTimedOut = errors.New("timed out")
	ErrCanceled = errors.New("canceled")
)

// DefaultExecTimeout bounds every tool subprocess so a hung tool cannot hang abacus.
const DefaultExecTimeout = 10 * time.Second

// waitDelay lets a killed command's pipes drain, so a grandchild that inherited stdout
// cannot block the caller after the kill.
const waitDelay = 2 * time.Second

// ExecRunner is the production Runner. A zero Timeout means DefaultExecTimeout; a non-empty
// Dir is the working directory of the command. Commands see the sidecar directories first on
// PATH (so bv finds the bundled bd) and the variables Env returns, if set, on top of the
// process environment.
type ExecRunner struct {
	Timeout time.Duration
	Dir     string
	Env     func() []string
	// Scrub filters the inherited environment before Env is added (see ScrubBeadsEnv).
	Scrub func([]string) []string
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultExecTimeout
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.Dir
	setProcessGroup(cmd)
	env := os.Environ()
	if r.Scrub != nil {
		env = r.Scrub(env)
	}
	if r.Env != nil {
		env = append(env, r.Env()...)
	}
	cmd.Env = SidecarEnv(env)
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if ctx.Err() != nil {
		if parent.Err() != nil {
			return nil, -1, fmt.Errorf("%w: %w", ErrCanceled, parent.Err())
		}
		return nil, -1, fmt.Errorf("%w after %s", ErrTimedOut, timeout)
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
