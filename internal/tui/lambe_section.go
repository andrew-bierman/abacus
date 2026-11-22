package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
)

const (
	pollEvery    = 5 * time.Second
	fetchTimeout = 8 * time.Second
)

func tick() tea.Cmd {
	return tea.Tick(pollEvery, func(time.Time) tea.Msg { return lambeTickMsg{} })
}

type lambeViewMsg struct {
	view lambe.View
	err  error
}
type lambeTickMsg struct{}

// lambeSection renders Lambe's runs, queue and attention, read-only. All data and the
// Rex command come from the lambe core module; this file only draws and routes keys.
type lambeSection struct {
	m       *lambe.Module
	handoff func(argv []string) tea.Cmd
	th      Theme
	geom
	view   lambe.View
	err    error
	loaded bool
	cursor int
	// selected is the identity of the chosen run, so a poll refresh that reorders or removes
	// rows cannot move the selection (and the Rex handoff) onto a different run.
	selected string
	notice   string
	drilled  bool // stacked layout only: showing the detail pane
	filter   string
	typing   bool
	top      int
}

func newLambeSection(m *lambe.Module, handoff func([]string) tea.Cmd) *lambeSection {
	return &lambeSection{m: m, handoff: handoff, th: DefaultTheme()}
}

func (s *lambeSection) SetTheme(t Theme) { s.th = t }
func (s *lambeSection) Capturing() bool  { return s.typing }

func (s *lambeSection) Bindings() []key.Binding {
	stacked := isStacked(s.width())
	enter, back := keyEnter, keyBack
	enter.SetEnabled(stacked && !s.drilled)
	back.SetEnabled(s.drilled || s.filter != "")
	return append(moveBindings(), enter, back, keyFilter, keyOpen)
}

func (s *lambeSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		v, err := s.m.Read(ctx)
		return lambeViewMsg{view: v, err: err}
	}
}

func (s *lambeSection) refresh() tea.Cmd { return s.fetch() }

func (s *lambeSection) Init() tea.Cmd {
	if s.m.IsDemo() {
		return s.fetch()
	}
	return tea.Batch(s.fetch(), tick())
}

// runs is the filtered run list the cursor indexes.
func (s *lambeSection) runs() []lambe.Run {
	all := s.view.Snapshot.Runs
	if s.filter == "" {
		return all
	}
	needle := strings.ToLower(s.filter)
	var out []lambe.Run
	for _, r := range all {
		if strings.Contains(strings.ToLower(strings.Join([]string{r.Issue, r.State, r.Phase, r.Machine, r.Workflow}, " ")), needle) {
			out = append(out, r)
		}
	}
	return out
}

func (s *lambeSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case lambeViewMsg:
		s.view, s.err, s.loaded = msg.view, msg.err, true
		s.reanchor()
	case handoffDoneMsg:
		if msg.err != nil {
			s.notice = "rex: " + core.Sanitize(msg.err.Error())
		}
	case lambeTickMsg:
		return s, tea.Batch(s.fetch(), tick())
	case tea.WindowSizeMsg:
		s.resize(msg)
	case tea.KeyMsg:
		s.notice = ""
		if s.typing {
			s.typeKey(msg)
			return s, nil
		}
		runs := s.runs()
		switch {
		case key.Matches(msg, keyUp):
			s.cursor = max(0, s.cursor-1)
			s.remember()
		case key.Matches(msg, keyDown):
			s.cursor = min(max(0, len(runs)-1), s.cursor+1)
			s.remember()
		case key.Matches(msg, keyEnter):
			s.drilled = isStacked(s.width()) && len(runs) > 0
		case key.Matches(msg, keyBack):
			// Esc peels one layer: the detail pane first, then the filter.
			if s.drilled {
				s.drilled = false
			} else if s.filter != "" {
				s.filter = ""
				s.reanchor()
			}
		case key.Matches(msg, keyFilter):
			s.typing, s.drilled = true, false
		case key.Matches(msg, keyOpen):
			if s.cursor >= len(runs) {
				return s, nil
			}
			machine := runs[s.cursor].Machine
			if machine == "" {
				s.notice = "no machine recorded for this run"
				return s, nil
			}
			return s, s.handoff(s.m.RexArgv(machine))
		}
	}
	return s, nil
}

