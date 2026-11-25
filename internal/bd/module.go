// Package bd is the Tasks provider and writer. It composes the Beads CLI only through
// `bd ... --json`: reads always run `--readonly`, writes only through the separate write
// allowlist in guard.go, and only after core's confirm step.
package bd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// Options configure the module; every process boundary is injectable.
type Options struct {
	Bin      string // explicit binary; empty means look it up on PATH
	Dir      string // tracker directory passed as -C; empty means the working directory
	LookPath func(string) (string, error)
	Runner   core.Runner
	Timeout  time.Duration
	// ExportRunner runs the one large read (export). Nil means the Runner when one was injected,
	// else a real process with stdout capped at ExportMaxOut and a longer deadline.
	ExportRunner core.Runner

	// Ref, when set, replaces Dir with a directory shared with other modules (the graph engine), so a
	// workspace created while abacus runs is read without a restart.
	Ref *core.TrackerRef
	// InitDir is the managed workspace a confirmed `bd init` may create. Set only when there
	// is no tracker to read; it is the one directory the init write can touch.
	InitDir string
	// ConfigMissing is the tracker_dir saved in the config when that path no longer holds a
	// tracker. Tasks then says so plainly and nothing offers to replace it.
	ConfigMissing string
	// OnInit persists the new workspace (the config file); it runs after a successful init.
	OnInit func(dir string) error
	// InitRunner runs `bd init` with dir as its working directory (bd init refuses -C for a
	// directory with no tracker yet). Nil means a real process with a longer deadline.
	InitRunner func(dir string) core.Runner
}

// Module is the Beads CLI as a core module, Tasks provider and task writer.
type Module struct {
	core.Locator
	o Options
}

// ExportMaxOut is the most export stdout that is kept (one byte over the graph engine's 64 MiB
// limit, so "too large" is detectable); the rest of the output is discarded, never buffered.
const ExportMaxOut = 64<<20 + 1

// exportTimeout is longer than a normal read: the export walks every issue.
const exportTimeout = 30 * time.Second

