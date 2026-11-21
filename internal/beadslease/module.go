// Package beadslease is the umbrella's second module. It reads beads-lease (leases on the
// Beads hub) only through `version` and `--json list`, and never writes or syncs.
package beadslease

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// versionLine matches `<name> <version> — <tagline>`; beads-lease has no `version --json`
// and no api_version yet (andrew-bierman/beads-lease#6).
var versionRE = regexp.MustCompile(`^\S+\s+v?(\d+\.\d+\S*)`)

func (m *Module) Detect(ctx context.Context) core.Detection {
	bin := m.explicit
	if bin == "" {
		p, err := m.lookup("beads-lease")
		if err != nil {
			return core.Detection{State: core.Absent, Detail: "beads-lease not found on PATH"}
		}
		bin = p
	} else if _, err := os.Stat(bin); err != nil {
		return core.Detection{State: core.Absent, Detail: "beads-lease not found at " + bin}
	}
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
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
	// shortcut: api_version stays unknown until beads-lease ships `version --json`
	// (#6); require it then.
	return core.Detection{State: core.Ready, Version: match[1]}
}

// Doctor reports a skip: beads-lease has no doctor yet (andrew-bierman/beads-lease#7), and
// abacus does not invent health claims for a tool it does not own.
func (m *Module) Doctor(context.Context) []core.Check {
	return []core.Check{{Name: "doctor", Status: core.StatusSkip, Detail: "beads-lease provides no doctor yet (andrew-bierman/beads-lease#7)"}}
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
