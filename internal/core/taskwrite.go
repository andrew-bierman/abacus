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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// TaskOp is a Tasks write verb. Delete is deliberately not offered.
type TaskOp string

const (
	TaskCreate TaskOp = "create"
	TaskUpdate TaskOp = "update"
	TaskClose  TaskOp = "close"
	// TaskReopen moves a closed task back to open (bd reopen). It is the only way out of closed.
	TaskReopen TaskOp = "reopen"
	// TaskInit creates the managed workspace (bd init). It is the one write that is not about
	// an existing issue, and it goes through the same plan, confirm, single-use and audit gate.
	TaskInit TaskOp = "init"

	// The edits beyond create, update and close. Each is one exact bd argv; none is a delete.
	TaskLabelAdd    TaskOp = "label-add"
	TaskLabelRemove TaskOp = "label-remove"
	TaskPriority    TaskOp = "priority"
	TaskAssign      TaskOp = "assign"
	TaskDepAdd      TaskOp = "dep-add"
	TaskDepRemove   TaskOp = "dep-remove"
	TaskAddComment  TaskOp = "comment"
)

// TaskChange is what a user asked for. Before is display-only (the current values, so the
// confirm screen can show old -> new); it never reaches an argv.
type TaskChange struct {
	Op          TaskOp
	ID          string
	Title       string
	Description string
	Status      string // update only; closing goes through TaskClose
	// IfStatus is the status the task must have right now for a Status change to apply: bd refuses
	// (exit 13) and writes nothing when another actor changed it first. A Status change requires it.
	IfStatus string
	Reason   string // close and reopen only
	Type     string // create only
	Dir      string // init only: the workspace directory (display; the provider owns the real one)
	Priority *int
	Before   Task
	// Expect is the status the task had when the change was planned. It never reaches an argv: a front
	// end re-reads the task at confirm time and refuses the change when the status is no longer this
	// (close and reopen have no bd-side guard, so this check is their only protection against a move
	// made by someone else in between).
	Expect string
	// Warning is display-only, one line on the confirm screen (a card moved while it is blocked).
	Warning string

	// Label is the label a label-add or label-remove names.
	Label string
	// Assignee is who an assign hands the task to: nil is not an assign, "" unassigns. IfAssignee is who
	// holds it now ("" is nobody): bd refuses (exit 13) and writes nothing when someone else changed it.
	Assignee   *string
	IfAssignee string
	// Blocker is the task a dep-add or dep-remove says holds ID back (ID is the held task). BlockerTitle is
	// display-only.
	Blocker      string
	BlockerTitle string
	// Text is the comment a comment adds.
	Text string
}

// TaskStatuses are the statuses an update may set; closed is only reachable through close.
var TaskStatuses = []string{"open", "in_progress", "blocked", "deferred"}

// TaskTypes are the issue types a create may use.
var TaskTypes = []string{"bug", "feature", "task", "epic", "chore", "decision", "spike", "story", "milestone"}