// typeKey edits the filter: enter keeps it, esc clears it, anything printable extends it.
func (s *lambeSection) typeKey(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEnter:
		s.typing = false
	case tea.KeyEsc:
		s.typing, s.filter = false, ""
	case tea.KeyBackspace:
		if r := []rune(s.filter); len(r) > 0 {
			s.filter = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		s.filter += string(msg.Runes)
	}
	s.reanchor()
}

func runKey(r lambe.Run) string { return r.Issue + "\x00" + r.Session }

// remember records the run under the cursor as the selection.
func (s *lambeSection) remember() {
	if runs := s.runs(); s.cursor < len(runs) {
		s.selected = runKey(runs[s.cursor])
	}
}

// reanchor puts the cursor back on the selected run after a refresh, falling back to the
// clamped index when that run is gone.
func (s *lambeSection) reanchor() {
	runs := s.runs()
	for i, r := range runs {
		if s.selected != "" && runKey(r) == s.selected {
			s.cursor = i
			return
		}
	}
	s.cursor = min(s.cursor, max(0, len(runs)-1))
	// Only cursor movement changes the selection: a filter that hides it, or a refresh that
	// drops it, must not silently move it, so clearing the filter restores the same run.
	if s.selected == "" {
		s.remember()
	}
}

// stateKind is the color role of a run state; the severity itself is decided once, in core.
func stateKind(state string) Kind { return toneKind(core.RunState(strings.ToLower(state)).Tone()) }

func (s *lambeSection) countLabel(label string, n int) string {
	return s.th.Header.Render(label) + " " + s.th.Text(KindMuted, fmt.Sprint(n))
}

// attentionLines is the Needs attention block. It is pinned below the scrolling list so a
// long run list can never push it out of reach.
func (s *lambeSection) attentionLines() []string {
	n := s.view.Snapshot.NeedsHuman
	if len(n) == 0 {
		return nil
	}
	out := []string{s.countLabel("Needs attention", len(n))}
	for _, h := range n {
		out = append(out, fmt.Sprintf("  %s  %s  %s", san(h.Issue), s.th.Text(KindFail, san(h.Reason)), san(h.Detail)))
	}
	return out
}

// listLines is the scrolling part of the list pane and the line the cursor is on.
func (s *lambeSection) listLines(lw int) ([]string, int) {
	th := s.th
	runs := s.runs()
	head := s.countLabel("Runs", len(runs))
	if s.view.Source == lambe.SourceTracker {
		head += " " + th.Badge(KindWarn, lambe.SourceTracker)
	}
	if s.m.IsDemo() {
		head += " " + th.Badge(KindWarn, "demo data")
	}
	out := []string{head}
	if s.view.Source == lambe.SourceTracker {
		out = append(out, th.Text(KindMuted, "no live daemon, no phases or queue"))
	}
	if s.typing || s.filter != "" {
		caret := ""
		if s.typing {
			caret = "▏"
		}
		out = append(out, th.Text(KindAccent, "/"+san(s.filter)+caret)+" "+th.Text(KindMuted, fmt.Sprintf("%d/%d", len(runs), len(s.view.Snapshot.Runs))))
	}
	cursorLine := len(out)
	if len(runs) == 0 {
		out = append(out, th.Text(KindMuted, "  no runs"))
	}
	iw := 6
	for _, r := range runs {
		iw = max(iw, min(14, len([]rune(san(r.Issue)))))
	}
	for i, r := range runs {
		issue, state, phase := padTo(trunc(san(r.Issue), iw), iw), orDash(r.State), orDash(r.Phase)
		if i == s.cursor {
			row := "▸ " + issue + "  " + state + "  " + phase
			if r.Stalled {
				row += "  (stalled)"
			}
			out = append(out, th.Selected.Render(padTo(trunc(row, lw), lw)))
			continue
		}
		row := "  " + issue + "  " + th.Text(stateKind(r.State), state) + "  " + phase
		if r.Stalled {
			row += "  " + th.Badge(KindWarn, "stalled")
		}
		out = append(out, row)
	}
	if s.view.Source != lambe.SourceTracker {
		out = append(out, "", s.countLabel("Queue", len(s.view.Queue.Rows)))
		for _, q := range s.view.Queue.Rows {
			state := th.Text(KindOK, "ready")
			if q.Blocked {
				state = th.Badge(KindWarn, "blocked")
			}
			out = append(out, fmt.Sprintf("  %s  %s  %s", san(q.ID), state, san(q.Title)))
		}
	}
	return out, cursorLine + s.cursor
}

