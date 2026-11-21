// Package module is the seam every Beads tool plugs into: detection, health checks,
// a state summary, and a TUI section. The CLI and TUI never name a specific tool.
package module

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// State is whether a tool is usable by the umbrella.
type State string

const (
	Absent       State = "absent"
	Incompatible State = "incompatible"
	Ready        State = "ready"
)

// Check statuses mirror Lambe's doctor: only Fail changes the exit code.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// Detection is the outcome of probing for a tool.
type Detection struct {
	State      State  `json:"state"`
	Version    string `json:"version,omitempty"`
	APIVersion int    `json:"api_version,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// Check is one doctor result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
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
	// Section is the TUI model shown when the module is selected.
	Section() tea.Model
}
