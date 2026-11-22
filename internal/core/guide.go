package core

import (
	"context"
	"errors"
)

// StepID names a first-run setup step. Each is an explicit, confirmed action.
type StepID string

const (
	StepWorkspace StepID = "workspace" // create the managed tracker (bd init)
	StepQueues    StepID = "queues"    // write an abacus-owned Lambe config (lambe init)
	StepActor     StepID = "actor"     // choose the name leases are held under
)

// NeedsSetup is a domain whose tool is present but not set up yet. It is not a failure:
// the front-end shows one guided line and offers the setup step.
const NeedsSetup Connection = "needs-setup"

// Guide is the one plain-language state a domain shows when it needs setup: what is missing,
// the key that starts the step, and where it will happen. No tool jargon beyond the command
// that will run, which the confirm screen shows in full anyway.
type Guide struct {
	Domain  DomainID `json:"domain"`
	Step    StepID   `json:"step"`
	Key     string   `json:"key"`
	Message string   `json:"message"`
	Detail  string   `json:"detail,omitempty"`
}

// Guider is implemented by a provider that can say its domain needs setup. Guide is cheap
// (file checks, or one cached call) and returns nil when the domain needs nothing.
type Guider interface {
	Domain() DomainID
	Guide(ctx context.Context) *Guide
}

// NeedsSetupError is what a provider's read returns when the real cause is "not set up yet".
type NeedsSetupError struct{ Guide Guide }

func (e *NeedsSetupError) Error() string { return e.Guide.Message }

// AsNeedsSetup extracts the guide from a read error.
func AsNeedsSetup(err error) (Guide, bool) {
	var ns *NeedsSetupError
	if errors.As(err, &ns) {
		return ns.Guide, true
	}
	return Guide{}, false
}
