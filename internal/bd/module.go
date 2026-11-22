// Package bd is the Tasks provider and writer. It composes the Beads CLI only through
// `bd ... --json`: reads always run `--readonly`, writes only through the separate write
// allowlist in guard.go, and only after core's confirm step.
package bd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Options configure the module; every process boundary is injectable.
type Options struct {
	Bin      string // explicit binary; empty means look it up on PATH
	Dir      string // tracker directory passed as -C; empty means the working directory
	LookPath func(string) (string, error)
	Runner   core.Runner
	Timeout  time.Duration
}

// Module is the Beads CLI as a core module, Tasks provider and task writer.
type Module struct {
	o   Options
	bin string
}

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
	return &Module{o: o}
}

func (m *Module) ID() string   { return "bd" }
func (m *Module) Name() string { return "bd" }

func (m *Module) TrackerDir() string { return m.o.Dir }

// supportedSchema is the only `bd version --json` schema_version this module reads.
const supportedSchema = 1

func (m *Module) dirArgs(a ...string) []string {
	if m.o.Dir != "" {
		return append([]string{"-C", m.o.Dir}, a...)
	}
	return a
}

// read runs one allowlisted read.
func (m *Module) read(ctx context.Context, a []string) ([]byte, int, error) {
	args := m.dirArgs(a...)
	if !readAllowed(args, m.o.Dir) {
		return nil, -1, fmt.Errorf("bd %q is not allowed: reads are fixed", args)
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	return m.o.Runner.Run(ctx, m.bin, args...)
}

func (m *Module) Detect(ctx context.Context) core.Detection {
	bin, absent := core.Locate("bd", m.o.Bin, m.o.LookPath)
	if absent != "" {
		return core.Detection{State: core.Absent, Detail: absent}
	}
	m.bin = bin
	// version needs no tracker, so it never takes -C: a bad tracker dir must not read as a broken install.
	cctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	out, exit, err := m.o.Runner.Run(cctx, m.bin, readVersion...)
	if err != nil {
		return core.Detection{State: core.Broken, Detail: "bd version could not run: " + core.Sanitize(err.Error())}
	}
	if exit != 0 {
		return core.Detection{State: core.Broken, Detail: fmt.Sprintf("bd version exited %d", exit)}
	}
	var v struct {
		Version       string `json:"version"`
		SchemaVersion int    `json:"schema_version"`
	}
	if json.Unmarshal(out, &v) != nil || v.Version == "" {
		return core.Detection{State: core.Incompatible, Detail: "bd version --json: unreadable output"}
	}
	ver := core.Sanitize(v.Version)
	if v.SchemaVersion != supportedSchema {
		return core.Detection{State: core.Incompatible, Version: ver, APIVersion: v.SchemaVersion,
			Detail: fmt.Sprintf("schema_version %d unsupported (supported: %d)", v.SchemaVersion, supportedSchema)}
	}
	return core.Detection{State: core.Ready, Version: ver, APIVersion: v.SchemaVersion}
}

const noTrackerHint = "no tracker found here: run abacus inside a tracker directory or set ABACUS_BD_DIR"

type statusDoc struct {
	SchemaVersion int `json:"schema_version"`
	Summary       struct {
		Total      int `json:"total_issues"`
		Open       int `json:"open_issues"`
		InProgress int `json:"in_progress_issues"`
		Blocked    int `json:"blocked_issues"`
		Closed     int `json:"closed_issues"`
		Deferred   int `json:"deferred_issues"`
		Ready      int `json:"ready_issues"`
	} `json:"summary"`
}

type issueJSON struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Priority  int    `json:"priority"`
	Type      string `json:"issue_type"`
	Assignee  string `json:"assignee"`
	UpdatedAt string `json:"updated_at"`
}