// New returns the tracker module configured by o.
func New(o Options) *Module {
	if o.ExportRunner == nil {
		if o.Runner == nil {
			o.ExportRunner = core.ExecRunner{Scrub: core.ScrubBeadsEnv, MaxOut: ExportMaxOut, Timeout: exportTimeout}
		} else {
			o.ExportRunner = o.Runner
		}
	}
	if o.Runner == nil {
		o.Runner = core.ExecRunner{Scrub: core.ScrubBeadsEnv}
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

// TrackerDir is the directory Tasks reads ("" means the working directory).
func (m *Module) TrackerDir() string {
	if m.o.Ref != nil {
		return m.o.Ref.Dir()
	}
	return m.o.Dir
}

// InitDir is where a confirmed init would create the workspace ("" when none is offered).
func (m *Module) InitDir() string { return m.o.InitDir }

// Domain and Guide make bd the Tasks guider: with no tracker to read it offers the workspace.
func (m *Module) Domain() core.DomainID { return core.DomainTasks }

// writeBlocked refuses a write when the bundled bd exists but was rejected as unsafe and a PATH
// bd was used for reading instead: a write never silently falls back to a different binary.
func (m *Module) writeBlocked() error {
	if f := m.Located(); f.Src == core.SourcePATH && f.Ignored != "" {
		return fmt.Errorf("not writing: %s; not writing with a different copy of bd", f.Ignored)
	}
	return nil
}

func (m *Module) configMissing() *core.NotConnectedError {
	if m.o.ConfigMissing == "" || m.TrackerDir() != "" || core.HasTracker("") {
		return nil
	}
	return &core.NotConnectedError{
		Reason:       "The tracker saved in your settings (" + m.o.ConfigMissing + ") is not there any more.",
		HowToConnect: "Restore it, or clear the setting with: " + brand.Name + ` config set tracker_dir ""  (abacus will then offer a new workspace).`}
}

func (m *Module) Guide(context.Context) *core.Guide {
	if m.o.InitDir == "" || m.TrackerDir() != "" || core.HasTracker("") {
		return nil
	}
	if h, _ := core.InspectWorkspace(m.o.InitDir); h == core.WorkspaceHalfBuilt {
		return &core.Guide{Domain: core.DomainTasks, Step: core.StepRepair, Message: "Your fleet workspace was only partly created", Action: "repair it (nothing is deleted)",
			Detail: "The unfinished files in " + m.o.InitDir + " will be moved aside, not deleted, and the workspace created again."}
	}
	return &core.Guide{Domain: core.DomainTasks, Step: core.StepWorkspace, Message: "No Beads tracker found", Action: "create a workspace (bd init)",
		Detail: "It will be created in " + m.o.InitDir + " and abacus will use it from then on."}
}

// supportedSchema is the only `bd version --json` schema_version this module reads.
const supportedSchema = 1

func (m *Module) dirArgs(a ...string) []string {
	if d := m.TrackerDir(); d != "" {
		return append([]string{"-C", d}, a...)
	}
	return a
}

// read runs one allowlisted read.
func (m *Module) read(ctx context.Context, a []string) ([]byte, int, error) {
	args := m.dirArgs(a...)
	if !readAllowed(args, m.TrackerDir()) {
		return nil, -1, fmt.Errorf("bd %q is not allowed: reads are fixed", args)
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	return m.o.Runner.Run(ctx, m.Bin(), args...)
}

func (m *Module) Detect(ctx context.Context) core.Detection {
	if _, absent := m.Find("bd", m.o.Bin, m.o.LookPath); absent != "" {
		return core.Detection{State: core.Absent, Detail: absent}
	}
	// version needs no tracker, so it never takes -C: a bad tracker dir must not read as a broken install.
	cctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	out, exit, err := m.o.Runner.Run(cctx, m.Bin(), readVersion...)
	if p := core.BuiltInExecProblem(m.Bin(), exit, err); p != "" {
		return core.Detection{State: core.Broken, Detail: p}
	}
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
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Priority  int      `json:"priority"`
	Type      string   `json:"issue_type"`
	Assignee  string   `json:"assignee"`
	UpdatedAt string   `json:"updated_at"`
	Labels    []string `json:"labels"`
	// Dependencies are what this issue depends on (its blockers, with the kind of each link).
	Dependencies []struct {
		ID   string `json:"id"`
		Type string `json:"dependency_type"`
	} `json:"dependencies"`
}

func (m *Module) Doctor(ctx context.Context) []core.Check {
	if _, err := m.Tasks(ctx); err != nil {
		// An install that runs but has no tracker here is not broken: a warning with a fix.
		return []core.Check{{Name: "tracker", Status: core.StatusWarn, Detail: core.Sanitize(err.Error()), Fix: "run abacus in a tracker directory or set ABACUS_BD_DIR"}}
	}
	return []core.Check{{Name: "tracker", Status: core.StatusOK, Detail: "tracker readable"}}
}

// DoctorWith is Doctor over an already-read Tasks header, so the overview and the doctor do not
// each read the tracker.
func (m *Module) DoctorWith(_ context.Context, d core.DomainsReport) []core.Check {
	h := d.Tasks.Header
	switch h.State {
	case core.Connected:
		return []core.Check{{Name: "tracker", Status: core.StatusOK, Detail: "tracker readable"}}
	case core.NeedsSetup:
		return []core.Check{{Name: "tracker", Status: core.StatusWarn, Detail: core.Sanitize(h.Setup.Message), Fix: "run " + brand.Name + " setup, or press the setup key on the Tasks tab"}}
	}
	fix := h.HowToConnect
	if fix == "" {
		fix = "run abacus in a tracker directory or set ABACUS_BD_DIR"
	}
	return []core.Check{{Name: "tracker", Status: core.StatusWarn, Detail: core.Sanitize(h.Reason), Fix: core.Sanitize(fix)}}
}

func (m *Module) Status(ctx context.Context) core.Status {
	r, err := m.Tasks(ctx)
	if err != nil {
		return core.Status{Unavailable: core.Sanitize(err.Error())}
	}
	return core.Status{Summary: r.Summarize()}
}

// Tasks is the compact Tasks read: counts plus the most recently changed issues. It is
// deliberately not an issue browser.
func (m *Module) Tasks(ctx context.Context) (core.TasksReport, error) {
	if nc := m.configMissing(); nc != nil {
		return core.TasksReport{}, nc
	}
	if g := m.Guide(ctx); g != nil {
		return core.TasksReport{}, &core.NeedsSetupError{Guide: *g}
	}
	out, exit, err := m.read(ctx, readStatus)
	switch {
	case errors.Is(err, core.ErrTimedOut):
		return core.TasksReport{}, errors.New("bd timed out reading the tracker")
	case err != nil:
		return core.TasksReport{}, fmt.Errorf("bd could not run: %s", core.Sanitize(err.Error()))
	case exit != 0:
		return core.TasksReport{}, errors.New(noTrackerHint)
	}
	var st statusDoc
	if json.Unmarshal(out, &st) != nil {
		return core.TasksReport{}, errors.New("bd status: unreadable output")
	}
	if st.SchemaVersion != supportedSchema {
		return core.TasksReport{}, fmt.Errorf("bd status: unsupported schema_version %d", st.SchemaVersion)
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

// Snapshot reads the whole tracker once (`bd --readonly export`) for the Tasks list and detail.
// Every view derives from this one read, so they cannot disagree about what is blocked or ready.
func (m *Module) Snapshot(ctx context.Context) (core.TaskSnapshot, error) {
	if nc := m.configMissing(); nc != nil {
		return core.TaskSnapshot{}, nc
	}
	if g := m.Guide(ctx); g != nil {
		return core.TaskSnapshot{}, &core.NeedsSetupError{Guide: *g}
	}
	out, err := m.Export(ctx) // capped, with the long export deadline
	switch {
	case errors.Is(err, core.ErrOutputTooLarge):
		return core.TaskSnapshot{}, taskview.ErrTooLarge
	case err != nil:
		return core.TaskSnapshot{}, err
	}
	// Name the directory that holds the tracker, as bd finds it from the configured or working
	// directory, so the header says which project the list and a write really use.
	name, root := "", core.TrackerRoot(m.TrackerDir())
	if root != "" {
		name = filepath.Base(root)
	} else if d := m.TrackerDir(); d != "" {
		name = filepath.Base(d)
		root, _ = filepath.Abs(d)
	}
	snap, err := taskview.Parse(bytes.NewReader(out), time.Now(), name)
	snap.Root = root // the absolute project directory: what per-project state is keyed on
	return snap, err
}

// InitPreflight is whether an init could run now: the same two checks RunTask makes first, with
// no side effects, so a repair never moves files when init could not follow.
func (m *Module) InitPreflight() error {
	if err := m.writeBlocked(); err != nil {
		return err
	}
	if m.Bin() == "" {
		if _, absent := m.Find("bd", m.o.Bin, m.o.LookPath); absent != "" {
			return errors.New(absent)
		}
	}
	return nil
}

// RunTask runs a planned write. It re-checks the write allowlist, so nothing but a built
// argv can reach the process, and decodes bd's `--json` answer (an object or a one-element array).
func (m *Module) RunTask(ctx context.Context, argv []string) (core.TaskResult, error) {
	if len(argv) < 2 || argv[0] != "bd" || !writeAllowed(argv[1:], m.TrackerDir()) {
		return core.TaskResult{}, errors.New("refused: not an allowed bd write")
	}
	if err := m.writeBlocked(); err != nil {
		return core.TaskResult{}, err
	}
	if m.Bin() == "" {
		if _, absent := m.Find("bd", m.o.Bin, m.o.LookPath); absent != "" {
			return core.TaskResult{}, errors.New(absent)
		}
	}
	if argv[1] == "init" {
		return m.runInit(ctx, argv[1:])
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	// stderr is kept apart: bd explains plain-text refusals ("cannot close blocked issue", "already open")
	// there, while stdout carries the JSON answer.
	var out, errOut []byte
	var exit int
	var err error
	if sr, ok := m.o.Runner.(core.SplitRunner); ok {
		out, errOut, exit, err = sr.RunSplit(ctx, m.Bin(), argv[1:]...)
	} else {
		out, exit, err = m.o.Runner.Run(ctx, m.Bin(), argv[1:]...)
	}
	if err != nil {
		if errors.Is(err, core.ErrTimedOut) || errors.Is(err, core.ErrCanceled) {
			return core.TaskResult{}, unknownOutcomeFor(argv[1:], m.TrackerDir())
		}
		return core.TaskResult{}, fmt.Errorf("bd write could not run: %s", core.Sanitize(err.Error()))
	}
	if exit < 0 { // killed by a signal: it may have written first
		return core.TaskResult{}, unknownOutcomeFor(argv[1:], m.TrackerDir())
	}
	// bd's own exit code for a precondition (--if-status) that no longer held: only an update carries one.
	// The verb is read after any -C, so a configured tracker directory does not hide it.
	if verb, _ := stripDir(argv[1:], m.TrackerDir()); exit == 13 && len(verb) > 0 && verb[0] == "update" {
		return core.TaskResult{}, core.ErrChanged
	}
	issue, bdErr := decodeIssue(out)
	if exit != 0 {
		if bdErr != "" {
			return core.TaskResult{}, errors.New("bd: " + core.Sanitize(bdErr))
		}
		if l := firstLine(errOut); l != "" {
			return core.TaskResult{}, fmt.Errorf("bd exited %d: %s", exit, l)
		}
		if l := firstLine(out); l != "" {
			return core.TaskResult{}, fmt.Errorf("bd exited %d: %s", exit, l)
		}
		return core.TaskResult{}, fmt.Errorf("bd exited %d", exit)
	}
	if bdErr != "" {
		return core.TaskResult{}, errors.New("bd: " + core.Sanitize(bdErr))
	}
	// A dependency and a comment answer with their own object, not an issue.
	if v, _ := stripDir(argv[1:], m.TrackerDir()); len(v) > 0 && (v[0] == "dep" || v[0] == "comment") {
		return edgeOrCommentResult(v, out)
	}
	// Exit 0 is not enough: the write counts only if bd returned the issue that was asked for.
	if issue.ID == "" {
		if v, _ := stripDir(argv[1:], m.TrackerDir()); len(v) > 0 && v[0] == "reopen" && strings.Contains(strings.ToLower(string(out)+string(errOut)), "already open") {
			return core.TaskResult{}, errors.New("already open: nothing to do")
		}
		return core.TaskResult{}, core.UnknownOutcome("bd wrote but returned no issue: check the tracker; refresh before retrying")
	}
	if want := requestedID(argv[1:], m.TrackerDir()); want != "" && issue.ID != want {
		return core.TaskResult{}, core.UnknownOutcome(fmt.Sprintf("bd returned an unexpected issue (%s, wanted %s): check the tracker; refresh before retrying", core.Sanitize(issue.ID), want))
	}
	// A status change counts only when the answer carries the status that was asked for, and a re-read
	// of the task agrees (a read that cannot be made does not undo a write bd itself confirmed).
	if want := wantedStatus(argv[1:], m.TrackerDir()); want != "" {
		if issue.Status != want {
			return core.TaskResult{}, core.AppliedMismatch(fmt.Sprintf("written, but bd answered with status %s, not %s: changed by someone else? check the tracker", core.Sanitize(issue.Status), want))
		}
		if t, err := m.ReadTask(ctx, issue.ID); err == nil && t.Status != want {
			return core.TaskResult{}, core.AppliedMismatch(fmt.Sprintf("written, but the status is now %s, not %s: changed by someone else?", core.Sanitize(t.Status), want))
		}
	}
	if err := checkEdit(argv[1:], m.TrackerDir(), issue); err != nil {
		return core.TaskResult{}, err
	}
	return core.TaskResult{ID: core.Sanitize(issue.ID), Title: core.Sanitize(issue.Title), Status: core.Sanitize(issue.Status)}, nil
}

// checkEdit verifies a label, assignee or priority write against bd's answer: exit 0 with an issue that
// does not show the edit is acknowledged but doubtful, so it is reported as written-but-different, never
// as "not applied".
func checkEdit(a []string, dir string, i issueJSON) error {
	a, _ = stripDir(a, dir)
	if len(a) == 0 || a[0] != "update" {
		return nil
	}
	for _, f := range a {
		switch {
		case strings.HasPrefix(f, "--add-label="):
			if !slices.Contains(i.Labels, strings.TrimPrefix(f, "--add-label=")) {
				return core.AppliedMismatch("written, but bd's answer does not list the label: check the task")
			}
		case strings.HasPrefix(f, "--remove-label="):
			if slices.Contains(i.Labels, strings.TrimPrefix(f, "--remove-label=")) {
				return core.AppliedMismatch("written, but bd's answer still lists the label: check the task")
			}
		case strings.HasPrefix(f, "--assignee="):
			if want := strings.TrimPrefix(f, "--assignee="); i.Assignee != want {
				return core.AppliedMismatch("written, but bd's answer shows another assignee: check the task")
			}
		case strings.HasPrefix(f, "--priority="):
			if want := strings.TrimPrefix(f, "--priority="); fmt.Sprint(i.Priority) != want {
				return core.AppliedMismatch("written, but bd's answer shows another priority: check the task")
			}
		}
	}
	return nil
}

// edgeOrCommentResult reads bd's answer to `dep add|remove` ({issue_id, depends_on_id, status}) or
// `comment` ({id, issue_id, text}) and holds it to the ids that were asked for.
func edgeOrCommentResult(a []string, out []byte) (core.TaskResult, error) {
	var o struct {
		ID        string `json:"id"`
		IssueID   string `json:"issue_id"`
		DependsOn string `json:"depends_on_id"`
		Status    string `json:"status"`
	}
	bad := core.UnknownOutcome("bd wrote but returned an unexpected answer: check the task; refresh before retrying")
	if json.Unmarshal(out, &o) != nil {
		return core.TaskResult{}, bad
	}
	if a[0] == "comment" {
		if o.IssueID != a[1] || o.ID == "" {
			return core.TaskResult{}, bad
		}
		return core.TaskResult{ID: core.Sanitize(o.IssueID), Status: "commented"}, nil
	}
	want := map[string]string{"add": "added", "remove": "removed"}[a[1]]
	if o.IssueID != a[2] || o.DependsOn != a[3] || o.Status != want {
		return core.TaskResult{}, bad
	}
	return core.TaskResult{ID: core.Sanitize(o.IssueID), Status: want}, nil
}

// wantedStatus is the status a write must leave the issue in: "" when the argv sets none.
func wantedStatus(a []string, dir string) string {
	a, _ = stripDir(a, dir)
	if len(a) == 0 {
		return ""
	}
	switch a[0] {
	case "close":
		return "closed"
	case "reopen":
		return "open"
	case "update":
		for _, f := range a {
			if v, ok := strings.CutPrefix(f, "--status="); ok {
				return v
			}
		}
	}
	return ""
}

// firstLine is the first non-empty line of bd's plain-text output, sanitized and cut to 120 characters
// ("" when there is none or it looks like JSON, which decodeIssue already handled).
func firstLine(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			return ""
		}
		l = strings.Join(strings.Fields(core.Sanitize(l)), " ")
		if l == "" {
			continue
		}
		if r := []rune(l); len(r) > 120 {
			l = string(r[:119]) + "…"
		}
		return l
	}
	return ""
}

func unknownOutcome() error {
	return core.UnknownOutcome("bd timed out; refresh before retrying")
}

// unknownOutcomeFor words the unknown outcome of a write: a comment is not idempotent, so a retry could
// post it twice.
func unknownOutcomeFor(a []string, dir string) error {
	if v, _ := stripDir(a, dir); len(v) > 0 && v[0] == "comment" {
		return core.UnknownOutcome("bd timed out; the comment may or may not have been added: check the task's comments before retrying")
	}
	return unknownOutcome()
}

// decodeIssue reads bd's `--json` answer: a bare issue, a one-element array, or an error
// object. Anything else yields an empty issue.
func decodeIssue(out []byte) (issueJSON, string) {
	var many []issueJSON
	if json.Unmarshal(out, &many) == nil {
		if len(many) == 1 {
			return many[0], ""
		}
		return issueJSON{}, ""
	}
	var o struct {
		issueJSON
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &o) != nil {
		return issueJSON{}, ""
	}
	return o.issueJSON, o.Error
}

// requestedID is the issue id an update or close names ("" for create).
func requestedID(a []string, dir string) string {
	a, _ = stripDir(a, dir)
	if len(a) >= 2 && (a[0] == "update" || a[0] == "close" || a[0] == "reopen") {
		return a[1]
	}
	return ""
}

// Export reads every issue, dependencies included, as JSONL (`bd --readonly export`, stdout only).
// The graph engine uses it when the tracker keeps no .beads/issues.jsonl, which is bd's default.
// Stdout is capped at ExportMaxOut: past it the error wraps core.ErrOutputTooLarge. A failure
// carries the tail of bd's stderr, sanitized, so Doctor can say why.
func (m *Module) Export(ctx context.Context) ([]byte, error) {
	args := m.dirArgs(readExport...)
	if !readAllowed(args, m.TrackerDir()) {
		return nil, fmt.Errorf("bd %q is not allowed: reads are fixed", args)
	}
	ctx, cancel := context.WithTimeout(ctx, exportTimeout)
	defer cancel()
	var out, errOut []byte
	var exit int
	var err error
	if sr, ok := m.o.ExportRunner.(core.SplitRunner); ok {
		out, errOut, exit, err = sr.RunSplit(ctx, m.Bin(), args...)
	} else {
		out, exit, err = m.o.ExportRunner.Run(ctx, m.Bin(), args...)
	}
	switch {
	case errors.Is(err, core.ErrOutputTooLarge) || len(out) >= ExportMaxOut:
		return nil, fmt.Errorf("bd export: %w", core.ErrOutputTooLarge)
	case errors.Is(err, core.ErrTimedOut):
		return nil, fmt.Errorf("bd timed out exporting the tracker: %w", core.ErrTimedOut)
	case err != nil:
		return nil, fmt.Errorf("bd export could not run: %s", core.Sanitize(err.Error()))
	case exit != 0:
		return nil, fmt.Errorf("bd export exited %d%s", exit, stderrTail(errOut))
	}
	return out, nil
}

// stderrTail is ": <last 200 characters of stderr>", sanitized, or "" when there is none.
func stderrTail(b []byte) string {
	s := strings.Join(strings.Fields(core.Sanitize(string(b))), " ")
	if s == "" {
		return ""
	}
	if r := []rune(s); len(r) > 200 {
		s = "..." + string(r[len(r)-200:])
	}
	return ": " + s
}

// ReadTask re-reads one issue (`bd --readonly show ID --json`) so a confirm screen can show
// what is there now. The id must match the one asked for.
func (m *Module) ReadTask(ctx context.Context, id string) (core.Task, error) {
	if !core.TaskIDRE.MatchString(id) {
		return core.Task{}, fmt.Errorf("invalid issue id %q", id)
	}
	out, exit, err := m.read(ctx, []string{"--readonly", "show", id, "--json"})
	if err != nil {
		return core.Task{}, fmt.Errorf("bd show could not run: %s", core.Sanitize(err.Error()))
	}
	if exit != 0 {
		return core.Task{}, fmt.Errorf("bd show exited %d: issue not found?", exit)
	}
	i, _ := decodeIssue(out)
	if i.ID != id {
		return core.Task{}, errors.New("bd show returned a different issue")
	}
	t := core.Task{ID: core.Sanitize(i.ID), Title: core.Sanitize(i.Title), Status: core.Sanitize(i.Status),
		Priority: i.Priority, Type: core.Sanitize(i.Type), Assignee: core.Sanitize(i.Assignee), UpdatedAt: core.Sanitize(i.UpdatedAt)}
	for _, l := range i.Labels {
		t.Labels = append(t.Labels, core.Sanitize(l))
	}
	for _, d := range i.Dependencies {
		if d.Type == "blocks" || d.Type == "conditional-blocks" {
			t.DependsOn = append(t.DependsOn, core.Sanitize(d.ID))
		}
	}
	return t, nil
}

// initTimeout is longer than a read: bd init creates a git repository and a database.
const initTimeout = 60 * time.Second

// runInit creates the managed workspace: the folder (private), then `bd init` run inside it,
// then a check that a tracker really appeared. Only then does Tasks switch to it and the
// config remember it. An init that may have run but cannot be confirmed is an unknown outcome.
func (m *Module) runInit(ctx context.Context, argv []string) (core.TaskResult, error) {
	dir := m.o.InitDir
	if err := m.writeBlocked(); err != nil {
		return core.TaskResult{}, err
	}
	if _, err := os.Stat(filepath.Join(dir, ".beads")); err == nil && !core.HasOwnTracker(dir) {
		return core.TaskResult{}, fmt.Errorf("half-created workspace at %s (a .beads folder without metadata.json): run `%s setup --repair` to set it aside when nothing else of yours is in that folder", dir, brand.Name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return core.TaskResult{}, fmt.Errorf("could not create %s: %s", dir, core.Sanitize(err.Error()))
	}
	run := core.Runner(core.ExecRunner{Dir: dir, Timeout: max(m.o.Timeout, initTimeout), Scrub: core.ScrubBeadsEnv})
	if m.o.InitRunner != nil {
		run = m.o.InitRunner(dir)
	}
	ctx, cancel := context.WithTimeout(ctx, max(m.o.Timeout, initTimeout))
	defer cancel()
	_, exit, err := run.Run(ctx, m.Bin(), argv...)
	if err != nil {
		if errors.Is(err, core.ErrTimedOut) || errors.Is(err, core.ErrCanceled) {
			return core.TaskResult{}, core.UnknownOutcome("bd init timed out; check " + dir + " before retrying")
		}
		return core.TaskResult{}, fmt.Errorf("bd init could not run: %s", core.Sanitize(err.Error()))
	}
	if exit != 0 {
		return core.TaskResult{}, fmt.Errorf("bd init exited %d: nothing was set up (see `bd init` in %s)", exit, dir)
	}
	if !core.HasOwnTracker(dir) {
		return core.TaskResult{}, errors.New("bd init exited 0 but no tracker appeared in " + dir)
	}
	if m.o.Ref != nil {
		m.o.Ref.Set(dir)
	} else {
		m.o.Dir = dir
	}
	m.o.InitDir = ""
	if m.o.OnInit != nil {
		if err := m.o.OnInit(dir); err != nil {
			return core.TaskResult{}, fmt.Errorf("the workspace was created in %s but could not be remembered (%s): run `abacus config set tracker_dir %s`", dir, core.Sanitize(err.Error()), dir)
		}
	}
	return core.TaskResult{ID: brand.Name, Title: "fleet workspace in " + dir, Status: "initialized"}, nil
}
