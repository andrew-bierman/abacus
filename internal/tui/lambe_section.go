package tui

import (
	"context"
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
	back.SetEnabled(stacked && s.drilled)
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
			s.drilled = len(runs) > 0
		case key.Matches(msg, keyBack):
			s.drilled = false
			s.filter = ""
			s.reanchor()
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
	s.remember()
}

// stateKind maps a run state to a color role; unknown states stay quiet.
func stateKind(state string) Kind {
	switch strings.ToLower(state) {
	case "running", "active":
		return KindOK
	case "failed", "error", "blocked":
		return KindFail
	case "waiting", "retrying", "paused", "deferred":
		return KindWarn
	}
	return KindMuted
}

func (s *lambeSection) countLabel(label string, n int) string {
	return s.th.Header.Render(label) + " " + s.th.Text(KindMuted, fmt.Sprint(n))
}

// listLines is the list pane and the line the cursor is on.
func (s *lambeSection) listLines(lw int) ([]string, int) {
	th := s.th
	runs := s.runs()
	head := s.countLabel("Runs", len(runs))
	if s.view.Source == lambe.SourceTracker {
		head += " " + th.Badge(KindWarn, lambe.SourceTracker)
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
	if n := s.view.Snapshot.NeedsHuman; len(n) > 0 {
		out = append(out, "", s.countLabel("Needs attention", len(n)))
		for _, h := range n {
			out = append(out, fmt.Sprintf("  %s  %s  %s", san(h.Issue), th.Text(KindFail, san(h.Reason)), san(h.Detail)))
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
	case s.err != nil:
		body = wrap(s.th.Text(KindWarn, "state unavailable: "+core.Sanitize(s.err.Error())), w)
	default:
		h := s.h
		if s.notice != "" && h > 0 {
			h = max(1, h-1)
		}
		lw, _ := paneWidths(w)
		list, cursorLine := s.listLines(lw)
		if !(isStacked(w) && s.drilled) {
			list, s.top = window(list, cursorLine, h, s.top)
		}
		body = panes(s.th, w, h, list, s.detailLines(), s.drilled)
	}
	if s.notice != "" {
		body = append(body, trunc(s.th.Text(KindWarn, s.notice), w))
	}
	return strings.Join(body, "\n") + "\n"
}
