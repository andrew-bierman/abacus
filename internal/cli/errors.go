package cli

import (
	"bytes"
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
