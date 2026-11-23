package core

import (
	"fmt"
	"strings"
)

// ContextLine is what the header says about where the user is working: the tracker, the counts
// and the board target. It is pure data; the front-end decides how much of it fits.
type ContextLine struct {
	Tracker    string // tracker display name ("" when unknown)
	Board      string // board target, scheme://host/path only (never a credential)
	Tasks      int
	Ready      int
	HaveTasks  bool // Tasks is a real count
	ReadyKnown bool // dependencies were read, so Ready means something
}

// Parts are the pieces in the order they are kept when room runs short (the last is dropped
// first), sanitized, with the empty ones left out.
func (c ContextLine) Parts() []string {
	var out []string
	if t := strings.TrimSpace(Sanitize(c.Tracker)); t != "" {
		out = append(out, t)
	}
	if c.HaveTasks {
		n := fmt.Sprintf("%d tasks", c.Tasks)
		if c.Tasks == 1 {
			n = "1 task"
		}
		if c.ReadyKnown {
			n += fmt.Sprintf(" · %d ready", c.Ready)
		}
		out = append(out, n)
	}
	if b := strings.TrimSpace(Sanitize(c.Board)); b != "" {
		out = append(out, "board "+b)
	}
	return out
}

// String is every part on one line.
func (c ContextLine) String() string { return strings.Join(c.Parts(), " · ") }
