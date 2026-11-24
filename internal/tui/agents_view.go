package tui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// The Agents view: what is the fleet doing. It exists only when a provider for it is connected
// (the same optional layer as the workspace placements), as a tab after Doctor so the digit jumps 1
// to 5 never move. Core reads the panes, sorts them for attention and builds each pane's open plan;
// this file only draws them, and on an explicit keypress hands the terminal to the first step of a
// plan core produced, through the same gate the workspace layer uses. It binds no key that types
// into, focuses or closes a pane. Text in a pane (message, title, names) is untrusted and has
// already been cleaned by core; it still passes through san before it is styled.

// Refresh. The view polls only while it is on screen: every agentsPollEvery while any pane is
// blocked or working, every agentsIdleEvery otherwise, backing off to agentsPollMax after a slow or
// failed sweep, one sweep at a time (taskview.Poller). r refreshes at once.
const (
	agentsPollEvery = 5 * time.Second
	agentsIdleEvery = 15 * time.Second
	agentsPollMax   = 60 * time.Second
	agentsSlowRead  = 2 * time.Second
	// agentsTickEvery is how often the view checks whether a sweep is due. A check costs nothing; it
	// runs no process.
	agentsTickEvery = time.Second
)

// agentsTitle is the tab name.
const agentsTitle = "Agents"

var keyAgentsOpen = key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "open workspace"))

type agentsTickMsg struct{}

// agentsMsg is the answer to one sweep.
type agentsMsg struct {
	rep  core.AgentReport
	took time.Duration
}

// agentsSection renders the typed AgentReport as a list of panes sorted for attention, with the
// selected pane in a detail panel beside it.
type agentsSection struct {
	prov   core.AgentProvider
	items  func() []core.WorkspaceItem // the work items to match panes against, from the other tabs
	rep    *core.AgentReport           // nil until the first sweep answers
	readAt time.Time
	poll   *taskview.Poller
	hidden bool
	tick   func() tea.Cmd // injectable so tests never sleep
	th     Theme
	geom
	listNav
	ws *wsLayer
}

func newAgentsSection(prov core.AgentProvider, items func() []core.WorkspaceItem, ws *wsLayer) *agentsSection {
	return &agentsSection{prov: prov, items: items, ws: ws, th: DefaultTheme(), hidden: true,
		poll: taskview.NewPoller(agentsPollEvery, agentsPollMax, agentsSlowRead),
		tick: func() tea.Cmd { return tea.Tick(agentsTickEvery, func(time.Time) tea.Msg { return agentsTickMsg{} }) }}
}

func (s *agentsSection) SetTheme(t Theme) { s.th = t }
func (s *agentsSection) Capturing() bool  { return false }

func (s *agentsSection) panes() []core.AgentPane {
	if s.rep == nil {
		return nil
	}
	return s.rep.Panes
}

// paneID is the stable identity of a row, so a refresh never moves the selection onto another pane.
func paneID(p core.AgentPane) string { return p.Host + "\x00" + p.Session + "\x00" + p.Block }

func (s *agentsSection) ids() []string {
	ps := s.panes()
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = paneID(p)
	}
	return out
}

func (s *agentsSection) selected() *core.AgentPane {
	if ps := s.panes(); s.cursor < len(ps) {
		return &ps[s.cursor]
	}
	return nil
}

// activate and deactivate tell the view whether it is on screen: a hidden view never sweeps.
func (s *agentsSection) activate() tea.Cmd {
	s.hidden = false
	return s.fetch()
}

func (s *agentsSection) deactivate() { s.hidden = true }

// wait is the wanted rhythm: quick while anything is blocked or working.
func (s *agentsSection) retune() {
	s.poll.Base = agentsIdleEvery
	for _, p := range s.panes() {
		if p.State == core.AgentBlocked || p.State == core.AgentWorking {
			s.poll.Base = agentsPollEvery
			break
		}
	}
}

