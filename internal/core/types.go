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
	// Path and Source say which binary was found and where ("bundled", "PATH", "override").
	// Both are additive (schema_version stays 1) and filled in by the registry.
	Path   string `json:"path,omitempty"`
	Source Source `json:"source,omitempty"`
	// Ignored says why a bundled copy that exists was refused when PATH was used instead.
	Ignored string `json:"ignored,omitempty"`
}

// Located is implemented by modules that find a binary; the registry copies the answer into
// their Detection so Doctor and status can say where each tool came from.
type Located interface {
	Located() Found
}

// Check is one doctor result.
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
	Fix    string      `json:"fix,omitempty"`
}

// Tone is a front-end-neutral severity for a row, so no front-end re-parses text to color it.
type Tone string

const (
	ToneOK    Tone = "ok"
	ToneWarn  Tone = "warn"
	ToneFail  Tone = "fail"
	ToneInfo  Tone = "info"
	ToneMuted Tone = "muted"
)

// Row is one status line in typed form (for example a lease). Status.Rows parallels
// Status.Lines when a module provides it; Lines stays the plain-text rendering.
type Row struct {
	Resource string `json:"resource"`
	State    string `json:"state"`
	Tone     Tone   `json:"tone"`
	Detail   string `json:"detail,omitempty"`
}

// Status is a module's state summary. Unavailable carries the reason a ready tool
// has no readable state (API disabled, no live daemon, ambiguous daemon). Source is empty
// for a live read and names the reduced source when state came from a degraded one (for
// example "tracker subset"); it is additive, so schema_version is unchanged.
type Status struct {
	Summary     string   `json:"summary,omitempty"`
	Lines       []string `json:"lines,omitempty"`
	Rows        []Row    `json:"rows,omitempty"` // additive: typed twin of Lines
	Unavailable string   `json:"unavailable,omitempty"`
	// HowToConnect accompanies Unavailable when the reason is a plain "not connected" with a
	// fix the user can act on (not an internal error). Additive.
	HowToConnect string `json:"how_to_connect,omitempty"`
	Source       string `json:"source,omitempty"`
	// Workspace is the typed state of the optional workspace layer; only its provider sets it.
	// Additive.
	Workspace *WorkspaceInfo `json:"workspace,omitempty"`
}

// Module is one composable tool.
type Module interface {
	ID() string
	Name() string
	Detect(ctx context.Context) Detection
	Doctor(ctx context.Context) []Check
	Status(ctx context.Context) Status
}
