package abtop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
)

// keyQuit, keyReload and the selection keys are the bindings the footer advertises.
var (
	keyQuit   = key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit"))
	keyReload = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload"))
	keyUp     = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("j/k", "select"))
	keyDown   = key.NewBinding(key.WithKeys("down", "j"))
)

// reportMsg is one finished read. A read runs off the update loop so a slow store cannot stall
// the keyboard. The host sample rides with the report because both are drawn from one frame;
// a host failure never fails the sessions read.
type reportMsg struct {
	rep       core.SessionsReport
	err       error
	vitals    agents.HostVitals
	vitalsErr error
}

// hostHistoryPoints is the CPU history ring: at the default interval it covers about two
// minutes, which is long enough to show a build spike without growing the frame with uptime.
const hostHistoryPoints = 60

// tickMsg asks for the next read. Ticks are scheduled when the previous read finishes, never in
// parallel, so the stores' cross-tick cursors see one pass at a time.
type tickMsg struct{}

// model is the monitor's frame. It holds the last good report, because a read that fails after
// a good one must not replace a live machine with an empty error screen.
type model struct {
	read     Reader
	host     HostReader
	interval time.Duration
	now      func() time.Time

	rep    core.SessionsReport
	err    error
	loaded bool
	busy   bool
	// hostVitals is the last good host sample and cpuHistory its ring. A later failed host read
	// keeps the last frame, like a failed sessions read does.
	hostVitals agents.HostVitals
	hostErr    error
	cpuHistory []float64
	// sel is the row the detail panel draws and selID is that session's stable harness:pid.
	// An index alone would silently move the detail when a refresh reorders the list, so the
	// identity is re-found on every read and the index is only the fallback when it is gone.
	sel   int
	selID string
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

// fetch starts one read with its own deadline. The host sample has its own short deadline, so
// a slow process table cannot spend the monitor's whole frame on a supplementary read.
func (m *model) fetch() tea.Cmd {
	m.busy = true
	read, host := m.read, m.host
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		rep, err := read(ctx)
		msg := reportMsg{rep: rep, err: err}
		if host != nil {
			hctx, hcancel := context.WithTimeout(context.Background(), hostTimeout)
			msg.vitals, msg.vitalsErr = host(hctx)
			hcancel()
		}
		return msg
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
		if m.host != nil {
			switch {
			case msg.vitalsErr != nil:
				m.hostErr = msg.vitalsErr
			case msg.vitals.Known():
				m.hostVitals, m.hostErr = msg.vitals, nil
				m.cpuHistory = appendCPUHistory(m.cpuHistory, msg.vitals.CPUPercent)
			}
		}
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
			m.rep, m.err, m.loaded = msg.rep, nil, true
			m.reanchorSelection()
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
		case key.Matches(msg, keyUp):
			m.sel = clampSel(m.sel-1, len(m.rep.Sessions))
			m.rememberSelection()
		case key.Matches(msg, keyDown):
			m.sel = clampSel(m.sel+1, len(m.rep.Sessions))
			m.rememberSelection()
		case key.Matches(msg, keyReload):
			if !m.busy {
				return m, m.fetch()
			}
		}
	}
	return m, nil
}

// View redraws from the cached report on every repaint. Panels are clipped to the terminal,
// so a narrow or short terminal loses detail instead of scrolling the frame away.
func (m *model) View() string {
	switch {
	case m.err != nil && !m.loaded:
		lines := []string{
			brand.Monitor + "  cannot read the machine",
			"  " + core.Sanitize(m.err.Error()),
			"",
			footerLine,
		}
		return strings.Join(clipFrame(lines, m.w, m.h), "\n")
	case !m.loaded:
		lines := []string{brand.Monitor + "  reading the machine…", "", footerLine}
		return strings.Join(clipFrame(lines, m.w, m.h), "\n")
	}
	notice := ""
	if m.err != nil {
		notice = "read failed: " + m.err.Error()
	}
	return renderMonitor(m.rep, m.w, m.h, m.sel, m.now(), notice,
		hostFrame{vitals: m.hostVitals, history: m.cpuHistory, err: m.hostErr})
}

// appendCPUHistory keeps the host CPU series bounded. The oldest sample drops first, so the
// panel always shows the most recent window of the same size.
func appendCPUHistory(points []float64, point float64) []float64 {
	points = append(points, point)
	if len(points) > hostHistoryPoints {
		points = points[len(points)-hostHistoryPoints:]
	}
	return points
}

func clampSel(sel, n int) int {
	if n <= 0 {
		return 0
	}
	if sel >= n {
		return n - 1
	}
	if sel < 0 {
		return 0
	}
	return sel
}

// reanchorSelection keeps the detail on the same session across a refresh that reorders the
// list, and falls back to the old index only when that session has exited.
func (m *model) reanchorSelection() {
	if m.selID != "" {
		for i, s := range m.rep.Sessions {
			if sessionKey(s) == m.selID {
				m.sel = i
				return
			}
		}
	}
	m.sel = clampSel(m.sel, len(m.rep.Sessions))
	m.rememberSelection()
}

func (m *model) rememberSelection() {
	if m.sel >= 0 && m.sel < len(m.rep.Sessions) {
		m.selID = sessionKey(m.rep.Sessions[m.sel])
		return
	}
	m.selID = ""
}

func sessionKey(s agents.Session) string {
	return fmt.Sprintf("%s:%d", s.Harness, s.PID)
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
	return r.State.Label()
}

func footer() string {
	q, r := keyQuit.Help(), keyReload.Help()
	up := keyUp.Help()
	return fmt.Sprintf("%s %s  %s %s  %s %s", q.Key, q.Desc, r.Key, r.Desc, up.Key, up.Desc)
}

var footerLine = footer()
