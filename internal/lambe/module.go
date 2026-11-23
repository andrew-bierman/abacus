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
	"strings"
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
	// InitRunner runs `lambe init` with dir as its working directory; nil means a real process.
	InitRunner func(dir string) core.Runner
}

// config is the Lambe config the module reads, and whether it is the abacus-owned one that a
// confirmed setup step wrote.
func (m *Module) config() (path string, owned bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.o.ConfigPath, m.ownedCfg
}

func (m *Module) selector() Selector {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.o.Selector
}

// SetConfigPath points the module at a config (after setup wrote one).
func (m *Module) SetConfigPath(path string) {
	m.mu.Lock()
	m.o.ConfigPath, m.o.Selector.ConfigPath, m.ownedCfg = path, path, true
	m.guideKnown = false
	m.mu.Unlock()
}

// trackerTTL bounds how often the degraded read spawns `lambe status -json` while polled.
const trackerTTL = 15 * time.Second

// Module implements core.Module for Lambe.
type Module struct {
	core.Locator
	o Options

	mu           sync.Mutex
	cached       string      // cached `lambe state-dir -json` answer
	tried        bool        // the state-dir command already ran (even if it failed) since Detect
	ownedCfg     bool        // the config was written by abacus's setup step
	guide        *core.Guide // cached config check, see Guide
	guideAt      time.Time
	guideKnown   bool // guide holds an answer (nil means: nothing to set up)
	guideProbing bool // a doctor probe is in flight
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
		o.Runner = core.ExecRunner{Scrub: core.ScrubBeadsEnv}
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
		return core.Detection{State: core.Ready, Version: "demo", APIVersion: SupportedAPIVersion, Source: core.SourceDemo}
	}
	f, absent := core.LocateFound("lambe", m.o.Bin, m.o.LookPath)
	bin := f.Bin
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
		return core.Detection{State: core.Incompatible, Detail: "lambe version --json: unparsable output"}
	}
	if v.APIVersion != SupportedAPIVersion {
		return core.Detection{State: core.Incompatible, Version: v.Version, APIVersion: v.APIVersion,
			Detail: fmt.Sprintf("api_version %d unsupported (supported: %d)", v.APIVersion, SupportedAPIVersion)}
	}
	m.mu.Lock()
	m.SetFound(f)
	m.cached, m.tried, m.trackerAt = "", false, time.Time{}
	m.mu.Unlock()
	return core.Detection{State: core.Ready, Version: v.Version, APIVersion: v.APIVersion}
}

