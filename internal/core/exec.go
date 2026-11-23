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

// ErrOutputTooLarge is returned by a capped ExecRunner whose command printed more than MaxOut.
var ErrOutputTooLarge = errors.New("output too large")

type limitBuf struct {
	b     []byte
	max   int
	total int
}

func (l *limitBuf) Write(p []byte) (int, error) {
	if room := l.max - len(l.b); room > 0 {
		l.b = append(l.b, p[:min(room, len(p))]...)
	}
	l.total += len(p)
	return len(p), nil
}

// DefaultExecTimeout bounds every tool subprocess so a hung tool cannot hang abacus.
const DefaultExecTimeout = 10 * time.Second

// waitDelay lets a killed command's pipes drain, so a grandchild that inherited stdout
// cannot block the caller after the kill.
const waitDelay = 2 * time.Second

// ExecRunner is the production Runner. A zero Timeout means DefaultExecTimeout; a non-empty
// Dir is the working directory of the command. Commands see the sidecar directories first on
// PATH (so a tool that runs another finds the bundled one) and the variables Env returns, if set, on top of the
// process environment.
type ExecRunner struct {
	Timeout time.Duration
	Dir     string
	Env     func() []string
	// MaxOut, when positive, caps stdout: the rest is discarded and ErrOutputTooLarge returned.
	MaxOut int
	// Scrub filters the inherited environment before Env is added (see ScrubBeadsEnv).
	Scrub func([]string) []string
}

// SplitRunner is a Runner that also returns stderr, for a tool (Vercel Board's CLI) that reports
// its failures as a JSON document on stderr. stdout stays data only.
type SplitRunner interface {
	RunSplit(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exit int, err error)
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	out, _, exit, err := r.RunSplit(ctx, name, args...)
	return out, exit, err
}

// maxStderr bounds what is kept of a tool's stderr.
const maxStderr = 16 << 10

type capBuf struct{ b []byte }

func (c *capBuf) Write(p []byte) (int, error) {
	if room := maxStderr - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (r ExecRunner) RunSplit(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
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
	var stderr capBuf
	cmd.Stderr = &stderr
	var out []byte
	var err error
	var lim *limitBuf
	if r.MaxOut > 0 {
		lim = &limitBuf{max: r.MaxOut}
		cmd.Stdout = lim
		err = cmd.Run()
		out = lim.b
	} else {
		out, err = cmd.Output()
	}
	if ctx.Err() != nil {
		if parent.Err() != nil {
			return nil, nil, -1, fmt.Errorf("%w: %w", ErrCanceled, parent.Err())
		}
		return nil, nil, -1, fmt.Errorf("%w after %s", ErrTimedOut, timeout)
	}
	if lim != nil && lim.total > lim.max {
		return out, stderr.b, 0, ErrOutputTooLarge
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, stderr.b, ee.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, -1, err
	}
	return out, stderr.b, 0, nil
}
