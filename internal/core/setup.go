package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/andrew-bierman/abacus/internal/brand"
)

// LambeInitializer is what the Queues provider offers for the one Queues write: writing a
// starter config. BuildInitArgv must refuse anything but the fixed argv.
type LambeInitializer interface {
	BuildInitArgv(configPath string) ([]string, error)
	RunInit(ctx context.Context, dir, configPath string) error
}

// SetupOptions are the paths and seams of the first-run setup. Everything abacus may create
// lives under them: the managed workspace, an abacus-owned Lambe config and abacus's own config.
type SetupOptions struct {
	Config      ConfigStore
	Workspace   string // the managed workspace directory
	LambeConfig string // where an init may write the abacus-owned Lambe config
	Audit       Audit  // nil means the default file audit
	// Suggest proposes the actor name; nil derives one from the user and machine names.
	Suggest func() string
	// Now is the clock for names a repair makes; nil means time.Now.
	Now func() time.Time
}

// DefaultSetupOptions resolves the standard XDG locations. A missing home directory leaves
// the paths empty, which makes every step refuse rather than write somewhere relative.
func DefaultSetupOptions() SetupOptions {
	var o SetupOptions
	if p, err := DefaultConfigPath(); err == nil {
		o.Config = ConfigStore{Path: p}
	}
	o.Workspace, _ = DefaultWorkspaceDir()
	o.LambeConfig, _ = DefaultLambeConfigPath()
	return o
}

// Setup is the one door to first-run setup. Each step is planned (validated, with the exact
// command), needs an explicit Confirm, can be applied once, and is audited like a Tasks write
// (both go through the same gate). Nothing here starts a daemon.
type Setup struct {
	o       SetupOptions
	writer  TaskWriter
	applier *Applier
	lambe   LambeInitializer
	guiders map[DomainID]Guider
	gate    *gate
}

// NewSetup picks the providers it needs out of the detected modules.
func NewSetup(entries []Entry, o SetupOptions) *Setup {
	if o.Audit == nil {
		o.Audit = DefaultAudit()
	}
	p := ResolveProviders(entries)
	s := &Setup{o: o, writer: p.Writer, guiders: p.Guiders, gate: newGate(o.Audit)}
	if p.Writer != nil {
		s.applier = NewApplier(p.Writer, o.Audit)
	}
	s.lambe, _, _ = pick[LambeInitializer](entries)
	return s
}

// Guide is the guided state of a domain, nil when it needs nothing.
func (s *Setup) Guide(ctx context.Context, d DomainID) *Guide {
	if g := s.guiders[d]; g != nil {
		return g.Guide(ctx)
	}
	return nil
}

// SetupPlan is a validated step with its exact command and a description. It cannot be applied
// until Confirm is called.
type SetupPlan struct {
	id        uint64
	Step      StepID
	Domain    DomainID
	Lines     []string
	Argv      []string
	wp        WritePlan // the workspace step goes through the Tasks write gate
	actor     string
	dir       string // queues: the tracker directory lambe init runs in
	adopt     bool   // queues: only record an existing config
	aside     string // repair: where the unfinished .beads is moved
	confirmed bool
}

// Confirm marks the plan as explicitly approved by the user.
func (p SetupPlan) Confirm() SetupPlan { p.confirmed = true; return p }

var setupSeq atomic.Uint64

// trackerDir is where the Queues starter files go: the tracker Tasks reads (the working
// directory when it holds one), else the managed workspace once it exists.
func (s *Setup) trackerDir() (string, bool) {
	if s.writer != nil {
		if d := s.writer.TrackerDir(); d != "" {
			return d, HasTracker(d)
		}
		if HasTracker("") {
			wd, err := os.Getwd()
			return wd, err == nil
		}
	}
	return s.o.Workspace, HasOwnTracker(s.o.Workspace)
}

func (s *Setup) blockedQueuesReason() error {
	if d, ok := s.writer.(InitDirer); ok && d.InitDir() != "" {
		return errors.New("create your fleet workspace first (Tasks tab, press i, or run `" + brand.Name + " setup`)")
	}
	return errors.New("Tasks has no usable tracker yet: fix what the Tasks tab says first")
}

