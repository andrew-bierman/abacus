// Package lambe is the umbrella's first module. It composes Lambe only through
// `lambe version --json`, `lambe doctor -json` and the read-only state API.
package lambe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/beaddeck/internal/module"
)

// Runner executes a command. A non-zero exit is reported via the exit code, not err.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (out []byte, exit int, err error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, ee.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, err
	}
	return out, 0, nil
}

// Options configure the module. Every field has a production default, and every process
// boundary is injectable so tests never reach a real Lambe, daemon, Rex or terminal.
type Options struct {
	Bin        string // explicit lambe binary; empty means look it up on PATH
	LookPath   func(string) (string, error)
	Runner     Runner
	StateDir   string // empty means Lambe's documented default
	Selector   Selector
	ConfigPath string // passed to `lambe doctor -config`
	Identity   func(int) (string, error)
	PidAlive   func(int) bool
	RexHandoff func(machine string) tea.Cmd
	Timeout    time.Duration
	Demo       bool
}

// Module implements module.Module for Lambe.
type Module struct {
	o   Options
	bin string
}

// DefaultStateDir is Lambe's documented default: $XDG_STATE_HOME/lambe or ~/.local/state/lambe.
func DefaultStateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "lambe")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "lambe")
}

// New returns a Lambe module with defaults filled in.
func New(o Options) *Module {
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Runner == nil {
		o.Runner = execRunner{}
	}
	if o.StateDir == "" {
		o.StateDir = DefaultStateDir()
	}
	if o.RexHandoff == nil {
		o.RexHandoff = execRex
	}
	return &Module{o: o}
}

func (m *Module) ID() string   { return "lambe" }
func (m *Module) Name() string { return "Lambe" }

type versionInfo struct {
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
}

func (m *Module) Detect(ctx context.Context) module.Detection {
	if m.o.Demo {
		return module.Detection{State: module.Ready, Version: "demo", APIVersion: SupportedAPIVersion}
	}
	bin := m.o.Bin
	if bin == "" {
		p, err := m.o.LookPath("lambe")
		if err != nil {
			return module.Detection{State: module.Absent, Detail: "lambe not found on PATH"}
		}
		bin = p
	}
	out, exit, err := m.o.Runner.Run(ctx, bin, "version", "--json")
	if err != nil || exit != 0 {
		return module.Detection{State: module.Absent, Detail: "lambe version --json could not run"}
	}
	var v versionInfo
	if json.Unmarshal(out, &v) != nil || v.Version == "" {
		return module.Detection{State: module.Incompatible, Detail: "lambe version --json: unparsable output (upgrade lambe)"}
	}
	if v.APIVersion != SupportedAPIVersion {
		return module.Detection{State: module.Incompatible, Version: v.Version, APIVersion: v.APIVersion,
			Detail: fmt.Sprintf("api_version %d unsupported (supported: %d)", v.APIVersion, SupportedAPIVersion)}
	}
	m.bin = bin
	return module.Detection{State: module.Ready, Version: v.Version, APIVersion: v.APIVersion}
}

func (m *Module) Doctor(ctx context.Context) []module.Check {
	if m.o.Demo {
		return []module.Check{{Name: "demo", Status: module.StatusOK, Detail: "fixture data, no daemon contacted"}}
	}
	args := []string{"doctor", "-json"}
	if m.o.ConfigPath != "" {
		args = append(args, "-config", m.o.ConfigPath)
	}
	// Lambe exits 1 when a check fails but still prints the report; both are normal here.
	out, _, err := m.o.Runner.Run(ctx, m.bin, args...)
	var raw []module.Check
	if err != nil || json.Unmarshal(out, &raw) != nil {
		return []module.Check{{Name: "lambe doctor", Status: module.StatusFail, Detail: "lambe doctor -json produced no readable report"}}
	}
	for i := range raw {
		switch raw[i].Status {
		case module.StatusOK, module.StatusWarn, module.StatusFail, module.StatusSkip:
		default:
			raw[i].Status = module.StatusWarn
		}
	}
	return raw
}

// read fetches the live view, or returns the reason it is unavailable.
func (m *Module) read(ctx context.Context) (View, string) {
	if m.o.Demo {
		v, err := demoView()
		if err != nil {
			return View{}, "demo fixtures unreadable"
		}
		return v, ""
	}
	d, err := Discover(DiscoverOptions{StateDir: m.o.StateDir, Selector: m.o.Selector, Identity: m.o.Identity, PidAlive: m.o.PidAlive})
	if err != nil {
		return View{}, err.Error()
	}
	c, err := NewClient(d, m.o.Timeout)
	if err != nil {
		return View{}, err.Error()
	}
	v, err := c.View(ctx)
	if err != nil {
		return View{}, err.Error()
	}
	return v, ""
}

func (m *Module) Status(ctx context.Context) module.Status {
	v, reason := m.read(ctx)
	if reason != "" {
		return module.Status{Unavailable: reason}
	}
	st := module.Status{Summary: fmt.Sprintf("%d runs, %d queued, %d need attention",
		len(v.Snapshot.Runs), len(v.Queue.Rows), len(v.Snapshot.NeedsHuman))}
	for _, r := range v.Snapshot.Runs {
		st.Lines = append(st.Lines, runLine(r))
	}
	return st
}

func runLine(r Run) string {
	s := fmt.Sprintf("%s  %s  %s", r.Issue, orUnknown(r.State), orUnknown(r.Phase))
	if r.Stalled {
		s += "  (stalled)"
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
