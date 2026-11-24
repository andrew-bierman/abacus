package tui

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The Agents tab: one dashboard of every running coding agent. It draws core's typed report and
// holds no logic of its own beyond which rows are shown. Nothing here writes or runs anything: the
// way to an agent's terminal is shown as a line to copy.

const agentsPollEvery = 10 * time.Second

var (
	keyAgentSort    = key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort"))
	keyAgentState   = key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "state"))
	keyAgentHarness = key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "harness"))
	keyAgentScroll  = key.NewBinding(key.WithKeys("J", "K"), key.WithHelp("J/K", "scroll detail"))
)

type agentsMsg struct{ report core.AgentsReport }
type agentsTickMsg struct{ gen int }

// agentSorts are the sort keys, in the order `s` cycles them.
var agentSorts = []string{"attention", "recent", "harness", "repo"}

type agentsSection struct {
	read   func(context.Context) core.AgentsReport
	report core.AgentsReport
	loaded bool
	readAt time.Time
	busy   bool
	active bool
	gen    int
	th     Theme
	geom
	listNav          // ids are agent ids
	ws      *wsLayer // the optional workspace layer; nil without one

	query     string
	typing    bool
	state     core.AgentState // "" = every state
	harness   string          // "" = every harness
	sortKey   int
	top       int
	detailTop int
	notice    string
}

func newAgentsSection(read func(context.Context) core.AgentsReport) *agentsSection {
	return &agentsSection{read: read, th: DefaultTheme()}
}

func (s *agentsSection) setWorkspace(w *wsLayer) { s.ws = w }
func (s *agentsSection) layer() *wsLayer         { return s.ws }

// wsDetailVisible: the detail is drawn beside the list, or on its own once opened.
func (s *agentsSection) wsDetailVisible() bool { return !isStacked(s.width()) || s.drilled }

// agentItem is the workspace item of an agent: the name a terminal tab for it would carry, its
// worktree or else its repository. An agent with neither has no item, so nothing is guessed.
func agentItem(a core.Agent) (core.WorkspaceItem, bool) {
	id := a.Worktree
	if id == "" {
		id = a.Repo
	}
	if !core.ItemIDRE.MatchString(id) {
		return core.WorkspaceItem{}, false
	}
	return itemOf(core.WorkspaceAgent, id, "")
}

// uniqueItem is the agent's workspace item only when no other agent shares its name. A terminal is
// looked up by name, so two agents in one repository would both land on whichever tab carries it: the
// lookup is refused rather than pointing one agent's key at the other's terminal.
func (s *agentsSection) uniqueItem(a core.Agent) (core.WorkspaceItem, bool) {
	it, ok := agentItem(a)
	if !ok {
		return it, false
	}
	n := 0
	for _, o := range s.report.Agents {
		if oi, ok := agentItem(o); ok && oi == it {
			n++
		}
	}
	return it, n == 1
}

func (s *agentsSection) wsItems() []core.WorkspaceItem {
	var items []core.WorkspaceItem
	for _, a := range s.report.Agents {
		if it, ok := s.uniqueItem(a); ok {
			items = append(items, it)
		}
	}
	return itemList(items...)
}

// wsSelected is the agent under the cursor and the name that fills a command.
func (s *agentsSection) wsSelected() (core.WorkspaceItem, string, bool) {
	l := s.visible()
	if s.cursor >= len(l) {
		return core.WorkspaceItem{}, "", false
	}
	it, ok := s.uniqueItem(l[s.cursor])
	return it, it.ID, ok
}

func (s *agentsSection) SetTheme(t Theme) { s.th = t }
func (s *agentsSection) Capturing() bool  { return s.typing }

func (s *agentsSection) Bindings() []key.Binding {
	enter, back := s.bindings(s.width(), len(s.visible()))
	back.SetEnabled(s.drilled || s.query != "" || s.state != "" || s.harness != "")
	it, _, ok := s.wsSelected()
	return append(moveBindings(), enter, back, keyFilter, keyAgentState, keyAgentHarness, keyAgentSort, keyAgentScroll, keyReload, s.ws.jumpBinding(it, ok))
}

func (s *agentsSection) fetch() tea.Cmd {
	s.busy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
		defer cancel()
		return agentsMsg{s.read(ctx)}
	}
}

