package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/andrew-bierman/abacus/internal/brand"
)

// TaskOp is a Tasks write verb. Delete is deliberately not offered.
type TaskOp string

const (
	TaskCreate TaskOp = "create"
	TaskUpdate TaskOp = "update"
	TaskClose  TaskOp = "close"
	// TaskInit creates the managed workspace (bd init). It is the one write that is not about
	// an existing issue, and it goes through the same plan, confirm, single-use and audit gate.
	TaskInit TaskOp = "init"
)

// TaskChange is what a user asked for. Before is display-only (the current values, so the
// confirm screen can show old -> new); it never reaches an argv.
type TaskChange struct {
	Op          TaskOp
	ID          string
	Title       string
	Description string
	Status      string // update only; closing goes through TaskClose
	Reason      string // close only
	Type        string // create only
	Dir         string // init only: the workspace directory (display; the provider owns the real one)
	Priority    *int
	Before      Task
}

// TaskStatuses are the statuses an update may set; closed is only reachable through close.
var TaskStatuses = []string{"open", "in_progress", "blocked", "deferred"}

// TaskTypes are the issue types a create may use.
var TaskTypes = []string{"bug", "feature", "task", "epic", "chore", "decision", "spike", "story", "milestone"}

// TaskIDRE is the only shape of issue ID that ever reaches an argv.
var TaskIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// TaskResult is the issue a write returned.
type TaskResult struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// TaskWriter is what a Tasks provider offers for writes. BuildTaskArgv must refuse anything
// outside the provider's write allowlist, and RunTask must re-check it before running.
type TaskWriter interface {
	BuildTaskArgv(TaskChange) ([]string, error)
	RunTask(ctx context.Context, argv []string) (TaskResult, error)
	TrackerDir() string
}

// InitDirer is implemented by a TaskWriter that can create the managed workspace, and says
// where.
type InitDirer interface{ InitDir() string }

var (
	ErrNotConfirmed   = errors.New("change was not confirmed")
	ErrAlreadyApplied = errors.New("this change was already applied")
	// ErrOutcomeUnknown means the write may or may not have happened (timeout, cancel, killed).
	// Never report it as "not applied": the user must refresh before retrying.
	ErrOutcomeUnknown = errors.New("result unknown")
)

type unknownOutcomeError struct{ msg string }

func (e unknownOutcomeError) Error() string        { return "result unknown: " + e.msg }
func (e unknownOutcomeError) Is(target error) bool { return target == ErrOutcomeUnknown }

// UnknownOutcome is the error for a write that may or may not have happened.
func UnknownOutcome(msg string) error { return unknownOutcomeError{msg} }

// DescribeWriteError is the one wording every front-end shows for a failed write: an unknown
// outcome is never called "not applied".
func DescribeWriteError(err error) string {
	if errors.Is(err, ErrOutcomeUnknown) {
		return Sanitize(err.Error())
	}
	return "not applied: " + Sanitize(err.Error())
}

var planSeq atomic.Uint64

// WritePlan is a validated change with its exact argv and a human-readable description.
// It cannot be applied until Confirm is called (the user's explicit yes).
type WritePlan struct {
	id        uint64
	Change    TaskChange
	Argv      []string
	Lines     []string
	confirmed bool
}

// Confirm marks the plan as explicitly approved by the user.
func (p WritePlan) Confirm() WritePlan { p.confirmed = true; return p }

