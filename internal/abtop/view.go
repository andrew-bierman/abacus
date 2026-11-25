package abtop

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/brand"
	"github.com/andrew-bierman/abacus/internal/core"
)

// The monitor draws abtop's shape: a header, a sessions table, the selected session's detail
// and a ports panel. Every panel is clipped to its own box, so a long title or a store's
// unexpected text can never break the frame.
const (
	defaultViewWidth  = 100
	defaultViewHeight = 40
	minViewWidth      = 40
	minViewHeight     = 12
	// cursorCells is "▸ " or "  " before every table row; boxBorder is one rounded cell on
	// each side of a panel.
	cursorCells = 2
	boxBorder   = 2
	// minBodyHeight is the least body the frame draws below its header and footer; the other
	// minima are the smallest useful height for a table, a panel and the space below the table.
	minBodyHeight   = 6
	minTableHeight  = 5
	minTableFloor   = 4
	minPanelHeight  = 3
	minBelowTable   = 4
	minSideBySide   = 96
	minSideBodyRows = 8
)

var (
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleMuted    = lipgloss.NewStyle().Faint(true)
	styleAccent   = lipgloss.NewStyle().Bold(true)
	styleWarn     = lipgloss.NewStyle().Bold(true)
	styleBorder   = lipgloss.NewStyle().Faint(true)
	styleFocus    = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Bold(true).Reverse(true)
)

// renderMonitor draws the whole frame from one typed report. It derives no facts of its own:
// rows, states, ports and quota all come from the report, so the standalone monitor and the
// abacus Sessions tab show one read.
func renderMonitor(r core.SessionsReport, w, h, sel int, now time.Time, notice string) string {
	if w <= 0 {
		w = defaultViewWidth
	}
	if h <= 0 {
		h = defaultViewHeight
	}
	if w < minViewWidth || h < minViewHeight {
		return renderTooSmall(w, h)
	}

	header := monitorHeader(r, notice, w)
	foot := []string{footerLine}
	bodyH := h - len(header) - len(foot)
	if bodyH < minBodyHeight {
		bodyH = minBodyHeight
	}

	tableH := tableHeight(bodyH, len(r.Sessions))
	tableBody := sessionTableBody(r, sel, w-boxBorder, tableH-boxBorder)
	remaining := bodyH - tableH

	lines := make([]string, 0, h)
	lines = append(lines, header...)
	lines = append(lines, boxLines("Sessions", tableBody, w, tableH, true)...)

	if remaining >= minPanelHeight {
		portBody := portLines(r)
		sideBySide := w >= minSideBySide && remaining >= minSideBodyRows
		detailW := w
		if sideBySide {
			detailW = w * 3 / 5
		}
		detailBody := detailLines(r, sel, now, detailW-boxBorder)
		if sideBySide {
			lines = append(lines, joinRows(
				boxLines("Session", detailBody, detailW, remaining, false),
				boxLines("Ports", portBody, w-detailW, remaining, false),
			)...)
		} else {
			detailH, portH := stackedHeights(remaining, len(portBody))
			if detailH > 0 {
				lines = append(lines, boxLines("Session", detailBody, w, detailH, false)...)
			}
			if portH > 0 {
				lines = append(lines, boxLines("Ports", portBody, w, portH, false)...)
			}
		}
	}

	lines = append(lines, foot...)
	return strings.Join(clipHeight(lines, h), "\n")
}

func tableHeight(bodyH, sessions int) int {
	height := max(sessions, 1) + 1 + boxBorder
	if height < minTableHeight {
		height = minTableHeight
	}
	if half := max(bodyH/2, minTableHeight); height > half {
		height = half
	}
	if reserve := bodyH - minBelowTable; height > reserve {
		height = reserve
	}
	if height < minTableFloor {
		height = minTableFloor
	}
	if height > bodyH {
		height = bodyH
	}
	return max(height, minPanelHeight)
}

