package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/core"
)

// sessionsPollEvery is how often the tab reads the machine again. One snapshot of the live
// sessions measured 150ms, and the git facts it carries are cached for longer, so a few
// seconds keeps the tab current without making it the most expensive thing abacus does.
const sessionsPollEvery = 5 * time.Second

// keyDetailScroll moves the selected session's detail when it is taller than its panel. It is
// separate from the list cursor: up and down still select sessions in the side-by-side layout.
var keyDetailScroll = key.NewBinding(
	key.WithKeys("pgup", "pgdown", "ctrl+u", "ctrl+d"),
	key.WithHelp("pgup/pgdn", "scroll detail"),
)

type sessionsMsg struct {
	report core.SessionsReport
	err    error
}
type sessionsTickMsg struct{ gen int }

// sessionsSection is the Sessions tab: the live agent processes, what each is spending, the
// ports and children it owns, and the account quota gauges. It renders the typed
// core.SessionsReport and derives no facts of its own beyond layout.
type sessionsSection struct {
	read func(context.Context) (core.SessionsReport, error)

	report  core.SessionsReport
	err     error
	loaded  bool
	busy    bool
	active  bool
	gen     int // bumped on every (de)activation: a tick from an older visit is dropped
	fetches int // reads started (a test seam: a poll never overlaps another read)
	notice  string
	th      Theme
	geom
	listNav // ids are harness:pid, in the report's active-first order
	detail  scroller
}

func newSessionsSection(read func(context.Context) (core.SessionsReport, error)) *sessionsSection {
	return &sessionsSection{read: read, th: DefaultTheme()}
}

func (s *sessionsSection) SetTheme(t Theme) { s.th = t }
func (s *sessionsSection) Capturing() bool  { return false }

func (s *sessionsSection) sessions() []agents.Session { return s.report.Sessions }

// ids is one stable id per row: a pid is unique while it lives, and the harness keeps two
// harnesses that somehow share one from swapping identities.
func (s *sessionsSection) ids() []string {
	xs := s.sessions()
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = fmt.Sprintf("%s:%d", x.Harness, x.PID)
	}
	return out
}

func (s *sessionsSection) Bindings() []key.Binding {
	enter, back := s.bindings(s.width(), len(s.sessions()))
	return append(append(moveBindings(), enter, back, keyReload), keyDetailScroll)
}

func (s *sessionsSection) fetch() tea.Cmd {
	s.busy = true
	s.fetches++
	read := s.read
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		r, err := read(ctx)
		return sessionsMsg{report: r, err: err}
	}
}

func (s *sessionsSection) tick() tea.Cmd {
	gen := s.gen
	return tea.Tick(sessionsPollEvery, func(time.Time) tea.Msg { return sessionsTickMsg{gen} })
}

func (s *sessionsSection) Init() tea.Cmd { return nil }

func (s *sessionsSection) activate() tea.Cmd {
	s.active = true
	s.gen++
	if s.busy {
		return s.tick()
	}
	return tea.Batch(s.fetch(), s.tick())
}

func (s *sessionsSection) deactivate() { s.active = false; s.gen++ }

func (s *sessionsSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case sessionsMsg:
		s.loaded, s.busy = true, false
		switch {
		case m.err != nil && s.report.Taken.IsZero():
			s.err = m.err
		case m.err != nil:
			// A later read failed: keep the last good frame on screen rather than replacing a
			// live machine with an empty error panel.
			s.notice = "read failed: " + core.Sanitize(m.err.Error())
		default:
			s.report, s.err, s.notice = m.report, nil, ""
			s.reanchor(s.ids(), true)
			s.settle(len(s.sessions()))
		}
	case sessionsTickMsg:
		if !s.active || m.gen != s.gen { // not visible, or a tick of an earlier visit
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
		var ids []string
		switch {
		case key.Matches(m, keyUp):
			if s.scrollDetail(-3) {
				return s, nil
			}
			ids = s.ids()
			s.up(ids)
			s.detail.off = 0
		case key.Matches(m, keyDown):
			if s.scrollDetail(3) {
				return s, nil
			}
			ids = s.ids()
			s.down(ids)
			s.detail.off = 0
		case key.Matches(m, keyDetailScroll):
			delta := 1
			if m.String() == "pgup" || m.String() == "ctrl+u" {
				delta = -1
			}
			g := s.detailGeom()
			s.detail.move(delta*5, len(s.detailLines(g)), g.inner)
			return s, nil
		case key.Matches(m, keyEnter):
			ids = s.ids()
			s.enter(s.width(), len(ids))
			s.detail.off = 0
		case key.Matches(m, keyBack):
			s.back()
		case key.Matches(m, keyReload):
			if !s.busy {
				return s, s.fetch()
			}
		}
		s.remember(ids)
	}
	return s, nil
}