func checkText(label, s string, multiline bool) error {
	for _, r := range s {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t')) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func validate(ch TaskChange) error {
	switch ch.Op {
	case TaskCreate:
		if strings.TrimSpace(ch.Title) == "" {
			return errors.New("a title is required")
		}
		if ch.Type != "" && !slices.Contains(TaskTypes, ch.Type) {
			return fmt.Errorf("unknown type %q", ch.Type)
		}
	case TaskUpdate:
		for label, v := range map[string]string{"title": ch.Title, "description": ch.Description} {
			if v != "" && strings.TrimSpace(v) == "" {
				return fmt.Errorf("%s must not be blank", label)
			}
		}
		if ch.Title == "" && ch.Description == "" && ch.Status == "" && ch.Priority == nil {
			return errors.New("nothing to change")
		}
		if ch.Status != "" && !slices.Contains(TaskStatuses, ch.Status) {
			return fmt.Errorf("status must be one of %s (use close to close)", strings.Join(TaskStatuses, ", "))
		}
	case TaskClose:
	case TaskInit:
		return checkText("directory", ch.Dir, false)
	default:
		return fmt.Errorf("unsupported operation %q", ch.Op)
	}
	if ch.Op != TaskCreate && !TaskIDRE.MatchString(ch.ID) {
		return fmt.Errorf("invalid issue id %q", ch.ID)
	}
	if ch.Priority != nil && (*ch.Priority < 0 || *ch.Priority > 4) {
		return errors.New("priority must be 0-4")
	}
	for label, v := range map[string]string{"title": ch.Title, "reason": ch.Reason} {
		if err := checkText(label, v, false); err != nil {
			return err
		}
	}
	return checkText("description", ch.Description, true)
}

// PlanTaskChange validates a change and builds its argv and description without side effects.
func PlanTaskChange(w TaskWriter, ch TaskChange) (WritePlan, error) {
	if err := validate(ch); err != nil {
		return WritePlan{}, err
	}
	argv, err := w.BuildTaskArgv(ch)
	if err != nil {
		return WritePlan{}, err
	}
	return WritePlan{id: planSeq.Add(1), Change: ch, Argv: argv, Lines: describe(ch)}, nil
}

func describe(ch TaskChange) []string {
	field := func(name, old, nw string) string {
		if old != "" && old != nw {
			return fmt.Sprintf("%s: %q -> %q", name, old, nw)
		}
		return fmt.Sprintf("%s: %q", name, nw)
	}
	switch ch.Op {
	case TaskInit:
		return []string{"create your fleet workspace", "folder: " + ch.Dir, "this creates a local task tracker (a git repository with a task database) in that folder; nothing else on this machine is changed"}
	case TaskCreate:
		out := []string{"create a new task", field("title", "", ch.Title)}
		if ch.Description != "" {
			out = append(out, field("description", "", ch.Description))
		}
		if ch.Priority != nil {
			out = append(out, fmt.Sprintf("priority: %d", *ch.Priority))
		}
		if ch.Type != "" {
			out = append(out, "type: "+ch.Type)
		}
		return out
	case TaskUpdate:
		out := []string{"update task " + ch.ID}
		if ch.Title != "" {
			out = append(out, field("title", ch.Before.Title, ch.Title))
		}
		if ch.Description != "" {
			out = append(out, field("description", "", ch.Description))
		}
		if ch.Status != "" {
			out = append(out, field("status", ch.Before.Status, ch.Status))
		}
		if ch.Priority != nil {
			old := ""
			if ch.Before.ID != "" {
				old = fmt.Sprint(ch.Before.Priority)
			}
			out = append(out, field("priority", old, fmt.Sprint(*ch.Priority)))
		}
		return out
	}
	out := []string{"close task " + ch.ID}
	if ch.Before.Title != "" {
		out = append(out, field("title", "", ch.Before.Title))
	}
	if ch.Reason != "" {
		out = append(out, field("reason", "", ch.Reason))
	}
	return out
}

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	Time    time.Time `json:"time"`
	Domain  string    `json:"domain"`
	Verb    string    `json:"verb"`
	Phase   string    `json:"phase"` // attempt, ok, error, unknown
	Argv    []string  `json:"argv"`
	Tracker string    `json:"tracker,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Audit records mutations. A failed Record blocks the mutation (fail closed).
type Audit interface{ Record(AuditEntry) error }

type fileAudit struct {
	mu   sync.Mutex
	path string
}

// FileAudit appends JSON lines to path, creating its directory.
func FileAudit(path string) Audit { return &fileAudit{path: path} }

// DefaultAuditPath is $XDG_STATE_HOME/abacus/audit.jsonl (only when absolute) or
// ~/.local/state/abacus/audit.jsonl. Without a usable home it is an error, never a relative path.
func DefaultAuditPath() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, brand.Name, "audit.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("audit log: no home directory to keep it in")
	}
	return filepath.Join(home, ".local", "state", brand.Name, "audit.jsonl"), nil
}

type failAudit struct{ err error }

func (f failAudit) Record(AuditEntry) error { return f.err }

// DefaultAudit is the file audit at DefaultAuditPath; if no path can be resolved every Record
// fails, so Apply blocks the write.
func DefaultAudit() Audit {
	p, err := DefaultAuditPath()
	if err != nil {
		return failAudit{err}
	}
	return FileAudit(p)
}

func (f *fileAudit) Record(e AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !filepath.IsAbs(f.path) {
		return errors.New("audit log: path is empty or not absolute")
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	defer fh.Close()
	b, _ := json.Marshal(e)
	_, err = fh.Write(append(b, '\n'))
	return err
}

// Applier is the one door to a Tasks write: it enforces the confirm step, then hands the
// plan to the shared gate (single use, audit) and the argv to the provider.
type Applier struct {
	w TaskWriter
	g *gate
}

func NewApplier(w TaskWriter, a Audit) *Applier { return &Applier{w: w, g: newGate(a)} }

func (a *Applier) Apply(ctx context.Context, p WritePlan) (TaskResult, error) {
	if !p.confirmed {
		return TaskResult{}, ErrNotConfirmed
	}
	e := AuditEntry{Domain: string(DomainTasks), Verb: string(p.Change.Op), Argv: p.Argv, Tracker: a.w.TrackerDir()}
	var res TaskResult
	err := a.g.do(p.id, e, func() error {
		var err error
		res, err = a.w.RunTask(ctx, p.Argv)
		return err
	})
	return res, err
}

// DisplayArgv renders an argv for a person: arguments with spaces or quotes are quoted.
func DisplayArgv(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`") {
			a = fmt.Sprintf("%q", a)
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}
