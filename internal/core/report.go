package core

import "context"

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
