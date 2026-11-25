package abtop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
)

// keyQuit and keyReload are the only bindings, and the footer is built from them so the help
// cannot drift from what a key does.
var (
	keyQuit   = key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit"))
	keyReload = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload"))
)

// reportMsg is one finished read. A read runs off the update loop so a slow store cannot stall
// the keyboard.
type reportMsg struct {
	rep core.SessionsReport
	err error
}

// tickMsg asks for the next read. Ticks are scheduled when the previous read finishes, never in
// parallel, so the stores' cross-tick cursors see one pass at a time.
type tickMsg struct{}

// model is the monitor's frame. It holds the last good report, because a read that fails after
// a good one must not replace a live machine with an empty error screen.
type model struct {
	read     Reader
	interval time.Duration
	now      func() time.Time

	rep    core.SessionsReport
	lines  []string
	err    error
	loaded bool
	busy   bool
	// ticking is true while one timer is pending, so a manual reload joins the existing chain
	// instead of adding a second one that would double the refresh rate for the rest of the run.
	ticking bool

	w, h int
}

func newModel(read Reader, interval time.Duration, now func() time.Time) *model {
	if now == nil {
		now = time.Now
	}
	return &model{read: read, interval: interval, now: now}
}

func (m *model) Init() tea.Cmd { return m.fetch() }

// fetch starts one read with its own deadline.
func (m *model) fetch() tea.Cmd {
	m.busy = true
	read := m.read
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		rep, err := read(ctx)
		return reportMsg{rep: rep, err: err}
	}
}

func (m *model) tick() tea.Cmd {
	m.ticking = true
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case reportMsg:
		m.busy = false
		if msg.err != nil {
			// A failure after a good read keeps the last frame; a failure before any read is
			// the only thing View has to show. One field carries both, so a later success
			// clears the failure either way.
			m.err = msg.err
		} else if m.loaded && unusable(msg.rep) {
			// The read edges fold a failed or unavailable read into the report header and
			// return a nil error, so the branch above never fires in the wired binary. A
			// refresh that came back without a usable read must not replace a live frame with
			// an empty one: keep the last good report and show the header's reason instead.
			m.err = errors.New(unusableReason(msg.rep))
		} else {
			m.rep, m.lines, m.err, m.loaded = msg.rep, reportLines(msg.rep, m.now()), nil, true
		}
		if m.ticking {
			return m, nil
		}
		return m, m.tick()
	case tickMsg:
		m.ticking = false
		if m.busy { // a read overran its interval; try again once it finishes
			return m, m.tick()
		}
		return m, m.fetch()
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keyQuit):
			return m, tea.Quit
		case key.Matches(msg, keyReload):
			if !m.busy {
				return m, m.fetch()
			}
		}
	}
	return m, nil
}

// View draws the cached frame. The report is rendered once per read, not once per message, so a
// resize or keypress does not re-format every session.
func (m *model) View() string {
	var lines []string
	switch {
	case m.err != nil && !m.loaded:
		lines = []string{
			brand.Monitor + "  cannot read the machine",
			"  " + core.Sanitize(m.err.Error()),
		}
	case !m.loaded:
		lines = []string{brand.Monitor + "  reading the machine…"}
	default:
		lines = m.lines
		if m.err != nil {
			lines = append(append([]string{}, lines...), "", "  read failed: "+core.Sanitize(m.err.Error()))
		}
	}
	lines = append(lines, "", footer())
	if m.w > 0 {
		// Clip into a fresh slice: writing the narrowed rows back into m.lines would shrink the
		// cached frame, so a later wider resize could not restore the full rows.
		clipped := make([]string, len(lines))
		for i, line := range lines {
			clipped[i] = core.ClipRunes(line, m.w)
		}
		lines = clipped
	}
	return strings.Join(lines, "\n")
}

// unusable reports say the refresh produced no live machine: the read edge turned a failure into
// a not-connected header, or into a degraded one with no sessions. A degraded report that still
// carries sessions is fresh partial data, so it replaces the frame like any other read.
func unusable(r core.SessionsReport) bool {
	switch r.State {
	case core.NotConnected:
		return true
	case core.Degraded:
		return len(r.Sessions) == 0
	}
	return false
}

// unusableReason is the notice for a refresh whose header says the read was unavailable: the
// header's reason when it carries one, else the state itself.
func unusableReason(r core.SessionsReport) string {
	if r.Reason != "" {
		return r.Reason
	}
	return strings.ReplaceAll(string(r.State), "-", " ")
}

func footer() string {
	q, r := keyQuit.Help(), keyReload.Help()
	return fmt.Sprintf("%s %s  %s %s", q.Key, q.Desc, r.Key, r.Desc)
}
