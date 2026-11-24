package cli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// A failing command prints its text on stderr (for people) and, run with --json, one JSON error
// document on stdout (for clients): {schema_version, error: {code, message, ...}}. Exit codes are
// unchanged. Run wraps the two writers so this needs no change to a command that already fails
// with a message and an exit code: it defaults to code "usage" for exit 2 and "failed" for exit 1,
// and a command that knows better names the code with failf.

// sink tees stderr (the human text stays exactly as it was) and remembers what a command said.
type sink struct {
	w    io.Writer
	buf  bytes.Buffer // the first part of what was written: its first line is the default message
	code core.ErrorCode
	msg  string
}

func (s *sink) Write(p []byte) (int, error) {
	if s.buf.Len() < 4096 {
		s.buf.Write(p)
	}
	return s.w.Write(p)
}

// counter counts what a command wrote to stdout: a failing command that already printed a document
// (a bulk batch that did not all apply, a failed check) keeps it and gets no second one.
type counter struct {
	w io.Writer
	n int
}

func (c *counter) Write(p []byte) (int, error) { c.n += len(p); return c.w.Write(p) }

// failf prints a failure on stderr, names its code for the JSON error document, and returns the
// exit code 1 (exit 2, usage, needs no call: it is the default for an exit 2).
func failf(d Deps, code core.ErrorCode, format string, a ...any) int {
	msg := fmt.Sprintf(format, a...)
	_, _ = io.WriteString(d.Err, msg)
	if s, ok := d.Err.(*sink); ok && s.code == "" {
		s.code, s.msg = code, firstLine(msg)
	}
	return core.ExitFail
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// wantsJSON is whether the command line asks for --json (the flag, bare or with =true).
func wantsJSON(args []string) bool {
	for _, a := range args {
		switch a {
		case "--json", "-json", "--json=true", "-json=true", "--json=1", "-json=1":
			return true
		}
	}
	return false
}

// finishError prints the error document when a command failed without printing a document of its own.
func finishError(args []string, code int, s *sink, o *counter) {
	if code == core.ExitOK || o.n > 0 || !wantsJSON(args) {
		return
	}
	ec, msg := s.code, s.msg
	if code == core.ExitUsage {
		ec = core.CodeUsage
	}
	if msg == "" {
		msg = firstLine(s.buf.String())
	}
	switch {
	case ec != "":
	default:
		ec = core.CodeFailed
	}
	if msg == "" {
		msg = "the command failed"
	}
	jsonOut(o, core.NewErrorDoc(ec, msg))
}

// --expect-digest: the plan digest (core.PlanDigest) of the plan the user reviewed. A write that
// re-plans to a different digest is refused with code "changed" and writes nothing.

func expectFlag(fs *flag.FlagSet) *string {
	return fs.String("expect-digest", "", "apply only if the plan made now has this digest (plan_digest of --dry-run --json)")
}

// parseExpect validates the flag's value: "" means none, a malformed digest is a usage error.
func parseExpect(d Deps, label, raw string) (digest string, ok bool) {
	if raw == "" {
		return "", true
	}
	if digest, ok = core.ParseDigest(raw); !ok {
		fmt.Fprintf(d.Err, "%s: --expect-digest takes the 64 hex digits of a plan_digest\n", label)
	}
	return digest, ok
}

// planChanged is the refusal when the plan made now is not the plan that was reviewed.
func planChanged(d Deps, label, expect, digest string) int {
	return failf(d, core.CodeChanged, "%s: the plan changed since you reviewed it (reviewed %s, now %s): nothing was written; review it again\n", label, expect[:12], digest[:12])
}

// planFailed reports a plan that could not be built. With --expect-digest the caller had a valid plan
// before, so a plan that no longer builds means the thing it was planned against changed.
func planFailed(d Deps, label, expect string, err error) int {
	if expect != "" {
		return failf(d, core.CodeChanged, "%s: %v (the plan you reviewed no longer applies): nothing was written\n", label, err)
	}
	fmt.Fprintf(d.Err, "%s: %v\n", label, err)
	return core.ExitUsage
}