// fetch starts one sweep when the poller allows it (none in flight, the wait is over).
func (s *agentsSection) fetch() tea.Cmd {
	if s.prov == nil || s.hidden || !s.poll.Start(nowFn()) {
		return nil
	}
	var items []core.WorkspaceItem
	if s.items != nil {
		items = s.items()
	}
	prov := s.prov
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		start := time.Now()
		rep := core.ReadAgentsFrom(ctx, prov, items)
		return agentsMsg{rep: rep, took: time.Since(start)}
	}
}

func (s *agentsSection) Init() tea.Cmd { return s.tick() }

func (s *agentsSection) Bindings() []key.Binding {
	enter, back := s.bindings(s.width(), len(s.panes()))
	open := keyAgentsOpen
	open.SetEnabled(s.selected() != nil && s.selected().Open != nil)
	return append(moveBindings(), enter, back, keyReload, open)
}

func (s *agentsSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case agentsMsg:
		s.poll.Done(nowFn(), m.took, m.rep.State != core.Degraded)
		rep := m.rep
		s.rep, s.readAt = &rep, nowFn()
		s.retune()
		s.reanchor(s.ids(), false)
		s.settle(len(s.panes()))
	case agentsTickMsg:
		return s, tea.Batch(s.fetch(), s.tick())
	case handoffDoneMsg:
		if s.ws.done() && m.err != nil {
			s.ws.note = []string{"The workspace did not open: " + core.Sanitize(m.err.Error())}
		}
	case tea.WindowSizeMsg:
		s.resize(m)
		s.fit(s.width())
	case tea.KeyMsg:
		if s.ws.swallow() { // the key that closes the notice does nothing else
			return s, nil
		}
		switch {
		case key.Matches(m, keyReload):
			s.poll.Expedite()
			return s, s.fetch()
		case key.Matches(m, keyAgentsOpen):
			return s, s.open()
		case key.Matches(m, keyUp):
			s.up(s.ids())
		case key.Matches(m, keyDown):
			s.down(s.ids())
		case key.Matches(m, keyEnter):
			s.enter(s.width(), len(s.panes()))
		case key.Matches(m, keyBack):
			s.back()
		}
	}
	return s, nil
}

// open goes to the selected pane through the workspace gate: its plan came from core, and only the
// handoff to its own machine may run; everything else is a notice with the command to copy.
func (s *agentsSection) open() tea.Cmd {
	p := s.selected()
	if p == nil || s.ws == nil || s.ws.running() {
		return nil
	}
	return s.ws.openPlacement(&core.Placement{Host: p.Host, Session: p.Session, Label: p.SessionLabel, Open: p.Open})
}

// agentStateKind is the color role of a state. A blocked agent needs a person, so it is the loud one.
func agentStateKind(st core.AgentState) Kind {
	switch st {
	case core.AgentBlocked:
		return KindWarn
	case core.AgentError:
		return KindFail
	case core.AgentDone:
		return KindOK
	case core.AgentWorking:
		return KindAccent
	}
	return KindMuted
}

func stateWord(p core.AgentPane) string {
	if !p.Reported {
		return "quiet"
	}
	return string(p.State)
}

func kindWord(k core.AgentKind) string {
	if k == core.AgentKindNone {
		return ""
	}
	return string(k)
}

// waitText is how long the pane has been in its newest report: "~" before it when the pane is on
// another machine (its clock is not ours), "stale" after it when nothing newer came, and a dash
// when the time is unknown or in the future.
func waitText(p core.AgentPane, now time.Time) string {
	if !p.Reported {
		return "-"
	}
	age := taskview.Age(p.UpdatedAt, now)
	if age == "" {
		return "-"
	}
	if p.Host != "" {
		age = "~" + age
	}
	if p.IsStale(now) {
		age += " stale"
	}
	return age
}

// whereText is what the pane is about: the work item it names, "ambiguous", or its session label.
func whereText(p core.AgentPane) string {
	var w string
	switch {
	case len(p.Candidates) > 0:
		w = "ambiguous"
	case p.Item != nil:
		w = p.Item.ID
	case p.SessionLabel != "":
		w = p.SessionLabel
	default:
		w = "-"
	}
	if p.Host != "" {
		w += " @" + p.Host
	}
	return san(w)
}

