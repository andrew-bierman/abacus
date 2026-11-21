// Package modtest provides a fake module for CLI and TUI tests.
package modtest

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/beaddeck/internal/module"
)

// Fake is a scriptable module that counts Detect calls.
type Fake struct {
	IDStr       string
	Det         module.Detection
	Checks      []module.Check
	Stat        module.Status
	DetectCalls int
}

func (f *Fake) ID() string   { return f.IDStr }
func (f *Fake) Name() string { return "Fake " + f.IDStr }
func (f *Fake) Detect(context.Context) module.Detection {
	f.DetectCalls++
	return f.Det
}
func (f *Fake) Doctor(context.Context) []module.Check { return f.Checks }
func (f *Fake) Status(context.Context) module.Status  { return f.Stat }
func (f *Fake) Section() tea.Model                    { return fakeSection{f.IDStr} }

type fakeSection struct{ id string }

func (fakeSection) Init() tea.Cmd                         { return nil }
func (s fakeSection) Update(tea.Msg) (tea.Model, tea.Cmd) { return s, nil }
func (s fakeSection) View() string                        { return "section:" + s.id }
