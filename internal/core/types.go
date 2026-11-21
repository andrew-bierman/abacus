// Package core holds all the real logic: the module seam every Beads tool implements,
// detection, doctor and status aggregation, and exit-code rules. CLI, TUI and any GUI are thin front-ends over it.
package core

import "context"

// State is whether a tool is usable by the umbrella.
type State string

const (
	Absent       State = "absent"
	Incompatible State = "incompatible"
	Ready        State = "ready"
	// Broken means the tool was found but cannot run; doctor reports it as a failure.
	Broken State = "broken"
)

// CheckStatus is a doctor result status. Only StatusFail changes the exit code.
type CheckStatus string

// The four declared statuses, mirroring Lambe's doctor.
const (
	StatusOK   CheckStatus = "ok"
	StatusWarn CheckStatus = "warn"
	StatusFail CheckStatus = "fail"
	StatusSkip CheckStatus = "skip"
)

// Normalize maps any undeclared status (from a foreign tool) to StatusWarn, so no
// front-end or native client ever sees a value outside the four declared ones.
func (s CheckStatus) Normalize() CheckStatus {
	switch s {
	case StatusOK, StatusWarn, StatusFail, StatusSkip:
		return s
	}
	return StatusWarn
}

// Detection is the outcome of probing for a tool.
type Detection struct {
	State      State  `json:"state"`
	Version    string `json:"version,omitempty"`
	APIVersion int    `json:"api_version,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// Check is one doctor result.
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
	Fix    string      `json:"fix,omitempty"`
}

// Status is a module's state summary. Unavailable carries the reason a ready tool
// has no readable state (API disabled, no live daemon, ambiguous daemon).
type Status struct {
	Summary     string   `json:"summary,omitempty"`
	Lines       []string `json:"lines,omitempty"`
	Unavailable string   `json:"unavailable,omitempty"`
}

// Module is one composable tool.
type Module interface {
	ID() string
	Name() string
	Detect(ctx context.Context) Detection
	Doctor(ctx context.Context) []Check
	Status(ctx context.Context) Status
}
