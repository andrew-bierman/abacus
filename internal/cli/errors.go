package cli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"strconv"
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
	// json is the value of --json as the command's own flag parsing read it, when it got that far.
	json, registered bool
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

// jsonVar registers --json on a command's flag set, so the error document follows the command's own
// parsing (a value after a flag, a --json after `--`, --json=T and --json=false mean what they mean there).
func jsonVar(d Deps, fs *flag.FlagSet, p *bool, usage string) {
	s, _ := d.Err.(*sink)
	if s != nil {
		s.registered = true
	}
	fs.Var(&jsonFlag{p: p, s: s}, "json", usage)
}

func jsonBool(d Deps, fs *flag.FlagSet, usage string) *bool {
	p := new(bool)
	jsonVar(d, fs, p, usage)
	return p
}

type jsonFlag struct {
	p *bool
	s *sink
}

func (f *jsonFlag) String() string {
	if f == nil || f.p == nil {
		return "false"
	}
	return strconv.FormatBool(*f.p)
}
func (f *jsonFlag) IsBoolFlag() bool { return true }
func (f *jsonFlag) Set(v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	*f.p = b
	if f.s != nil {
		f.s.json = b
	}
	return nil
}

// wantsJSON is the fallback for a command line that failed before its flags were parsed (an unknown
// command, a bad flag ahead of --json): --json as a flag word with a boolean value, nothing after `--`.
func wantsJSON(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name != "json" || !strings.HasPrefix(a, "-") || strings.HasPrefix(a, "---") {
			continue
		}
		if !hasVal {
			return true
		}
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return false
}

// finishError prints the error document when a command failed without printing a document of its own.
func finishError(args []string, code int, s *sink, o *counter) {
	if code == core.ExitOK || o.n > 0 || !(s.json || ((!s.registered || code == core.ExitUsage) && wantsJSON(args))) {
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

// expectVal remembers whether the flag was given at all, so an empty value (`--expect-digest=`, or an
// unset shell variable) is an error and never "no check".
type expectVal struct {
	raw string
	set bool
}

func (e *expectVal) String() string { return e.raw }
func (e *expectVal) Set(s string) error {
	e.raw, e.set = s, true
	return nil
}

func expectFlag(fs *flag.FlagSet) *expectVal {
	e := &expectVal{}
	fs.Var(e, "expect-digest", "apply only if the plan made now has this digest (plan_digest of --dry-run --json)")
	return e
}

// parseExpect: not given means no check; given, it must be a digest, else a usage error (exit 2).
func parseExpect(d Deps, label string, e *expectVal) (digest string, ok bool) {
	if !e.set {
		return "", true
	}
	if digest, ok = core.ParseDigest(e.raw); !ok {
		fmt.Fprintf(d.Err, "%s: --expect-digest takes the 64 hex digits of a plan_digest (it was empty or malformed, so nothing was checked or written)\n", label)
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
