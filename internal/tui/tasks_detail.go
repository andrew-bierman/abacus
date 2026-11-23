package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// taskGlyph is the status mark of a task: bd's own icons for the first four, a diamond for
// deferred. Every use pairs it with a word or a fixed column, so NO_COLOR loses nothing.
func taskGlyph(status string) string {
	switch status {
	case "open":
		return "○"
	case "in_progress":
		return "◐"
	case "blocked":
		return "●"
	case "closed":
		return "✓"
	case "deferred":
		return "◇"
	case "missing":
		return "?"
	}
	return "·"
}

func (s *tasksSection) glyph(status string) string {
	if k, ok := taskStatusKind(status); ok {
		return s.th.Text(k, taskGlyph(status))
	}
	return taskGlyph(status)
}

// progressBar is a fixed-width bar of done of total, with the count after it.
func progressBar(done, total int) string {
	const w = 10
	if total <= 0 {
		return ""
	}
	n := min(done*w/total, w)
	return strings.Repeat("█", n) + strings.Repeat("░", w-n) + fmt.Sprintf(" %d/%d", done, total)
}

var (
	mdBold = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdCode = regexp.MustCompile("`([^`\n]+)`")
	mdBul  = regexp.MustCompile(`^(\s*)([-*+]|\d{1,3}[.)])\s+(.*)$`)
	mdHead = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
)

// inlineMD styles **bold** and `code` in one already-sanitized line.
func (s *tasksSection) inlineMD(line string) string {
	line = mdBold.ReplaceAllStringFunc(line, func(m string) string { return s.th.Header.Render(mdBold.FindStringSubmatch(m)[1]) })
	return mdCode.ReplaceAllStringFunc(line, func(m string) string { return s.th.Text(KindAccent, mdCode.FindStringSubmatch(m)[1]) })
}