// StatusGuardRE is the only shape of status a guard (--if-status) may carry: bd's own status names
// are lower case words with underscores, and a custom status follows the same rule.
var StatusGuardRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// TaskIDRE is the only shape of issue ID that ever reaches an argv.
var TaskIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// LabelRE is the only shape of label that reaches an argv: no comma (bd would split it into several),
// no space and nothing a flag parser could read as an option.
var LabelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._/-]{0,63}$`)

// MaxCommentBytes bounds one comment (it is one argv element).
const MaxCommentBytes = 8000

// CommentTextOK is whether text may reach an argv as a comment: non-blank, valid, at most
// MaxCommentBytes, and no control character but a newline or tab.
func CommentTextOK(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= MaxCommentBytes && utf8.ValidString(s) && checkText("comment", s, true) == nil &&
		!strings.ContainsFunc(s, hiddenRune)
}

// AssigneeOK is whether a name may reach an argv as an assignee or an assignee guard: "" (nobody) or
// printable text of at most 128 bytes with no edge space.
func AssigneeOK(s string) bool {
	return len(s) <= 128 && utf8.ValidString(s) && strings.TrimSpace(s) == s &&
		!strings.ContainsFunc(s, func(r rune) bool { return !strconv.IsPrint(r) || hiddenRune(r) })
}

// hiddenRune is a rune that draws nothing or reorders what is around it: format characters (zero-width,
// bidi controls, BOM, soft hyphen) and the line and paragraph separators.
func hiddenRune(r rune) bool { return unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 }

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
	// ErrChanged means the task no longer had the status the change was planned against: bd refused
	// (exit 13) and nothing was written.
	ErrChanged = errors.New("changed since you opened it: nothing was written; refresh and plan the change again")
	// ErrOutcomeUnknown means the write may or may not have happened (timeout, cancel, killed).
	// Never report it as "not applied": the user must refresh before retrying.
	ErrOutcomeUnknown = errors.New("result unknown")
)

type unknownOutcomeError struct{ msg string }

func (e unknownOutcomeError) Error() string        { return "result unknown: " + e.msg }
func (e unknownOutcomeError) Is(target error) bool { return target == ErrOutcomeUnknown }

// ErrAppliedMismatch means the tracker confirmed the write but the task is not in the status that was
// asked for (someone else moved it again): it landed, so it is neither "not applied" nor an error.
var ErrAppliedMismatch = errors.New("written, but the status differs")

type appliedMismatchError struct{ msg string }

func (e appliedMismatchError) Error() string        { return e.msg }
func (e appliedMismatchError) Is(target error) bool { return target == ErrAppliedMismatch }

// AppliedMismatch is the error for a write that landed but whose result is not the one asked for.
func AppliedMismatch(msg string) error { return appliedMismatchError{msg} }

// UnknownOutcome is the error for a write that may or may not have happened.
func UnknownOutcome(msg string) error { return unknownOutcomeError{msg} }

// DescribeWriteError is the one wording every front-end shows for a failed write: an unknown
// outcome is never called "not applied".
func DescribeWriteError(err error) string {
	if errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrAppliedMismatch) {
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
		switch {
		case ch.Status == "" && ch.IfStatus != "":
			return errors.New("a status guard needs a status change")
		case ch.Status != "" && ch.IfStatus == "":
			return errors.New("a status change needs the status the task has now (so a change by someone else is refused)")
		case ch.Status != "" && !StatusGuardRE.MatchString(ch.IfStatus):
			return fmt.Errorf("the task's status %q is not a plain lower-case word (letters, digits, _ and -, up to 32 characters), so it cannot be used as the guard of a status change", ch.IfStatus)
		case ch.IfStatus == "closed":
			return errors.New("closed work leaves closed by reopening it, not by a status update")
		case ch.Status != "" && ch.IfStatus == ch.Status:
			return fmt.Errorf("the task is already %s", ch.Status)
		}
	case TaskClose, TaskReopen:
	case TaskLabelAdd, TaskLabelRemove:
		if !LabelRE.MatchString(ch.Label) {
			return fmt.Errorf("invalid label %q (letters, digits and : . _ / -, up to 64 characters, no spaces or commas)", ch.Label)
		}
	case TaskPriority:
		if ch.Priority == nil {
			return errors.New("a priority is required")
		}
		if ch.Before.ID != "" && ch.Before.Priority == *ch.Priority {
			return fmt.Errorf("%s is already priority %d", ch.ID, *ch.Priority)
		}
	case TaskAssign:
		switch {
		case ch.Assignee == nil:
			return errors.New("an assignee is required (empty to unassign)")
		case !AssigneeOK(*ch.Assignee):
			return errors.New("invalid assignee name")
		case !AssigneeOK(ch.IfAssignee):
			return errors.New("the current assignee is not a plain name, so it cannot be used as the guard of an assignment")
		case *ch.Assignee == ch.IfAssignee && ch.IfAssignee == "":
			return fmt.Errorf("%s is already unassigned", ch.ID)
		case *ch.Assignee == ch.IfAssignee:
			return fmt.Errorf("%s is already assigned to %s", ch.ID, ch.IfAssignee)
		}
	case TaskDepAdd, TaskDepRemove:
		switch {
		case !TaskIDRE.MatchString(ch.Blocker):
			return fmt.Errorf("invalid issue id %q", ch.Blocker)
		case ch.Blocker == ch.ID:
			return errors.New("a task cannot depend on itself")
		}
	case TaskAddComment:
		switch {
		case strings.TrimSpace(ch.Text) == "":
			return errors.New("a comment needs text")
		case !CommentTextOK(ch.Text):
			return fmt.Errorf("a comment is at most %d bytes of text with no control characters", MaxCommentBytes)
		}
	case TaskInit:
		return checkText("directory", ch.Dir, false)
	default:
		return fmt.Errorf("unsupported operation %q", ch.Op)
	}
	if ch.Op != TaskCreate && !TaskIDRE.MatchString(ch.ID) {
		return fmt.Errorf("invalid issue id %q", ch.ID)
	}
	if ch.Op != TaskDepAdd && ch.Op != TaskDepRemove && ch.Blocker != "" {
		return errors.New("a blocker belongs to a dependency change only")
	}
	if ch.Priority != nil && (*ch.Priority < 0 || *ch.Priority > 4) {
		return errors.New("priority must be 0-4")
	}
	for label, v := range map[string]string{"title": ch.Title, "reason": ch.Reason, "warning": ch.Warning, "blocker title": ch.BlockerTitle} {
		if err := checkText(label, v, false); err != nil {
			return err
		}
	}
	if err := checkText("text", ch.Text, true); err != nil {
		return err
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
			old := ch.Before.Status
			if old == "" {
				old = ch.IfStatus
			}
			out = append(out, field("status", old, ch.Status))
		}
		if ch.Warning != "" {
			out = append(out, "warning: "+ch.Warning)
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
	if out, ok := describeEdit(ch, field); ok {
		return out
	}
	verb, to := "close", "closed"
	if ch.Op == TaskReopen {
		verb, to = "reopen", "open"
	}
	out := []string{verb + " task " + ch.ID}
	if ch.Before.Title != "" {
		out = append(out, field("title", "", ch.Before.Title))
	}
	if ch.Before.Status != "" {
		out = append(out, field("status", ch.Before.Status, to))
	}
	if ch.Warning != "" {
		out = append(out, "warning: "+ch.Warning)
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
	// Batch is the id shared by every entry of one bulk change (additive; empty for a single write).
	Batch string `json:"batch,omitempty"`
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
	d, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return "", errors.New("audit log: no home directory to keep it in")
	}
	return filepath.Join(d, "audit.jsonl"), nil
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

// NewApplier returns an applier that writes through w and records every attempt in a.
func NewApplier(w TaskWriter, a Audit) *Applier { return &Applier{w: w, g: newGate(a)} }

// Apply runs a confirmed task change once. A timeout or cancel is reported as an unknown result, never as not applied.
func (a *Applier) Apply(ctx context.Context, p WritePlan) (TaskResult, error) {
	res, _, err := a.apply(ctx, p, "")
	return res, err
}

// apply is Apply plus whether the write was handed to the provider (false: refused before it, so an
// error is the confirm check, a reused plan or the audit log) and the batch id for the audit line.
func (a *Applier) apply(ctx context.Context, p WritePlan, batch string) (TaskResult, bool, error) {
	if !p.confirmed {
		return TaskResult{}, false, ErrNotConfirmed
	}
	e := AuditEntry{Domain: string(DomainTasks), Verb: string(p.Change.Op), Argv: p.Argv, Tracker: a.w.TrackerDir(), Batch: batch}
	var res TaskResult
	ran := false
	err := a.g.do(p.id, e, func() error {
		var err error
		ran = true
		res, err = a.w.RunTask(ctx, p.Argv)
		return err
	})
	return res, ran, err
}

// DisplayArgv renders an argv for a person: arguments with spaces or quotes are quoted, and so is
// any argument holding a rune that is not printable (a bidi override, a zero-width joiner, a
// control character), which %q writes as an escape. The command a person reads is then the argv
// that runs: Sanitize has nothing left to strip from it.
func DisplayArgv(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`") || !utf8.ValidString(a) || strings.ContainsFunc(a, func(r rune) bool { return !strconv.IsPrint(r) }) {
			a = fmt.Sprintf("%q", a)
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// describeEdit is the confirm text of the edits beyond create, update and close.
func describeEdit(ch TaskChange, field func(name, old, nw string) string) ([]string, bool) {
	head := func(l string) []string {
		out := []string{l}
		if ch.Before.Title != "" {
			out = append(out, field("title", "", ch.Before.Title))
		}
		return out
	}
	var out []string
	switch ch.Op {
	case TaskLabelAdd:
		out = append(head("add a label to task "+ch.ID), field("label", "", ch.Label))
	case TaskLabelRemove:
		out = append(head("remove a label from task "+ch.ID), field("label", "", ch.Label))
	case TaskPriority:
		out = head("set the priority of task " + ch.ID)
		old := ""
		if ch.Before.ID != "" {
			old = fmt.Sprint(ch.Before.Priority)
		}
		out = append(out, field("priority", old, fmt.Sprint(*ch.Priority)))
	case TaskAssign:
		verb := "assign task " + ch.ID
		if ch.Assignee != nil && *ch.Assignee == "" {
			verb = "unassign task " + ch.ID
		}
		out = head(verb)
		who := func(s string) string {
			if s == "" {
				return "nobody"
			}
			return s
		}
		if ch.Assignee != nil {
			out = append(out, field("assignee", who(ch.IfAssignee), who(*ch.Assignee)))
		}
	case TaskDepAdd, TaskDepRemove:
		verb := "make task " + ch.ID + " wait for " + ch.Blocker
		if ch.Op == TaskDepRemove {
			verb = "stop task " + ch.ID + " waiting for " + ch.Blocker
		}
		out = head(verb)
		if ch.BlockerTitle != "" {
			out = append(out, field("blocker", "", ch.Blocker+" "+ch.BlockerTitle))
		}
	case TaskAddComment:
		out = append(head("add a comment to task "+ch.ID), field("comment", "", ch.Text))
	default:
		return nil, false
	}
	if ch.Warning != "" {
		out = append(out, "warning: "+ch.Warning)
	}
	return out, true
}

