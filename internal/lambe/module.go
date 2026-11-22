// Package lambe is the umbrella's first module. It composes Lambe only through
// `lambe version --json`, `lambe doctor -json` and the read-only state API.
package lambe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Options configure the module. Every field has a production default, and every process
// boundary is injectable so tests never reach a real Lambe, daemon, Rex or terminal.
type Options struct {
	Bin        string // explicit lambe binary; empty means look it up on PATH
	LookPath   func(string) (string, error)
	Runner     core.Runner
	StateDir   string // empty means Lambe's documented default
	Selector   Selector
	ConfigPath string // passed to `lambe doctor -config`
	Identity   func(int) (string, error)
	PidAlive   func(int) bool
	Timeout    time.Duration
	Demo       bool
}

// Module implements core.Module for Lambe.
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
		o.Runner = core.ExecRunner{}
	}
	if o.StateDir == "" {
		o.StateDir = DefaultStateDir()
	}
	return &Module{o: o}
}

func (m *Module) ID() string   { return "lambe" }
func (m *Module) Name() string { return "Lambe" }

type versionInfo struct {
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
}

func (m *Module) Detect(ctx context.Context) core.Detection {
	if m.o.Demo {
		return core.Detection{State: core.Ready, Version: "demo", APIVersion: SupportedAPIVersion}
	}
	bin, absent := core.Locate("lambe", m.o.Bin, m.o.LookPath)
	if absent != "" {
		return core.Detection{State: core.Absent, Detail: absent}
	}
	out, exit, err := m.o.Runner.Run(ctx, bin, "version", "--json")
	if err != nil {
		return core.Detection{State: core.Broken, Detail: "lambe version --json could not run: " + core.Sanitize(err.Error())}
	}
	if exit != 0 {
		return core.Detection{State: core.Broken, Detail: fmt.Sprintf("lambe version --json exited %d", exit)}
	}
	var v versionInfo
	if json.Unmarshal(out, &v) != nil || v.Version == "" {
		return core.Detection{State: core.Incompatible, Detail: "lambe version --json: unparsable output (upgrade lambe)"}
	}
	if v.APIVersion != SupportedAPIVersion {
		return core.Detection{State: core.Incompatible, Version: v.Version, APIVersion: v.APIVersion,
			Detail: fmt.Sprintf("api_version %d unsupported (supported: %d)", v.APIVersion, SupportedAPIVersion)}
	}
	m.bin = bin
	return core.Detection{State: core.Ready, Version: v.Version, APIVersion: v.APIVersion}
}

func (m *Module) Doctor(ctx context.Context) []core.Check {
	if m.o.Demo {
		return []core.Check{{Name: "demo", Status: core.StatusOK, Detail: "fixture data, no daemon contacted"}}
	}
	args := []string{"doctor", "-json"}
	if m.o.ConfigPath != "" {
		args = append(args, "-config", m.o.ConfigPath)
	}
	// Lambe exits 1 when a check fails but still prints the report; both are normal here.
	out, exit, err := m.o.Runner.Run(ctx, m.bin, args...)
	var raw []core.Check
	if err != nil || json.Unmarshal(out, &raw) != nil {
		return []core.Check{{Name: "lambe doctor", Status: core.StatusFail, Detail: "lambe doctor -json produced no readable report"}}
	}
	failed := false
	for i := range raw {
		raw[i].Name, raw[i].Detail, raw[i].Fix = core.Sanitize(raw[i].Name), core.Sanitize(raw[i].Detail), core.Sanitize(raw[i].Fix)
		raw[i].Status = raw[i].Status.Normalize()
		failed = failed || raw[i].Status == core.StatusFail
	}
	// Backstop: a non-zero exit with no failing check must not read as healthy.
	if exit != 0 && !failed {
		raw = append(raw, core.Check{Name: "lambe doctor", Status: core.StatusFail, Detail: fmt.Sprintf("exited %d without reporting a failed check", exit)})
	}
	return raw
}

// Read fetches the live view. A failure is an error: ErrNoDaemon and ErrAmbiguous are
// sentinels callers can match with errors.Is. Decoded text is sanitized.
func (m *Module) Read(ctx context.Context) (View, error) {
	if m.o.Demo {
		v, err := demoView()
		if err != nil {
			return View{}, fmt.Errorf("demo fixtures unreadable: %w", err)
		}
		sanitizeView(&v)
		return v, nil
	}
	d, err := Discover(DiscoverOptions{StateDir: m.o.StateDir, Selector: m.o.Selector, Identity: m.o.Identity, PidAlive: m.o.PidAlive})
	if err != nil {
		return View{}, err
	}
	c, err := NewClient(d, m.o.Timeout)
	if err != nil {
		return View{}, err
	}
	v, err := c.View(ctx)
	if err != nil {
		return View{}, err
	}
	sanitizeView(&v)
	return v, nil
}

func (m *Module) Status(ctx context.Context) core.Status {
	v, err := m.Read(ctx)
	if err != nil {
		return core.Status{Unavailable: core.Sanitize(err.Error())}
	}
	st := core.Status{Summary: fmt.Sprintf("%d runs, %d queued, %d need attention",
		len(v.Snapshot.Runs), len(v.Queue.Rows), len(v.Snapshot.NeedsHuman))}
	for _, r := range v.Snapshot.Runs {
		st.Lines = append(st.Lines, r.Line())
	}
	return st
}

// Line is the one-line run summary every front-end shows.
func (r Run) Line() string {
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

// IsDemo reports whether the module reads fixtures instead of a live daemon.
func (m *Module) IsDemo() bool { return m.o.Demo }

// RexArgv is the command that hands a machine to Rex. A front-end decides how to run it,
// and only in response to an explicit user action.
func (m *Module) RexArgv(machine string) []string { return []string{"rex", "-S", machine} }