func (m *Module) Doctor(ctx context.Context) []core.Check {
	if m.o.Demo {
		return []core.Check{{Name: "demo", Status: core.StatusOK, Detail: "fixture data, no daemon contacted"}}
	}
	args := []string{"doctor", "-json"}
	if cfg, _ := m.config(); cfg != "" {
		args = append(args, "-config", cfg)
	}
	// Lambe exits 1 when a check fails but still prints the report; both are normal here.
	out, exit, err := m.o.Runner.Run(ctx, m.Bin(), args...)
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
	if m.Bin() != "" && !m.tried {
		m.tried = true
		var out struct {
			StateDir string `json:"state_dir"`
		}
		b, exit, err := m.o.Runner.Run(ctx, m.Bin(), "state-dir", "-json")
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
	d, err := Discover(DiscoverOptions{StateDir: m.stateDir(ctx), Selector: m.selector(), Identity: m.o.Identity, PidAlive: m.o.PidAlive})
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
	// History is best effort: an older daemon without the route still shows runs and queue.
	var herr error
	if v.History, herr = c.History(ctx); herr != nil {
		v.HistoryErr = herr.Error()
	}
	sanitizeView(&v)
	return v, nil
}

// readTracker reads `lambe status -json`: a documented subset (in-progress claims as runs,
// no phase, no queue, no daemon, pid 0). Any failure, wrong schema or trailing data means
// no view; nothing is inferred.
func (m *Module) readTracker(ctx context.Context) (View, bool) {
	if m.Bin() == "" {
		return View{}, false
	}
	now := time.Now
	if m.o.Now != nil {
		now = m.o.Now
	}
	cfg, _ := m.config() // before taking mu: config locks it too
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.trackerAt.IsZero() && now().Sub(m.trackerAt) < trackerTTL {
		return m.tracker, true
	}
	args := []string{"status", "-json"}
	if cfg != "" {
		args = append(args, "-config", cfg)
	}
	out, exit, err := m.o.Runner.Run(ctx, m.Bin(), args...)
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
	r := queuesReport(v, m.o.Demo)
	st := core.Status{Summary: r.Summarize()}
	if v.Source == SourceTracker {
		st.Source = SourceTracker
	}
	for _, x := range r.Runs {
		st.Lines = append(st.Lines, x.Line())
	}
	return st
}

// Line is the one-line run summary every front-end shows.
func (r Run) Line() string {
	return core.QueueRun{Issue: r.Issue, State: core.RunState(r.State), Phase: r.Phase, Stalled: r.Stalled}.Line()
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
func (m *Module) RexArgv(machine string) []string { return core.HandoffArgv("", machine) }

// Queues is the Queues domain read. The Source is provider-neutral: a demo is a snapshot,
// the tracker-only fallback is labelled as such, a live daemon read is live.
func (m *Module) Queues(ctx context.Context) (core.QueuesReport, error) {
	v, err := m.Read(ctx)
	if errors.Is(err, ErrNoDaemon) {
		if g := m.Guide(ctx); g != nil {
			return core.QueuesReport{}, &core.NeedsSetupError{Guide: *g}
		}
		return core.QueuesReport{}, &core.NotConnectedError{Reason: NoDaemonReason, HowToConnect: m.StartHint()}
	}
	if err != nil {
		return core.QueuesReport{}, errors.New(core.Sanitize(err.Error()))
	}
	return queuesReport(v, m.o.Demo), nil
}

func queuesReport(v View, demo bool) core.QueuesReport {
	src := core.SourceLive
	switch {
	case demo:
		src = core.SourceSnapshot
	case v.Source == SourceTracker:
		src = core.SourceTracker
	}
	r := core.QueuesReport{Header: core.Header{Source: src}}
	var why []string
	switch {
	case demo:
		why = append(why, "demo data: fixtures, no live daemon")
	case v.Source == SourceTracker:
		why = append(why, "no live daemon: showing the tracker's in-progress claims only")
	}
	if v.HistoryErr != "" {
		why = append(why, "history unavailable: "+core.Sanitize(v.HistoryErr))
	}
	r.Reason = strings.Join(why, "; ")
	for _, x := range v.Snapshot.Runs {
		r.Runs = append(r.Runs, core.QueueRun{Issue: x.Issue, State: core.RunState(x.State), Phase: x.Phase, Machine: x.Machine, Stalled: x.Stalled, Workflow: x.Workflow})
	}
	for _, x := range v.Queue.Rows {
		r.Queue = append(r.Queue, core.QueueItem{ID: x.ID, Title: x.Title, Blocked: x.Blocked})
	}
	for _, x := range v.Snapshot.NeedsHuman {
		r.Attention = append(r.Attention, core.Attention{Issue: x.Issue, Reason: x.Reason, Detail: x.Detail})
	}
	for _, x := range v.History.Runs {
		r.History = append(r.History, core.HistoryItem{Issue: x.Issue, Title: x.Title, Outcome: x.Outcome, UpdatedAt: x.UpdatedAt})
	}
	return r
}

// Domain and Guide make Lambe the Queues guider. With no config of its own anywhere (no
// explicit one, and `lambe doctor` reports its default config missing) it offers to write one.
// The doctor answer is cached briefly because Guide is asked on every redraw tick.
func (m *Module) Domain() core.DomainID { return core.DomainQueues }

// guideTTL is how long a "needs setup" answer is trusted before asking again (someone may run
// `lambe init` themselves). An answer of "nothing to set up" is kept until something changes
// (a setup step, or InvalidateGuide), so the tab is never probed again and again.
const guideTTL = 30 * time.Second

// InvalidateGuide forgets the cached setup answer; the next Guide asks lambe again.
func (m *Module) InvalidateGuide() {
	m.mu.Lock()
	m.guideKnown = false
	m.mu.Unlock()
}

func (m *Module) Guide(ctx context.Context) *core.Guide {
	if m.o.Demo || m.o.StateDir != "" { // a state dir chosen by the user means a setup of their own
		return nil
	}
	if cfg, _ := m.config(); cfg != "" {
		return nil
	}
	m.mu.Lock()
	if m.guideProbing || (m.guideKnown && (m.guide == nil || time.Since(m.guideAt) < guideTTL)) {
		g := m.guide
		m.mu.Unlock()
		return g
	}
	m.guideProbing = true
	bin := m.Bin()
	m.mu.Unlock()
	var g *core.Guide
	if out, _, err := m.o.Runner.Run(ctx, bin, "doctor", "-json"); err == nil {
		var checks []core.Check
		if json.Unmarshal(out, &checks) == nil {
			for _, c := range checks {
				if c.Name == "config" && c.Status == core.StatusFail {
					g = &queuesGuide
				}
			}
		}
	}
	m.mu.Lock()
	m.guide, m.guideAt, m.guideKnown, m.guideProbing = g, time.Now(), true, false
	m.mu.Unlock()
	return g
}

var queuesGuide = core.Guide{Domain: core.DomainQueues, Step: core.StepQueues, Message: "Queues need a supervisor config", Action: "set up",
	Detail: "Writes a starter Lambe config for your fleet workspace. Nothing is started: you run the supervisor yourself."}

// BuildInitArgv is the one argv the init write may run, for a config at configPath.
func (m *Module) BuildInitArgv(configPath string) ([]string, error) {
	if !filepath.IsAbs(configPath) || core.Sanitize(configPath) != configPath {
		return nil, errors.New("refused: the lambe config path must be absolute")
	}
	return []string{"lambe", "init", "-non-interactive", "-config", configPath}, nil
}

// RunInit runs `lambe init -non-interactive -config configPath` inside dir (the tracker
// directory; init writes a starter WORKFLOW.md and formula there, and the config to configPath),
// and then reads that config from now on. It never starts a daemon.
func (m *Module) RunInit(ctx context.Context, dir, configPath string) error {
	argv, err := m.BuildInitArgv(configPath)
	if err != nil {
		return err
	}
	m.mu.Lock()
	bin := m.Bin()
	m.mu.Unlock()
	if bin == "" {
		return errors.New("abacus's own lambe is unavailable (see `abacus doctor`)")
	}
	if f := m.Located(); f.Src == core.SourcePATH && f.Ignored != "" {
		return fmt.Errorf("not writing: the copy of lambe next to abacus was refused (%s); not running init with a different one", f.Ignored)
	}
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("refused: %s already exists", configPath)
	}
	run := core.Runner(core.ExecRunner{Dir: dir, Timeout: 30 * time.Second, Scrub: core.ScrubBeadsEnv})
	if m.o.InitRunner != nil {
		run = m.o.InitRunner(dir)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, exit, err := run.Run(ctx, bin, argv[1:]...)
	switch {
	case errors.Is(err, core.ErrTimedOut) || errors.Is(err, core.ErrCanceled):
		return core.UnknownOutcome("lambe init timed out; check " + configPath + " before retrying")
	case err != nil:
		return fmt.Errorf("lambe init could not run: %s", core.Sanitize(err.Error()))
	case exit != 0:
		return fmt.Errorf("lambe init exited %d: nothing was set up", exit)
	}
	if _, err := os.Stat(configPath); err != nil {
		return errors.New("lambe init exited 0 but wrote no config at " + configPath)
	}
	m.SetConfigPath(configPath)
	return nil
}

// NoDaemonReason is the plain wording for "nothing is supervising": the queue is empty because
// no supervisor is running, not because anything is broken.
const NoDaemonReason = "The supervisor is not running, so there are no live runs or queue to show."

// StartHint says how to start the supervisor. abacus never does it for you.
func (m *Module) StartHint() string {
	if cfg, _ := m.config(); cfg != "" {
		return "Start it yourself with: lambe run -config " + cfg + "  (abacus never starts it for you). Its state API must be on to show live runs."
	}
	return "Start it yourself with: lambe run  (abacus never starts it for you). Its state API must be on to show live runs."
}
