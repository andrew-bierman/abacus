package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The edits beyond create, edit and close (labels, assignee, priority, blockers, a comment) each open
// a one-field form and then the same confirm screen as every other write: the task is re-read before it
// opens and once more right before the write, and the exact command is shown. The prompt (:) can
// describe these keys but never runs one.

func editTitle(op core.TaskOp, id string) string {
	switch op {
	case core.TaskCreate:
		return "New task"
	case core.TaskUpdate:
		return "Edit task " + id
	case core.TaskLabelAdd:
		return "Add a label to " + id
	case core.TaskLabelRemove:
		return "Remove a label from " + id
	case core.TaskAssign:
		return "Assign " + id
	case core.TaskPriority:
		return "Set the priority of " + id
	case core.TaskDepAdd:
		return "Add a blocker to " + id
	case core.TaskDepRemove:
		return "Remove a blocker from " + id
	case core.TaskAddComment:
		return "Comment on " + id
	}
	return ""
}

// openEdit opens the form of the edit key k on the selected task.
func (s *tasksSection) openEdit(k tea.KeyMsg) {
	t, ok := s.current()
	switch {
	case s.applier == nil:
		s.notice = "writes are not available: no task provider can write"
		return
	case !ok:
		s.notice = "no task selected"
		return
	}
	var labels []string
	if r := s.rows(); s.cursor < len(r) {
		labels = r[s.cursor].Labels
	}
	f := formField{}
	switch k.String() {
	case "t":
		s.op, f.label = core.TaskLabelAdd, "Label"
	case "u":
		s.op, f.label = core.TaskLabelRemove, "Label"
		if len(labels) == 1 {
			f.value = labels[0]
		}
	case "a":
		s.op, f.label, f.value = core.TaskAssign, "Assignee (blank: unassign)", t.Assignee
	case "p":
		s.op, f.label, f.value = core.TaskPriority, "Priority 0-4", fmt.Sprint(t.Priority)
	case "b":
		s.op, f.label = core.TaskDepAdd, "Waits for (task id)"
	case "B":
		s.op, f.label = core.TaskDepRemove, "No longer waits for (task id)"
	default:
		s.op, f.label = core.TaskAddComment, "Comment"
	}
	s.target, s.fields, s.fi, s.formErr, s.mode = t, []formField{f}, 0, "", modeForm
}

// submitEdit turns the one-field form of an edit into a change and moves toward the confirm screen.
// Nothing is written here.
func (s *tasksSection) submitEdit() tea.Cmd {
	t, v := s.target, strings.TrimSpace(s.fields[0].value)
	ch := core.TaskChange{Op: s.op, ID: t.ID, Before: t, Expect: t.Status}
	switch s.op {
	case core.TaskLabelAdd, core.TaskLabelRemove:
		ch.Label = v
	case core.TaskPriority:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || fmt.Sprint(n) != v {
			s.formErr = "priority must be a number 0-4"
			return nil
		}
		ch.Priority = &n
	case core.TaskAssign:
		ch.Assignee, ch.IfAssignee = &v, t.Assignee
	case core.TaskDepAdd, core.TaskDepRemove:
		if s.list.view == nil {
			s.formErr = "the tracker has not loaded yet"
			return nil
		}
		var err error
		if ch, err = s.list.view.PlanDep(s.op == core.TaskDepAdd, t.ID, v); err != nil {
			s.formErr = core.Sanitize(err.Error())
			return nil
		}
	case core.TaskAddComment:
		ch.Text = v
	}
	return s.beginConfirm(ch)
}

// appliedNotice is the one line after a write: what was done to which task.
func appliedNotice(m taskAppliedMsg) string {
	switch m.op {
	case core.TaskDepAdd:
		return fmt.Sprintf("%s now waits for %s", san(m.res.ID), san(m.ch.Blocker))
	case core.TaskDepRemove:
		return fmt.Sprintf("%s no longer waits for %s", san(m.res.ID), san(m.ch.Blocker))
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s", opVerb(m.op), san(m.res.ID), san(m.res.Title)))
}