// scrollDetail moves the detail when the stacked layout is showing it; the list has no cursor
// while drilled in, so up and down belong to the detail there.
func (s *sessionsSection) scrollDetail(delta int) bool {
	if !s.drilled || !isStacked(s.width()) {
		return false
	}
	g := s.detailGeom()
	s.detail.move(delta, len(s.detailLines(g)), g.inner)
	return true
}

func (s *sessionsSection) detailGeom() duoGeom {
	return duoLayout(s.width(), s.bodyH(s.notice))
}

// sessionCols are the table's columns. The session name and its state stay longest; the
// harness, branch and port count are the first details a narrow panel can live without.
var sessionCols = table{cols: []column{
	{min: 9, max: 11, prio: 1},             // state
	{min: 6, max: 8, prio: 4},              // harness
	{min: 12, prio: 0},                     // session
	{min: 5, max: 7, prio: 3, right: true}, // tokens
	{min: 9, max: 10, prio: 2},             // context
	{min: 9, max: 14, prio: 5},             // branch
	{min: 5, max: 6, prio: 6, right: true}, // ports
}, gap: 2}

func (s *sessionsSection) listTitle() string {
	r := s.report
	return titled("Sessions · "+san(r.Summarize()), string(r.Source))
}

func (s *sessionsSection) listLines(g duoGeom) []string {
	th := s.th
	iw := max(g.listInner(), 1)
	xs := s.sessions()

	var lines []string
	if s.report.Reason != "" {
		lines = append(lines, wrap(th.Text(KindWarn, san(s.report.Reason)), iw)...)
	}
	if qs := s.quotaSummary(iw); len(qs) > 0 {
		lines = append(lines, qs...)
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	if len(xs) == 0 {
		return append(lines, th.Text(KindMuted, "no agent sessions are running"))
	}

	styled := make([][]string, len(xs))
	plain := make([][]string, len(xs))
	for i, x := range xs {
		stateCell := stateGlyph(x.State) + " " + sessionStateString(x.State)
		ctxCell, ctxStyled := contextCells(th, x.Context)
		styled[i] = []string{
			th.Text(sessionStateKind(x.State), stateCell),
			th.Tint(string(x.Harness), san(string(x.Harness))),
			sessionName(x),
			tokenCell(x.Tokens),
			ctxStyled,
			gitCell(x.Git),
			portsCell(x.Ports),
		}
		plain[i] = []string{stateCell, san(string(x.Harness)), sessionName(x), tokenCell(x.Tokens), ctxCell, gitCell(x.Git), portsCell(x.Ports)}
	}

	header := []string{"state", "harness", "session", "tokens", "ctx", "branch", "ports"}
	widths := sessionCols.fitRows(max(iw-2, 1), append(styled, header)) // two cells for the selection marker
	lines = append(lines, "  "+th.Text(KindMuted, sessionCols.row(widths, header)))
	rowsAt := len(lines)
	for i := range styled {
		if i == s.cursor {
			lines = append(lines, th.Selected.Render(padTo(trunc("▸ "+sessionCols.row(widths, plain[i]), iw), iw)))
			continue
		}
		lines = append(lines, "  "+sessionCols.row(widths, styled[i]))
	}
	return windowLines(th, lines, rowsAt+s.cursor, g.inner, &s.top)
}

// quotaSummary is the account gauges as one compact line per source, the shape the CLI prints
// in text. The selected session's detail draws the bars and reset countdowns.
func (s *sessionsSection) quotaSummary(iw int) []string {
	th := s.th
	var out []string
	for _, q := range s.report.Quotas {
		parts := []string{"quota", san(string(q.Source))}
		if q.Short != nil {
			parts = append(parts, quotaWindowSummary(q.Short, agents.QuotaShortWindowMinutes))
		}
		if q.Long != nil {
			parts = append(parts, quotaWindowSummary(q.Long, agents.QuotaLongWindowMinutes))
		}
		if q.Stale(nowFn()) {
			parts = append(parts, "(stale)")
		}
		out = append(out, th.Text(KindMuted, trunc(strings.Join(parts, "  "), iw)))
	}
	return out
}

func quotaWindowSummary(w *agents.QuotaWindow, fallbackMinutes int64) string {
	return fmt.Sprintf("%s %.0f%%", windowSpan(w.WindowMinutes, fallbackMinutes), w.UsedPercent)
}

func (s *sessionsSection) detailLines(g duoGeom) []string {
	iw := max(g.detail-panelChromeW, 1)
	xs := s.sessions()
	if s.cursor >= len(xs) {
		return s.globalDetail(iw)
	}
	return s.sessionDetail(xs[s.cursor], iw)
}

func (s *sessionsSection) sessionDetail(x agents.Session, iw int) []string {
	th := s.th
	state := sessionStateString(x.State)
	field := func(label, v string) string {
		return th.Label.Render(padTo(label, 12)) + orDash(v)
	}

	out := []string{
		th.Header.Render(sessionName(x)) + " " + th.Badge(sessionStateKind(x.State), state),
		field("harness", string(x.Harness)),
		field("pid", strconv.Itoa(x.PID)),
		field("model", x.Model),
		field("cwd", x.CWD),
	}
	if x.Git.Branch != "" || x.Git.Added > 0 || x.Git.Modified > 0 {
		out = append(out, field("git", gitDetail(x.Git)))
	}
	if !x.Started.IsZero() {
		out = append(out, field("running", elapsed(nowFn().Sub(x.Started))))
	}
	if !x.LastActive.IsZero() {
		out = append(out, field("last write", shortAge(x.LastActive)))
	}

	out = append(out, "", th.Section("Context"))
	out = append(out, contextDetail(th, x.Context, iw)...)
	out = append(out, "", th.Section("Tokens"))
	out = append(out, tokenDetail(x.Tokens)...)
	out = append(out, "", th.Section("Per turn"))
	out = append(out, sparkDetail(th, x.TokenHistory, iw)...)

	if q, ok := quotaFor(s.report.Quotas, x.Harness); ok {
		out = append(out, quotaBlock(th, q)...)
	}
	if len(x.Children) > 0 {
		out = append(out, "", th.Section("Children"))
		for _, c := range x.Children {
			line := fmt.Sprintf("%d  %s", c.PID, san(c.Command))
			if rss := rssLabel(c.RSSKB); rss != "" {
				line += th.Text(KindMuted, "  "+rss)
			}
			out = append(out, line)
		}
	}
	if len(x.Ports) > 0 {
		out = append(out, "", th.Section("Ports"))
		for _, p := range x.Ports {
			out = append(out, fmt.Sprintf("%d  %s", p.Port, san(p.Command)))
		}
	}
	out = append(out, s.orphanLines(th)...)
	out = append(out, s.unreadableLines(th)...)
	return out
}

func (s *sessionsSection) globalDetail(iw int) []string {
	th := s.th
	xs := s.sessions()
	msg := "select a session"
	if len(xs) == 0 {
		msg = "no agent sessions are running"
	}
	out := []string{th.Text(KindMuted, msg)}
	for _, q := range s.report.Quotas {
		out = append(out, quotaBlock(th, q)...)
	}
	out = append(out, s.orphanLines(th)...)
	out = append(out, s.unreadableLines(th)...)
	return out
}

func (s *sessionsSection) orphanLines(th Theme) []string {
	if len(s.report.Orphans) == 0 {
		return nil
	}
	out := []string{"", th.Section("Orphaned ports")}
	for _, p := range s.report.Orphans {
		line := fmt.Sprintf("%d  %s", p.Port, san(p.Command))
		if p.Project != "" {
			line += th.Text(KindMuted, "  ("+san(p.Project)+")")
		}
		out = append(out, line)
	}
	return out
}

func (s *sessionsSection) unreadableLines(th Theme) []string {
	if len(s.report.Unreadable) == 0 {
		return nil
	}
	out := []string{"", th.Section("Unreadable")}
	for _, why := range s.report.Unreadable {
		out = append(out, th.Text(KindWarn, san(why)))
	}
	return out
}

func quotaBlock(th Theme, q agents.Quota) []string {
	out := []string{"", th.Section("Quota " + san(string(q.Source)))}
	stale := q.Stale(nowFn())
	for _, w := range []struct {
		fallback int64
		window   *agents.QuotaWindow
	}{
		{agents.QuotaShortWindowMinutes, q.Short},
		{agents.QuotaLongWindowMinutes, q.Long},
	} {
		if w.window == nil {
			continue
		}
		kind := KindMuted
		if !stale {
			kind = percentKind(w.window.UsedPercent)
		}
		line := fmt.Sprintf("%-4s %s %.0f%%", windowSpan(w.window.WindowMinutes, w.fallback),
			th.Text(kind, sessionsBar(w.window.UsedPercent, 10)), w.window.UsedPercent)
		if !stale && !w.window.ResetsAt.IsZero() {
			line += th.Text(KindMuted, "  resets "+timeLeft(w.window.ResetsAt))
		}
		out = append(out, line)
	}
	if stale {
		out = append(out, th.Text(KindMuted, "stale: not updated in the last 10 minutes"))
	}
	return out
}

func quotaFor(quotas []agents.Quota, h agents.Harness) (agents.Quota, bool) {
	for _, q := range quotas {
		if q.Source == h {
			return q, true
		}
	}
	return agents.Quota{}, false
}

func (s *sessionsSection) connectedView() string {
	th, w := s.th, s.width()
	g := duoLayout(w, s.bodyH(s.notice))
	g.fill = true
	body := g.draw(th, s.listTitle(), s.listLines(g), "Session", s.detailView(g), s.drilled)
	if s.notice != "" {
		body = append(body, noticeLine(th, w, s.notice)...)
	}
	return joinLines(body)
}

func (s *sessionsSection) detailView(g duoGeom) []string {
	lines := s.detailLines(g)
	if g.inner <= 0 {
		return lines
	}
	vis, hidden := s.detail.view(lines, g.inner)
	if hidden > 0 && g.inner >= 2 {
		vis = append(append([]string(nil), vis...), s.th.moreMarker(hidden))
	}
	return vis
}

func (s *sessionsSection) stateView() string {
	th, w := s.th, s.width()
	if !s.loaded {
		return joinLines([]string{th.Text(KindMuted, "loading…")})
	}
	var nc *core.NotConnectedError
	if errors.As(s.err, &nc) {
		how := nc.HowToConnect
		if how == nc.Reason {
			how = ""
		}
		return joinLines(notConnectedBox(th, "Sessions", nc.Reason, how, w, s.h))
	}
	body := unavailableBody(th, s.err.Error(), innerW(w))
	body = append(body, "")
	body = append(body, wrap(th.Text(KindMuted, strings.ToLower(core.HowToConnectDegraded[:1])+core.HowToConnectDegraded[1:]), innerW(w))...)
	return joinLines(box(th, "Sessions", body, w, s.h))
}

func (s *sessionsSection) View() string {
	if s.loaded && s.err == nil {
		return s.connectedView()
	}
	return s.stateView()
}

// sessionName is what a session is called: its transcript title, else the project directory,
// else its harness and pid. Every part came from outside abacus and is sanitized.
func sessionName(x agents.Session) string {
	switch {
	case x.Title != "":
		return san(x.Title)
	case x.CWD != "":
		return san(filepath.Base(x.CWD))
	}
	return fmt.Sprintf("%s:%d", x.Harness, x.PID)
}

func sessionStateString(s agents.State) string {
	if s == "" {
		return string(agents.StateUnknown)
	}
	return string(s)
}

// sessionStateViews is the one place a state's mark and tone are paired, so a new state cannot
// gain a glyph and silently keep an old color.
var sessionStateViews = map[agents.State]struct {
	glyph string
	kind  Kind
}{
	agents.StateThinking:  {"◐", KindOK},
	agents.StateExecuting: {"▶", KindOK},
	agents.StateWaiting:   {"○", KindMuted},
	agents.StateUnknown:   {"?", KindWarn},
}

func stateGlyph(s agents.State) string {
	if v, ok := sessionStateViews[s]; ok {
		return v.glyph
	}
	return sessionStateViews[agents.StateUnknown].glyph
}

func sessionStateKind(s agents.State) Kind {
	if v, ok := sessionStateViews[s]; ok {
		return v.kind
	}
	return sessionStateViews[agents.StateUnknown].kind
}

func tokenCell(t agents.Tokens) string {
	if n := t.Total(); n > 0 {
		return compactCount(n)
	}
	return "-"
}

func portsCell(ports []agents.Port) string {
	if len(ports) == 0 {
		return "-"
	}
	return strconv.Itoa(len(ports))
}

func gitCell(g agents.GitStatus) string {
	if g.Branch == "" {
		return "-"
	}
	s := san(g.Branch)
	if g.Added > 0 {
		s += fmt.Sprintf(" +%d", g.Added)
	}
	if g.Modified > 0 {
		s += fmt.Sprintf(" ~%d", g.Modified)
	}
	return s
}

func gitDetail(g agents.GitStatus) string {
	if g.Branch == "" {
		return ""
	}
	s := san(g.Branch)
	if g.Added > 0 {
		s += fmt.Sprintf("  +%d added", g.Added)
	}
	if g.Modified > 0 {
		s += fmt.Sprintf("  ~%d modified", g.Modified)
	}
	return s
}

// contextCells returns one context cell plain and styled, so the selected row can be drawn
// without per-cell color while every other row keeps its bar's tone.
func contextCells(th Theme, c agents.Context) (plain, styled string) {
	p := c.Percent()
	if c.Limit <= 0 {
		return "-", "-"
	}
	bar := sessionsBar(p, 5)
	plain = fmt.Sprintf("%s %.0f%%", bar, p)
	return plain, th.Text(percentKind(p), bar) + fmt.Sprintf(" %.0f%%", p)
}

func contextDetail(th Theme, c agents.Context, iw int) []string {
	p := c.Percent()
	if c.Limit <= 0 {
		return []string{fmt.Sprintf("%s used, window not recorded", compactCount(c.Used))}
	}
	width := min(max(iw-22, 8), 16)
	bar := th.Text(percentKind(p), sessionsBar(p, width))
	return []string{fmt.Sprintf("%s  %.0f%%  %s / %s", bar, p, compactCount(c.Used), compactCount(c.Limit))}
}

func tokenDetail(t agents.Tokens) []string {
	if t.Total() == 0 && t.CacheRead == 0 {
		return []string{"no tokens recorded"}
	}
	return []string{
		fmt.Sprintf("total %s   input %s   output %s", compactCount(t.Total()), compactCount(t.Input), compactCount(t.Output)),
		fmt.Sprintf("cache read %s   cache create %s", compactCount(t.CacheRead), compactCount(t.CacheCreate)),
	}
}

func sparkDetail(th Theme, points []int64, iw int) []string {
	if len(points) == 0 {
		return []string{th.Text(KindMuted, "this harness records no per-turn history")}
	}
	width := min(max(iw-12, 8), len(points))
	return []string{sparkline(points, width) + th.Text(KindMuted, fmt.Sprintf("  %d turns", len(points)))}
}

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// sparkline draws one block per point, scaled to the largest point in the window. The oldest
// points are dropped first when the panel is narrower than the history.
func sparkline(points []int64, width int) string {
	if len(points) == 0 || width <= 0 {
		return ""
	}
	if len(points) > width {
		points = points[len(points)-width:]
	}
	var peak int64
	for _, p := range points {
		peak = max(peak, p)
	}
	var b strings.Builder
	for _, p := range points {
		i := 0
		if peak > 0 {
			i = int(float64(p) / float64(peak) * float64(len(sparkBlocks)-1))
		}
		i = max(0, min(i, len(sparkBlocks)-1))
		b.WriteRune(sparkBlocks[i])
	}
	return b.String()
}

// sessionsBar is a filled proportion of width cells. Percent is used, not remaining, because
// both context and quota report how much is spent.
func sessionsBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	pct = max(0, min(pct, 100))
	n := int(pct*float64(width)/100 + 0.5)
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

func percentKind(pct float64) Kind {
	switch {
	case pct >= 90:
		return KindFail
	case pct >= 70:
		return KindWarn
	}
	return KindOK
}

func compactCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return trimDecimal(float64(n)/1_000_000_000) + "B"
	case n >= 1_000_000:
		return trimDecimal(float64(n)/1_000_000) + "M"
	case n >= 100_000:
		return fmt.Sprintf("%.0fk", float64(n)/1_000)
	case n >= 1_000:
		return trimDecimal(float64(n)/1_000) + "k"
	}
	return strconv.FormatInt(n, 10)
}

func trimDecimal(f float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0") }

func rssLabel(kb int64) string {
	if kb <= 0 {
		return ""
	}
	if kb >= 1024 {
		return fmt.Sprintf("%dM", kb/1024)
	}
	return fmt.Sprintf("%dK", kb)
}

// windowSpan names a quota window from its recorded length, falling back to the slot's
// documented default when the source did not record one.
func windowSpan(minutes, fallback int64) string {
	if minutes <= 0 {
		minutes = fallback
	}
	return agents.WindowLength(minutes)
}

func shortAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := nowFn().Sub(t)
	if d < 0 {
		d = 0
	}
	return elapsed(d) + " ago"
}

func timeLeft(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := t.Sub(nowFn())
	if d <= 0 {
		return "now"
	}
	return elapsed(d)
}

func elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Seconds())
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	case sec < 24*3600:
		return fmt.Sprintf("%dh%02dm", sec/3600, sec%3600/60)
	}
	return fmt.Sprintf("%dd%dh", sec/(24*3600), sec%(24*3600)/3600)
}
