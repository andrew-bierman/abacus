package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The prompt, the describe and JSON panels and the header context, through the same golden grid
// (40/80/120 by 12/30) plus the two sizes the plan names, 60x24 and 100x32. The theme is the fixed
// ascii profile, so the text is what a NO_COLOR terminal shows.
func promptScenarios() []scenario {
	std := func(t *testing.T, th Theme) tea.Model { return load(t, th, nil) }
	colon := func(line string) []string { return append([]string{"2", ":"}, typed(line)...) }
	return []scenario{
		{name: "prompt-open", build: std, keys: []string{"2", ":"}, tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "prompt-typing", build: std, keys: colon("filter label:u"), tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "prompt-completing", build: std, keys: colon("q"), tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "prompt-completed", build: std, keys: append(colon("fi"), "tab"), tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "prompt-error", build: std, keys: append(colon("bord"), "enter"), tab: "Tasks", hiddenOK: true, footHas: []string{"unknown command: bord (try :help)"}, extra: framedSizes},
		{name: "prompt-bad-filter", build: std, keys: append(colon("filter nosuch:x"), "enter"), tab: "Tasks", hiddenOK: true, footHas: []string{"bad filter"}, extra: framedSizes},
		{name: "prompt-jumped", build: std, keys: append(colon("board"), "enter"), tab: "Board", hiddenOK: true, footHas: []string{"? help"}, extra: framedSizes},
		{name: "prompt-graph", build: std, keys: append(colon("graph"), "enter"), tab: "Tasks", hiddenOK: true, extra: framedSizes},
		{name: "prompt-filtered", build: std, keys: append(colon("filter label:ui"), "enter"), tab: "Tasks", hiddenOK: true, extra: framedSizes},
		{name: "prompt-kanban", build: std, keys: append(colon("kanban"), "enter"), tab: "Tasks", hiddenOK: true, footHas: []string{"kanban"}, extra: framedSizes},
		{name: "prompt-ctx", build: std, keys: append(colon("ctx"), "enter"), tab: "Tasks", hiddenOK: true, footHas: []string{"context: fleet"}, extra: framedSizes},
		{name: "prompt-history", build: std, keys: append(append(colon("leases"), "enter", ":", "up"), "up"), tab: "Leases", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "describe", build: std, keys: []string{"2", "T", "d"}, tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "describe-scrolled", build: std, keys: []string{"2", "T", "d", "j", "j", "j"}, tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "json", build: std, keys: []string{"2", "T", "y"}, tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "json-scrolled", build: std, keys: []string{"2", "T", "y", "j", "j", "j"}, tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "describe-hostile", build: hostileFixture, keys: append(append([]string{"2", "T", "/"}, typed("evil")...), "enter", "d"), tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "json-hostile", build: hostileFixture, keys: append(append([]string{"2", "T", "/"}, typed("evil")...), "enter", "y"), tab: "Tasks", overlay: true, hiddenOK: true, extra: framedSizes},
		{name: "header-context-board", tab: "Tasks", keys: []string{"2"}, extra: [][2]int{{100, 32}, {140, 32}}, build: func(t *testing.T, th Theme) tea.Model {
			m := load(t, th, nil)
			m.(*Model).boardTarget = "https://board.example/api"
			return m
		}},
	}
}
