package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"regexp"
	"strings"
	"sync"
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
// command), needs an explicit Confirm, can be applied once, and is audited like a Tasks write.
// Nothing here starts a daemon.
type Setup struct {
	o       SetupOptions
	writer  TaskWriter
	applier *Applier
	lambe   LambeInitializer
	guiders map[DomainID]Guider
	mu      sync.Mutex
	applied map[uint64]bool
}

// NewSetup picks the providers it needs out of the detected modules.
func NewSetup(entries []Entry, o SetupOptions) *Setup {
	if o.Audit == nil {
		o.Audit = DefaultAudit()
	}
	p := ResolveProviders(entries)
	s := &Setup{o: o, writer: p.Writer, guiders: p.Guiders, applied: map[uint64]bool{}}
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
	confirmed bool
}

// Confirm marks the plan as explicitly approved by the user.
func (p SetupPlan) Confirm() SetupPlan { p.confirmed = true; return p }

var setupSeq atomic.Uint64

// Plan builds the plan for a step without side effects. It fails with a plain reason when the
// step cannot be done safely (no tool, nothing to do, a prerequisite missing).
func (s *Setup) Plan(step StepID) (SetupPlan, error) {
	switch step {
	case StepWorkspace:
		d, ok := s.writer.(InitDirer)
		if s.writer == nil || !ok || d.InitDir() == "" {
			return SetupPlan{}, errors.New("a tracker is already set up (or bd was not found): nothing to create")
		}
		wp, err := PlanTaskChange(s.writer, TaskChange{Op: TaskInit, Dir: d.InitDir()})
		if err != nil {
			return SetupPlan{}, err
		}
		return SetupPlan{id: setupSeq.Add(1), Step: step, Domain: DomainTasks, Lines: wp.Lines, Argv: wp.Argv, wp: wp}, nil
	case StepQueues:
		if s.lambe == nil {
			return SetupPlan{}, errors.New("Lambe was not found: nothing to set up")
		}
		if s.o.Workspace == "" || s.o.LambeConfig == "" {
			return SetupPlan{}, errors.New("no home directory to keep the config in")
		}
		if !HasTracker(s.o.Workspace) {
			return SetupPlan{}, errors.New("create your fleet workspace first (Tasks tab, press i)")
		}
		argv, err := s.lambe.BuildInitArgv(s.o.LambeConfig)
		if err != nil {
			return SetupPlan{}, err
		}
		return SetupPlan{id: setupSeq.Add(1), Step: step, Domain: DomainQueues, Argv: argv, Lines: []string{
			"write a starter supervisor config for your fleet workspace",
			"config: " + s.o.LambeConfig,
			"workspace files: a starter WORKFLOW.md and formula in " + s.o.Workspace,
			"nothing is started: you run the supervisor yourself (lambe run)",
		}}, nil
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

// Apply runs a confirmed plan once and returns a one-line result. The attempt is audited
// before anything runs and a failed audit blocks the step; an outcome that cannot be known is
// reported as unknown, never as "not applied".
func (s *Setup) Apply(ctx context.Context, p SetupPlan) (string, error) {
	if !p.confirmed {
		return "", ErrNotConfirmed
	}
	switch p.Step {
	case StepWorkspace:
		res, err := s.applier.Apply(ctx, p.wp.Confirm())
		if err != nil {
			return "", err
		}
		return "created " + Sanitize(res.Title), nil
	case StepQueues, StepActor:
	default:
		return "", fmt.Errorf("unknown setup step %q", p.Step)
	}
	s.mu.Lock()
	if s.applied[p.id] {
		s.mu.Unlock()
		return "", ErrAlreadyApplied
	}
	s.applied[p.id] = true
	s.mu.Unlock()
	e := AuditEntry{Time: time.Now().UTC(), Domain: string(p.Domain), Verb: "setup-" + string(p.Step), Phase: "attempt", Argv: p.Argv}
	if err := s.o.Audit.Record(e); err != nil {
		return "", err
	}
	msg, err := s.run(ctx, p)
	e.Time, e.Phase = time.Now().UTC(), "ok"
	if err != nil {
		e.Phase, e.Error = "error", Sanitize(err.Error())
		if errors.Is(err, ErrOutcomeUnknown) {
			e.Phase = "unknown"
		}
	}
	_ = s.o.Audit.Record(e)
	return msg, err
}

func (s *Setup) run(ctx context.Context, p SetupPlan) (string, error) {
	if p.Step == StepActor {
		if err := s.o.Config.Update(func(c *Config) error { return c.Set("actor", p.actor) }); err != nil {
			return "", err
		}
		return "leases will be held as " + p.actor, nil
	}
	if err := s.lambe.RunInit(ctx, s.o.Workspace, s.o.LambeConfig); err != nil {
		return "", err
	}
	if err := s.o.Config.Update(func(c *Config) error { return c.Set("lambe_config", s.o.LambeConfig) }); err != nil {
		return "", fmt.Errorf("the config was written to %s but could not be remembered (%s): run `%s config set lambe_config %s`", s.o.LambeConfig, Sanitize(err.Error()), brand.Name, s.o.LambeConfig)
	}
	return "wrote " + s.o.LambeConfig + " (start the supervisor yourself: lambe run -config " + s.o.LambeConfig + ")", nil
}

// StepInfo is one step as `abacus setup` reports it.
type StepInfo struct {
	Step    StepID   `json:"step"`
	Domain  DomainID `json:"domain"`
	State   string   `json:"state"` // done, needed, blocked, unavailable
	Message string   `json:"message"`
	Lines   []string `json:"plan,omitempty"`
	Command string   `json:"command,omitempty"`
}

// StepOrder is the order steps are offered in: the workspace first, since Queues needs it.
var StepOrder = []StepID{StepWorkspace, StepQueues, StepActor}

var stepDomain = map[StepID]DomainID{StepWorkspace: DomainTasks, StepQueues: DomainQueues, StepActor: DomainLeases}

// Steps reports every step: done (nothing to do), needed (a plan is available), blocked (needed
// but cannot be planned yet, with the reason) or unavailable (the tool is not here).
func (s *Setup) Steps(ctx context.Context) []StepInfo {
	var out []StepInfo
	for _, id := range StepOrder {
		d := stepDomain[id]
		info := StepInfo{Step: id, Domain: d, State: "done", Message: d.Title() + " is set up"}
		g := s.Guide(ctx, d)
		switch {
		case s.guiders[d] == nil:
			info.State, info.Message = "unavailable", d.Title()+": the tool for this was not found"
		case g == nil:
		default:
			info.Message = g.CLILine()
			if p, err := s.Plan(id); err != nil {
				info.State, info.Message = "blocked", g.Message+": "+err.Error()
			} else {
				info.State, info.Lines, info.Command = "needed", p.Lines, DisplayArgv(p.Argv)
			}
		}
		out = append(out, info)
	}
	return out
}

// StepDomain is the domain a step belongs to.
func StepDomain(id StepID) DomainID { return stepDomain[id] }
