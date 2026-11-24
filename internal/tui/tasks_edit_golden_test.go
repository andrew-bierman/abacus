package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// editScenarios are the goldens of the edit forms and their confirm screens, over the demo tracker. The
// selected task is the first row of the Tasks list (ac-12).
func editScenarios() []scenario {
	std := func(t *testing.T, th Theme) tea.Model { return load(t, th, nil) }
	verified := func(keys []string, mod func(*core.Task)) func(*testing.T, Theme) tea.Model {
		return func(t *testing.T, th Theme) tea.Model {
			m := load(t, th, nil)
			for _, k := range keys {
				m = press(m, k)
			}
			task := baseTask("ac-12")
			if mod != nil {
				mod(&task)
			}
			m, _ = m.Update(taskVerifiedMsg{id: "ac-12", task: task})
			return m
		}
	}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	return []scenario{
		{name: "tasks-label-form", build: std, keys: cat([]string{"2", "t"}, typed("bug")), tab: "Tasks", extra: framedSizes},
		{name: "tasks-assign-form", build: std, keys: []string{"2", "a"}, tab: "Tasks", extra: framedSizes},
		{name: "tasks-dep-cycle", build: std, keys: cat([]string{"2", "b"}, typed("ac-09"), []string{"enter"}), tab: "Tasks", extra: framedSizes},
		{name: "tasks-label-confirm", build: verified(cat([]string{"2", "t"}, typed("bug"), []string{"enter"}), nil), tab: "Tasks", extra: framedSizes},
		{name: "tasks-assign-confirm", build: verified(cat([]string{"2", "a", "backspace", "backspace", "backspace"}, typed("mei"), []string{"enter"}), nil), tab: "Tasks", extra: framedSizes},
		{name: "tasks-dep-confirm", build: verified(cat([]string{"2", "b"}, typed("ac-30"), []string{"enter"}), nil), tab: "Tasks", extra: framedSizes},
		{name: "tasks-comment-confirm", build: verified(cat([]string{"2", "c"}, typed("-x looks fine"), []string{"enter"}), nil), tab: "Tasks", extra: framedSizes},
		{name: "tasks-edit-stale", build: verified(cat([]string{"2", "a", "backspace", "backspace", "backspace"}, typed("mei"), []string{"enter"}), func(t *core.Task) { t.Assignee = "eve" }), tab: "Tasks", extra: framedSizes},
	}
}