// agentCols are the list's columns. The state and what the pane is about keep their width (a
// higher prio number drops first); the message takes what is left.
var agentCols = table{cols: []column{
	{min: 9, max: 9, prio: 0},
	{min: 12, max: 28, prio: 0},
	{min: 9, max: 11, prio: 1, right: true},
	{min: 11, max: 14, prio: 3},
	{min: 10, max: 10, prio: 5},
	{min: 3, max: 4, prio: 6, right: true},
	{min: 12, prio: 4},
}, gap: 2}

// summary is the list title: how many panes, and how many need a person.
func (s *agentsSection) summary() string {
	ps := s.panes()
	var blocked, errs int
	for _, p := range ps {
		switch p.State {
		case core.AgentBlocked:
			blocked++
		case core.AgentError:
			errs++
		}
	}
	out := strconv.Itoa(len(ps)) + " agents"
	if len(ps) == 1 {
		out = "1 agent"
	}
	if blocked > 0 {
		out += fmt.Sprintf(" · %d blocked", blocked)
	}
	if errs > 0 {
		out += fmt.Sprintf(" · %d error", errs)
	}
	return out
}

func (s *agentsSection) listLines(g duoGeom) []string {
	th, now := s.th, nowFn()
	ps := s.panes()
	styled := make([][]string, len(ps))
	plain := make([][]string, len(ps))
	for i, p := range ps {
		prog := "-"
		if p.Progress != nil {
			prog = strconv.Itoa(*p.Progress) + "%"
		}
		msg := orDash(p.Msg)
		cells := []string{stateWord(p), whereText(p), waitText(p, now), orDash(p.Agent), orDash(kindWord(p.Kind)), prog, msg}
		plain[i] = cells
		st := append([]string(nil), cells...)
		st[0] = th.Badge(agentStateKind(p.State), cells[0])
		if !p.Reported {
			st[0] = th.Text(KindMuted, cells[0])
		}
		if p.IsStale(now) {
			st[2] = th.Text(KindWarn, cells[2])
		}
		styled[i] = st
	}
	lines := renderRows(th, agentCols, g.listInner(), styled, plain, s.cursor)
	return windowLines(th, lines, s.cursor, g.inner, &s.top)
}