func (s *agentsSection) tick() tea.Cmd {
	gen := s.gen
	return tea.Tick(agentsPollEvery, func(time.Time) tea.Msg { return agentsTickMsg{gen} })
}

func (s *agentsSection) Init() tea.Cmd { return nil }

// activate loads on the first visit and then reads again while the tab is visible, one read at a time.
func (s *agentsSection) activate() tea.Cmd {
	s.active = true
	s.gen++
	if s.busy {
		return s.tick()
	}
	return tea.Batch(s.fetch(), s.tick())
}

func (s *agentsSection) deactivate() { s.active = false; s.gen++ }

// visible is the filtered, sorted list the cursor indexes.
func (s *agentsSection) visible() []core.Agent {
	l := core.FilterAgents(s.report.Agents, core.AgentFilter{Harness: s.harness, State: s.state, Query: s.query})
	switch agentSorts[s.sortKey] {
	case "recent":
		sortBy(l, func(a, b core.Agent) bool { return a.Last().After(b.Last()) })
	case "harness":
		sortBy(l, func(a, b core.Agent) bool { return a.Harness < b.Harness })
	case "repo":
		sortBy(l, func(a, b core.Agent) bool { return a.Repo < b.Repo })
	}
	return l
}

// sortBy is a stable sort; ties keep the core order (what needs a person first).
func sortBy(l []core.Agent, less func(a, b core.Agent) bool) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && less(l[j], l[j-1]); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

func (s *agentsSection) ids() []string {
	l := s.visible()
	out := make([]string, len(l))
	for i, a := range l {
		out[i] = a.ID
	}
	return out
}

// nextState cycles the state filter through "all" and the states that exist.
func (s *agentsSection) nextState() {
	opts := []core.AgentState{""}
	have := s.report.Counts()
	for _, st := range core.AgentStates {
		if have[st] > 0 {
			opts = append(opts, st)
		}
	}
	s.state = cycle(opts, s.state)
}

func (s *agentsSection) nextHarness() {
	opts := []string{""}
	seen := map[string]bool{}
	for _, a := range s.report.Agents {
		if !seen[a.Harness] {
			seen[a.Harness] = true
			opts = append(opts, a.Harness)
		}
	}
	s.harness = cycle(opts, s.harness)
}

func cycle[T comparable](opts []T, cur T) T {
	for i, o := range opts {
		if o == cur {
			return opts[(i+1)%len(opts)]
		}
	}
	return opts[0]
}

func (s *agentsSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case agentsMsg:
		s.report, s.loaded, s.busy, s.readAt = m.report, true, false, nowFn()
		s.reanchor(s.ids(), false)
		s.settle(len(s.visible()))
		return s, s.ws.fetch(s.wsItems())
	case workspaceMsg:
		s.ws.receive(m)
	case handoffDoneMsg:
		if s.ws.done() && m.err != nil {
			s.ws.note = []string{"The workspace did not open: " + core.Sanitize(m.err.Error())}
		}
	case agentsTickMsg:
		if m.gen != s.gen || !s.active {
			return s, nil
		}
		if s.busy {
			return s, s.tick()
		}
		return s, tea.Batch(s.fetch(), s.tick())
	case tea.WindowSizeMsg:
		s.resize(m)
		s.fit(s.width())
	case tea.KeyMsg:
		s.notice = ""
		if s.typing {
			s.typeKey(m)
			return s, nil
		}
		if s.ws.swallow() { // the key that closes a workspace notice does nothing else
			return s, nil
		}
		switch {
		case key.Matches(m, keyWorkspace):
			it, _, ok := s.wsSelected()
			return s, s.ws.jump(it, ok)
		case key.Matches(m, keyUp):
			s.up(s.ids())
			s.detailTop = 0
		case key.Matches(m, keyDown):
			s.down(s.ids())
			s.detailTop = 0
		case key.Matches(m, keyEnter):
			s.enter(s.width(), len(s.visible()))
		case key.Matches(m, keyBack):
			if !s.back() { // esc peels one layer: the detail, then the filters
				switch {
				case s.query != "":
					s.query = ""
				case s.state != "":
					s.state = ""
				case s.harness != "":
					s.harness = ""
				}
				s.reanchor(s.ids(), false)
			}
		case key.Matches(m, keyFilter):
			s.typing, s.drilled = true, false
		case key.Matches(m, keyAgentState):
			s.nextState()
			s.reanchor(s.ids(), false)
		case key.Matches(m, keyAgentHarness):
			s.nextHarness()
			s.reanchor(s.ids(), false)
		case key.Matches(m, keyAgentSort):
			s.sortKey = (s.sortKey + 1) % len(agentSorts)
			s.reanchor(s.ids(), false)
		case m.String() == "J":
			s.detailTop++
		case m.String() == "K":
			s.detailTop = max(0, s.detailTop-1)
		case key.Matches(m, keyReload):
			if !s.busy {
				return s, s.fetch()
			}
		}
	}
	return s, nil
}