func stackedHeights(remaining, portLines int) (detailH, portH int) {
	portH = portLines + boxBorder
	if portH < minPanelHeight {
		portH = minPanelHeight
	}
	if portH > remaining/2 {
		portH = remaining / 2
	}
	detailH = remaining - portH
	if detailH < minPanelHeight {
		detailH = minPanelHeight
		portH = remaining - detailH
	}
	if portH < minPanelHeight {
		portH = 0
	}
	return detailH, portH
}

func renderTooSmall(w, h int) string {
	lines := []string{
		styleHeader.Render(brand.Monitor + "  terminal too small"),
		styleMuted.Render(fmt.Sprintf("  need at least %dx%d, have %dx%d", minViewWidth, minViewHeight, w, h)),
		"",
		footerLine,
	}
	return strings.Join(clipFrame(lines, w, h), "\n")
}

// monitorHeader is the one-line machine summary plus the reasons a read is incomplete. The
// same words the text snapshot uses head the full-screen frame, so both front-ends agree.
func monitorHeader(r core.SessionsReport, notice string, w int) []string {
	head, source := headline(r)
	line := styleHeader.Render(brand.Monitor) + "  " + core.Sanitize(head)
	if source != "" {
		line += styleMuted.Render("  (" + core.Sanitize(source) + ")")
	}
	lines := []string{fit(line, w)}
	if r.Reason != "" {
		lines = append(lines, fit("  "+styleWarn.Render(core.Sanitize(r.Reason)), w))
	}
	if r.HowToConnect != "" {
		lines = append(lines, fit("  how to connect: "+core.Sanitize(r.HowToConnect), w))
	}
	if notice != "" {
		lines = append(lines, fit("  "+styleWarn.Render(core.Sanitize(notice)), w))
	}
	return lines
}

// sessionTableBody is the table's header plus the rows that fit. The selected row is kept in
// view when the list is taller than the panel, so the detail panel and the cursor cannot
// disagree about which session is selected.
func sessionTableBody(r core.SessionsReport, sel, innerW, maxRows int) []string {
	if innerW < 1 {
		innerW = 1
	}
	cols := sessionColumns(innerW - cursorCells)

	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = c.label
	}
	body := []string{"  " + styleMuted.Render(joinCells(head, cols))}
	if len(r.Sessions) == 0 {
		return append(body, styleMuted.Render("no agent sessions are running"))
	}
	sel = clampSel(sel, len(r.Sessions))

	rows := maxRows - 1
	if rows < 1 {
		return body
	}
	start := 0
	if len(r.Sessions) > rows {
		start = sel - rows/2
		if start < 0 {
			start = 0
		}
		if start > len(r.Sessions)-rows {
			start = len(r.Sessions) - rows
		}
	}
	end := min(start+rows, len(r.Sessions))

	for i := start; i < end; i++ {
		s := r.Sessions[i]
		cells := make([]string, len(cols))
		for j, c := range cols {
			cells[j] = c.value(s)
		}
		joined := joinCells(cells, cols)
		if i == sel {
			body = append(body, styleSelected.Render(fit("▸ "+joined, innerW)))
			continue
		}
		body = append(body, "  "+joined)
	}
	return body
}

type sessionColumn struct {
	label string
	width int
	right bool
	value func(agents.Session) string
}

var (
	colState = sessionColumn{label: "state", width: 10, value: func(s agents.Session) string {
		return stateGlyph(s.State) + " " + stateString(s.State)
	}}
	colHarness = sessionColumn{label: "agent", width: 8, value: func(s agents.Session) string {
		return string(s.Harness)
	}}
	colName    = sessionColumn{label: "session", width: 6, value: sessionName}
	colContext = sessionColumn{label: "ctx", width: 10, value: func(s agents.Session) string {
		if s.Context.Limit <= 0 {
			return "-"
		}
		return fmt.Sprintf("%s %.0f%%", percentBar(s.Context.Percent(), 5), s.Context.Percent())
	}}
	colTokens = sessionColumn{label: "tokens", width: 7, right: true, value: func(s agents.Session) string {
		if n := s.Tokens.Total(); n > 0 {
			return compactCount(n)
		}
		return "-"
	}}
	colBranch = sessionColumn{label: "branch", width: 10, value: func(s agents.Session) string {
		if s.Git.Branch == "" {
			return "-"
		}
		out := core.Sanitize(s.Git.Branch)
		if s.Git.Added > 0 {
			out += fmt.Sprintf(" +%d", s.Git.Added)
		}
		if s.Git.Modified > 0 {
			out += fmt.Sprintf(" ~%d", s.Git.Modified)
		}
		return out
	}}
)

