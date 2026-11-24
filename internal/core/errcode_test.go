package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCodeForMapsTypedErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		want ErrorCode
	}{
		{UnknownOutcome("timed out"), CodeUnknownOutcome},
		{fmt.Errorf("wrapped: %w", AppliedMismatch("moved again")), CodeAppliedMismatch},
		{ErrAlreadyApplied, CodeAlreadyApplied},
		{Stale("t-1 is now held"), CodeChanged},
		{ErrChanged, CodeChanged},
		{&AuditError{errors.New("disk full")}, CodeAuditFailed},
		{ErrNotConfirmed, CodeRefused},
		{ErrCannotVerify, CodeRefused},
		{&NotConnectedError{Reason: "x"}, CodeNotConnected},
		{&NeedsSetupError{}, CodeNeedsSetup},
		{fmt.Errorf("%w after 5s", ErrTimedOut), CodeTimeout},
		{context.DeadlineExceeded, CodeTimeout},
		{errors.New("boom"), CodeFailed},
		{nil, CodeFailed},
	} {
		if got := CodeFor(c.err); got != c.want {
			t.Errorf("%v: %s, want %s", c.err, got, c.want)
		}
	}
}

// A failed audit record blocks the write and is reported as such; the write never ran.
func TestGateReportsAFailedAuditAsAnAuditError(t *testing.T) {
	g := newGate(failAudit{errors.New("disk full")})
	ran := false
	err := g.do(1, AuditEntry{}, func() error { ran = true; return nil })
	var ae *AuditError
	if !errors.As(err, &ae) || ran || err.Error() != "disk full" {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
}

func TestNewErrorDocFlags(t *testing.T) {
	if d := NewErrorDoc(CodeUnknownOutcome, "x"); !d.Error.UnknownOutcome || d.Error.Retryable {
		t.Errorf("an unknown outcome is never retryable: %+v", d)
	}
	if d := NewErrorDoc(CodeTimeout, "x"); !d.Error.Retryable || d.Error.UnknownOutcome {
		t.Errorf("%+v", d)
	}
	if d := NewErrorDoc(CodeRefused, "a\x1b[31mb"); d.Error.Retryable || strings.ContainsRune(d.Error.Message, 0x1b) {
		t.Errorf("a refusal is not retryable and its message is sanitized: %+v", d)
	}
}
