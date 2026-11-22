// Package beadslease is the umbrella's second module. It reads beads-lease (leases on the
// Beads hub) only through `version [--json]`, `doctor --json` and `--json list`, and never
// writes or syncs.
package beadslease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Options configure the module. Every process boundary is injectable so tests never run
// the real beads-lease, which would read the production coordination hub.
type Options struct {
	Bin      string // explicit binary; empty means look it up on PATH
	LookPath func(string) (string, error)
	// Actor returns the actor name from abacus's config; BEADS_ACTOR in the environment wins.
	// It is passed to a real beads-lease as BEADS_ACTOR, so setting it once is enough.
	Actor   func() string
	Runner  core.Runner // wrapped by the read-only allowlist whatever it is
	Timeout time.Duration
}

// Module implements core.Module for beads-lease.
type Module struct {
	core.Locator
	run      readOnly
	lookup   func(string) (string, error)
	explicit string
	actor    func() string
	timeout  time.Duration
	// hasDoctor is set by Detect when the binary reports api_version 1, which includes
	// `doctor --json`; older binaries keep the skip check.
	hasDoctor bool
}

// New returns a beads-lease module with defaults filled in.
func New(o Options) *Module {
	if o.Runner == nil {
		o.Runner = core.ExecRunner{Env: func() []string {
			if a := actorOf(o); os.Getenv(actorEnv) == "" && a != "" {
				return []string{actorEnv + "=" + a}
			}
			return nil
		}}
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Timeout <= 0 {
		o.Timeout = core.DefaultExecTimeout
	}
	return &Module{run: readOnly{inner: o.Runner}, lookup: o.LookPath, explicit: o.Bin, timeout: o.Timeout, actor: o.Actor}
}

const actorEnv = "BEADS_ACTOR"

func actorOf(o Options) string {
	if o.Actor == nil {
		return ""
	}
	return o.Actor()
}

// Domain and Guide make beads-lease the Leases guider: with no actor name from the
// environment or abacus's config it asks for one.
func (m *Module) Domain() core.DomainID { return core.DomainLeases }

func (m *Module) Guide(context.Context) *core.Guide {
	if os.Getenv(actorEnv) != "" || (m.actor != nil && m.actor() != "") {
		return nil
	}
	return &actorGuide
}

var actorGuide = core.Guide{Domain: core.DomainLeases, Step: core.StepActor, Key: "s",
	Message: "Leases need an actor name", Action: "choose one",
	Detail: "The name this machine's agents hold leases under. Change it any time with: abacus config set actor NAME"}

func (m *Module) ID() string   { return "beads-lease" }
func (m *Module) Name() string { return "beads-lease" }

// versionRE matches the legacy `<name> <version> — <tagline>` line printed by binaries
// that predate `version --json`.
var versionRE = regexp.MustCompile(`^\S+\s+v?(\d+\.\d+\S*)`)

// supportedAPIVersion is the only `version --json` api_version this module reads.
const supportedAPIVersion = 1

type versionInfo struct {
	Version    string `json:"version"`
	APIVersion *int   `json:"api_version"` // nil means the key is absent
}

func (m *Module) Detect(ctx context.Context) core.Detection {
	f, absent := core.LocateFound("beads-lease", m.explicit, m.lookup)
	bin := f.Bin
	if absent != "" {
		return core.Detection{State: core.Absent, Detail: absent}
	}
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	m.hasDoctor = false
	// Only non-JSON or api_version-less output (an older binary) takes the legacy path; an
	// explicit unsupported api_version is incompatible.
	if out, exit, err := m.run.Run(ctx, bin, "version", "--json"); err == nil && exit == 0 {
		var v versionInfo
		if json.Unmarshal(out, &v) == nil && v.APIVersion != nil {
			ver := core.Sanitize(v.Version)
			if *v.APIVersion != supportedAPIVersion {
				return core.Detection{State: core.Incompatible, Version: ver, APIVersion: *v.APIVersion,
					Detail: fmt.Sprintf("api_version %d unsupported (supported: %d)", *v.APIVersion, supportedAPIVersion)}
			}
			if ver != "" {
				m.SetFound(f)
				m.hasDoctor = true
				return core.Detection{State: core.Ready, Version: ver, APIVersion: supportedAPIVersion}
			}
		}
	}
	out, exit, err := m.run.Run(ctx, bin, "version")
	if err != nil {
		return core.Detection{State: core.Broken, Detail: "beads-lease version could not run: " + core.Sanitize(err.Error())}
	}
	if exit != 0 {
		return core.Detection{State: core.Broken, Detail: fmt.Sprintf("beads-lease version exited %d", exit)}
	}
	match := versionRE.FindStringSubmatch(core.Sanitize(string(out)))
	if match == nil {
		return core.Detection{State: core.Incompatible, Detail: "beads-lease version: unrecognized output"}
	}
	m.SetFound(f)
	// Legacy binary: api_version stays unknown, but it is usable for `--json list`.
	return core.Detection{State: core.Ready, Version: match[1]}
}

// Doctor runs `doctor --json` when the binary supports it. A non-zero exit with no failing
// check is itself a failure, so a red tool never reads green. Older binaries get a skip.
func (m *Module) Doctor(ctx context.Context) []core.Check {
	if !m.hasDoctor {
		return []core.Check{{Name: "doctor", Status: core.StatusSkip, Detail: "this beads-lease predates `doctor --json`; upgrade to a build with api_version 1"}}
	}
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	out, exit, err := m.run.Run(ctx, m.Bin(), "doctor", "--json")
	return core.ReadDoctor("beads-lease", out, exit, err)
}

type lease struct {
	Resource  string     `json:"resource"`
	Holder    string     `json:"holder"`
	State     string     `json:"state"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

type envelope struct {
	SchemaVersion int  `json:"schemaVersion"`
	OK            bool `json:"ok"`
	Error         *struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"error"`
	Leases []lease `json:"leases"`
}

// readLeases is the one read of `--json list`, shared by Status and the Leases domain.
func (m *Module) readLeases(ctx context.Context) ([]lease, error) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	out, exit, err := m.run.Run(ctx, m.Bin(), "--json", "list")
	if err != nil {
		return nil, fmt.Errorf("beads-lease list could not run: %w", err)
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil {
		if exit != 0 {
			return nil, fmt.Errorf("beads-lease list exited %d", exit)
		}
		return nil, errors.New("beads-lease list: unreadable output")
	}
	if env.Error != nil && env.Error.Message != "" {
		switch {
		case strings.Contains(env.Error.Message, "BEADS_ACTOR is not set"):
			return nil, &core.NeedsSetupError{Guide: actorGuide}
		case env.Error.Reason == "hub-failure" && strings.Contains(env.Error.Message, "no such file"):
			return nil, &core.NotConnectedError{
				Reason:       "Leases are shared through a coordination hub, and there is none on this machine.",
				HowToConnect: "Point beads-lease at your hub's coord wrapper with BEADS_LEASE_COORD. Nothing else in abacus depends on Leases."}
		}
		return nil, errors.New("beads-lease: " + env.Error.Message)
	}
	if !env.OK || env.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported beads-lease list output (schemaVersion %d)", env.SchemaVersion)
	}
	if exit != 0 {
		return nil, fmt.Errorf("beads-lease list exited %d", exit)
	}
	return env.Leases, nil
}