// sessionColumns drops the least important columns first on a narrow panel, so a small
// terminal keeps the state, agent and session name instead of clipping every cell.
func sessionColumns(width int) []sessionColumn {
	const gap = 2
	cols := []sessionColumn{colState, colHarness, colName, colContext, colTokens, colBranch}
	for {
		fixed := 0
		for _, c := range cols {
			if c.label != colName.label {
				fixed += c.width
			}
		}
		available := width - fixed - gap*(len(cols)-1)
		if available >= 8 || len(cols) <= 2 {
			for i := range cols {
				if cols[i].label == colName.label {
					cols[i].width = max(available, 6)
				}
			}
			return cols
		}
		for i := len(cols) - 1; i >= 0; i-- {
			if cols[i].label != colName.label {
				cols = append(cols[:i], cols[i+1:]...)
				break
			}
		}
	}
}

func joinCells(cells []string, cols []sessionColumn) string {
	out := make([]string, len(cells))
	for i, cell := range cells {
		w := cols[i].width
		cell = ansi.Truncate(core.Sanitize(cell), w, "…")
		if cols[i].right {
			if n := lipgloss.Width(cell); n < w {
				cell = strings.Repeat(" ", w-n) + cell
			}
		} else {
			cell = padTo(cell, w)
		}
		out[i] = cell
	}
	return strings.Join(out, "  ")
}

// detailLines is the selected session's facts, plus the account quota and anything that needs
// attention. With no session selected it still reports quota, orphan ports and unreadable
// stores, so an empty machine is not a blank panel.
func detailLines(r core.SessionsReport, sel int, now time.Time, innerW int) []string {
	if sel < 0 || sel >= len(r.Sessions) {
		return globalDetail(r, now)
	}
	s := r.Sessions[sel]
	field := func(label, value string) string {
		if value == "" {
			value = "-"
		}
		return styleMuted.Render(padTo(label, 12)) + core.Sanitize(value)
	}

	out := []string{
		styleAccent.Render(sessionName(s)) + "  " + styleMuted.Render(stateString(s.State)),
		field("agent", string(s.Harness)),
		field("pid", strconv.Itoa(s.PID)),
		field("model", s.Model),
		field("cwd", s.CWD),
	}
	if s.Git.Branch != "" || s.Git.Added > 0 || s.Git.Modified > 0 {
		out = append(out, field("git", gitDetail(s.Git)))
	}
	if !s.Started.IsZero() {
		out = append(out, field("running", elapsed(now.Sub(s.Started))))
	}
	if !s.LastActive.IsZero() {
		out = append(out, field("last write", elapsed(now.Sub(s.LastActive))+" ago"))
	}

	out = append(out, "", styleAccent.Render("Context"))
	out = append(out, contextDetail(s.Context)...)
	out = append(out, "", styleAccent.Render("Tokens"))
	out = append(out, tokenDetail(s.Tokens)...)
	out = append(out, "", styleAccent.Render("Per turn"))
	out = append(out, sparkDetail(s.TokenHistory, innerW)...)

	if q, ok := quotaFor(r.Quotas, s.Harness); ok {
		out = append(out, quotaDetail(q, now)...)
	}
	if len(s.Children) > 0 {
		out = append(out, "", styleAccent.Render("Children"))
		for _, c := range s.Children {
			line := fmt.Sprintf("%d  %s", c.PID, core.Sanitize(c.Command))
			if rss := rssLabel(c.RSSKB); rss != "" {
				line += styleMuted.Render("  " + rss)
			}
			out = append(out, line)
		}
	}
	if len(s.Ports) > 0 {
		out = append(out, "", styleAccent.Render("Ports"))
		for _, p := range s.Ports {
			out = append(out, fmt.Sprintf("%d  %s", p.Port, core.Sanitize(p.Command)))
		}
	}
	out = append(out, orphanDetail(r)...)
	out = append(out, unreadableDetail(r)...)
	return out
}

