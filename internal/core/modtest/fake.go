// Package modtest provides a fake module for CLI and TUI tests.
package modtest

import (
	"context"

	"github.com/andrew-bierman/beaddeck/internal/core"
)

// Fake is a scriptable module that counts Detect calls.
type Fake struct {
	IDStr       string
	Det         core.Detection
	Checks      []core.Check
	Stat        core.Status
	DetectCalls int
}

func (f *Fake) ID() string   { return f.IDStr }
func (f *Fake) Name() string { return "Fake " + f.IDStr }
func (f *Fake) Detect(context.Context) core.Detection {
	f.DetectCalls++
	return f.Det
}
func (f *Fake) Doctor(context.Context) []core.Check { return f.Checks }
func (f *Fake) Status(context.Context) core.Status  { return f.Stat }
