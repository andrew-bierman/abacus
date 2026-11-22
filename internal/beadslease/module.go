// Package beadslease is the umbrella's second module. It reads beads-lease (leases on the
// Beads hub) only through `version [--json]`, `doctor --json` and `--json list`, and never
// writes or syncs.
package beadslease

import (
	"context"
	"encoding/json"
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

func (m *Module) ID() string   { return "beads-lease" }
func (m *Module) Name() string { return "beads-lease" }

// versionRE matches the legacy `<name> <version> — <tagline>` line printed by binaries
// that predate `version --json`.
var versionRE = regexp.MustCompile(`^\S+\s+v?(\d+\.\d+\S*)`)

// supportedAPIVersion is the only `version --json` api_version this module reads.
const supportedAPIVersion = 1

type versionInfo struct {
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
}

func (m *Module) Detect(ctx context.Context) core.Detection {
	bin, absent := core.Locate("beads-lease", m.explicit, m.lookup)
	if absent != "" {
		return core.Detection{State: core.Absent, Detail: absent}
	}
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	m.hasDoctor = false
	// Missing, unparsable or api_version-less JSON (an older binary) takes the legacy path.
	if out, exit, err := m.run.Run(ctx, bin, "version", "--json"); err == nil && exit == 0 {
		var v versionInfo
		if json.Unmarshal(out, &v) == nil && v.APIVersion > 0 && v.Version != "" {
			ver := core.Sanitize(v.Version)
			if v.APIVersion != supportedAPIVersion {
				return core.Detection{State: core.Incompatible, Version: ver, APIVersion: v.APIVersion,
					Detail: fmt.Sprintf("api_version %d unsupported (supported: %d)", v.APIVersion, supportedAPIVersion)}
			}
			m.bin, m.hasDoctor = bin, true
			return core.Detection{State: core.Ready, Version: ver, APIVersion: v.APIVersion}
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
	m.bin = bin
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
	var raw []core.Check
	if err != nil || json.Unmarshal(out, &raw) != nil {
		return []core.Check{{Name: "beads-lease doctor", Status: core.StatusFail, Detail: "doctor --json produced no readable report"}}
	}
	if len(raw) == 0 {
		return []core.Check{{Name: "beads-lease doctor", Status: core.StatusWarn, Detail: "doctor --json reported no checks"}}
	}
	failed := false
	for i := range raw {
		raw[i].Name, raw[i].Detail, raw[i].Fix = core.Sanitize(raw[i].Name), core.Sanitize(raw[i].Detail), core.Sanitize(raw[i].Fix)
		raw[i].Status = raw[i].Status.Normalize()
		failed = failed || raw[i].Status == core.StatusFail
	}
	if exit != 0 && !failed {
		raw = append(raw, core.Check{Name: "beads-lease doctor", Status: core.StatusFail, Detail: fmt.Sprintf("exited %d without reporting a failed check", exit)})
	}
	return raw
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

func (m *Module) Status(ctx context.Context) core.Status {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	out, exit, err := m.run.Run(ctx, m.bin, "--json", "list")
	if err != nil {
		return unavailable("beads-lease list could not run: " + err.Error())
	}
	var env envelope
	if json.Unmarshal(out, &env) != nil {
		if exit != 0 {
			return unavailable(fmt.Sprintf("beads-lease list exited %d", exit))
		}
		return unavailable("beads-lease list: unreadable output")
	}
	if env.Error != nil && env.Error.Message != "" {
		return unavailable("beads-lease: " + env.Error.Message)
	}
	if !env.OK || env.SchemaVersion != 1 {
		return unavailable(fmt.Sprintf("unsupported beads-lease list output (schemaVersion %d)", env.SchemaVersion))
	}
	if exit != 0 {
		return unavailable(fmt.Sprintf("beads-lease list exited %d", exit))
	}
	held, stale, free := 0, 0, 0
	st := core.Status{}
	for _, l := range env.Leases {
		res, holder := core.Sanitize(l.Resource), core.Sanitize(l.Holder)
		switch l.State {
		case "held":
			held++
			exp := "?"
			if l.ExpiresAt != nil {
				exp = l.ExpiresAt.UTC().Format(time.RFC3339)
			}
			st.Lines = append(st.Lines, fmt.Sprintf("%s  held by %s (expires %s)", res, holder, exp))
		case "stale":
			stale++
			st.Lines = append(st.Lines, fmt.Sprintf("%s  stale (last holder %s)", res, holder))
		case "free":
			free++
			st.Lines = append(st.Lines, fmt.Sprintf("%s  free", res))
		default:
			st.Lines = append(st.Lines, fmt.Sprintf("%s  %s", res, core.Sanitize(l.State)))
		}
	}
	st.Summary = fmt.Sprintf("%d held, %d stale, %d free", held, stale, free)
	return st
}

func unavailable(reason string) core.Status {
	return core.Status{Unavailable: core.Sanitize(reason)}
}