func (m *Module) Doctor(ctx context.Context) []core.Check {
	if _, err := m.Tasks(ctx); err != nil {
		// An install that runs but has no tracker here is not broken: a warning with a fix.
		return []core.Check{{Name: "tracker", Status: core.StatusWarn, Detail: core.Sanitize(err.Error()), Fix: "run abacus in a tracker directory or set ABACUS_BD_DIR"}}
	}
	return []core.Check{{Name: "tracker", Status: core.StatusOK, Detail: "tracker readable"}}
}

func (m *Module) Status(ctx context.Context) core.Status {
	r, err := m.Tasks(ctx)
	if err != nil {
		return core.Status{Unavailable: core.Sanitize(err.Error())}
	}
	c := r.Counts
	return core.Status{Summary: fmt.Sprintf("%d tasks: %d open, %d in progress, %d blocked, %d ready", c.Total, c.Open, c.InProgress, c.Blocked, c.Ready)}
}

// Tasks is the compact Tasks read: counts plus the most recently changed issues. It is
// deliberately not an issue browser.
func (m *Module) Tasks(ctx context.Context) (core.TasksReport, error) {
	out, exit, err := m.read(ctx, readStatus)
	var st statusDoc
	if err != nil || exit != 0 || json.Unmarshal(out, &st) != nil || st.SchemaVersion != 1 {
		return core.TasksReport{}, errors.New(noTrackerHint)
	}
	r := core.TasksReport{Header: core.Header{Source: core.SourceLive}}
	s := st.Summary
	r.Counts = core.TaskCounts{Total: s.Total, Open: s.Open, InProgress: s.InProgress, Blocked: s.Blocked, Deferred: s.Deferred, Closed: s.Closed, Ready: s.Ready}
	out, exit, err = m.read(ctx, readList)
	var issues []issueJSON
	if err != nil || exit != 0 || json.Unmarshal(out, &issues) != nil {
		return core.TasksReport{}, errors.New("bd list: unreadable output")
	}
	for _, i := range issues {
		r.Recent = append(r.Recent, core.Task{ID: core.Sanitize(i.ID), Title: core.Sanitize(i.Title), Status: core.Sanitize(i.Status),
			Priority: i.Priority, Type: core.Sanitize(i.Type), Assignee: core.Sanitize(i.Assignee), UpdatedAt: core.Sanitize(i.UpdatedAt)})
	}
	return r, nil
}

// RunTask runs a planned write. It re-checks the write allowlist, so nothing but a built
// argv can reach the process, and decodes bd's `--json` answer (an object or a one-element array).
func (m *Module) RunTask(ctx context.Context, argv []string) (core.TaskResult, error) {
	if len(argv) < 2 || argv[0] != "bd" || !writeAllowed(argv[1:], m.o.Dir) {
		return core.TaskResult{}, errors.New("refused: not an allowed bd write")
	}
	if m.bin == "" {
		bin, absent := core.Locate("bd", m.o.Bin, m.o.LookPath)
		if absent != "" {
			return core.TaskResult{}, errors.New(absent)
		}
		m.bin = bin
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	out, exit, err := m.o.Runner.Run(ctx, m.bin, argv[1:]...)
	if err != nil {
		return core.TaskResult{}, fmt.Errorf("bd write could not run: %s", core.Sanitize(err.Error()))
	}
	var one issueJSON
	var many []issueJSON
	var e struct {
		Error string `json:"error"`
	}
	switch {
	case json.Unmarshal(out, &many) == nil && len(many) > 0:
		one = many[0]
	case json.Unmarshal(out, &e) == nil && e.Error != "":
		return core.TaskResult{}, errors.New("bd: " + core.Sanitize(e.Error))
	case exit == 0 && json.Unmarshal(out, &one) == nil:
	}
	if exit != 0 {
		return core.TaskResult{}, fmt.Errorf("bd exited %d", exit)
	}
	return core.TaskResult{ID: core.Sanitize(one.ID), Title: core.Sanitize(one.Title), Status: core.Sanitize(one.Status)}, nil
}
