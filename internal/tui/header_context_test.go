package tui

import (
	"strings"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andrew-bierman/abacus/internal/core"
)

func head(m tea.Model) string { return lines(m)[0] }

func TestHeaderContextAppearsFromOneHundredColumnsAndNeverMovesTheTabs(t *testing.T) {
	for _, w := range []int{100, 101, 120, 140, 200} {
		h := head(press(pm(t, w, 32), "2"))
		if lipgloss.Width(h) != w {
			t.Errorf("%d: the tab bar row is %d wide", w, lipgloss.Width(h))
		}
		if !strings.HasPrefix(h, "abacus   Board  [Tasks]  Leases  Queues  Agents  Doctor") {
			t.Errorf("%d: the tab bar is not intact at the left: %q", w, h)
		}
		if !strings.HasSuffix(strings.TrimRight(h, " "), "fleet · 14 tasks · 3 ready") {
			t.Errorf("%d: the context is not at the right: %q", w, h)
		}
	}
	for _, w := range []int{99, 80, 60, 40} {
		h := head(press(pm(t, w, 32), "2"))
		if strings.Contains(h, "fleet") || strings.Contains(h, " ready") {
			t.Errorf("%d: context below the wide breakpoint: %q", w, h)
		}
	}
}

func TestHeaderContextIsRightAlignedAndTheSameOnEveryTab(t *testing.T) {
	var ctx []string
	for _, k := range []string{"1", "2", "3", "4", "5", "6"} {
		h := head(press(pm(t, 120, 32), k))
		i := strings.Index(h, "fleet")
		if i < 0 {
			t.Fatalf("tab %s has no context: %q", k, h)
		}
		ctx = append(ctx, strings.TrimSpace(h[i:]))
		if !strings.HasSuffix(h, strings.TrimSpace(h[i:])) || lipgloss.Width(h) != 120 {
			t.Errorf("tab %s: not right-aligned: %q", k, h)
		}
	}
	for _, c := range ctx[1:] {
		if c != ctx[0] {
			t.Errorf("the context changed between tabs: %q vs %q", c, ctx[0])
		}
	}
}

func TestHeaderContextBoardTargetAndDropOrder(t *testing.T) {
	m := pm(t, 140, 32)
	m.(*Model).boardTarget = "https://board.example/api"
	if h := head(m); !strings.Contains(h, "board https://board.example/api") || !strings.Contains(h, "fleet") {
		t.Errorf("140: %q", h)
	}
	// less room: the board goes first, then the count, the tracker last
	var seen []string
	for w := 140; w >= 100; w-- {
		h := head(sized(m, w, 32))
		if lipgloss.Width(h) > w {
			t.Fatalf("%d: %d wide", w, lipgloss.Width(h))
		}
		state := ""
		for _, p := range []string{"fleet", " tasks", "board https"} {
			if strings.Contains(h, p) {
				state += "1"
			} else {
				state += "0"
			}
		}
		if len(seen) == 0 || seen[len(seen)-1] != state {
			seen = append(seen, state)
		}
	}
	for _, s := range seen {
		if s[0] == '0' && s != "000" { // the tracker is the last to go
			t.Errorf("a part outlived the tracker: %v", seen)
		}
		if s[1] == '0' && s[2] == '1' { // the board never outlives the count
			t.Errorf("the board outlived the count: %v", seen)
		}
	}
}

func TestHeaderContextTruncatesALongTrackerNameWithAnEllipsis(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Tracker = strings.Repeat("a-very-long-tracker-name-", 10)
	m := pm(t, 100, 32)
	m, _ = m.Update(taskSnapshotMsg{snap: snap})
	for _, w := range []int{100, 110, 160} {
		m = sized(m, w, 32)
		l := lines(m)
		if len(l) > 32 || lipgloss.Width(l[0]) > w || !strings.Contains(l[0], "Board") {
			t.Errorf("%d: header %q", w, l[0])
		}
		if w == 100 && (!strings.Contains(l[0], "…") || !strings.Contains(l[0], "14 tasks")) {
			t.Errorf("a long name is cut with an ellipsis and the count stays: %q", l[0])
		}
	}
}

func TestHeaderContextIsSanitized(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Tracker = "tr\x1b[31m\u202eacker\x07\x1b]0;x\x07"
	m := pm(t, 120, 32)
	m, _ = m.Update(taskSnapshotMsg{snap: snap})
	h := head(m)
	for _, r := range h {
		if unicode.IsControl(r) || r == '\u202e' {
			t.Fatalf("header carries %q: %q", r, h)
		}
	}
	m.(*Model).boardTarget = "http://h\x1b[2J/p"
	h = head(sized(m, 160, 32))
	if strings.ContainsRune(h, '\x1b') || lipgloss.Width(h) > 160 {
		t.Errorf("board target not sanitized: %q", h)
	}
}

func TestHeaderContextCountsAreTruthfulWithoutTheWholeTracker(t *testing.T) {
	p := tasksFake()
	var m tea.Model = New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(asciiTheme()), WithFullView(noFullView))
	m = sized(m, 120, 32)
	m, _ = m.Update(tasksMsg{report: fixtureTasks()})
	h := head(m)
	if strings.Contains(h, "ready") {
		t.Errorf("a list of recent changes read no dependencies, so nothing is ready: %q", h)
	}
	if !strings.Contains(h, "tasks") {
		t.Errorf("the summary's total still belongs in the header: %q", h)
	}
}

func TestContextBeforeAnythingLoadsIsEmpty(t *testing.T) {
	p := tasksFake()
	var m tea.Model = New([]core.Entry{{Module: p, Detection: p.Det}}, WithTheme(asciiTheme()), WithFullView(noFullView))
	m = sized(m, 120, 32)
	if h := head(m); strings.Contains(h, "tasks ·") || strings.Contains(h, " 0 tasks") {
		t.Errorf("context before data: %q", h)
	}
}
