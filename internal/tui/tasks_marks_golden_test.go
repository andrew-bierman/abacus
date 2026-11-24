package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// marksScenarios are the goldens of the marks and the bulk screens (over the demo tracker, whose open list
// starts with ac-15, ac-12, ac-20, ac-01, ...) and of the Insights view's states.
func marksScenarios() []scenario {
	std := func(t *testing.T, th Theme) tea.Model { return load(t, th, nil) }
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	rep := func(items ...core.BulkItemResult) core.BulkReport {
		r := core.BulkReport{SchemaVersion: 1, BatchID: "batch-0a1b2c3d4e5f", Op: "close"}
		for _, it := range items {
			switch it.Outcome {
			case core.BulkApplied:
				r.Applied++
			case core.BulkSkipped:
				r.Skipped++
			case core.BulkFailed:
				r.Failed++
			}
			r.Items = append(r.Items, it)
		}
		return r
	}
	marks3 := []string{"2", " ", " ", " "}
	return []scenario{
		{name: "tasks-marked", build: std, keys: marks3, tab: "Tasks", extra: framedSizes},
		{name: "tasks-bulk-form", build: std, keys: cat(marks3, []string{"p"}), tab: "Tasks", extra: framedSizes},
		{name: "tasks-bulk-form-error", build: std, keys: cat(marks3, []string{"p", "9", "enter"}), tab: "Tasks", extra: framedSizes},
		{name: "tasks-bulk-confirm", build: std, keys: cat(marks3, []string{"x"}), tab: "Tasks", extra: framedSizes},
		{name: "tasks-bulk-confirm-assign", build: std, keys: cat(marks3, []string{"a"}, typed("mei"), []string{"enter"}), tab: "Tasks", extra: framedSizes},
		{name: "tasks-bulk-confirm-long", build: std, keys: cat([]string{"2"}, []string{" ", " ", " ", " ", " ", " ", " ", " ", " ", " ", "x"}), tab: "Tasks", extra: framedSizes},
		{name: "tasks-bulk-confirm-scrolled", build: std, keys: cat([]string{"2"}, []string{" ", " ", " ", " ", " ", " ", " ", " ", " ", " ", "x", "pgdown", "pgdown", "pgdown"}), tab: "Tasks", extra: framedSizes},
		{name: "tasks-bulk-busy", tab: "Tasks", extra: framedSizes, build: func(t *testing.T, th Theme) tea.Model {
			m := load(t, th, nil)
			for _, k := range cat(marks3, []string{"x", "y"}) { // the apply command is never run: the screen stays on "applying"
				m = press(m, k)
			}
			return m
		}},
		{name: "tasks-bulk-result", tab: "Tasks", extra: framedSizes, build: func(t *testing.T, th Theme) tea.Model {
			m := load(t, th, nil)
			for _, k := range cat(marks3, []string{"x"}) {
				m = press(m, k)
			}
			m, _ = m.Update(bulkAppliedMsg{rep: rep(
				core.BulkItemResult{ID: "ac-15", Outcome: core.BulkApplied},
				core.BulkItemResult{ID: "ac-12", Outcome: core.BulkSkipped, Detail: "ac-12 is now blocked (it was in_progress): nothing was written"},
				core.BulkItemResult{ID: "ac-20", Outcome: core.BulkFailed, Detail: "bd refused the change"})})
			return m
		}},
		{name: "tasks-insights-selected", build: std, keys: []string{"2", "V", "j", "j", "j", "j", "j", "j"}, tab: "Tasks", extra: framedSizes},
		{name: "tasks-insights-sorted", build: std, keys: []string{"2", "V", "s", "S"}, tab: "Tasks", extra: framedSizes},
		{name: "tasks-insights-help", build: std, keys: []string{"2", "V", "?"}, tab: "Tasks", overlay: true, extra: framedSizes},
		{name: "tasks-insights-cycle", tab: "Tasks", extra: framedSizes, keys: []string{"2", "V"}, build: func(t *testing.T, th Theme) tea.Model {
			m := load(t, th, nil)
			in := fixtureInsights()
			in.TopPicks, in.Blockers, in.Bottlenecks, in.CriticalPath, in.Influencers, in.Tracks = nil, nil, nil, nil, nil, nil
			in.CriticalPathNote = "critical path skipped: the graph has a dependency cycle"
			m, _ = m.Update(insightsMsg{report: in})
			return m
		}},
		{name: "tasks-insights-flat", tab: "Tasks", extra: framedSizes, keys: []string{"2", "V"}, build: func(t *testing.T, th Theme) tea.Model {
			m := load(t, th, nil)
			m, _ = m.Update(insightsMsg{report: core.InsightsReport{Header: core.Header{Source: core.SourceGraph, State: core.Connected}, TotalActionable: 3}})
			return m
		}},
	}
}
