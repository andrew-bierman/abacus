package tui

import "github.com/charmbracelet/bubbles/key"

// Global bindings. Sections add their own; the footer and the `?` overlay are generated from
// these key.Binding values, so the help can never drift from what a key does.
var (
	keyQuit = key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit"))
	keyNext = key.NewBinding(key.WithKeys("tab", "right"), key.WithHelp("tab/→", "next tab"))
	keyPrev = key.NewBinding(key.WithKeys("shift+tab", "left"), key.WithHelp("shift+tab/←", "prev tab"))
	keyJump = key.NewBinding(key.WithKeys("1", "2", "3", "4", "5", "6", "7", "8", "9"), key.WithHelp("1-9", "jump to tab"))
	keyHelp = key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help"))
	keyBack = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
)

var (
	keyUp     = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown   = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	keyEnter  = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "details"))
	keyFilter = key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter"))
	keyOpen   = key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open session"))
	keyFull   = key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "full view"))
	keyNew    = key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new"))
	keyEdit   = key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit"))
	keyClose  = key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "close task"))
	keyYes    = key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "apply"))
	keyNo     = key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "cancel"))
	keyReload = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh"))
)

// moveBindings is the shared up/down pair, shown as one "↑/↓ move" entry in help.
func moveBindings() []key.Binding {
	return []key.Binding{key.NewBinding(key.WithKeys("up", "k", "down", "j"), key.WithHelp("↑/↓", "move"))}
}