// mdLines renders text as plain wrapped lines with light markdown: headings, bullets and
// numbered items with a hanging indent, fenced and inline code, **bold**. The text is untrusted:
// every line passes through san before it is styled or measured, and nothing here can emit a
// control sequence of its own. It is not a markdown renderer and does not try to be one.
func (s *tasksSection) mdLines(text string, w int) []string {
	var out []string
	code, blank := false, false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(san(raw), " ")
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			code = !code
			continue
		case code:
			for _, l := range wrap(s.th.Text(KindAccent, "  "+line), w) {
				out = append(out, l)
			}
			blank = false
		case strings.TrimSpace(line) == "":
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
		case mdHead.MatchString(line):
			out = append(out, wrap(s.th.Section(mdHead.FindStringSubmatch(line)[1]), w)...)
			blank = false
		case mdBul.MatchString(line):
			m := mdBul.FindStringSubmatch(line)
			mark := "• "
			if m[2][0] >= '0' && m[2][0] <= '9' {
				mark = m[2] + " "
			}
			out = append(out, hang(strings.Repeat(" ", min(len(m[1]), 8))+mark, s.inlineMD(m[3]), w)...)
			blank = false
		default:
			out = append(out, wrap(s.inlineMD(line), w)...)
			blank = false
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// detailLines is the detail pane for one task, w cells wide: what it is, what blocks it and what
// it blocks, its children, its text and its comments.
func (s *tasksSection) detailLines(d core.TaskDetailReport, w int) []string {
	th, r := s.th, d.Row
	w = max(w, 1)
	var out []string
	add := func(lines ...string) { out = append(out, lines...) }
	head := san(r.ID)
	if r.Type != "" {
		head += "  " + th.Text(KindMuted, san(r.Type))
	}
	add(trunc(head, w))
	add(wrap(th.Header.Render(san(r.Title)), w)...)
	facts := []string{s.glyph(r.Status) + " " + orDash(strings.ReplaceAll(r.Status, "_", " ")), fmt.Sprintf("P%d", r.Priority)}
	if p, ok := taskPriorityKind(r.Priority); ok {
		facts[1] = th.Text(p, facts[1])
	}
	if r.Assignee != "" {
		facts = append(facts, san(r.Assignee))
	}
	add(wrap(strings.Join(facts, "  "), w)...)
	switch {
	case r.BlockedBy > 0:
		add(wrap(th.Text(KindFail, fmt.Sprintf("held by %d unfinished", r.BlockedBy))+th.Text(KindMuted, "  (blocked)"), w)...)
	case r.HeldBy != "":
		add(wrap(th.Text(KindFail, "held by parent "+san(r.HeldBy))+th.Text(KindMuted, "  (blocked)"), w)...)
	case r.Ready:
		add(th.Text(KindOK, "ready"))
	}
	when := []string{}
	if r.Age != "" {
		when = append(when, "created "+r.Age+" ago")
	}
	if u, err := time.Parse(time.RFC3339, r.UpdatedAt); err == nil {
		if a := taskview.Age(u, nowFn()); a != "" {
			when = append(when, "updated "+a+" ago")
		}
	}
	if len(when) > 0 {
		add(wrap(th.Text(KindMuted, strings.Join(when, " · ")), w)...)
	}
	if len(r.Labels) > 0 {
		add(hang("labels  ", san(strings.Join(r.Labels, ", ")), w)...)
	}
	if d.Parent != nil {
		add(hang("parent  ", s.glyph(d.Parent.Status)+" "+san(d.Parent.ID)+" "+san(d.Parent.Title), w)...)
	}
	refs := func(title string, rs []core.TaskRef) {
		add("", th.Section(fmt.Sprintf("%s (%d)", title, len(rs))))
		if len(rs) == 0 {
			add(th.Text(KindMuted, "  none"))
		}
		for _, x := range rs {
			add(hang("  "+s.glyph(x.Status)+" ", san(x.ID)+" "+san(x.Title), w)...)
		}
	}
	if !s.list.depsUnknown { // a recent-changes list read no dependencies: it does not say "none"
		refs("Blocked by", d.BlockedBy)
		refs("Blocks", d.Blocks)
	}
	if r.ChildTotal > 0 {
		add("", th.Section("Children"), "  "+progressBar(r.ChildDone, r.ChildTotal))
		for _, x := range d.Children {
			add(hang("  "+s.glyph(x.Status)+" ", san(x.ID)+" "+san(x.Title), w)...)
		}
	}
	if d.Description == "" && !s.list.depsUnknown {
		add("", th.Text(KindMuted, "no description"))
	}
	for _, sec := range []struct{ name, text string }{{"", d.Description}, {"Design", d.Design}, {"Notes", d.Notes}, {"Acceptance", d.Acceptance}} {
		if sec.text == "" {
			continue
		}
		add("")
		if sec.name != "" {
			add(th.Section(sec.name))
		}
		add(s.mdLines(sec.text, w)...)
	}
	if d.CloseReason != "" {
		add("", th.Section("Closed"))
		add(wrap(san(d.CloseReason), w)...)
	}
	if len(d.Comments) > 0 {
		add("", th.Section(fmt.Sprintf("Comments (%d)", len(d.Comments))))
		for _, c := range d.Comments {
			add(trunc(th.Tint(c.Author, orDash(c.Author))+"  "+th.Text(KindMuted, c.Age), w))
			for _, l := range s.mdLines(c.Text, max(w-2, 1)) {
				add("  " + l)
			}
		}
	}
	return out
}

// scrollWindow shows rows lines of lines from off, with a "+N more" row when the end is cut. It
// returns the lines and the clamped offset; at the end it shows the last rows lines whole.
func scrollWindow(th Theme, lines []string, off, rows int) ([]string, int) {
	if rows <= 0 || len(lines) <= rows {
		return lines, 0
	}
	off = max(0, min(off, len(lines)-rows))
	if off+rows >= len(lines) {
		return lines[off : off+rows], off
	}
	n := max(rows-1, 1)
	vis := append([]string(nil), lines[off:off+n]...)
	return append(vis, th.moreMarker(len(lines)-off-n)), off
}
