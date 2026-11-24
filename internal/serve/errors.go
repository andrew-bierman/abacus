package serve

import (
	"errors"
	"fmt"

	"github.com/andrew-bierman/abacus/internal/core"
)

// JSON-RPC 2.0 error numbers. The abacus code (the closed set of core.ErrorCode) is in error.data.code:
// a client branches on that, never on the number or the message.
const (
	rpcParse          = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternal       = -32603
	rpcServer         = -32000 // any other failure; the abacus code says which
	rpcNotInitialized = -32002
	rpcCancelled      = -32800
)

// apiError is a failed request: a JSON-RPC number, an abacus code and a message that is already plain text.
type apiError struct {
	rpc                     int
	code                    core.ErrorCode
	msg                     string
	planID, digest, outcome string
}

func (e *apiError) Error() string { return e.msg }

// data is the error.data object: core's error body plus what concerns a plan.
func (e *apiError) data() core.ServeErrorData {
	b := core.NewErrorDoc(e.code, e.msg).Error
	return core.ServeErrorData{ErrorBody: b, PlanID: e.planID, PlanDigest: e.digest, Outcome: e.outcome}
}

func usageErr(format string, a ...any) *apiError {
	return &apiError{rpc: rpcInvalidParams, code: core.CodeUsage, msg: fmt.Sprintf(format, a...)}
}

func failErr(code core.ErrorCode, format string, a ...any) *apiError {
	return &apiError{rpc: rpcServer, code: code, msg: fmt.Sprintf(format, a...)}
}

// fromErr maps any error core returned to the closed set of codes.
func fromErr(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	code := core.CodeFor(err)
	e := &apiError{rpc: rpcServer, code: code, msg: readText(err)}
	if code == core.CodeUsage {
		e.rpc = rpcInvalidParams
	}
	return e
}

// readText is the plain wording of a failed read: for a domain that is not connected, why and how to connect.
func readText(err error) string {
	var nc *core.NotConnectedError
	if errors.As(err, &nc) {
		return core.Sanitize(nc.Reason + " " + nc.HowToConnect)
	}
	if g, ok := core.AsNeedsSetup(err); ok {
		return core.Sanitize(g.CLILine())
	}
	return core.Sanitize(err.Error())
}

// outcomeOf is the P3 outcome word of a failed write.
func outcomeOf(code core.ErrorCode) string {
	switch code {
	case core.CodeUnknownOutcome:
		return "unknown"
	case core.CodeAppliedMismatch:
		return "applied_mismatch"
	case core.CodeAlreadyApplied:
		return "already_applied"
	case core.CodeChanged:
		return "changed"
	}
	return "not_applied"
}
