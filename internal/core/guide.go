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

// Guide is the one plain-language state a domain shows when it needs setup: what is missing
// (Message), what the step does (Action), the key that starts it in the TUI, and where it will
// happen (Detail). No tool jargon beyond the command that will run, which the confirm screen
// shows in full anyway.
type Guide struct {
	Domain  DomainID `json:"domain"`
	Step    StepID   `json:"step"`
	Key     string   `json:"key"`
	Message string   `json:"message"`
	Action  string   `json:"action"`
	Detail  string   `json:"detail,omitempty"`
}

// Line is the single guided sentence for the TUI: "<message> — press <key> to <action>".
func (g Guide) Line() string { return g.Message + " — press " + g.Key + " to " + g.Action }

// CLILine is the same sentence for the command line, where the step is `abacus setup`.
func (g Guide) CLILine() string {
	return g.Message + " — run `abacus setup` to " + g.Action
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

// NotConnectedError is a read that failed for a reason with a plain explanation and a fix
// the user can act on, not an internal error. Front-ends show it as "not connected".
type NotConnectedError struct{ Reason, HowToConnect string }

func (e *NotConnectedError) Error() string { return e.Reason }

// AsNeedsSetup extracts the guide from a read error.
func AsNeedsSetup(err error) (Guide, bool) {
	var ns *NeedsSetupError
	if errors.As(err, &ns) {
		return ns.Guide, true
	}
	return Guide{}, false
}
