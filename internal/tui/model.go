// Package tui is the interactive shell: a tab per detected module, hosting its section.
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/beaddeck/internal/brand"
	"github.com/andrew-bierman/beaddeck/internal/module"
)

// Model hosts one section per ready module.
type Model struct {
	names    []string
	sections []tea.Model
	sel      int
	width    int
}

// New builds the shell from detection entries; only ready modules get a section.
func New(entries []module.Entry) *Model {
	m := &Model{}
	for _, e := range module.ReadyOnly(entries) {
		m.names = append(m.names, e.Module.Name())
		m.sections = append(m.sections, e.Module.Section())
	}
	return m
}

// Run starts the program on the alternate screen.
func Run(entries []module.Entry) error {
	_, err := tea.NewProgram(New(entries), tea.WithAltScreen()).Run()
	return err
}

func (m *Model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, len(m.sections))
	for i, s := range m.sections {
		cmds[i] = s.Init()
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right":
			if len(m.sections) > 0 {
				m.sel = (m.sel + 1) % len(m.sections)
			}
			return m, nil
		case "shift+tab", "left":
			if len(m.sections) > 0 {
				m.sel = (m.sel + len(m.sections) - 1) % len(m.sections)
			}
			return m, nil
		}
		if len(m.sections) == 0 {
			return m, nil
		}
		// Other keys go to the visible section only.
		var cmd tea.Cmd
		m.sections[m.sel], cmd = m.sections[m.sel].Update(msg)
		return m, cmd
	}
	if w, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = w.Width
	}
	// Everything else (window size, async results, ticks) goes to every section;
	// each ignores message types it does not own.
	cmds := make([]tea.Cmd, len(m.sections))
	for i := range m.sections {
		m.sections[i], cmds[i] = m.sections[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) View() string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(brand.Name+" · Beads tool deck") + "\n")
	if len(m.sections) == 0 {
		b.WriteString("No Beads tools detected. Install one (for example lambe), then run `" + brand.Name + " doctor`.\n\nq quits\n")
		return b.String()
	}
	tabs := make([]string, len(m.names))
	for i, n := range m.names {
		if i == m.sel {
			tabs[i] = lipgloss.NewStyle().Reverse(true).Render(" " + n + " ")
		} else {
			tabs[i] = " " + n + " "
		}
	}
	b.WriteString(strings.Join(tabs, "|") + "\n\n")
	b.WriteString(m.sections[m.sel].View())
	b.WriteString("\ntab switch · q quit\n")
	return b.String()
}
