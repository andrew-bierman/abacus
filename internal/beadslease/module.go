// Package beadslease is the umbrella's second module. It reads beads-lease (leases on the
// Beads hub) only through `version [--json]`, `doctor --json` and `--json list`, and never
// writes or syncs.
package beadslease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Options configure the module. Every process boundary is injectable so tests never run
// the real beads-lease, which would read the production coordination hub.
type Options struct {
	Bin      string // explicit binary; empty means look it up on PATH
	LookPath func(string) (string, error)
	Runner   core.Runner // wrapped by the read-only allowlist whatever it is
	Timeout  time.Duration
}

// Module implements core.Module for beads-lease.
type Module struct {
	run      readOnly
	lookup   func(string) (string, error)
	bin      string
	src      core.Source
	explicit string
	timeout  time.Duration
	// hasDoctor is set by Detect when the binary reports api_version 1, which includes
	// `doctor --json`; older binaries keep the skip check.
	hasDoctor bool
}

// New returns a beads-lease module with defaults filled in.
func New(o Options) *Module {
	if o.Runner == nil {
		o.Runner = core.ExecRunner{}
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Timeout <= 0 {
		o.Timeout = core.DefaultExecTimeout
	}
	return &Module{run: readOnly{inner: o.Runner}, lookup: o.LookPath, explicit: o.Bin, timeout: o.Timeout}
}

// Located reports which binary Detect found and where it came from.
func (m *Module) Located() (string, core.Source) { return m.bin, m.src }

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
	bin, src, absent := core.LocateSource("beads-lease", m.explicit, m.lookup)
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
				m.bin, m.src, m.hasDoctor = bin, src, true
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
	m.bin, m.src = bin, src
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
	out, exit, err := m.run.Run(ctx, m.bin, "doctor", "--json")
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
		Message string `json:"message"`
	} `json:"error"`
	Leases []lease `json:"leases"`
}

// readLeases is the one read of `--json list`, shared by Status and the Leases domain.
func (m *Module) readLeases(ctx context.Context) ([]lease, error) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	out, exit, err := m.run.Run(ctx, m.bin, "--json", "list")
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