func (s *agentsSection) detailLines(g duoGeom) []string {
	th, now := s.th, nowFn()
	p := s.selected()
	if p == nil {
		return []string{th.Text(KindMuted, "select a row")}
	}
	const pad = 11
	field := func(label, v string) string { return th.Label.Render(padTo(label, pad)) + orDash(v) }
	name := orDash(p.Agent)
	out := []string{th.Header.Render(name) + " " + th.Badge(agentStateKind(p.State), stateWord(*p))}
	if !p.Reported {
		out = append(out, "", th.Text(KindMuted, "runs an agent but has reported no status"))
	} else {
		out = append(out, "")
		if k := kindWord(p.Kind); k != "" {
			out = append(out, field("needs", k))
		}
		if p.Msg != "" { // the message is the point: wrapped, never cut
			for i, l := range wrap(san(p.Msg), max(innerW(g.detail)-pad, 8)) {
				label := ""
				if i == 0 {
					label = "message"
				}
				out = append(out, th.Label.Render(padTo(label, pad))+l)
			}
		}
		if p.Title != "" {
			out = append(out, field("title", p.Title))
		}
		if p.Progress != nil {
			out = append(out, field("progress", strconv.Itoa(*p.Progress)+"%"))
		}
		rec := p.RecordID
		if p.NRecords > 1 {
			if rec != "" {
				rec += " "
			}
			rec += fmt.Sprintf("(%d records)", p.NRecords)
		}
		if rec != "" {
			out = append(out, field("record", rec))
		}
		out = append(out, field("since", waitText(*p, now)))
		if p.IsStale(now) {
			out = append(out, th.Text(KindWarn, "no newer report: this may no longer be true"))
		}
	}
	out = append(out, "")
	switch {
	case len(p.Candidates) > 0:
		out = append(out, field("task", th.Text(KindWarn, fmt.Sprintf("ambiguous: %d claim it", len(p.Candidates)))))
		for _, c := range p.Candidates {
			out = append(out, "  "+san(c.ID)+" "+th.Text(KindMuted, san(string(c.Kind))))
		}
	case p.Item != nil:
		out = append(out, field("task", san(p.Item.ID)+" "+th.Text(KindMuted, san(string(p.Item.Kind)))), field("match", matchWord(p.Match)))
	default:
		out = append(out, field("task", th.Text(KindMuted, "none named")))
	}
	if p.Process != "" {
		out = append(out, field("process", p.Process))
	}
	if p.Cwd != "" {
		out = append(out, field("directory", p.Cwd))
	}
	if p.Repo != "" {
		out = append(out, field("repo", p.Repo))
	}
	if p.Branch != "" {
		out = append(out, field("branch", p.Branch))
	}
	if p.Host != "" {
		out = append(out, field("machine", p.Host))
	}
	out = append(out, field("session", firstOf(p.SessionLabel, p.Session)))
	if r := s.rep; r != nil {
		if r.State == core.Degraded && r.Reason != "" {
			out = append(out, "", th.Text(KindWarn, "workspace: "+san(r.Reason)))
		}
		if r.Stale {
			out = append(out, th.Text(KindMuted, "from a recent cached read"))
		}
		if r.Truncated {
			out = append(out, th.Text(KindWarn, "some panes were left out"))
		}
	}
	return out
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// emptyBody is the body of a view with nothing to list: no workspace running, a workspace with no
// agents (and how one shows up), or a read that failed.
func (s *agentsSection) emptyBody(inner int) []string {
	th, r := s.th, s.rep
	var body []string
	switch {
	case r.State == core.NotConnected:
		body = append(body, wrap(th.Text(KindWarn, san(r.Reason)), inner)...)
		body = append(body, "")
		body = append(body, wrap("Start your terminal workspace, or run abacus inside it. It is optional; nothing else in abacus depends on it.", inner)...)
		return body
	case r.State == core.Degraded:
		body = append(body, wrap(th.Text(KindWarn, "workspace: "+san(r.Reason)), inner)...)
		body = append(body, "")
	}
	body = append(body, th.Text(KindMuted, "no agents are reporting status"))
	if r.Terminals > 0 {
		body = append(body, th.Text(KindMuted, fmt.Sprintf("%d terminals looked at", r.Terminals)))
	}
	body = append(body, "")
	body = append(body, wrap("A program shows up here when it publishes status to its terminal pane (the status escape sequence, OSC 7501, which an agent tool can send from a hook) or when a known agent is the pane's foreground program.", inner)...)
	if r.Truncated {
		body = append(body, "", th.Text(KindWarn, "some terminals were left out"))
	}
	return body
}

func (s *agentsSection) View() string {
	th, w := s.th, s.width()
	if s.rep == nil {
		return joinLines([]string{th.Text(KindMuted, "loading…")})
	}
	if box, ok := s.ws.noteBox(th, w, s.h); ok {
		return joinLines(box)
	}
	if len(s.panes()) == 0 {
		return joinLines(box(th, agentsTitle, s.emptyBody(innerW(w)), w, s.h))
	}
	g := duoLayout(w, s.h)
	title := agentsTitle + " · " + san(s.summary())
	if seen := s.readAt; !seen.IsZero() && !taskview.Fresh(seen, nowFn(), s.poll.Wait()) {
		title += " · " + th.Text(KindWarn, "stale "+taskview.Age(seen, nowFn()))
	}
	return joinLines(g.draw(th, title, s.listLines(g), "Agent", s.detailLines(g), s.drilled))
}

// agentItems collects the work items the other tabs show, once each, for the pane matcher.
func agentItems(tabs func() []tab) func() []core.WorkspaceItem {
	return func() []core.WorkspaceItem {
		var all []core.WorkspaceItem
		for _, t := range tabs() {
			if src, ok := inner(t.sec).(interface{ wsItems() []core.WorkspaceItem }); ok {
				all = append(all, src.wsItems()...)
			}
		}
		return itemList(all...)
	}
}