// Leases is the Leases domain read: typed, sanitized, live.
func (m *Module) Leases(ctx context.Context) (core.LeasesReport, error) {
	ls, err := m.readLeases(ctx)
	if err != nil {
		var nc *core.NotConnectedError
		if _, ok := core.AsNeedsSetup(err); ok || errors.As(err, &nc) {
			return core.LeasesReport{}, err // typed: the front-end shows a guided state
		}
		return core.LeasesReport{}, errors.New(core.Sanitize(err.Error()))
	}
	r := core.LeasesReport{Header: core.Header{Source: core.SourceLive}, Leases: []core.Lease{}}
	for _, l := range ls {
		cl := core.Lease{Resource: core.Sanitize(l.Resource), State: core.LeaseState(core.Sanitize(l.State)), Holder: core.Sanitize(l.Holder)}
		if l.ExpiresAt != nil {
			cl.ExpiresAt = l.ExpiresAt.UTC().Format(time.RFC3339)
		}
		r.Leases = append(r.Leases, cl)
	}
	return r, nil
}

func (m *Module) Status(ctx context.Context) core.Status {
	r, err := m.Leases(ctx)
	if err != nil {
		return unavailable(err.Error())
	}
	return r.Status()
}

func unavailable(reason string) core.Status {
	return core.Status{Unavailable: core.Sanitize(reason)}
}