// detailLines describes the selected run.
func (s *lambeSection) detailLines() []string {
	th := s.th
	runs := s.runs()
	if s.cursor >= len(runs) {
		return []string{th.Header.Render("Run"), th.Text(KindMuted, "nothing selected")}
	}
	r := runs[s.cursor]
	head := th.Header.Render(orDash(r.Issue)) + " " + th.Badge(stateKind(r.State), orDash(r.State))
	if r.Stalled {
		head += " " + th.Badge(KindWarn, "stalled")
	}
	field := func(label, v string) string { return th.Label.Render(padTo(label, 11)) + orDash(v) }
	out := []string{head, "",
		field("phase", r.Phase), field("workflow", r.Workflow), field("session", r.Session),
		field("attempt", fmt.Sprint(r.Attempt)), field("machine", r.Machine), field("actor", r.Actor),
		field("last event", r.LastEventAge)}
	for _, h := range s.view.Snapshot.NeedsHuman {
		if h.Issue == r.Issue {
			out = append(out, "", th.Text(KindFail, "needs attention: "+san(h.Reason)), "  "+san(h.Detail))
		}
	}
	return out
}

func (s *lambeSection) View() string {
	w := s.width()
	var body []string
	switch {
	case !s.loaded:
		body = []string{s.th.Text(KindMuted, "loading…")}
	case errors.Is(s.err, lambe.ErrNoDaemon):
		// Not a failure: nothing is supervising. Say so plainly and how to start it.
		body = []string{s.th.Header.Render("Queues") + " " + s.th.Badge(KindSkip, "not connected"), ""}
		body = append(body, wrap(s.th.Text(KindWarn, lambe.NoDaemonReason), w)...)
		body = append(body, "")
		body = append(body, wrap(s.th.Text(KindAccent, "how to connect: ")+san(s.m.StartHint()), w)...)
	case s.err != nil:
		body = wrap(s.th.Text(KindWarn, "state unavailable: "+core.Sanitize(s.err.Error())), w)
	default:
		h := s.h
		if s.notice != "" && h > 0 {
			h = max(1, h-1)
		}
		lw, _ := paneWidths(w)
		scrolling, cursorLine := s.listLines(lw)
		list := s.compose(scrolling, cursorLine, h)
		body = panes(s.th, w, h, list, s.detailLines(), s.drilled)
	}
	if s.notice != "" {
		body = append(body, trunc(s.th.Text(KindWarn, s.notice), w))
	}
	return strings.Join(body, "\n") + "\n"
}

// compose stacks the scrolling list over the pinned attention block inside h rows (h <= 0:
// unconstrained), marking clipped content with "+N more".
func (s *lambeSection) compose(scrolling []string, cursorLine, h int) []string {
	pinned := s.attentionLines()
	if h <= 0 {
		if len(pinned) > 0 {
			return append(append(scrolling, ""), pinned...)
		}
		return scrolling
	}
	a := min(len(pinned), max(1, h/3), max(h-1, 0))
	if a < len(pinned) && a >= 2 {
		pinned = append(pinned[:a-1], s.th.Text(KindMuted, fmt.Sprintf("  +%d more", len(pinned)-a+1)))
	} else {
		pinned = pinned[:a]
	}
	vis, top, hidden := windowMore(scrolling, cursorLine, h-a, s.top)
	s.top = top
	out := append([]string{}, vis...)
	if hidden > 0 && h-a >= 2 {
		out = append(out, s.th.moreMarker(hidden))
	}
	if a > 0 && len(out)+a < h {
		out = append(out, "")
	}
	return append(out, pinned...)
}