// Plan builds the plan for a step without side effects. It fails with a plain reason when the
// step cannot be done safely (no tool, nothing to do, a prerequisite missing).
func (s *Setup) Plan(step StepID) (SetupPlan, error) {
	switch step {
	case StepWorkspace:
		d, ok := s.writer.(InitDirer)
		if s.writer == nil || !ok || d.InitDir() == "" {
			return SetupPlan{}, errors.New("a tracker is already set up (or abacus's own bd is unavailable: see `abacus doctor`): nothing to create")
		}
		if HasOwnTracker(d.InitDir()) {
			return SetupPlan{}, errors.New("the workspace already has a tracker: nothing to create")
		}
		if h, _ := InspectWorkspace(d.InitDir()); h == WorkspaceHalfBuilt {
			return SetupPlan{}, errors.New("the workspace was only partly created: run `" + brand.Name + " setup --repair`")
		}
		wp, err := PlanTaskChange(s.writer, TaskChange{Op: TaskInit, Dir: d.InitDir()})
		if err != nil {
			return SetupPlan{}, err
		}
		return SetupPlan{id: setupSeq.Add(1), Step: step, Domain: DomainTasks, Lines: wp.Lines, Argv: wp.Argv, wp: wp}, nil
	case StepRepair:
		return s.planRepair()
	case StepQueues:
		return s.planQueues()
	case StepActor:
		if s.o.Config.Path == "" {
			return SetupPlan{}, errors.New("no home directory to keep the config in")
		}
		name := s.suggest()
		return SetupPlan{id: setupSeq.Add(1), Step: step, Domain: DomainLeases, actor: name,
			Argv: []string{brand.Name, "config", "set", "actor", name}, Lines: []string{
				"set the actor name leases are held under: " + name,
				"change it any time with: " + brand.Name + " config set actor NAME",
			}}, nil
	}
	return SetupPlan{}, fmt.Errorf("unknown setup step %q", step)
}

func (s *Setup) planQueues() (SetupPlan, error) {
	if s.lambe == nil {
		return SetupPlan{}, errors.New("abacus's own Lambe is unavailable (see `abacus doctor`): nothing to set up")
	}
	if s.o.Workspace == "" || s.o.LambeConfig == "" {
		return SetupPlan{}, errors.New("no home directory to keep the config in")
	}
	// An abacus config that is already there (an earlier run was interrupted after writing it) is
	// adopted, not refused: the step only records the path. A link or a non-file is refused.
	if fi, err := os.Lstat(s.o.LambeConfig); err == nil {
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return SetupPlan{}, fmt.Errorf("refusing %s: it is a symlink", s.o.LambeConfig)
		case !fi.Mode().IsRegular():
			return SetupPlan{}, fmt.Errorf("refusing %s: it is not a regular file", s.o.LambeConfig)
		}
		return SetupPlan{id: setupSeq.Add(1), Step: StepQueues, Domain: DomainQueues, adopt: true,
			Argv: []string{brand.Name, "config", "set", "lambe_config", s.o.LambeConfig}, Lines: []string{
				"use the existing supervisor config: " + s.o.LambeConfig,
				"nothing is written or started: abacus only remembers the path",
			}}, nil
	}
	dir, ok := s.trackerDir()
	if !ok {
		return SetupPlan{}, s.blockedQueuesReason()
	}
	argv, err := s.lambe.BuildInitArgv(s.o.LambeConfig)
	if err != nil {
		return SetupPlan{}, err
	}
	return SetupPlan{id: setupSeq.Add(1), Step: StepQueues, Domain: DomainQueues, Argv: argv, dir: dir, Lines: []string{
		"write a starter supervisor config",
		"config: " + s.o.LambeConfig,
		"workspace files: a starter WORKFLOW.md and formula in " + dir,
		"nothing is started: you run the supervisor yourself (lambe run)",
	}}, nil
}

// changed is the fail-closed error for a precondition that no longer holds at apply time.
func changed(what string) error {
	return errors.New("changed since you opened it: " + what + "; nothing was done, look again")
}

// recheck re-verifies, at apply time, what the plan assumed when it was made.
func (s *Setup) recheck(ctx context.Context, p SetupPlan) error {
	switch p.Step {
	case StepWorkspace:
		d, ok := s.writer.(InitDirer)
		if !ok || d.InitDir() == "" || HasOwnTracker(d.InitDir()) {
			return changed("a tracker already exists")
		}
	case StepActor:
		if g := s.guiders[DomainLeases]; g != nil && g.Guide(ctx) == nil {
			return changed("an actor name is already set")
		}
	case StepQueues:
		if g := s.guiders[DomainQueues]; g != nil && g.Guide(ctx) == nil {
			return changed("Queues no longer needs a config")
		}
	}
	return nil
}

// Apply runs a confirmed plan once and returns a one-line result. Preconditions are re-checked
// first (fail closed); the attempt is audited before anything runs and a failed audit blocks the
// step; an outcome that cannot be known is reported as unknown, never as "not applied".
func (s *Setup) Apply(ctx context.Context, p SetupPlan) (string, error) {
	if !p.confirmed {
		return "", ErrNotConfirmed
	}
	if p.Step != StepWorkspace && p.Step != StepQueues && p.Step != StepActor && p.Step != StepRepair {
		return "", fmt.Errorf("unknown setup step %q", p.Step)
	}
	if p.Step == StepRepair {
		if s.applier == nil {
			return "", errors.New("there is no workspace to repair")
		}
		return s.applyRepair(ctx, p)
	}
	if err := s.recheck(ctx, p); err != nil {
		return "", err
	}
	if p.Step == StepWorkspace {
		if _, err := s.applier.Apply(ctx, p.wp.Confirm()); err != nil {
			return "", err
		}
		return "created your fleet workspace", nil
	}
	var msg string
	e := AuditEntry{Domain: string(p.Domain), Verb: "setup-" + string(p.Step), Argv: p.Argv, Tracker: p.dir}
	err := s.gate.do(p.id, e, func() error {
		var err error
		msg, err = s.run(ctx, p)
		return err
	})
	return msg, err
}

