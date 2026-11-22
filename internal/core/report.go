package core

import (
	"context"
	"encoding/json"
	"fmt"
)

// ModuleReport is one module's slice of a doctor, status or version report.
type ModuleReport struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Detection Detection `json:"detection"`
	Checks    []Check   `json:"checks,omitempty"`
	Status    *Status   `json:"status,omitempty"`
}

// SchemaVersion is the version of the report JSON that native clients decode. Bump it
// only for a breaking change to Report, ModuleReport, Detection, Check or Status.
const SchemaVersion = 1

// Report is the typed result every front-end renders.
type Report struct {
	SchemaVersion int            `json:"schema_version"`
	Version       string         `json:"version"`
	Modules       []ModuleReport `json:"modules"`
	// Domains is the fleet-domain view; additive, so schema_version stays 1. Set by `status`.
	Domains *DomainsReport `json:"domains,omitempty"`
	// Overview leads `doctor`: ready or needs attention, each domain and tool in one line.
	// Additive, so schema_version stays 1.
	Overview *Overview `json:"overview,omitempty"`
}

func newReport(version string) Report {
	return Report{SchemaVersion: SchemaVersion, Version: version, Modules: []ModuleReport{}}
}

// Exit codes shared by every front-end.
const (
	ExitOK    = 0
	ExitFail  = 1
	ExitUsage = 2
)

// ExitCode is the one exit-code rule: 1 only when a check failed, which includes a
// broken tool (found but unable to run). Absent, incompatible, warn, skip and an
// unreadable state never fail the run.
func (r Report) ExitCode() int {
	for _, m := range r.Modules {
		for _, c := range m.Checks {
			if c.Status == StatusFail {
				return ExitFail
			}
		}
	}
	return ExitOK
}

func base(e Entry) ModuleReport {
	return ModuleReport{ID: e.Module.ID(), Name: e.Module.Name(), Detection: e.Detection}
}

// present drops absent tools: `version` reports them, doctor and status do not.
func present(entries []Entry) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Detection.State != Absent {
			out = append(out, e)
		}
	}
	return out
}

// VersionReport lists every module's detection, absent ones included.
func VersionReport(version string, entries []Entry) Report {
	r := newReport(version)
	for _, e := range entries {
		r.Modules = append(r.Modules, base(e))
	}
	return r
}

// DoctorReport runs health checks on every present module. A broken tool is a failing
// check; an incompatible tool is a warn.
func DoctorReport(ctx context.Context, version string, entries []Entry) Report {
	r := newReport(version)
	ov := BuildOverview(ctx, entries)
	r.Overview = &ov
	for _, e := range present(entries) {
		m := base(e)
		switch e.Detection.State {
		case Broken:
			m.Checks = []Check{{Name: "installation", Status: StatusFail, Detail: e.Detection.Detail}}
		case Incompatible:
			m.Checks = []Check{{Name: "compatibility", Status: StatusWarn, Detail: e.Detection.Detail}}
		default:
			m.Checks = normalized(e.Module.Doctor(ctx))
		}
		r.Modules = append(r.Modules, m)
	}
	return r
}

// StatusReport reads state from every ready module.
func StatusReport(ctx context.Context, version string, entries []Entry) Report {
	r := newReport(version)
	for _, e := range present(entries) {
		m := base(e)
		if e.Detection.State == Ready {
			st := e.Module.Status(ctx)
			m.Status = &st
		}
		r.Modules = append(r.Modules, m)
	}
	return r
}

// normalized guards every module's checks with the declared status set.
func normalized(checks []Check) []Check {
	for i := range checks {
		checks[i].Status = checks[i].Status.Normalize()
	}
	return checks
}

// ReadDoctor turns a tool's `doctor` run (output, exit code, runner error) into checks, so
// no tool's red report reads green: an unreadable report is a failure, text is sanitized,
// statuses are normalized, an empty report is a failure on a non-zero exit (else a warning),
// and a non-zero exit with no failing check adds one.
func ReadDoctor(tool string, out []byte, exit int, err error) []Check {
	name := tool + " doctor"
	var raw []Check
	if err != nil || json.Unmarshal(out, &raw) != nil {
		return []Check{{Name: name, Status: StatusFail, Detail: "no readable doctor report"}}
	}
	if len(raw) == 0 {
		if exit != 0 {
			return []Check{{Name: name, Status: StatusFail, Detail: fmt.Sprintf("exited %d with an empty report", exit)}}
		}
		return []Check{{Name: name, Status: StatusWarn, Detail: "doctor reported no checks"}}
	}
	failed := false
	for i := range raw {
		raw[i].Name, raw[i].Detail, raw[i].Fix = Sanitize(raw[i].Name), Sanitize(raw[i].Detail), Sanitize(raw[i].Fix)
		raw[i].Status = raw[i].Status.Normalize()
		failed = failed || raw[i].Status == StatusFail
	}
	if exit != 0 && !failed {
		raw = append(raw, Check{Name: name, Status: StatusFail, Detail: fmt.Sprintf("exited %d without reporting a failed check", exit)})
	}
	return raw
}