func (s *agentsSection) typeKey(m tea.KeyMsg) {
	switch m.Type {
	case tea.KeyEnter:
		s.typing = false
	case tea.KeyEsc:
		s.typing, s.query = false, ""
	case tea.KeyBackspace:
		if r := []rune(s.query); len(r) > 0 {
			s.query = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		s.query += string(m.Runes)
	}
	s.reanchor(s.ids(), false)
}

// ---- drawing ----

func agentGlyph(st core.AgentState) string {
	switch st {
	case core.AgentRunning:
		return "●"
	case core.AgentWaiting:
		return "◐"
	case core.AgentIdle:
		return "○"
	case core.AgentError:
		return "✕"
	case core.AgentStopped:
		return "·"
	}
	return "?"
}

// gauge draws a share as filled and empty cells exactly w wide, with the number after it. A
// nearly full gauge is a warning and a full one a failure; an unknown share is a row of dots.
func (s *agentsSection) gauge(pct *float64, w int) string {
	if w < 1 {
		return ""
	}
	if pct == nil {
		return s.th.Text(KindMuted, strings.Repeat("·", w))
	}
	filled := int(*pct/100*float64(w) + 0.5)
	filled = max(0, min(w, filled))
	kind := KindOK
	switch {
	case *pct >= 90:
		kind = KindFail
	case *pct >= 75:
		kind = KindWarn
	}
	return s.th.Text(kind, strings.Repeat("▰", filled)) + s.th.Text(KindMuted, strings.Repeat("▱", w-filled))
}

func pctText(p *float64) string {
	if p == nil {
		return "?"
	}
	return fmt.Sprintf("%.0f%%", *p)
}

// agentCols: state, harness, where, task (the flexible one), age, context, evidence. A higher prio drops first.
var agentCols = table{cols: []column{
	{min: 10, max: 10, prio: 0},
	{min: 11, max: 11, prio: 1},
	{min: 10, max: 26, prio: 0},
	{min: 12, prio: 2},
	{min: 4, max: 4, prio: 1, right: true},
	{min: 9, max: 9, prio: 3},
	{min: 10, max: 11, prio: 4},
}, gap: 2}

func agentWhere(a core.Agent) string {
	w := a.Repo
	if a.Branch != "" {
		w += "@" + a.Branch
	}
	if w == "" && a.Cwd != "" { // no repository: the directory's own name, not a path that fills the column
		w = path.Base(a.Cwd)
	}
	return w
}

// evidence is how the state was learned; a guess carries a "~".
func evidence(a core.Agent) string {
	if a.Guess() {
		return "~" + string(a.Source)
	}
	return string(a.Source)
}

func (s *agentsSection) strip(w int) []string {
	r := s.report
	var out []string
	for _, l := range r.RateLimits {
		parts := []string{s.th.Header.Render(san(core.HarnessTitle(l.Harness)))}
		if l.FiveHourPct != nil {
			parts = append(parts, s.limitPart(core.WindowLabel(l.ShortWindowMinutes, "5h"), l.FiveHourPct, l.FiveHourResets, l.Stale))
		}
		if l.SevenDayPct != nil {
			parts = append(parts, s.limitPart(core.WindowLabel(l.LongWindowMinutes, "7d"), l.SevenDayPct, l.SevenDayResets, l.Stale))
		}
		if len(parts) > 1 {
			if l.Stale {
				parts = append(parts, s.th.Text(KindMuted, "stale"))
			}
			out = append(out, trunc(strings.Join(parts, "  "), w))
		}
	}
	if n := len(r.OrphanPorts); n > 0 {
		out = append(out, trunc(s.th.Text(KindWarn, fmt.Sprintf("%s still open after their agent ended: %s", core.Plural(n, "port"), orphanList(r.OrphanPorts))), w))
	}
	return out
}

// limitPart is one rate-limit window: label, gauge, share and, while the read is fresh, how long until
// it resets. A stale read is drawn dim and without the countdown: the window may have rolled over.
func (s *agentsSection) limitPart(label string, pct *float64, resets string, stale bool) string {
	if stale {
		return s.th.Text(KindMuted, label+" "+plainGauge(pct, 8)+" "+pctText(pct))
	}
	out := label + " " + s.gauge(pct, 8) + " " + pctText(pct)
	if t, err := time.Parse(time.RFC3339, resets); err == nil {
		if d := t.Sub(nowFn()); d > 0 {
			out += " ↻" + shortDuration(d)
		}
	}
	return out
}

// shortDuration is "25m", "2h" or "3d": the largest whole unit.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(1, int(d.Minutes())))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// tokenText is a token count in a short form: 950, 1.2k, 84k, 1.5M.
func tokenText(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}

// spark draws a series as one block per point, scaled to its own largest value (zero is the lowest
// block), the newest w points when there are more.
func spark(pts []int64, w int) string {
	if w < 1 || len(pts) == 0 {
		return ""
	}
	if len(pts) > w {
		pts = pts[len(pts)-w:]
	}
	top := int64(1)
	for _, v := range pts {
		top = max(top, v)
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	out := make([]rune, len(pts))
	for i, v := range pts {
		out[i] = blocks[min(len(blocks)-1, int(v*int64(len(blocks)-1)/top))]
	}
	return string(out)
}

func orphanList(ps []core.AgentPort) string {
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%d", p.Port))
	}
	return strings.Join(out, ", ")
}

func (s *agentsSection) listTitle() string {
	r := s.report
	t := fmt.Sprintf("Agents %d", len(s.visible()))
	if len(s.visible()) != len(r.Agents) {
		t += fmt.Sprintf("/%d", len(r.Agents))
	}
	if r.TokenRate != nil {
		t += fmt.Sprintf(" · %.0f tok/s", *r.TokenRate)
	}
	return titled(t, r.Source)
}

// filterLine shows the active narrowing, sort and the count.
func (s *agentsSection) filterLine() string {
	var parts []string
	if s.state != "" {
		parts = append(parts, "state:"+string(s.state))
	}
	if s.harness != "" {
		parts = append(parts, "harness:"+s.harness)
	}
	if s.typing || s.query != "" {
		caret := ""
		if s.typing {
			caret = "▏"
		}
		parts = append(parts, "/"+san(s.query)+caret)
	}
	parts = append(parts, "sort: "+agentSorts[s.sortKey], "~ guessed from files")
	return s.th.Text(KindMuted, strings.Join(parts, "  "))
}

func (s *agentsSection) listLines(g duoGeom) []string {
	th := s.th
	l := s.visible()
	if len(l) == 0 {
		msg := "no agents running"
		if len(s.report.Agents) > 0 {
			msg = "no agent matches the filters (esc clears them)"
		}
		return []string{th.Text(KindMuted, msg)}
	}
	var styled, plain [][]string
	now := nowFn()
	for _, a := range l {
		kind := toneKind(a.State.Tone())
		glyph := agentGlyph(a.State)
		st := string(a.State)
		if a.Guess() { // the tilde says the state is a guess from files, not the agent's own report
			st += "~"
		}
		mk := func(state, ctx, ev string) []string {
			return []string{state, san(a.Title()), orDash(agentWhere(a)), orDash(a.Task), orDash(a.Age(now)), ctx, ev}
		}
		ev := evidence(a)
		styled = append(styled, mk(th.Text(kind, glyph)+" "+th.Text(kind, st), s.gauge(a.ContextPct, 4)+" "+padTo(pctText(a.ContextPct), 3), th.Text(KindMuted, ev)))
		plain = append(plain, mk(glyph+" "+st, strings.Repeat("▰", 4)+" "+pctText(a.ContextPct), ev))
	}
	// the selected row is drawn without per-cell color, so its gauge is plain text of the same width
	for i, a := range l {
		plain[i][5] = plainGauge(a.ContextPct, 4) + " " + padTo(pctText(a.ContextPct), 3)
	}
	lines := renderRows(th, agentCols, g.listInner(), styled, plain, s.cursor)
	return windowLines(th, lines, s.cursor, g.inner, &s.top)
}

func plainGauge(p *float64, w int) string {
	if p == nil {
		return strings.Repeat("·", w)
	}
	f := max(0, min(w, int(*p/100*float64(w)+0.5)))
	return strings.Repeat("▰", f) + strings.Repeat("▱", w-f)
}

func (s *agentsSection) detailLines(iw int) []string {
	th := s.th
	l := s.visible()
	if s.cursor >= len(l) {
		return []string{th.Text(KindMuted, "select an agent")}
	}
	a := l[s.cursor]
	now := nowFn()
	field := func(label, v string) string { return th.Label.Render(padTo(label, 11)) + orDash(v) }
	kind := toneKind(a.State.Tone())
	out := []string{th.Header.Render(san(a.Title())) + " " + th.Badge(kind, string(a.State))}
	if a.Guess() {
		out = append(out, th.Text(KindMuted, "state guessed from "+string(a.Source)+", not reported by the agent"))
	}
	out = append(out, "")
	if a.Task != "" {
		for i, ln := range wrap(san(a.Task), max(iw-11, 8)) {
			label := ""
			if i == 0 {
				label = "task"
			}
			out = append(out, th.Label.Render(padTo(label, 11))+ln)
		}
	}
	place := a.Repo
	if a.Branch != "" {
		place += "@" + a.Branch
	}
	if a.Worktree != "" {
		place += " (worktree " + a.Worktree + ")"
	}
	out = append(out, field("where", place), field("directory", a.Cwd))
	ident := strings.TrimSpace(a.Model)
	if a.PID > 0 {
		ident += "  pid " + fmt.Sprint(a.PID)
	}
	out = append(out, field("agent", strings.TrimSpace(ident)))
	act := agoText(a.Age(now))
	if a.Tokens != nil {
		act += fmt.Sprintf("  %d tokens", *a.Tokens)
	}
	if a.CostUSD != nil {
		act += fmt.Sprintf("  $%.2f", *a.CostUSD)
	}
	out = append(out, field("last seen", strings.TrimSpace(act)))
	if g := a.Git; g != nil {
		changes := "clean"
		if g.Added+g.Modified > 0 {
			changes = fmt.Sprintf("%d added  %d modified", g.Added, g.Modified)
		}
		out = append(out, field("changes", changes))
	}
	ctx := th.Label.Render(padTo("context", 11)) + s.gauge(a.ContextPct, 12) + " " + pctText(a.ContextPct)
	switch {
	case a.ContextTokens != nil && a.ContextWindow != nil:
		ctx += "  " + tokenText(*a.ContextTokens) + " of " + tokenText(*a.ContextWindow)
	case a.ContextTokens != nil:
		ctx += "  " + tokenText(*a.ContextTokens) + " tokens, window not recorded"
	}
	out = append(out, trunc(ctx, iw))
	if n := len(a.TurnTokens); n >= 2 {
		peak := int64(0)
		for _, v := range a.TurnTokens {
			peak = max(peak, v)
		}
		sw := max(4, min(24, iw-11-20))
		out = append(out, trunc(th.Label.Render(padTo("turns", 11))+th.Text(KindAccent, spark(a.TurnTokens, sw))+th.Text(KindMuted, fmt.Sprintf("  last %s  peak %s", tokenText(a.TurnTokens[n-1]), tokenText(peak))), iw))
	}
	srcs := make([]string, 0, len(a.Sources))
	for _, e := range a.Sources {
		srcs = append(srcs, string(e))
	}
	out = append(out, field("seen by", strings.Join(srcs, ", ")))
	if it, ok := s.uniqueItem(a); ok {
		out = append(out, s.ws.detail(th, it, ok, 11)...)
	} else if _, named := agentItem(a); named {
		out = append(out, th.Text(KindMuted, "terminal lookup skipped: another agent shares this name"))
	}
	if a.Term != nil {
		out = append(out, field(a.Term.Kind, a.Term.Ref))
		if a.Term.Command != "" {
			out = append(out, th.Label.Render(padTo("go there", 11))+th.Text(KindAccent, san(a.Term.Command)))
		}
	}
	if len(a.Subs) > 0 {
		out = append(out, "", th.Header.Render("Subagents"))
		for i, sa := range a.Subs {
			branch := "├─"
			if i == len(a.Subs)-1 {
				branch = "└─"
			}
			tok := ""
			if sa.Tokens != nil {
				tok = fmt.Sprintf("  %d tok", *sa.Tokens)
			}
			out = append(out, trunc(" "+branch+" "+san(sa.Name)+"  "+th.Text(KindMuted, san(sa.State)+tok), iw))
		}
	}
	if len(a.Ports) > 0 {
		out = append(out, "", th.Header.Render("Ports"))
		for _, p := range a.Ports {
			out = append(out, fmt.Sprintf("  %d  %s", p.Port, san(p.Command)))
		}
	}
	if len(a.Children) > 0 {
		out = append(out, "", th.Header.Render("Child processes"))
		for _, c := range a.Children {
			mem := ""
			if c.MemKB != nil {
				mem = fmt.Sprintf("  %d MB", *c.MemKB/1024)
			}
			out = append(out, trunc("  "+san(c.Command)+th.Text(KindMuted, mem), iw))
		}
	}
	return out
}

func agoText(age string) string {
	if age == "" {
		return ""
	}
	return age + " ago"
}

func (s *agentsSection) View() string {
	th := s.th
	w := s.width()
	if !s.loaded {
		return joinLines([]string{th.Text(KindMuted, "loading…")})
	}
	r := s.report
	switch r.State {
	case core.NotConnected:
		return joinLines(notConnectedBox(th, "Agents", r.Reason, r.HowToConnect, w, s.h))
	case core.Degraded:
		if len(r.Agents) == 0 {
			return joinLines(box(th, "Agents", unavailableBody(th, r.Reason, innerW(w)), w, s.h))
		}
	}
	if box, ok := s.ws.noteBox(th, w, s.h); ok {
		return joinLines(box)
	}
	var top []string
	if s.h >= hSummary {
		top = s.strip(w)
	}
	top = append(top, trunc(s.filterLine(), w))
	for _, n := range r.Notes {
		top = append(top, trunc(th.Text(KindMuted, "note: "+san(n)), w))
	}
	if s.notice != "" {
		top = append(top, noticeLine(th, w, s.notice)...)
	}
	h := s.h
	if h > 0 {
		h = max(h-len(top), 3)
	}
	g := duoLayout(w, h)
	list := s.listLines(g)
	detail := s.detailLines(innerW(g.detail))
	s.detailTop = max(0, min(s.detailTop, len(detail)-1))
	if s.detailTop > 0 {
		detail = detail[s.detailTop:]
	}
	if g.inner > 0 {
		detail = clipLines(th, detail, g.inner)
	}
	body := append(top, g.draw(th, s.listTitle(), list, "Agent", detail, s.drilled)...)
	return joinLines(body)
}

// ---- global search ----

func (s *agentsSection) searchStamp() string {
	if !s.loaded {
		return stamp(core.SearchLoading, time.Time{}, "")
	}
	return stamp(core.SearchSearched, s.readAt, "")
}

func (s *agentsSection) searchFeed() core.SearchFeed {
	switch {
	case !s.loaded:
		return core.SearchFeed{Domain: core.DomainAgents, State: core.SearchLoading}
	case s.report.State == core.NotConnected:
		return core.SearchFeed{Domain: core.DomainAgents, State: core.SearchNotConnected}
	}
	return core.SearchFeed{Domain: core.DomainAgents, ReadAt: s.readAt, PollEvery: agentsPollEvery, Docs: core.AgentsSearchDocs(s.report), Reason: s.report.Reason}
}

func (s *agentsSection) searchLoad() tea.Cmd {
	if s.loaded || s.busy {
		return nil
	}
	return s.fetch()
}

func (s *agentsSection) jumpTo(r core.SearchResult) (jumpResult, string) {
	id := r.Jump()
	find := func() int {
		for i, x := range s.ids() {
			if x == id {
				return i
			}
		}
		return -1
	}
	res := jumpOK
	i := find()
	if i < 0 && (s.query != "" || s.state != "" || s.harness != "") { // the tab's own filters may hide it
		s.query, s.state, s.harness, s.typing = "", "", "", false
		if i = find(); i >= 0 {
			res = jumpCleared
		}
	}
	if i < 0 {
		return jumpGone, ""
	}
	s.cursor, s.drilled = i, false
	s.remember(s.ids())
	return res, ""
}