func (s *Setup) run(ctx context.Context, p SetupPlan) (string, error) {
	if p.Step == StepActor {
		note, err := s.o.Config.UpdateNote(func(c *Config) error { return c.Set("actor", p.actor) })
		if err != nil {
			return "", err
		}
		return joinNote("leases will be held as "+p.actor, note), nil
	}
	msg := "wrote the starter supervisor config (nothing was started)"
	if p.adopt {
		msg = "using the existing supervisor config"
	} else if err := s.lambe.RunInit(ctx, p.dir, s.o.LambeConfig); err != nil {
		return "", err
	}
	note, err := s.o.Config.UpdateNote(func(c *Config) error { return c.Set("lambe_config", s.o.LambeConfig) })
	if err != nil {
		return "", fmt.Errorf("the config is at %s but could not be remembered (%s): run `%s config set lambe_config %s`", s.o.LambeConfig, Sanitize(err.Error()), brand.Name, s.o.LambeConfig)
	}
	return joinNote(msg, note), nil
}

func joinNote(msg, note string) string {
	if note == "" {
		return msg
	}
	return msg + " (" + note + ")"
}

var notNameRE = regexp.MustCompile(`[^a-z0-9._-]+`)

// SuggestActor derives a lease actor name from the user and machine names.
func SuggestActor() string {
	u := os.Getenv("USER")
	if cu, err := user.Current(); err == nil && cu.Username != "" {
		u = cu.Username
	}
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	n := notNameRE.ReplaceAllString(strings.ToLower(strings.Trim(u+"-"+h, "-")), "-")
	n = strings.Trim(n, "-._")
	if len(n) > 64 {
		n = n[:64]
	}
	if !actorRE.MatchString(n) {
		return brand.Name + "-user"
	}
	return n
}

func (s *Setup) suggest() string {
	if s.o.Suggest != nil {
		return s.o.Suggest()
	}
	return SuggestActor()
}

// StepState is where a setup step stands. It is a closed set: a new value would be a schema_version change.
type StepState string

const (
	StepDone        StepState = "done"        // nothing to do
	StepNeeded      StepState = "needed"      // a plan is available (setup applies it)
	StepBlocked     StepState = "blocked"     // needed, but cannot be planned yet (the message says why)
	StepUnavailable StepState = "unavailable" // abacus's own tool for this is not here
	StepRepairing   StepState = "repair"      // a half-created workspace: only `setup --repair` applies it
)

// StepStates is every StepState, in the order the schema and the docs list them.
var StepStates = []StepState{StepDone, StepNeeded, StepBlocked, StepUnavailable, StepRepairing}

// SchemaValues makes the schema a closed enum (see SchemaEnum).
func (StepState) SchemaValues() []string {
	out := make([]string, len(StepStates))
	for i, s := range StepStates {
		out[i] = string(s)
	}
	return out
}

// StepInfo is one step as `abacus setup` reports it.
type StepInfo struct {
	Step    StepID    `json:"step"`
	Domain  DomainID  `json:"domain"`
	State   StepState `json:"state"`
	Message string    `json:"message"`
	Lines   []string  `json:"plan,omitempty"`
	Command string    `json:"command,omitempty"`
}

// StepOrder is the order steps are offered in: the workspace first, since Queues needs it.
var StepOrder = []StepID{StepWorkspace, StepQueues, StepActor}

var stepDomain = map[StepID]DomainID{StepRepair: DomainTasks, StepWorkspace: DomainTasks, StepQueues: DomainQueues, StepActor: DomainLeases}

// Steps reports every step: done (nothing to do), needed (a plan is available), blocked (needed
// but cannot be planned yet, with the reason) or unavailable (the tool is not here).
func (s *Setup) Steps(ctx context.Context) []StepInfo {
	var out []StepInfo
	for _, id := range StepOrder {
		d := stepDomain[id]
		info := StepInfo{Step: id, Domain: d, State: StepDone, Message: d.Title() + " is set up"}
		g := s.Guide(ctx, d)
		switch {
		case s.guiders[d] == nil:
			info.State, info.Message = StepUnavailable, d.Title()+": abacus's own tool for this is unavailable (see `abacus doctor`)"
		case g == nil:
		case g.Step == StepRepair: // never part of `setup --yes`: only `setup --repair` applies it
			info.Step, info.State, info.Message = StepRepair, StepRepairing, g.CLILine()
			if p, err := s.Plan(StepRepair); err != nil {
				info.State, info.Message = StepBlocked, g.Message+": "+err.Error()
			} else {
				info.Lines, info.Command = p.Lines, DisplayArgv(p.Argv)
			}
		default:
			info.Message = g.CLILine()
			if p, err := s.Plan(id); err != nil {
				info.State, info.Message = StepBlocked, g.Message+": "+err.Error()
			} else {
				info.State, info.Lines, info.Command = StepNeeded, p.Lines, DisplayArgv(p.Argv)
			}
		}
		out = append(out, info)
	}
	return out
}

// StepDomain is the domain a step belongs to.
func StepDomain(id StepID) DomainID { return stepDomain[id] }
