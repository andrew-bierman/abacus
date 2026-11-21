package lambe

import (
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

// execRex hands the terminal to `rex -S <machine>`. It runs only from a keypress.
func execRex(machine string) tea.Cmd {
	return tea.ExecProcess(exec.Command("rex", "-S", machine), func(error) tea.Msg { return nil })
}
