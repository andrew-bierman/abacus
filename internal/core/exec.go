package core

import (
	"bufio"
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

// ErrOutputTruncated is returned, with the output, by an ExecRunner that keeps the tail of what a
// command printed (KeepTail) when the command printed more than MaxOut: the start was dropped.
var ErrOutputTruncated = errors.New("output truncated to its end")

type limitBuf struct {
	tail  bool // keep the last max bytes instead of the first
	b     []byte
	max   int
	total int
}

func (l *limitBuf) Write(p []byte) (int, error) {
	if l.tail {
		l.total += len(p)
		l.b = append(l.b, p...)
		if len(l.b) > l.max {
			l.b = append(l.b[:0], l.b[len(l.b)-l.max:]...)
		}
		return len(p), nil
	}
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
	// KeepTail makes MaxOut keep the end of the output instead: the start is dropped and
	// ErrOutputTruncated returned with what was kept (a terminal's scrollback: its end is what matters).
	KeepTail bool
	// Scrub filters the inherited environment before Env is added (see ScrubBeadsEnv).
	Scrub func([]string) []string
}

// SplitRunner is a Runner that also returns stderr, for a tool (Vercel Board's CLI) that reports
// its failures as a JSON document on stderr. stdout stays data only.
type SplitRunner interface {
	RunSplit(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exit int, err error)
}

// Run runs name with args and returns its combined output and exit code. Built-in tools are re-verified first.
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

// RunSplit is Run with standard output and standard error kept apart.
func (r ExecRunner) RunSplit(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	if err := CheckBuiltIn(name); err != nil { // a built-in copy replaced since it was checked is not run
		return nil, nil, -1, err
	}
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
		lim = &limitBuf{max: r.MaxOut, tail: r.KeepTail}
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
		if lim.tail {
			return out, stderr.b, exitOf(err), ErrOutputTruncated
		}
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

// exitOf is the exit code of a finished command, 0 when it succeeded.
func exitOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 0
}

// StreamFunc runs a long-lived command and calls onLine with each line it prints until it exits
// or ctx ends. It returns the exit code; err means it could not run or was cut off.
type StreamFunc func(ctx context.Context, name string, args []string, onLine func([]byte)) (exit int, err error)

// maxStreamLine bounds one line of a stream: a longer line is skipped whole, never buffered.
const maxStreamLine = 256 << 10

// ExecStream is the production StreamFunc: the command runs in its own process group with the
// environment scrub filters and is killed, with everything it started, when ctx ends, so a
// front-end that exits never leaves it behind. stderr is discarded.
func ExecStream(scrub func([]string) []string) StreamFunc {
	return func(ctx context.Context, name string, args []string, onLine func([]byte)) (int, error) {
		if err := CheckBuiltIn(name); err != nil {
			return -1, err
		}
		cmd := exec.CommandContext(ctx, name, args...)
		setProcessGroup(cmd)
		env := os.Environ()
		if scrub != nil {
			env = scrub(env)
		}
		cmd.Env = SidecarEnv(env)
		cmd.WaitDelay = waitDelay
		out, err := cmd.StdoutPipe()
		if err != nil {
			return -1, err
		}
		if err := cmd.Start(); err != nil {
			return -1, err
		}
		r := bufio.NewReaderSize(out, 64<<10)
		var line []byte
		skipping := false
		for {
			chunk, isPrefix, rerr := r.ReadLine()
			if len(chunk) > 0 && !skipping {
				if len(line)+len(chunk) > maxStreamLine {
					skipping, line = true, nil
				} else {
					line = append(line, chunk...)
				}
			}
			if !isPrefix {
				if !skipping && len(line) > 0 {
					onLine(line)
				}
				line, skipping = nil, false
			}
			if rerr != nil {
				break
			}
		}
		werr := cmd.Wait()
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		if ee := (*exec.ExitError)(nil); errors.As(werr, &ee) {
			return ee.ExitCode(), nil
		}
		if werr != nil {
			return -1, werr
		}
		return 0, nil
	}
}