func globalDetail(r core.SessionsReport, now time.Time) []string {
	msg := "select a session"
	if len(r.Sessions) == 0 {
		msg = "no agent sessions are running"
	}
	out := []string{styleMuted.Render(msg)}
	for _, q := range r.Quotas {
		out = append(out, quotaDetail(q, now)...)
	}
	out = append(out, orphanDetail(r)...)
	out = append(out, unreadableDetail(r)...)
	return out
}

func orphanDetail(r core.SessionsReport) []string {
	if len(r.Orphans) == 0 {
		return nil
	}
	out := []string{"", styleAccent.Render("Orphaned ports")}
	for _, p := range r.Orphans {
		line := fmt.Sprintf("%d  %s", p.Port, core.Sanitize(p.Command))
		if p.Project != "" {
			line += styleMuted.Render("  (" + core.Sanitize(p.Project) + ")")
		}
		out = append(out, line)
	}
	return out
}

func unreadableDetail(r core.SessionsReport) []string {
	if len(r.Unreadable) == 0 {
		return nil
	}
	out := []string{"", styleAccent.Render("Unreadable")}
	for _, why := range r.Unreadable {
		out = append(out, styleWarn.Render(core.Sanitize(why)))
	}
	return out
}

func quotaDetail(q agents.Quota, now time.Time) []string {
	out := []string{"", styleAccent.Render("quota " + core.Sanitize(string(q.Source)))}
	stale := q.Stale(now)
	for _, slot := range []struct {
		fallback int64
		window   *agents.QuotaWindow
	}{
		{agents.QuotaShortWindowMinutes, q.Short},
		{agents.QuotaLongWindowMinutes, q.Long},
	} {
		if slot.window == nil {
			continue
		}
		line := fmt.Sprintf("%-4s %s %.0f%%", slot.window.Span(slot.fallback),
			percentBar(slot.window.UsedPercent, 10), slot.window.UsedPercent)
		if !stale && !slot.window.ResetsAt.IsZero() {
			line += styleMuted.Render("  resets " + timeLeft(slot.window.ResetsAt, now))
		}
		out = append(out, line)
	}
	if stale {
		out = append(out, styleMuted.Render("stale: not updated in the last 10 minutes"))
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

func contextDetail(c agents.Context) []string {
	p := c.Percent()
	if c.Limit <= 0 {
		return []string{fmt.Sprintf("%s used, window not recorded", compactCount(c.Used))}
	}
	return []string{fmt.Sprintf("%s  %.0f%%  %s / %s", percentBar(p, 10), p,
		compactCount(c.Used), compactCount(c.Limit))}
}

func tokenDetail(t agents.Tokens) []string {
	if t.Total() == 0 && t.CacheRead == 0 {
		return []string{"no tokens recorded"}
	}
	return []string{
		fmt.Sprintf("total %s  input %s  output %s", compactCount(t.Total()),
			compactCount(t.Input), compactCount(t.Output)),
		fmt.Sprintf("cache read %s  cache create %s", compactCount(t.CacheRead),
			compactCount(t.CacheCreate)),
	}
}

func sparkDetail(points []int64, innerW int) []string {
	if len(points) == 0 {
		return []string{styleMuted.Render("this harness records no per-turn history")}
	}
	width := min(max(innerW-12, 8), len(points))
	return []string{sparkline(points, width) + styleMuted.Render(fmt.Sprintf("  %d turns", len(points)))}
}

// portLines lists every listening port the report carries, live and orphaned, so the panel is
// useful even when the session list is empty.
func portLines(r core.SessionsReport) []string {
	var out []string
	for _, s := range r.Sessions {
		label := sessionName(s)
		for _, p := range s.Ports {
			out = append(out, fmt.Sprintf("%d  %s  %s", p.Port, s.Harness, label))
		}
	}
	for _, p := range r.Orphans {
		line := fmt.Sprintf("%d  %s", p.Port, core.Sanitize(p.Command))
		if p.Project != "" {
			line += styleMuted.Render("  (" + core.Sanitize(p.Project) + ")")
		}
		out = append(out, line+styleMuted.Render("  orphan"))
	}
	if len(out) == 0 {
		return []string{styleMuted.Render("no listening ports")}
	}
	return out
}

func sessionName(s agents.Session) string {
	switch {
	case s.Title != "":
		return core.Sanitize(s.Title)
	case s.CWD != "":
		return core.Sanitize(filepath.Base(s.CWD))
	}
	return fmt.Sprintf("%s:%d", s.Harness, s.PID)
}

func stateString(s agents.State) string {
	if s == "" {
		return string(agents.StateUnknown)
	}
	return string(s)
}

// stateGlyph gives each state a distinct mark, so the table is still readable when the
// terminal has no color at all.
func stateGlyph(s agents.State) string {
	switch s {
	case agents.StateThinking:
		return "◐"
	case agents.StateExecuting:
		return "▶"
	case agents.StateWaiting:
		return "○"
	}
	return "?"
}

func percentBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	pct = max(0, min(pct, 100))
	n := int(pct*float64(width)/100 + 0.5)
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

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

func gitDetail(g agents.GitStatus) string {
	if g.Branch == "" {
		return ""
	}
	out := core.Sanitize(g.Branch)
	if g.Added > 0 {
		out += fmt.Sprintf("  +%d added", g.Added)
	}
	if g.Modified > 0 {
		out += fmt.Sprintf("  ~%d modified", g.Modified)
	}
	return out
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

func timeLeft(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := t.Sub(now)
	if d <= 0 {
		return "now"
	}
	return elapsed(d)
}

func padTo(s string, w int) string {
	n := lipgloss.Width(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// boxLines draws one titled rounded box at an exact width and height. Every row is padded or
// clipped here, so callers cannot accidentally emit a line wider than the panel.
func boxLines(title string, body []string, width, height int, focus bool) []string {
	if width < 4 {
		width = 4
	}
	if height < 3 {
		height = 3
	}
	innerW := width - 2
	innerH := height - 2
	b := lipgloss.RoundedBorder()
	style := styleBorder
	if focus {
		style = styleFocus
	}

	label := fit(" "+title+" ", innerW-1)
	top := b.TopLeft + b.Top + label + strings.Repeat(b.Top, max(innerW-1-lipgloss.Width(label), 0)) + b.TopRight
	out := []string{style.Render(top)}
	for i := 0; i < innerH; i++ {
		content := ""
		if i < len(body) {
			content = body[i]
		}
		out = append(out, style.Render(b.Left)+fit(content, innerW)+style.Render(b.Right))
	}
	out = append(out, style.Render(b.BottomLeft+strings.Repeat(b.Bottom, innerW)+b.BottomRight))
	return out
}

func joinRows(left, right []string) []string {
	joined := lipgloss.JoinHorizontal(lipgloss.Top,
		strings.Join(left, "\n"), strings.Join(right, "\n"))
	return strings.Split(joined, "\n")
}

// fit clips to w cells, appends an ellipsis when it truncates, and pads to exactly w. ANSI
// styling is preserved because measurement goes through lipgloss.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return padTo(s, w)
}

// clipFrame bounds a frame that was not assembled by the panel helpers, used by the loading
// and first-failure screens. A rendered monitor is already exact-width, so it only needs
// clipHeight.
func clipFrame(lines []string, w, h int) []string {
	if w > 0 {
		clipped := make([]string, len(lines))
		for i, line := range lines {
			clipped[i] = fit(line, w)
		}
		lines = clipped
	}
	return clipHeight(lines, h)
}

func clipHeight(lines []string, h int) []string {
	if h > 0 && len(lines) > h {
		return lines[:h]
	}
	return lines
}