// NeedsFresh is whether a change must be checked against a re-read of its task right before it is
// confirmed and again right before it runs: every edit beyond create, update and close. Where bd offers
// a guard (assign) the argv carries it as well; the rest have none, so this check is their protection.
func NeedsFresh(op TaskOp) bool {
	switch op {
	case TaskLabelAdd, TaskLabelRemove, TaskPriority, TaskAssign, TaskDepAdd, TaskDepRemove, TaskAddComment:
		return true
	}
	return false
}

// ErrCannotVerify means the task could not be re-read, so a change that needs the check was not run.
var ErrCannotVerify = errors.New("could not verify the task's current state; nothing was written")

type staleError struct{ msg string }

func (e staleError) Error() string {
	return e.msg + ": nothing was written; refresh and plan the change again"
}
func (e staleError) Is(target error) bool { return target == ErrChanged }

// CheckFresh compares a change with the task as it is now. It returns an error (that is ErrChanged)
// when the change no longer fits: someone else moved the field it was planned against, or the change
// would now be a no-op (the label is already there, the edge already exists).
func CheckFresh(ch TaskChange, cur Task) error {
	has := func(l []string, v string) bool { return slices.Contains(l, v) }
	switch ch.Op {
	case TaskLabelAdd:
		if has(cur.Labels, ch.Label) {
			return staleError{fmt.Sprintf("%s already has the label %s", ch.ID, ch.Label)}
		}
	case TaskLabelRemove:
		if !has(cur.Labels, ch.Label) {
			return staleError{fmt.Sprintf("%s no longer has the label %s", ch.ID, ch.Label)}
		}
	case TaskPriority:
		if cur.Priority == *ch.Priority {
			return staleError{fmt.Sprintf("%s is already priority %d", ch.ID, cur.Priority)}
		}
		if ch.Before.ID != "" && cur.Priority != ch.Before.Priority {
			return staleError{fmt.Sprintf("%s is now priority %d (it was %d)", ch.ID, cur.Priority, ch.Before.Priority)}
		}
	case TaskUpdate:
		if ch.Status != "" && cur.Status == ch.Status {
			return staleError{fmt.Sprintf("%s is already %s", ch.ID, ch.Status)}
		}
	case TaskAssign:
		if ch.Assignee != nil && cur.Assignee == *ch.Assignee {
			return staleError{fmt.Sprintf("%s is already assigned to %q", ch.ID, cur.Assignee)}
		}
		if cur.Assignee != ch.IfAssignee {
			return staleError{fmt.Sprintf("%s is now held by %q (it was %q)", ch.ID, cur.Assignee, ch.IfAssignee)}
		}
	case TaskDepAdd:
		if has(cur.DependsOn, ch.Blocker) {
			return staleError{fmt.Sprintf("%s already waits for %s", ch.ID, ch.Blocker)}
		}
	case TaskDepRemove:
		if !has(cur.DependsOn, ch.Blocker) {
			return staleError{fmt.Sprintf("%s no longer waits for %s", ch.ID, ch.Blocker)}
		}
	}
	return nil
}

