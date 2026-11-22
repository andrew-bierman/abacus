// Package lambe is the umbrella's first module. It composes Lambe only through
// `lambe version --json`, `lambe doctor -json` and the read-only state API.
package lambe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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
	Now        func() time.Time // clock for the tracker cache; nil means time.Now
	Demo       bool
}

// trackerTTL bounds how often the degraded read spawns `lambe status -json` while polled.
const trackerTTL = 15 * time.Second

// Module implements core.Module for Lambe.
type Module struct {
	o   Options
	bin string

	mu     sync.Mutex
	cached string // cached `lambe state-dir -json` answer
	tried  bool   // the state-dir command already ran (even if it failed) since Detect
	// tracker is the last degraded view, reused until trackerAt+trackerTTL.
	tracker   View
	trackerAt time.Time
}

// allowedArgv is the whole set of lambe argvs this module may run, all read-only:
// version --json, state-dir -json, and doctor/status -json with an optional -config FILE.
func allowedArgv(a []string) bool {
	if len(a) < 2 {
		return false
	}
	switch a[0] {
	case "version":
		return len(a) == 2 && a[1] == "--json"
	case "state-dir":
		return len(a) == 2 && a[1] == "-json"
	case "doctor", "status":
		return a[1] == "-json" && (len(a) == 2 || len(a) == 4 && a[2] == "-config" && a[3] != "")
	}
	return false
}

// readOnly rejects any argv outside allowedArgv before it reaches the real runner.
type readOnly struct{ inner core.Runner }

func (r readOnly) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	if !allowedArgv(args) {
		return nil, -1, fmt.Errorf("lambe %q is not allowed: this module is read-only", args)
	}
	return r.inner.Run(ctx, name, args...)
}

// DefaultStateDir is Lambe's documented default, used only when `lambe state-dir -json`
// is unavailable: $XDG_STATE_HOME/lambe or ~/.local/state/lambe.
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
	o.Runner = readOnly{inner: o.Runner}
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
	m.mu.Lock()
	m.bin, m.cached, m.tried, m.trackerAt = bin, "", false, time.Time{}
	m.mu.Unlock()
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
	return core.ReadDoctor("lambe", out, exit, err)
}

// stateDir resolves where descriptors live: an explicit option wins, then the answer from
// `lambe state-dir -json` (cached once it succeeds), then the documented default. The
// command runs only for a detected binary.
func (m *Module) stateDir(ctx context.Context) string {
	if m.o.StateDir != "" {
		return m.o.StateDir
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != "" {
		return m.cached
	}
	if m.bin != "" && !m.tried {
		m.tried = true
		var out struct {
			StateDir string `json:"state_dir"`
		}
		b, exit, err := m.o.Runner.Run(ctx, m.bin, "state-dir", "-json")
		if err == nil && exit == 0 && json.Unmarshal(b, &out) == nil &&
			filepath.IsAbs(out.StateDir) && core.Sanitize(out.StateDir) == out.StateDir {
			m.cached = out.StateDir
			return m.cached
		}
	}
	return DefaultStateDir()
}

// Read fetches the live view. A failure is an error: ErrNoDaemon and ErrAmbiguous are
// sentinels callers can match with errors.Is. When no daemon is live it tries the
// tracker-only `lambe status -json` and returns a View labelled SourceTracker; if that also
// fails the original ErrNoDaemon is returned. Decoded text is sanitized.
func (m *Module) Read(ctx context.Context) (View, error) {
	if m.o.Demo {
		v, err := demoView()
		if err != nil {
			return View{}, fmt.Errorf("demo fixtures unreadable: %w", err)
		}
		sanitizeView(&v)
		return v, nil
	}
	d, err := Discover(DiscoverOptions{StateDir: m.stateDir(ctx), Selector: m.o.Selector, Identity: m.o.Identity, PidAlive: m.o.PidAlive})
	if errors.Is(err, ErrNoDaemon) {
		if v, ok := m.readTracker(ctx); ok {
			return v, nil
		}
	}
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

// readTracker reads `lambe status -json`: a documented subset (in-progress claims as runs,
// no phase, no queue, no daemon, pid 0). Any failure, wrong schema or trailing data means
// no view; nothing is inferred.
func (m *Module) readTracker(ctx context.Context) (View, bool) {
	if m.bin == "" {
		return View{}, false
	}
	now := time.Now
	if m.o.Now != nil {
		now = m.o.Now
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.trackerAt.IsZero() && now().Sub(m.trackerAt) < trackerTTL {
		return m.tracker, true
	}
	args := []string{"status", "-json"}
	if m.o.ConfigPath != "" {
		args = append(args, "-config", m.o.ConfigPath)
	}
	out, exit, err := m.o.Runner.Run(ctx, m.bin, args...)
	if err != nil || exit != 0 {
		return View{}, false
	}
	var s Snapshot
	dec := json.NewDecoder(bytes.NewReader(out))
	if dec.Decode(&s) != nil || s.SchemaVersion != 1 || dec.Decode(&struct{}{}) != io.EOF {
		return View{}, false
	}
	v := View{Snapshot: s, Source: SourceTracker}
	sanitizeView(&v)
	m.tracker, m.trackerAt = v, now()
	return v, true
}

func (m *Module) Status(ctx context.Context) core.Status {
	v, err := m.Read(ctx)
	if err != nil {
		return core.Status{Unavailable: core.Sanitize(err.Error())}
	}
	var st core.Status
	if v.Source == SourceTracker {
		st = core.Status{Source: SourceTracker, Summary: fmt.Sprintf("%d runs, %d need attention (%s, no live daemon)",
			len(v.Snapshot.Runs), len(v.Snapshot.NeedsHuman), SourceTracker)}
	} else {
		st = core.Status{Summary: fmt.Sprintf("%d runs, %d queued, %d need attention",
			len(v.Snapshot.Runs), len(v.Queue.Rows), len(v.Snapshot.NeedsHuman))}
	}
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
