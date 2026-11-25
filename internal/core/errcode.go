package core

import (
	"context"
	"errors"
)

// ErrorCode is a stable, machine-readable reason a command failed. The set is closed: a client
// branches on it, never on the message text. A new code is an additive change to schema_version 1
// (a client treats a code it does not know as failed). Exit codes are unchanged: 1 for every code
// but usage, which exits 2.
type ErrorCode string

const (
	// CodeUsage: the command line is wrong (unknown command or flag, bad value). Nothing ran.
	CodeUsage ErrorCode = "usage"
	// CodeNotConnected: the domain this needs has no working provider (or a read from it failed
	// for a reason with a plain fix).
	CodeNotConnected ErrorCode = "not_connected"
	// CodeNeedsSetup: the provider is there but first-run setup has not been done.
	CodeNeedsSetup ErrorCode = "needs_setup"
	// CodeNotFound: the task, message or other thing named does not exist.
	CodeNotFound ErrorCode = "not_found"
	// CodeRefused: abacus declined on purpose and nothing was written (not confirmed, a change that
	// cannot apply, an unsafe input).
	CodeRefused ErrorCode = "refused"
	// CodeChanged: what was planned no longer matches (the task moved, or --expect-digest differs
	// from the plan made now). Nothing was written; plan again.
	CodeChanged ErrorCode = "changed"
	// CodeAlreadyApplied: this plan was already applied once (single use).
	CodeAlreadyApplied ErrorCode = "already_applied"
	// CodeUnknownOutcome: the write may or may not have happened (timeout, cancel, killed). Refresh
	// before any retry.
	CodeUnknownOutcome ErrorCode = "unknown_outcome"
	// CodeAppliedMismatch: the write landed but the result is not the one asked for.
	CodeAppliedMismatch ErrorCode = "applied_mismatch"
	// CodeAuditFailed: the audit log could not be written, so the write was blocked (fail closed).
	CodeAuditFailed ErrorCode = "audit_failed"
	// CodeTimeout: a read ran out of time or was cancelled.
	CodeTimeout ErrorCode = "timeout"
	// CodeUnsupported: this build or provider does not offer the thing asked for.
	CodeUnsupported ErrorCode = "unsupported"
	// CodeExpired: the plan was held too long and is no longer appliable (serve: past its time to live).
	// Nothing was written; plan again.
	CodeExpired ErrorCode = "expired"
	// CodeBusy: the server has more requests than it will take at once (serve). Nothing ran; try again
	// after retry_after_ms.
	CodeBusy ErrorCode = "busy"
	// CodeFailed: anything else that failed.
	CodeFailed ErrorCode = "failed"
)

// ErrorCodes is the whole closed set, in documentation order.
var ErrorCodes = []ErrorCode{CodeUsage, CodeNotConnected, CodeNeedsSetup, CodeNotFound, CodeRefused, CodeChanged,
	CodeAlreadyApplied, CodeUnknownOutcome, CodeAppliedMismatch, CodeAuditFailed, CodeTimeout, CodeUnsupported, CodeExpired, CodeBusy, CodeFailed}

// ErrorBody is the error of a failed command in the JSON error document `{schema_version, error}`.
type ErrorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// Retryable is true when running the same command again can succeed without anyone changing
	// anything but time or the plan (a timed-out read, a changed plan). Never true for an unknown outcome.
	Retryable bool `json:"retryable,omitempty"`
	// UnknownOutcome is true when a write may have happened: show "result unknown", offer a refresh, never a retry.
	UnknownOutcome bool `json:"unknown_outcome,omitempty"`
}

// ErrorDoc is the one JSON document a failing command prints on stdout when run with --json.
type ErrorDoc struct {
	Error ErrorBody `json:"error"`
}

// NewErrorDoc builds the document for code and a human message (sanitized here).
func NewErrorDoc(code ErrorCode, msg string) ErrorDoc {
	b := ErrorBody{Code: code, Message: Sanitize(msg)}
	b.UnknownOutcome = code == CodeUnknownOutcome
	b.Retryable = code == CodeTimeout || code == CodeChanged || code == CodeExpired || code == CodeBusy
	return ErrorDoc{Error: b}
}

// AuditError is a failed audit record: the write it guarded was not run.
type AuditError struct{ Err error }

func (e *AuditError) Error() string { return e.Err.Error() }
func (e *AuditError) Unwrap() error { return e.Err }

// CodeFor maps the typed errors core returns to their code; anything untyped is CodeFailed.
func CodeFor(err error) ErrorCode {
	var (
		ae *AuditError
		nc *NotConnectedError
	)
	switch {
	case err == nil:
		return CodeFailed
	case errors.Is(err, ErrOutcomeUnknown):
		return CodeUnknownOutcome
	case errors.Is(err, ErrAppliedMismatch):
		return CodeAppliedMismatch
	case errors.Is(err, ErrAlreadyApplied):
		return CodeAlreadyApplied
	case errors.Is(err, ErrChanged):
		return CodeChanged
	case errors.As(err, &ae):
		return CodeAuditFailed
	case errors.Is(err, ErrNotConfirmed), errors.Is(err, ErrCannotVerify):
		return CodeRefused
	case errors.As(err, &nc):
		return CodeNotConnected
	case errors.Is(err, ErrTimedOut), errors.Is(err, ErrCanceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return CodeTimeout
	}
	if _, ok := AsNeedsSetup(err); ok {
		return CodeNeedsSetup
	}
	return CodeFailed
}