// DepRecheck re-runs a dependency edit's checks (existing edge, cycle, unknown task) against a fresh read
// of the whole tracker; it returns an error that is ErrChanged for a change that no longer fits, or
// ErrCannotVerify when the tracker cannot be read.
type DepRecheck func(ctx context.Context, ch TaskChange) error

// Stale is the error for a change that no longer fits the tracker (it is ErrChanged).
func Stale(msg string) error { return staleError{msg} }

// FreshDep is Fresh plus, for a dependency edit, the whole-tracker recheck: a cycle made between the
// plan and the write is refused here, the tracker's own refusal being only the backstop. A dependency edit
// without a recheck is refused.
func FreshDep(ctx context.Context, r TaskReader, ch TaskChange, recheck DepRecheck) error {
	if err := Fresh(ctx, r, ch); err != nil {
		return err
	}
	if ch.Op != TaskDepAdd && ch.Op != TaskDepRemove {
		return nil
	}
	if recheck == nil {
		return ErrCannotVerify
	}
	return recheck(ctx, ch)
}

// Fresh re-reads the task and runs CheckFresh: the check front ends make after the confirm and right
// before the write. Changes that need no check pass untouched. A task that cannot be read refuses the
// change (ErrCannotVerify): it is never applied unchecked.
func Fresh(ctx context.Context, r TaskReader, ch TaskChange) error {
	if !NeedsFresh(ch.Op) {
		return nil
	}
	if r == nil {
		return ErrCannotVerify
	}
	cur, err := r.ReadTask(ctx, ch.ID)
	if err != nil {
		return fmt.Errorf("%w (%s)", ErrCannotVerify, Sanitize(err.Error()))
	}
	return CheckFresh(ch, cur)
}
