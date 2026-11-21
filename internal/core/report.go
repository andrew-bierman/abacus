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

// Report is the typed result every front-end renders.
type Report struct {
	Version string         `json:"version"`
	Modules []ModuleReport `json:"modules"`
}

// Exit codes shared by every front-end.
const (
	ExitOK    = 0
	ExitFail  = 1
	ExitUsage = 2
)

// ExitCode is 1 only when a check failed. Absent, incompatible, warn, skip and
// unavailable state never fail the run.
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
	r := Report{Version: version, Modules: []ModuleReport{}}
	for _, e := range entries {
		r.Modules = append(r.Modules, base(e))
	}
	return r
}

// DoctorReport runs health checks on every present module. An incompatible tool is a warn.
func DoctorReport(ctx context.Context, version string, entries []Entry) Report {
	r := Report{Version: version, Modules: []ModuleReport{}}
	for _, e := range present(entries) {
		m := base(e)
		if e.Detection.State == Incompatible {
			m.Checks = []Check{{Name: "compatibility", Status: StatusWarn, Detail: e.Detection.Detail}}
		} else {
			m.Checks = e.Module.Doctor(ctx)
		}
		r.Modules = append(r.Modules, m)
	}
	return r
}

// StatusReport reads state from every ready module.
func StatusReport(ctx context.Context, version string, entries []Entry) Report {
	r := Report{Version: version, Modules: []ModuleReport{}}
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
